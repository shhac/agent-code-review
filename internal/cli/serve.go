package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shhac/lib-agent-mcp/tailscale"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/dashboard"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/logbuf"
	"github.com/shhac/agent-code-review/internal/scheduler"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

type serveOpts struct {
	addr          string
	publicURL     string
	tailscaleMode string
	tailscalePort int
	noSchedule    bool
	noDiscovery   bool
	noReviews     bool
	readOnly      bool
	version       string // the root command's ldflags-injected build version
}

func registerServe(root *cobra.Command) {
	opts := &serveOpts{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon: scheduler + dashboard (+ optional Tailscale)",
		Long: "Run the always-on daemon. Reviews candidates on the configured\n" +
			"interval and serves the dashboard. Use --tailscale serve|funnel to\n" +
			"expose the dashboard on your tailnet or the public internet.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// cobra already holds the build version; the dashboard's Config
			// page shows it so a browser can tell which daemon is serving.
			opts.version = cmd.Root().Version
			return runServe(cmd.Context(), *opts)
		},
	}
	cfg := config.Read()
	f := cmd.Flags()
	f.StringVar(&opts.addr, "http", cfg.DashboardAddr(), "HTTP listen address for the dashboard")
	f.StringVar(&opts.publicURL, "public-url", cfg.Dashboard.PublicURL, "Externally-reachable URL (derived from Tailscale when unset)")
	f.StringVar(&opts.tailscaleMode, "tailscale", cfg.Dashboard.Tailscale.Mode, `Expose via Tailscale: "serve" (tailnet) or "funnel" (public)`)
	f.IntVar(&opts.tailscalePort, "tailscale-port", tailscalePortOr(cfg.Dashboard.Tailscale.Port), "Tailscale port (443, 8443, or 10000)")
	f.BoolVar(&opts.noSchedule, "no-schedule", false, "Serve the dashboard only; run neither loop")
	f.BoolVar(&opts.noDiscovery, "no-discovery", false, "Don't run the discovery loop this boot (overrides discovery.enabled)")
	f.BoolVar(&opts.noReviews, "no-reviews", false, "Don't run the review loop this boot (overrides schedule.enabled)")
	f.BoolVar(&opts.readOnly, "read-only", false, "Inspect-only: open the store read-only (safe alongside a running daemon) and run neither loop")
	root.AddCommand(cmd)
}

func runServe(ctx context.Context, opts serveOpts) error {
	cfg := config.Read()
	openFn := openStore
	if opts.readOnly {
		openFn = openStoreReadOnly
	}
	s, err := openFn(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Tee the daemon's log sink into a ring so the dashboard's Logs page can
	// show a live tail; stderr remains the durable copy.
	logs := logbuf.New(1000)
	logf := func(format string, args ...any) {
		stderrLogf(format, args...)
		logs.Addf(format, args...)
	}
	logf("serve: starting (pid %d)", os.Getpid())
	if opts.readOnly {
		logf("serve: read-only mode: store opened read-only, both loops disabled")
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	shutdown := newShutdownController(ctx, sigCh, logf)
	defer shutdown.stop()

	// Bring up the Tailscale tunnel (if requested) and derive the public URL.
	publicURL, tsDown, err := tailscale.Wire(shutdown.reviewCtx, opts.tailscaleMode, opts.tailscalePort, opts.addr, opts.publicURL)
	if err != nil {
		return err
	}
	if tsDown != nil {
		logf("tailscale %s: %s -> http://%s (will shut down on exit)", opts.tailscaleMode, publicURL, opts.addr)
		if opts.tailscaleMode == "funnel" {
			logf("warning: the dashboard has no auth: funnel exposes it (including queue add/reorder) to the public internet; prefer --tailscale serve unless that's intended")
		}
		defer func() { _ = tsDown() }()
	}

	// Poll Codex usage in the background so the dashboard can show remaining
	// quota without a subprocess per request.
	usageCache := usage.NewCache()
	go usageCache.Poll(shutdown.gracefulCtx, cfg.UsagePollInterval(), cfg.Review.Codex.Bin)

	running := runningLoops(opts, cfg)
	schedCfg := pinnedLoopConfig(running)
	dash := dashboard.NewServer(s, config.Read, running, usageCache, discover.CurrentUser, logs, opts.version)
	// Bind BEFORE the scheduler starts: the port doubles as the "one daemon
	// per address" guard, and the loops fire immediately on start; an
	// accidental second instance must die here, not after it has already
	// claimed a PR and spent an engine invocation.
	srv, err := startDashboard(opts.addr, dash, logf, shutdown.graceful)
	if err != nil {
		return err
	}
	schedDone, err := startScheduler(ctx, running, schedCfg, s, logf, usageCache.Get, shutdown)
	if err != nil {
		return err
	}

	<-shutdown.gracefulCtx.Done()
	forced := waitForScheduler(schedDone, shutdown.reviewCtx, logf)
	if forced {
		_ = srv.Close()
		return nil
	}
	logf("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// runningLoops resolves the per-boot switch state. Config supplies defaults;
// command flags only ever turn a loop off for this daemon process.
// --read-only forces both off: the loops can't claim or record against a
// read-only store.
func runningLoops(opts serveOpts, cfg config.Config) dashboard.Running {
	off := opts.noSchedule || opts.readOnly
	return dashboard.Running{
		Discovery: !off && !opts.noDiscovery && cfg.DiscoveryEnabled(),
		Review:    !off && !opts.noReviews && cfg.ScheduleEnabled(),
	}
}

// pinnedLoopConfig keeps the two loop switches stable for one daemon boot,
// while every other scheduler dial continues to reload from config.json.
func pinnedLoopConfig(running dashboard.Running) func() config.Config {
	return func() config.Config {
		c := config.Read()
		c.Discovery.Enabled = config.Bool(running.Discovery)
		c.Schedule.Enabled = config.Bool(running.Review)
		return c
	}
}

func startDashboard(addr string, dash *dashboard.Server, logf scheduler.Logf, stop func()) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dashboard: %w (is another serve instance already running?)", err)
	}
	srv := &http.Server{Addr: addr, Handler: dash.Handler()}
	go func() {
		logf("dashboard: listening on %s", addr)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("dashboard error: %v", err)
			stop()
		}
	}()
	return srv, nil
}

func startScheduler(ctx context.Context, running dashboard.Running, cfg func() config.Config, s store.Store, logf scheduler.Logf, usageFn scheduler.UsageFn, shutdown shutdownController) (<-chan error, error) {
	if !running.Discovery && !running.Review {
		logf("scheduler: both loops disabled (config discovery.enabled/schedule.enabled, or --no-schedule/--no-discovery/--no-reviews)")
		return nil, nil
	}
	// Warnings fold into the daemon log so they reach stderr AND the
	// dashboard's log ring, unlike run's structured-notice route.
	warnf := func(notice, hint string) { logf("warning: %s (%s)", notice, hint) }
	sched, err := buildScheduler(ctx, cfg, s, logf, warnf, usageFn)
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := sched.StartGraceful(shutdown.gracefulCtx, shutdown.reviewCtx)
		if err != nil && !errors.Is(err, context.Canceled) {
			logf("scheduler stopped: %v", err)
		}
		done <- err
	}()
	return done, nil
}

type shutdownController struct {
	gracefulCtx context.Context
	reviewCtx   context.Context
	graceful    func()
	stop        func()
}

func newShutdownController(ctx context.Context, signals <-chan os.Signal, logf scheduler.Logf) shutdownController {
	gracefulCtx, gracefulStop := context.WithCancel(ctx)
	reviewCtx, forceStop := context.WithCancel(ctx)
	go func() {
		select {
		case <-ctx.Done():
			gracefulStop()
			forceStop()
		case sig := <-signals:
			logf("shutdown: received %s: stopping discovery and review scheduling; waiting for in-flight reviewers. Press Ctrl-C again to force exit.", sig)
			gracefulStop()
			select {
			case <-ctx.Done():
				forceStop()
			case sig := <-signals:
				logf("shutdown: received %s again: force shutdown", sig)
				forceStop()
			case <-reviewCtx.Done():
			}
		}
	}()
	return shutdownController{
		gracefulCtx: gracefulCtx,
		reviewCtx:   reviewCtx,
		graceful:    gracefulStop,
		stop: func() {
			gracefulStop()
			forceStop()
		},
	}
}

func waitForScheduler(done <-chan error, forceCtx context.Context, logf scheduler.Logf) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	case <-forceCtx.Done():
		logf("shutdown: force shutdown without waiting for in-flight reviewers")
		return true
	}
}

func tailscalePortOr(port int) int {
	if port == 0 {
		return 443
	}
	return port
}

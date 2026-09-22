package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/shhac/lib-agent-mcp/tailscale"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/dashboard"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/doctor"
	"github.com/shhac/agent-code-review/internal/logbuf"
	"github.com/shhac/agent-code-review/internal/pricing"
	"github.com/shhac/agent-code-review/internal/review"
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
	f.IntVar(&opts.tailscalePort, "tailscale-port", cfg.TailscalePort(), "Tailscale port (443, 8443, or 10000)")
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

	ring := logbuf.New(1000)
	sinks := teeSinks(ring)
	logf := sinks.infof
	logf("serve: starting (pid %d)", os.Getpid())
	if opts.readOnly {
		logf("serve: read-only mode: store opened read-only, both loops disabled")
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, shutdownSignals...)
	defer signal.Stop(sigCh)
	shutdown := newShutdownController(ctx, sigCh, logf)
	defer shutdown.cancelAll()

	// Bring up the Tailscale tunnel (if requested) and derive the public URL.
	publicURL, tsDown, err := tailscale.Wire(shutdown.Force, opts.tailscaleMode, opts.tailscalePort, opts.addr, opts.publicURL)
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

	logBootDiagnostics(shutdown.Force, cfg, logf)
	usageCache := startPolls(shutdown.Graceful, cfg, s, logf)

	running := runningLoops(opts, cfg)
	dash := dashboard.NewServer(dashboard.Deps{
		Store:              s,
		Config:             config.Read,
		Running:            running,
		Usage:              usageCache,
		GHUser:             discover.CurrentUser,
		Logs:               ring,
		Version:            opts.version,
		TrustProxyIdentity: trustsProxyIdentity(opts.tailscaleMode),
	})
	// Bind BEFORE the scheduler starts: the port doubles as the "one daemon
	// per address" guard, and the loops fire immediately on start; an
	// accidental second instance must die here, not after it has already
	// claimed a PR and spent an engine invocation.
	srv, err := startDashboard(opts.addr, dash, logf, shutdown.beginGraceful)
	if err != nil {
		return err
	}
	// The cache was already keyed by engine so the dashboard could show both;
	// the floor now reads it the same way, because either engine can run.
	schedDone, err := startScheduler(ctx, running, config.Read, s, sinks, usageCache.Get, shutdown.Stop)
	if err != nil {
		return err
	}

	<-shutdown.Graceful.Done()
	forced := waitForScheduler(schedDone, shutdown.Force, logf)
	if forced {
		_ = srv.Close()
		return nil
	}
	logf("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// teeSinks sends the daemon's log to stderr and into a ring, so the
// dashboard's Logs page can show a live tail; stderr remains the durable copy.
func teeSinks(ring *logbuf.Ring) logSinks {
	return logSinks{
		infof: func(format string, args ...any) {
			stderrLogf(format, args...)
			ring.Addf(format, args...)
		},
		warnf: func(format string, args ...any) {
			stderrWarnf(format, args...)
			ring.Addf(format, args...)
		},
	}
}

// logBootDiagnostics reports, before the loops start, what would otherwise
// surface only later. Not fatal: the dashboard is still worth serving, a
// missing CLI may come back, and refusing to boot would be a worse failure
// than reviewing nothing. But the reason belongs in the log now rather than
// inferred later from a queue full of ERRORs.
func logBootDiagnostics(ctx context.Context, cfg config.Config, logf scheduler.Logf) {
	for _, c := range doctor.Blocking(doctor.Run(ctx, cfg)) {
		logf("preflight: %s FAILED: %s (%s)", c.Name, c.Detail, c.Hint)
	}
	// Keys nothing reads, warned separately because they are not blocking:
	// reviews run exactly as they would without them. Said at boot because a
	// key that is not in effect is otherwise indistinguishable from one that
	// is, and because the next config write drops it from the file -- this
	// line may be the last record of what it held.
	for _, problem := range config.UnknownKeyProblems() {
		logf("config: %s", problem)
	}
}

// startPolls starts the daemon's background polls, which run until ctx ends.
//
// Every engine's usage is polled so the dashboard can show remaining quota
// without a round trip per request, and so both engines can be compared when
// deciding which to run on. Bins resolve once at boot, like the loop switches.
//
// The model price table is the other poll: refreshed on its own slow
// interval, read from disk so a boot never waits on it, and purely an
// enrichment (only claude values its own runs; codex reports no cost at all,
// so its spend has to be derived from the rates).
func startPolls(ctx context.Context, cfg config.Config, s store.Store, logf scheduler.Logf) *usage.Cache {
	usageCache := usage.NewCache()
	for _, src := range usageSources(cfg) {
		go usageCache.Poll(ctx, cfg.UsagePollInterval(), src)
	}
	prices := pricing.Open(config.PricingCacheDir())
	go prices.Poll(ctx, logf, func() {
		backfillEstimates(ctx, prices, s, logf)
	})
	return usageCache
}

// trustsProxyIdentity says whether the dashboard may read the identity header
// Tailscale asserts. Funnel is public internet traffic that Tailscale attaches
// no identity to, so it must not. Serve (or no tunnel at all, which is
// loopback-only) is where the header means something.
func trustsProxyIdentity(tailscaleMode string) bool {
	return tailscaleMode != "funnel"
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

// usageSources lists every engine to meter. All of them, not just the
// configured one: the dashboard shows them side by side so an operator can
// see the engine they are NOT using has headroom before deciding to switch,
// and an engine that is missing or logged out reports that as its state
// rather than vanishing. The floor consults them the same way, per candidate:
// a group can name its own engine, so which account a review spends from is a
// per-candidate answer, not a global one.
func usageSources(cfg config.Config) []usage.Source {
	sources := make([]usage.Source, 0, len(review.Engines))
	for _, engine := range review.Engines {
		sources = append(sources, usage.Source{Engine: engine, Bin: cfg.BinFor(engine)})
	}
	return sources
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

func startScheduler(ctx context.Context, running dashboard.Running, cfg func() config.Config, s store.Store, sinks logSinks, usageFn scheduler.UsageFn, stop scheduler.Stop) (<-chan error, error) {
	if !running.Discovery && !running.Review {
		sinks.infof("scheduler: both loops disabled (config discovery.enabled/schedule.enabled, or --no-schedule/--no-discovery/--no-reviews)")
		return nil, nil
	}
	// Warnings fold into the daemon log so they reach stderr AND the
	// dashboard's log ring, unlike run's structured-notice route.
	warnf := func(notice, hint string) { sinks.warnf("%s (%s)", notice, hint) }
	sched, err := buildScheduler(ctx, cfg, s, sinks, warnf, usageFn)
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := sched.StartGraceful(stop, running.Discovery, running.Review)
		if err != nil && !errors.Is(err, context.Canceled) {
			sinks.warnf("scheduler stopped: %v", err)
		}
		done <- err
	}()
	return done, nil
}

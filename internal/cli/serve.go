package cli

import (
	"context"
	"errors"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/shhac/lib-agent-mcp/tailscale"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/dashboard"
	"github.com/shhac/agent-code-review/internal/discover"
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
	root.AddCommand(cmd)
}

func runServe(ctx context.Context, opts serveOpts) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Read()
	s, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// Bring up the Tailscale tunnel (if requested) and derive the public URL.
	publicURL, tsDown, err := tailscale.Wire(ctx, opts.tailscaleMode, opts.tailscalePort, opts.addr, opts.publicURL)
	if err != nil {
		return err
	}
	if tsDown != nil {
		stderrLogf("tailscale %s: %s -> http://%s (will shut down on exit)", opts.tailscaleMode, publicURL, opts.addr)
		if opts.tailscaleMode == "funnel" {
			stderrLogf("warning: the dashboard has no auth — funnel exposes it (including queue add/reorder) to the public internet; prefer --tailscale serve unless that's intended")
		}
		defer func() { _ = tsDown() }()
	}

	// Poll Codex usage in the background so the dashboard can show remaining
	// quota without a subprocess per request.
	usageCache := usage.NewCache()
	go usageCache.Poll(ctx, cfg.UsagePollInterval(), cfg.Review.Codex.Bin)

	// Config sets the default per loop; flags override for this process only.
	// --no-schedule remains the "neither loop" shorthand.
	running := dashboard.Running{
		Discovery: !opts.noSchedule && !opts.noDiscovery && cfg.Discovery.Enabled,
		Review:    !opts.noSchedule && !opts.noReviews && cfg.Schedule.Enabled,
	}
	// The scheduler reads the switches from config — apply the effective
	// (flag-overridden) state so it starts exactly the loops shown above.
	cfg.Discovery.Enabled = running.Discovery
	cfg.Schedule.Enabled = running.Review
	dash := dashboard.NewServer(s, config.Read, running, usageCache, discover.CurrentUser)
	srv := &http.Server{Addr: opts.addr, Handler: dash.Handler()}
	go func() {
		stderrLogf("dashboard: listening on %s", opts.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stderrLogf("dashboard error: %v", err)
			stop()
		}
	}()

	if running.Discovery || running.Review {
		sched, err := buildScheduler(ctx, cfg, s)
		if err != nil {
			return err
		}
		go func() {
			if err := sched.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				stderrLogf("scheduler stopped: %v", err)
			}
		}()
	} else {
		stderrLogf("scheduler: both loops disabled (config discovery.enabled/schedule.enabled, or --no-schedule/--no-discovery/--no-reviews)")
	}

	<-ctx.Done()
	stderrLogf("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func tailscalePortOr(port int) int {
	if port == 0 {
		return 443
	}
	return port
}

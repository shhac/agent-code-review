package usage

// Headroom is read through lib-agent-harness's session.Inspect, which asks
// each engine's own CLI over its native protocol (codex app-server's
// account/rateLimits/read, claude's get_usage control request) using the login
// that CLI already holds. Nothing here reads, sends, or stores a credential,
// and no model is invoked. This package used to do both halves itself: a
// hand-written codex JSON-RPC client, and a claude path that pulled the OAuth
// token out of the keychain to call an undocumented endpoint.

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/shhac/lib-agent-harness/session"
)

// inspect is the harness read, a variable so Fetch's own tests never start a
// real CLI.
var inspect = session.Inspect

// Fetch reads one snapshot from the source's engine. An unrecognised engine
// falls back to codex, matching NewEngine's default.
func Fetch(ctx context.Context, src Source) (Snapshot, error) {
	engine := session.Codex
	if src.Engine == "claude" {
		engine = session.Claude
	}
	in, err := inspect(ctx, session.Options{Engine: engine, Binary: src.Bin})
	return fromInspection(engine, in, err)
}

// windowIDs names the account-wide windows Snapshot models, in preference
// order. Inspect also reports windows these overlap (claude's per-model weekly
// limits, codex's other limit buckets); no floor acts on those, and letting
// one reach BelowFloor would pause reviews over a scoped limit the review
// never spends from. A codex CLI that predates per-limit buckets reports its
// one bucket as "default".
type windowIDs struct{ primary, secondary []string }

var accountWindows = map[session.Engine]windowIDs{
	session.Codex:  {primary: []string{"codex/primary", "default/primary"}, secondary: []string{"codex/secondary", "default/secondary"}},
	session.Claude: {primary: []string{"five_hour"}, secondary: []string{"seven_day"}},
}

var loginCommand = map[session.Engine]string{
	session.Codex:  "codex login",
	session.Claude: "claude auth login",
}

// fromInspection maps one read onto a Snapshot. Success is judged by the quota
// alone: Inspect joins the account and quota errors and returns whatever it
// did get, so a failed account read beside a good quota still meters.
func fromInspection(engine session.Engine, in session.Inspection, err error) (Snapshot, error) {
	if !in.Quota.Known() {
		return Snapshot{}, unavailable(engine, in, err)
	}
	ids := accountWindows[engine]
	return Snapshot{
		Plan:      in.Account.Plan,
		Primary:   pick(in.Quota.Windows, ids.primary),
		Secondary: pick(in.Quota.Windows, ids.secondary),
		FetchedAt: time.Now(),
	}, nil
}

// unavailable explains a read that produced no headroom. The harness keeps
// captured CLI output out of its error values, so their text is safe to show
// on the dashboard as it is. A login the CLI reports absent gets the one
// actionable hint.
func unavailable(engine session.Engine, in session.Inspection, err error) error {
	if in.Account.LoggedIn != nil && !*in.Account.LoggedIn {
		return fmt.Errorf("%s is not logged in; run `%s`", engine, loginCommand[engine])
	}
	if err != nil {
		return fmt.Errorf("%s usage: %w", engine, err)
	}
	return fmt.Errorf("%s reports no rate limits (%s)", engine, cmp.Or(in.Quota.Reason, "no reason given"))
}

// pick returns the first of ids the read reported. A window without a
// duration is skipped, since BelowFloor tells the 5-hourly window from the
// weekly one by it.
func pick(windows []session.QuotaWindow, ids []string) *Window {
	for _, id := range ids {
		for _, w := range windows {
			if w.ID == id && w.WindowMinutes != nil {
				return toWindow(w)
			}
		}
	}
	return nil
}

func toWindow(w session.QuotaWindow) *Window {
	out := &Window{WindowMins: int(*w.WindowMinutes)}
	if w.UsedPercent != nil {
		out.UsedPercent = *w.UsedPercent
	}
	if w.ResetsAt != nil {
		out.ResetsAt = w.ResetsAt.Unix()
	}
	return out
}

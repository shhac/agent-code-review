package config

// Engine resolution: which engine reviews a candidate, which block of settings
// is that engine's, and which engines a config can reach at all. The raw
// per-engine blocks live in schema.go; the review package applies each
// engine's own defaults on top of what this resolves.

import "slices"

// EngineNames lists the wired review engines, default first. It lives here
// rather than in the review package because config is the one package every
// consumer already imports: review, the CLI, the dashboard, and doctor all
// depend on config, and none of them can be depended on in return. Holding it
// the other way round forced Engine() to restate the default as a literal and
// needed a cross-package test to catch the two drifting.
//
// review re-exports this as review.Engines, so existing callers are unchanged.
var EngineNames = []string{"codex", "claude"}

// ResolvedEngine is the review engine id, defaulting to the first wired one.
// On ReviewSettings rather than Config because that is the value the callers
// which need it actually hold, and three of them were re-deriving the same
// `if engine == "" { engine = EngineNames[0] }` on top of Config.Engine.
func (r ReviewSettings) ResolvedEngine() string {
	if r.Engine != "" {
		return r.Engine
	}
	return EngineNames[0]
}

// Engine is the review engine id, defaulting to the first wired engine.
func (c Config) Engine() string { return c.Review.ResolvedEngine() }

// EngineCommon selects the named engine's shared dials. THE engine switch:
// the one place that maps a name to its settings, so adding a fourth engine
// is a case here rather than a hunt through five call sites that each
// re-derived `if engine == "claude"`.
//
// An unknown name falls back to the default engine's block, which keeps
// BinFor's long-standing behaviour for a name nothing recognises.
func (r *ReviewSettings) EngineCommon(engine string) *EngineCommon {
	if engine == "claude" {
		return &r.Claude.EngineCommon
	}
	return &r.Codex.EngineCommon
}

// BinFor is the named engine's configured binary, whether or not it is the
// engine currently selected. Callers that meter or diagnose EVERY engine need
// this.
func (c Config) BinFor(engine string) string {
	return c.Review.EngineCommon(engine).Bin
}

// ResolveBin is BinFor with the engine's own name as the default, which is
// what every caller that actually RUNS something needs. BinFor deliberately
// reports the unresolved value, so six call sites across five packages each
// re-applied `if bin == "" { bin = "codex" }`; this is that rule, once.
func (c Config) ResolveBin(engine string) string {
	return DefaultBin(engine, c.BinFor(engine))
}

// DefaultBin resolves a possibly-empty binary against an engine name, for the
// callers that hold only those two strings and no Config.
func DefaultBin(engine, bin string) string {
	if bin != "" {
		return bin
	}
	return engine
}

// defaultUsageFloor is the headroom every engine leaves for interactive work
// unless its block says otherwise: pause at 90% used.
const defaultUsageFloor = 10

// UsageFloors are the remaining-percentage floors for one engine's two
// windows, below which that engine's candidates are held (default 10 each; an
// explicit 0 disables that window's floor).
//
// On ReviewSettings rather than Config so the policy cascade reaches it for
// free: cfg.Review.UsageFloors(engine) is the base floor a panel reports, and
// cfg.Review.WithPolicy(p).UsageFloors(engine) is the one a candidate is
// actually held against.
func (r ReviewSettings) UsageFloors(engine string) (fiveH, oneW int) {
	f := r.EngineCommon(engine).UsageFloor
	return intOr(f.FiveHourPercent, defaultUsageFloor), intOr(f.OneWeekPercent, defaultUsageFloor)
}

// WithPolicy returns these settings with the policy's engine dials applied:
// what the driver for this candidate is actually built from. Only the resolved
// engine's model and effort are touched, so a policy naming claude cannot
// leave a stray model on the codex settings.
func (r ReviewSettings) WithPolicy(p Policy) ReviewSettings {
	if p.Engine != "" {
		r.Engine = p.Engine
	}
	applyFloors(&r, p.UsageFloor)
	applyDials(&r, p)
	return r
}

// applyFloors patches EVERY engine the policy names, not just the resolved
// one -- the deliberate opposite of applyDials below. A cohort's floor is
// keyed by engine precisely so that moving the cohort between engines does
// not change how much headroom it leaves on either.
func applyFloors(r *ReviewSettings, floors map[string]UsageFloorLimits) {
	for engine, f := range floors {
		r.EngineCommon(engine).UsageFloor.merge(f)
	}
}

// applyDials patches only the RESOLVED engine's model and effort, so a policy
// naming claude cannot leave a stray model on the codex settings.
func applyDials(r *ReviewSettings, p Policy) {
	if p.Model == "" && p.Effort == "" {
		return
	}
	e := r.EngineCommon(r.ResolvedEngine())
	if p.Model != "" {
		e.Model = p.Model
	}
	if p.Effort != "" {
		e.Effort = p.Effort
	}
}

// EngineFor is the engine that will actually review a candidate whose author
// resolved to this policy: the policy's own engine when it names one, else the
// configured default. Callers that need to ask something OF that engine (its
// usage headroom, its binary) go through here rather than checking p.Engine
// and forgetting the empty case.
func (c Config) EngineFor(p Policy) string {
	if p.Engine != "" {
		return p.Engine
	}
	return c.Engine()
}

// ReachableEngines lists every engine any candidate could actually be reviewed
// by: the configured default plus every engine a group or override names,
// deduplicated, default first. Doctor and boot validation probe this set
// rather than the configured engine alone (a typo in a rarely-used group would
// otherwise surface at 3am as an ERROR row) or every wired engine (which would
// fail a deploy over an engine nothing references).
func (c Config) ReachableEngines() []string {
	engines := []string{c.Engine()}
	add := func(name string) {
		if name != "" && !slices.Contains(engines, name) {
			engines = append(engines, name)
		}
	}
	for _, cohort := range c.Cohorts() {
		add(cohort.Engine)
	}
	for _, o := range c.Authors.Overrides {
		add(o.Engine)
	}
	return engines
}

// GroupsUsing names the groups and overrides that select engine, so a failing
// engine check can say who depends on it. The default engine is reported as
// "(default)".
func (c Config) GroupsUsing(engine string) []string {
	var users []string
	if c.Engine() == engine {
		users = append(users, "(default)")
	}
	for _, cohort := range c.Cohorts() {
		if cohort.Engine == engine {
			users = append(users, "group "+cohort.Name)
		}
	}
	for _, o := range c.Authors.Overrides {
		if o.Engine == engine {
			users = append(users, "override "+o.Handle)
		}
	}
	return users
}

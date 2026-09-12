package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/score"
)

func f(v float64) *float64 { return &v }
func b(v bool) *bool       { return &v }

func TestResolveScoringDefaultsWhenUnset(t *testing.T) {
	got := Config{}.ResolveScoring("owner/name")
	if got.Hash() != score.DefaultRules().Hash() {
		t.Errorf("an empty config should resolve to the shipped defaults")
	}
	if !(Config{}).ScoringEnabled("owner/name") {
		t.Error("scoring should be on by default")
	}
	if !(Config{}).UseGitattributes("owner/name") {
		t.Error("gitattributes should be consulted by default")
	}
}

func TestGlobalOverridesPatchDefaults(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		SizePoints:   f(50),
		AttemptDecay: f(0.9),
		Verdicts:     VerdictMultipliers{Commented: f(0.1)},
	}}
	got := c.ResolveScoring("owner/name")
	if got.SizePoints != 50 || got.AttemptDecay != 0.9 || got.Commented != 0.1 {
		t.Errorf("overrides not applied: %+v", got)
	}
	// Untouched fields keep their defaults.
	if got.Approved != score.DefaultRules().Approved || got.PieceLines != score.DefaultRules().PieceLines {
		t.Errorf("unset fields should keep their defaults: %+v", got)
	}
}

// An explicit zero is a real setting, which is the whole reason the
// multipliers are pointers.
func TestExplicitZeroIsNotUnset(t *testing.T) {
	c := Config{Scoring: ScoringSettings{Verdicts: VerdictMultipliers{Approved: f(0)}}}
	if got := c.ResolveScoring("r"); got.Approved != 0 {
		t.Errorf("approved = %v, want an explicit 0 to survive", got.Approved)
	}
}

// base: 0 must FAIL the base > 0 check rather than silently meaning 100.
func TestExplicitZeroSizePointsIsInvalidNotDefault(t *testing.T) {
	c := Config{Scoring: ScoringSettings{SizePoints: f(0)}}
	if got := c.ValidateScoring(); len(got) == 0 {
		t.Error("base: 0 should be reported as invalid, not read as unset")
	}
}

func TestRepoOverridesPatchGlobal(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		SizePoints: f(50),
		Repos: map[string]ScoringSettings{
			"owner/special": {SizePoints: f(200)},
		},
	}}
	if got := c.ResolveScoring("owner/special"); got.SizePoints != 200 {
		t.Errorf("repo size_points = %v, want 200", got.SizePoints)
	}
	if got := c.ResolveScoring("owner/other"); got.SizePoints != 50 {
		t.Errorf("other repo size_points = %v, want the global 50", got.SizePoints)
	}
}

// Repo identity is case-insensitive everywhere else in this config; a scoring
// override keyed "Owner/Name" must still narrow "owner/name".
func TestRepoOverrideLookupIsCaseInsensitive(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		Repos: map[string]ScoringSettings{"Owner/Name": {SizePoints: f(7)}},
	}}
	if got := c.ResolveScoring("owner/name"); got.SizePoints != 7 {
		t.Errorf("size_points = %v, want the override to match case-insensitively", got.SizePoints)
	}
}

// The dials of the previous ruleset are reported rather than obeyed. There is
// no honest translation: that model could express a policy this one
// deliberately cannot, so a config still carrying its keys is being scored
// under something other than what it says.
func TestRetiredKeysAreReported(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		Base:           f(100),
		ChurnExponent:  f(0.15),
		DeletionWeight: f(1.5),
		Curve:          "linear",
	}}
	problems := c.ValidateScoring()
	for _, want := range []string{"base", "churn_exponent", "deletion_weight", "curve", "size_points", "size_falloff", "removal_points_per_100"} {
		found := false
		for _, p := range problems {
			if strings.Contains(p, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("problems = %v, want one mentioning %q", problems, want)
		}
	}
	// And they change nothing: the resolved ruleset is the shipped one.
	if got := c.ResolveScoring(""); got.Hash() != score.DefaultRules().Hash() {
		t.Errorf("retired keys changed the resolved ruleset: %+v", got)
	}
}

func TestRepoExcludePathsReplace(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		ExcludePaths: []string{"global/**"},
		Repos:        map[string]ScoringSettings{"owner/name": {ExcludePaths: []string{"repo/**"}}},
	}}
	if got := c.ExcludePaths("owner/name"); len(got) != 1 || got[0] != "repo/**" {
		t.Errorf("ExcludePaths = %v, want only the repo's own", got)
	}
	if got := c.ExcludePaths("owner/other"); len(got) != 1 || got[0] != "global/**" {
		t.Errorf("ExcludePaths = %v, want the global list", got)
	}
}

func TestScoringCanBeDisabledPerRepo(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		Repos: map[string]ScoringSettings{"owner/noisy": {Enabled: b(false)}},
	}}
	if c.ScoringEnabled("owner/noisy") {
		t.Error("the repo override should disable scoring")
	}
	if !c.ScoringEnabled("owner/other") {
		t.Error("other repos should stay enabled")
	}
}

// An invalid ruleset must not wedge the reviewer: it scores at defaults. But
// it must also not be silent, which is ValidateScoring's job.
func TestInvalidRulesFallBackToDefaultsAndAreReported(t *testing.T) {
	c := Config{Scoring: ScoringSettings{AttemptDecay: f(5)}}
	if got := c.ResolveScoring("r"); got.Hash() != score.DefaultRules().Hash() {
		t.Error("an invalid ruleset should resolve to the defaults rather than be used")
	}
	problems := c.ValidateScoring()
	if len(problems) == 0 {
		t.Fatal("ValidateScoring said nothing about an invalid attempt_decay")
	}
	if !strings.Contains(problems[0], "attempt_decay") {
		t.Errorf("problem = %q, want it to name attempt_decay", problems[0])
	}
}

func TestValidateScoringReportsPerRepoProblems(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		Repos: map[string]ScoringSettings{"owner/name": {SizePoints: f(-1)}},
	}}
	problems := c.ValidateScoring()
	if len(problems) == 0 || !strings.Contains(problems[0], "scoring.repos.owner/name") {
		t.Errorf("problems = %v, want one naming the repo", problems)
	}
}

// A nested repos map would never be read; saying so beats ignoring it.
func TestNestedReposIsReported(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		Repos: map[string]ScoringSettings{
			"owner/name": {Repos: map[string]ScoringSettings{"other/repo": {}}},
		},
	}}
	problems := c.ValidateScoring()
	found := false
	for _, p := range problems {
		if strings.Contains(p, "nested") {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %v, want one about the nested repos map", problems)
	}
}

func TestValidateScoringSilentOnGoodConfig(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		SizePoints: f(100), AttemptDecay: f(0.5),
		Repos: map[string]ScoringSettings{"owner/name": {PieceLines: f(20)}},
	}}
	if got := c.ValidateScoring(); len(got) != 0 {
		t.Errorf("ValidateScoring() = %v, want nothing", got)
	}
}

// The document must survive a write/read cycle through the creds store's JSON,
// pointers and nested map included.
func TestScoringRoundTripsThroughJSON(t *testing.T) {
	in := Config{Scoring: ScoringSettings{
		Enabled:             b(true),
		SizePoints:          f(100),
		PieceLines:          f(40),
		SizeFalloff:         f(3.5),
		RemovalPointsPer100: f(25),
		Verdicts:            VerdictMultipliers{Approved: f(1), Commented: f(0.25), RequestedChanges: f(-0.25)},
		ExcludePaths:        []string{"vendor/**"},
		Repos:               map[string]ScoringSettings{"owner/name": {SizePoints: f(200)}},
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Config
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ResolveScoring("owner/name").SizePoints != 200 {
		t.Error("repo override lost in the round trip")
	}
	if out.ResolveScoring("owner/other").RequestedChanges != -0.25 {
		t.Error("a negative multiplier did not survive the round trip")
	}
}

// A config that has never heard of scoring must resolve to working defaults,
// which is every existing install on upgrade.
func TestAbsentScoringBlockIsValid(t *testing.T) {
	var out Config
	if err := json.Unmarshal([]byte(`{"repos":["owner/name"]}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := out.ResolveScoring("owner/name").Validate(); err != nil {
		t.Errorf("defaults should be valid: %v", err)
	}
	if got := out.ValidateScoring(); len(got) != 0 {
		t.Errorf("ValidateScoring() = %v, want nothing for an absent block", got)
	}
}

// mergeScoring is a row of near-identical `if over.X != nil { out.X = over.X }`
// branches, which is exactly the shape where assigning the wrong field is
// plausible and invisible: a per-repo override silently not applying, or
// applying to the wrong dial, changes what that repo's authors earn with no
// error anywhere. Every field is asserted individually, at the repo level.
func TestEveryFieldCanBeOverriddenPerRepo(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		// Global values, all distinct from both the defaults and the override.
		SizePoints: f(10), PieceLines: f(10), SizeFalloff: f(2.1), RemovalPointsPer100: f(1.1), AttemptDecay: f(0.11),
		Verdicts:         VerdictMultipliers{Approved: f(0.1), Commented: f(0.11), RequestedChanges: f(-0.11)},
		UseGitattributes: b(true),
		Repos: map[string]ScoringSettings{
			"owner/name": {
				SizePoints: f(77), PieceLines: f(77), SizeFalloff: f(7.7), RemovalPointsPer100: f(7.7), AttemptDecay: f(0.77),
				Verdicts:         VerdictMultipliers{Approved: f(7), Commented: f(0.7), RequestedChanges: f(-0.7)},
				UseGitattributes: b(false),
			},
		},
	}}

	got := c.ResolveScoring("owner/name")
	for _, tc := range []struct {
		field string
		got   float64
		want  float64
	}{
		{"size_points", got.SizePoints, 77},
		{"piece_lines", got.PieceLines, 77},
		{"size_falloff", got.SizeFalloff, 7.7},
		{"removal_points_per_100", got.RemovalPointsPer100, 7.7},
		{"attempt_decay", got.AttemptDecay, 0.77},
		{"verdicts.approved", got.Approved, 7},
		{"verdicts.commented", got.Commented, 0.7},
		{"verdicts.requested_changes", got.RequestedChanges, -0.7},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want the repo override's %v", tc.field, tc.got, tc.want)
		}
	}
	if c.UseGitattributes("owner/name") {
		t.Error("use_gitattributes: the repo override should have turned it off")
	}

	// And a repo with no entry keeps every global value, so the assertions
	// above are about the override and not about the defaults leaking.
	other := c.ResolveScoring("owner/other")
	if other.SizePoints != 10 || other.AttemptDecay != 0.11 || other.Approved != 0.1 || other.SizeFalloff != 2.1 {
		t.Errorf("an unoverridden repo should keep the global values, got %+v", other)
	}
	if !c.UseGitattributes("owner/other") {
		t.Error("use_gitattributes should stay on for an unoverridden repo")
	}
}

// A partial override patches only what it names.
func TestRepoOverrideLeavesUnnamedFieldsAlone(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		SizePoints: f(10),
		Verdicts:   VerdictMultipliers{Approved: f(3)},
		Repos:      map[string]ScoringSettings{"owner/name": {SizePoints: f(99)}},
	}}
	got := c.ResolveScoring("owner/name")
	if got.SizePoints != 99 {
		t.Errorf("size_points = %v, want the override's 99", got.SizePoints)
	}
	if got.Approved != 3 {
		t.Errorf("approved = %v, want the global 3 to survive a partial override", got.Approved)
	}
}

// CurrentRuleHashes is what a stale sweep compares each row against, so it has
// to carry an entry per overridden repo plus the default.
func TestCurrentRuleHashes(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		Repos: map[string]ScoringSettings{"owner/special": {SizePoints: f(500)}},
	}}
	got := c.CurrentRuleHashes()

	if got[""] == "" {
		t.Fatal("the default hash must always be present")
	}
	if got["owner/special"] == got[""] {
		t.Error("an overridden repo must hash differently from the default")
	}
	if got["owner/special"] != c.ResolveScoring("owner/special").Hash() {
		t.Error("the map must agree with ResolveScoring for that repo")
	}

	// With no overrides the map is just the default, which is what lets the
	// SQL collapse to a plain literal instead of an empty CASE.
	plain := Config{}.CurrentRuleHashes()
	if len(plain) != 1 || plain[""] == "" {
		t.Errorf("CurrentRuleHashes() = %v, want only the default", plain)
	}
}

// The three modes separate "stop doing the work" from "hide the results",
// because scoring's only ongoing cost is the per-review GitHub call.
func TestScoringModes(t *testing.T) {
	mode := func(m string) Config { return Config{Scoring: ScoringSettings{Mode: m}} }

	for _, tc := range []struct {
		mode             string
		scores           bool
		showsLeaderboard bool
	}{
		{ScoringEnabled, true, true},
		{ScoringLeaderboardOnly, false, true},
		{ScoringDisabled, false, false},
	} {
		c := mode(tc.mode)
		if got := c.ScoringEnabled("o/r"); got != tc.scores {
			t.Errorf("%s: ScoringEnabled = %v, want %v", tc.mode, got, tc.scores)
		}
		if got := c.LeaderboardVisible("o/r"); got != tc.showsLeaderboard {
			t.Errorf("%s: LeaderboardVisible = %v, want %v", tc.mode, got, tc.showsLeaderboard)
		}
	}

	// Unset means enabled.
	if !(Config{}).ScoringEnabled("o/r") {
		t.Error("an unset mode should score")
	}
}

// A typo must not silently stop recording points, which is the failure nobody
// would notice. It reads as enabled and is reported through doctor instead.
func TestUnknownModeReadsAsEnabledAndIsReported(t *testing.T) {
	c := Config{Scoring: ScoringSettings{Mode: "off"}}
	if !c.ScoringEnabled("o/r") {
		t.Error("an unrecognised mode must not quietly disable scoring")
	}
	problems := c.ValidateScoring()
	if len(problems) == 0 || !strings.Contains(problems[0], "mode") {
		t.Errorf("problems = %v, want one naming the mode", problems)
	}
}

// The pre-Mode switch keeps its meaning for a config written against it.
func TestLegacyEnabledFalseReadsAsDisabled(t *testing.T) {
	c := Config{Scoring: ScoringSettings{Enabled: b(false)}}
	if c.ScoringEnabled("o/r") || c.LeaderboardVisible("o/r") {
		t.Error("the legacy enabled:false must read as fully disabled")
	}
	// Mode wins when both are set.
	c.Scoring.Mode = ScoringEnabled
	if !c.ScoringEnabled("o/r") {
		t.Error("mode must win over the legacy switch")
	}
}

// Scoring can be paused for one noisy repo without touching the rest.
func TestScoringModePerRepo(t *testing.T) {
	c := Config{Scoring: ScoringSettings{
		Repos: map[string]ScoringSettings{"owner/noisy": {Mode: ScoringLeaderboardOnly}},
	}}
	if c.ScoringEnabled("owner/noisy") {
		t.Error("the override should stop scoring that repo")
	}
	if !c.LeaderboardVisible("owner/noisy") {
		t.Error("leaderboard-only must keep the standings visible")
	}
	if !c.ScoringEnabled("owner/other") {
		t.Error("other repos should be unaffected")
	}
}

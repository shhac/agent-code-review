package config

import (
	"strings"
	"testing"
)

// The getters quietly fall back to a default for a duration they cannot use,
// so the only place a hand-edited mistake surfaces is here (via doctor and
// the boot report). Each case is one a person could plausibly write.
func TestValidateDurations(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want string // substring of the one expected problem; "" means none
	}{
		"unset is fine":             {Config{}, ""},
		"zero where it disables":    {Config{Candidates: CandidateSettings{QuietPeriod: "0s"}}, ""},
		"zero turns the sweep off":  {Config{Review: ReviewSettings{WorkspaceRetention: "0s"}}, ""},
		"negative retention":        {Config{Review: ReviewSettings{WorkspaceRetention: "-1h"}}, "review.workspace_retention"},
		"zero where it cannot mean": {Config{Schedule: ScheduleSettings{Interval: "0s"}}, "must be positive"},
		"not a duration":            {Config{Candidates: CandidateSettings{ErrorBackoff: "15 minutes"}}, "not a Go duration"},
		"negative poll":             {Config{Dashboard: DashboardSettings{UsagePollInterval: "-5m"}}, "dashboard.usage_poll_interval"},
	} {
		t.Run(name, func(t *testing.T) {
			problems := tc.cfg.ValidateDurations()
			if tc.want == "" {
				if len(problems) != 0 {
					t.Errorf("problems = %v, want none", problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Errorf("problems = %v, want one mentioning %q", problems, tc.want)
			}
		})
	}
}

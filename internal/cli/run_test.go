package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// TestRunSummary pins the trailing record `run` prints after the outcome rows.
// It was extracted specifically so the stdout shape could be tested without a
// store or a scheduler, and then never was — while being the contract a cron
// entry or a wrapping script actually reads.
func TestRunSummary(t *testing.T) {
	t.Run("buckets outcomes by verdict", func(t *testing.T) {
		got := runSummary([]store.Review{
			{Verdict: "APPROVED"},
			{Verdict: "COMMENTED"},
			{Verdict: "COMMENTED"},
			{Verdict: "ERROR"},
		}, 90*time.Second)

		if got["outcomes"] != 4 {
			t.Errorf("outcomes = %v, want 4", got["outcomes"])
		}
		if got["duration_secs"] != 90 {
			t.Errorf("duration_secs = %v, want 90", got["duration_secs"])
		}
		by, ok := got["by_verdict"].(map[string]int)
		if !ok {
			t.Fatalf("by_verdict = %T, want map[string]int", got["by_verdict"])
		}
		if by["COMMENTED"] != 2 || by["APPROVED"] != 1 || by["ERROR"] != 1 {
			t.Errorf("by_verdict = %v", by)
		}
	})

	// An empty run still has to emit a well-formed record: a nil map marshals
	// to JSON null, which a consumer indexing into it would choke on.
	t.Run("an idle run emits an empty object, not null", func(t *testing.T) {
		got := runSummary(nil, time.Second)
		if got["outcomes"] != 0 {
			t.Errorf("outcomes = %v, want 0", got["outcomes"])
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var back map[string]any
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if back["by_verdict"] == nil {
			t.Errorf("by_verdict marshalled to null, want an empty object: %s", b)
		}
	})
}

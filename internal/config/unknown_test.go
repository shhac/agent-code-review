package config

import (
	"os"
	"strings"
	"testing"

	"github.com/shhac/lib-agent-cli/creds"
)

// Finding unknown keys is creds.Store.UnknownKeys, tested there against a toy
// schema. What is worth asserting here is the part that is ours: the sentence,
// and that the walk finds what it should in THIS schema.

// writeConfig puts a document on disk under an isolated XDG root.
func writeConfig(t *testing.T, doc string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath(), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The retired key must report as a migration rather than a shrug: "not a
// known key" is equally true of a typo and of a key we moved ourselves.
func TestUnknownKeyProblemsExplainARename(t *testing.T) {
	got := unknownKeyProblems([]creds.UnknownKey{{
		Path:  "schedule.usage_floor",
		Value: `{"5h_percent":30}`,
	}})[0]
	for _, want := range []string{"not a config key", "30", "codex.usage_floor", "claude.usage_floor", "1w_percent"} {
		if !strings.Contains(got, want) {
			t.Errorf("problem %q is missing %q", got, want)
		}
	}
}

// A key nobody recognises gets no migration hint, because we did not move it.
func TestUnknownKeyProblemsLeaveATypoUnexplained(t *testing.T) {
	got := unknownKeyProblems([]creds.UnknownKey{{Path: "schedlue.interval", Value: "30s"}})[0]
	if strings.Contains(got, "moved to") {
		t.Errorf("a typo must not claim to have moved: %q", got)
	}
	if !strings.Contains(got, "schedlue.interval") || !strings.Contains(got, "30s") {
		t.Errorf("problem %q must name the key and its value", got)
	}
}

// The shape a config that never tuned the floor actually has: an empty
// object. It still reports, because the key is still gone, but there is no
// value to claim was lost.
func TestUnknownKeyProblemsOmitAnEmptyValue(t *testing.T) {
	got := unknownKeyProblems([]creds.UnknownKey{{Path: "schedule.usage_floor"}})[0]
	if strings.Contains(got, "is not in effect") {
		t.Errorf("there is no value to report: %q", got)
	}
	if !strings.Contains(got, "moved to") {
		t.Errorf("the migration hint is still the point: %q", got)
	}
}

// The integration: the real schema, read from a real file. An embedded
// EngineCommon must not read as unknown, a cohort's own keys must be walked,
// and the annotations config init ships must not report as findings.
func TestUnknownKeysAgainstTheRealSchema(t *testing.T) {
	writeConfig(t, `{
	  "//why": "an annotation, not a finding",
	  "repos": ["o/r"],
	  "schedule": {"max_parallel": 4, "usage_floor": {"5h_percent": 30}},
	  "review": {"codex": {"bin": "codex", "max_resumes": 2, "turbo": true}},
	  "authors": {"groups": {"core": {"review": "approve", "vibes": "good"}}}
	}`)
	var paths []string
	for _, k := range UnknownKeys() {
		paths = append(paths, k.Path)
	}
	want := "authors.groups.core.vibes,review.codex.turbo,schedule.usage_floor"
	if got := strings.Join(paths, ","); got != want {
		t.Errorf("UnknownKeys = %v, want %v", got, want)
	}
}

// The shipped starter config must not warn about itself on first boot.
func TestStarterConfigHasNoUnknownKeys(t *testing.T) {
	writeConfig(t, string(starterJSON))
	if got := UnknownKeys(); len(got) != 0 {
		t.Errorf("the shipped starter config reports unknown keys: %+v", got)
	}
}

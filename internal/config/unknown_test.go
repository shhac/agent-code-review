package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func paths(keys []UnknownKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Path)
	}
	return out
}

func TestUnknownKeysFindsOnlyWhatTheSchemaLacks(t *testing.T) {
	doc := []byte(`{
	  "repos": ["o/r"],
	  "gh_user": "ada",
	  "schedule": {"interval": "30s", "usage_floor": {"5h_percent": 30}},
	  "review": {"engine": "codex", "codex": {"bin": "codex", "sandbox": "read-only", "turbo": true}},
	  "nonsense": 1
	}`)
	got := paths(unknownKeysIn(doc))
	want := []string{"nonsense", "review.codex.turbo", "schedule.usage_floor"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unknown keys = %v, want %v", got, want)
	}
}

// The starter config documents itself in "//note" entries the schema
// deliberately has no field for. Reporting those would make the shipped
// config warn about itself on first boot.
func TestUnknownKeysIgnoresAnnotations(t *testing.T) {
	if got := unknownKeysIn(starterJSON); len(got) != 0 {
		t.Errorf("the shipped starter config reports unknown keys: %v", paths(got))
	}
}

// Embedded structs are flattened by encoding/json, so EngineCommon's dials
// are spelled at the engine's own level and must not read as unknown.
func TestUnknownKeysFlattensEmbeddedStructs(t *testing.T) {
	doc := []byte(`{"review": {"claude": {"max_resumes": 2, "model": "opus", "usage_floor": {"1w_percent": 5}}}}`)
	if got := unknownKeysIn(doc); len(got) != 0 {
		t.Errorf("promoted fields reported as unknown: %v", paths(got))
	}
}

// A map's keys are data, not field names: a cohort may be called anything,
// but what is INSIDE it is still described by the schema.
func TestUnknownKeysDescendsIntoMapValues(t *testing.T) {
	doc := []byte(`{"authors": {"groups": {"core": {"review": "approve", "vibes": "good"}}}}`)
	got := paths(unknownKeysIn(doc))
	if !reflect.DeepEqual(got, []string{"authors.groups.core.vibes"}) {
		t.Errorf("unknown keys = %v, want the field inside the cohort", got)
	}
}

func TestUnknownKeysToleratesAnUnparseableDocument(t *testing.T) {
	if got := unknownKeysIn([]byte("{not json")); got != nil {
		t.Errorf("a corrupt file must report nothing, got %v", paths(got))
	}
}

// The renamed key is the reason the warning exists: it must name where the
// setting went, and carry the value, because the next write drops it.
func TestUnknownKeyProblemsExplainARename(t *testing.T) {
	doc := []byte(`{"schedule": {"usage_floor": {"5h_percent": 30, "weekly_percent": 25}}}`)
	problems := UnknownKeyProblems(unknownKeysIn(doc))
	if len(problems) != 1 {
		t.Fatalf("want one problem for the parent object, got %v", problems)
	}
	// The whole object is unknown, so it reports once with its contents
	// rather than once per window.
	if !strings.Contains(problems[0], "schedule.usage_floor") || !strings.Contains(problems[0], "30") {
		t.Errorf("problem %q must name the key and its value", problems[0])
	}
}

// The retired key must report as a migration, not a shrug: where the setting
// went, and what it was, since the next write drops it from the file.
func TestUnknownKeyProblemsNameTheReplacementKeys(t *testing.T) {
	doc := []byte(`{"schedule": {"usage_floor": {"5h_percent": 30, "weekly_percent": 25}}}`)
	got := UnknownKeyProblems(unknownKeysIn(doc))[0]
	for _, want := range []string{"not a config key", "30", "codex.usage_floor", "claude.usage_floor", "1w_percent"} {
		if !strings.Contains(got, want) {
			t.Errorf("problem %q is missing %q", got, want)
		}
	}
}

// The shape a config that never tuned the floor actually has: an empty
// object. It still reports, because the key is still gone, but there is no
// value to claim was lost.
func TestUnknownKeyProblemsOmitAnEmptyValue(t *testing.T) {
	got := UnknownKeyProblems(unknownKeysIn([]byte(`{"schedule": {"usage_floor": {}}}`)))
	if len(got) != 1 || strings.Contains(got[0], "is not in effect") {
		t.Errorf("problems = %v, want one line with no value clause", got)
	}
}

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

func TestReadUnknownReachesWhatTheSchemaCannot(t *testing.T) {
	writeConfig(t, `{"schedule": {"max_parallel": 2, "usage_floor": {"5h_percent": 30}}}`)

	got, ok := ReadUnknown("schedule.usage_floor")
	if !ok || !strings.Contains(got, "30") {
		t.Errorf("ReadUnknown = %q, %v; want the object it holds", got, ok)
	}
	if _, ok := ReadUnknown("schedule.nothing_here"); ok {
		t.Error("a path the document does not hold must report missing")
	}
	// Descending through a scalar is a miss, not a panic.
	if _, ok := ReadUnknown("schedule.max_parallel.deeper"); ok {
		t.Error("a path through a scalar must report missing")
	}
}

// The raw rewrite is the point: unsetting one unknown key must not take the
// annotations (or the other unknown keys) with it, the way a struct
// round-trip would.
func TestUnsetUnknownPreservesTheRestOfTheDocument(t *testing.T) {
	writeConfig(t, `{
	  "//note": "deliberate annotation",
	  "repos": ["o/r"],
	  "review": {"engine": "codex", "codex": {"turbo": true, "bin": "codex"}}
	}`)

	removed, err := UnsetUnknown("review.codex.turbo")
	if err != nil || !removed {
		t.Fatalf("UnsetUnknown = %v, %v; want removed", removed, err)
	}
	if _, ok := ReadUnknown("review.codex.turbo"); ok {
		t.Error("the key is still in the document")
	}
	if note, ok := ReadUnknown("//note"); !ok || note != "deliberate annotation" {
		t.Error("the annotation did not survive the write")
	}
	if cfg := Read(); len(cfg.Repos) != 1 || cfg.Review.Codex.Bin != "codex" {
		t.Errorf("known settings did not survive: %+v", cfg)
	}
}

func TestUnsetUnknownReportsAMissingKey(t *testing.T) {
	writeConfig(t, `{"repos": ["o/r"]}`)
	removed, err := UnsetUnknown("schedule.usage_floor")
	if err != nil || removed {
		t.Errorf("UnsetUnknown = %v, %v; want not-removed and no error", removed, err)
	}
}

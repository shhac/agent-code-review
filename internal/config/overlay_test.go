package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// These cover the ADOPTION, not the mechanism: creds.Store{Overlay: true}
// owns the merge and its rules, and tests them against a toy schema. What is
// worth asserting here is that THIS schema survives it -- an embedded
// EngineCommon, a cohort map, a rules array, and the 34 annotations config
// init ships -- and that the store is still opted in. Flip Overlay off and
// these fail, which is the point of keeping them.

// roundTrip writes cfg over doc and returns the stored file, which is what
// every one of these is really asserting about.
func roundTrip(t *testing.T, doc string, mutate func(*Config)) map[string]any {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath(), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Read()
	mutate(&cfg)
	if err := Write(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filePath())
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("wrote unparseable JSON: %v\n%s", err, data)
	}
	return stored
}

// The bug this whole layer exists for: `config init` writes an annotated
// file, and the first `config set` of anything used to strip every note in
// it, because Write marshalled the struct and the struct has no field for a
// comment.
func TestWriteKeepsAnnotations(t *testing.T) {
	doc := `{
	  "//why": "object commentary",
	  "gh_user": "ada",
	  "//gh_user_note": "who reviews are attributed to",
	  "review": {"codex": {"//bin_note": "which binary", "bin": "codex"}}
	}`
	stored := roundTrip(t, doc, func(c *Config) { c.MaxParallel() })

	if stored["//why"] != "object commentary" {
		t.Error("object-level commentary did not survive the write")
	}
	if stored["//gh_user_note"] == nil {
		t.Error("a pinned note did not survive the write")
	}
	codex := stored["review"].(map[string]any)["codex"].(map[string]any)
	if codex["//bin_note"] == nil {
		t.Error("a nested note did not survive the write")
	}
}

// A key from another version is not ours to delete either. It is also the
// thing the boot warning points at, so it has to still be there to look at.
func TestWriteKeepsUnknownKeys(t *testing.T) {
	doc := `{"schedule": {"max_parallel": 4, "usage_floor": {"5h_percent": 30}},
	         "review": {"codex": {"turbo": true}}}`
	stored := roundTrip(t, doc, func(c *Config) { c.Schedule.MaxParallel = 8 })

	schedule := stored["schedule"].(map[string]any)
	if schedule["max_parallel"] != float64(8) {
		t.Errorf("the edit did not land: %+v", schedule)
	}
	floor, ok := schedule["usage_floor"].(map[string]any)
	if !ok || floor["5h_percent"] != float64(30) {
		t.Errorf("an unknown key was dropped by an unrelated write: %+v", schedule)
	}
	codex := stored["review"].(map[string]any)["codex"].(map[string]any)
	if codex["turbo"] != true {
		t.Errorf("a nested unknown key was dropped: %+v", codex)
	}
}

// The other half of the rule, and the one that makes it precise: a SCHEMA key
// the struct no longer states was unset, and must go. Without this the
// overlay would preserve it and `config unset` would silently no-op.
func TestWriteDeletesWhatTheStructUnset(t *testing.T) {
	doc := `{"gh_user": "ada", "//gh_user_note": "kept", "store": {"path": "/tmp/x.duckdb"}}`
	stored := roundTrip(t, doc, func(c *Config) {
		c.GHUser = ""
		c.Store.Path = ""
	})

	if _, present := stored["gh_user"]; present {
		t.Error("an unset scalar must be removed, not preserved")
	}
	if stored["//gh_user_note"] == nil {
		t.Error("the note explaining a reset dial should outlive the reset")
	}
	if store, ok := stored["store"].(map[string]any); ok {
		if _, present := store["path"]; present {
			t.Error("an unset nested scalar must be removed")
		}
	}
}

// Map keys are data, not schema: the struct round-trips the whole map, so an
// entry it no longer lists was deleted. Inside an entry the element's schema
// applies again, so a stray key there is preserved like any other.
func TestWriteMapEntriesAreOwnedButTheirContentsAreNot(t *testing.T) {
	doc := `{"authors": {"groups": {
	  "core": {"review": "approve", "vibes": "immaculate"},
	  "gone": {"review": "comment"}
	}}}`
	stored := roundTrip(t, doc, func(c *Config) { delete(c.Authors.Groups, "gone") })

	groups := stored["authors"].(map[string]any)["groups"].(map[string]any)
	if _, present := groups["gone"]; present {
		t.Error("a cohort removed from the struct must not survive the write")
	}
	core, ok := groups["core"].(map[string]any)
	if !ok || core["vibes"] != "immaculate" {
		t.Errorf("an unknown key INSIDE an entry must survive: %+v", groups)
	}
}

// Arrays replace wholesale: their elements have no identity to merge on, so
// the struct's list is the answer. Pinned here because it is the one place
// the overlay does NOT preserve an unknown key, and that should be a decision
// rather than a surprise.
func TestWriteReplacesArraysWholesale(t *testing.T) {
	doc := `{"review": {"rules": [{"name": "r", "prompt": "p", "stray": 1}]}}`
	stored := roundTrip(t, doc, func(c *Config) {})

	rules := stored["review"].(map[string]any)["rules"].([]any)
	if _, present := rules[0].(map[string]any)["stray"]; present {
		t.Error("array elements are replaced, so this documents a change in behaviour")
	}
}

// Writing the same content twice must produce the same bytes, or every
// unrelated command churns the file.
func TestWriteIsIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath(), starterJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	var passes []string
	for range 3 {
		if err := Write(Read()); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filePath())
		if err != nil {
			t.Fatal(err)
		}
		passes = append(passes, string(data))
	}
	if passes[0] != passes[1] || passes[1] != passes[2] {
		t.Error("repeated writes of the same content changed the file")
	}
	// And the shipped annotations are all still there after three rewrites.
	if n := strings.Count(passes[2], `"//`); n < 30 {
		t.Errorf("only %d annotations survived three writes of the starter config", n)
	}
}

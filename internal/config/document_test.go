package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The layout rule, stated as the order keys come back in.
func TestOrderedKeysPinsNotesToTheirKeys(t *testing.T) {
	obj := map[string]any{
		"model":        "opus",
		"//model_note": "which model",
		"bin":          "claude",
		"//bin_note":   "which binary",
		"turbo":        true, // unknown to the schema, but just a key here
		"//why":        "object-level commentary",
		"args":         []any{},
	}
	want := []string{
		"//why",        // free-standing: commentary on the object, so it leads
		"args",         //
		"//bin_note",   // pinned
		"bin",          //
		"//model_note", // pinned
		"model",        //
		"turbo",        // sorts at its own name, AFTER the model pair
	}
	if got := orderedKeys(obj); !reflect.DeepEqual(got, want) {
		t.Errorf("orderedKeys =\n %v\nwant\n %v", got, want)
	}
}

// The property the rule exists for: a pinned pair is indivisible, so no key
// can sort between a note and the key it explains.
func TestOrderedKeysNeverSplitsAPinnedPair(t *testing.T) {
	// "bin_x" sorts between "bin" and "model", and "bim" just before "bin".
	obj := map[string]any{
		"bin": "c", "//bin_note": "n", "model": "m", "//model_note": "n",
		"bin_x": 1, "bim": 2, "zzz": 3,
	}
	keys := orderedKeys(obj)
	for i, key := range keys {
		target, pinned := pinnedTo(key, obj)
		if !pinned {
			continue
		}
		if i+1 >= len(keys) || keys[i+1] != target {
			t.Errorf("%q must sit immediately before %q, got order %v", key, target, keys)
		}
	}
}

// A note naming no sibling documents the object, not a key, so it leads
// rather than pretending to be a key called "why".
func TestPinnedToRequiresASibling(t *testing.T) {
	obj := map[string]any{"model": "opus", "//model_note": "n", "//why": "c"}
	if target, ok := pinnedTo("//model_note", obj); !ok || target != "model" {
		t.Errorf(`pinnedTo("//model_note") = %q, %v; want "model", true`, target, ok)
	}
	if _, ok := pinnedTo("//why", obj); ok {
		t.Error(`"//why" has no sibling "why", so it must not pin`)
	}
	// The suffix is optional: "//model" pins to "model" too.
	if target, ok := pinnedTo("//model", obj); !ok || target != "model" {
		t.Errorf(`pinnedTo("//model") = %q, %v; want "model", true`, target, ok)
	}
}

// Order is a pure function of the key set, so writing the same content twice
// produces the same bytes. Without this, every save would churn the file.
func TestDocumentMarshalIsStable(t *testing.T) {
	doc := document{
		"b": map[string]any{"y": 1, "//y_note": "n", "x": []any{map[string]any{"q": 1, "//q_note": "n"}}},
		"a": "first",
	}
	first, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var round map[string]any
	if err := json.Unmarshal(first, &round); err != nil {
		t.Fatal(err)
	}
	second, err := json.MarshalIndent(document(round), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-marshalling changed the bytes:\n%s\nvs\n%s", first, second)
	}
	// The ordering must reach inside arrays too: a rule or an override is an
	// object like any other.
	if !strings.Contains(string(first), `"//q_note"`) ||
		strings.Index(string(first), `"//q_note"`) > strings.Index(string(first), `"q"`) {
		t.Errorf("a note inside an array element must precede its key:\n%s", first)
	}
}

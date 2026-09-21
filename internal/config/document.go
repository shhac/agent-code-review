package config

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// How the config file is laid out when we write it.
//
// JSON has no comments, so the annotations in the starter config are ordinary
// keys: "//model_note" sits beside "model" and says what it is for. That is
// the npm "//" convention with a suffix, so that more than one note can live
// in an object -- JSON would not allow two keys both called "//".
//
// Those notes are the whole reason this type exists. A plain map marshals in
// Go's own key order, which is byte order, and "/" sorts below every letter
// and digit: every note in an object would sink to the top, away from the key
// it explains. So the order is chosen here instead.
//
// The rule: within an object, sort by SORT NAME. A plain key sorts as itself.
// A note sorts as the key it is pinned to, and ties break with the note first.
// Because a pinned pair shares one sort name, nothing can be sorted between
// them -- an unknown key lands at its own name, before or after the pair as a
// unit, never inside it.
//
// Order is therefore a pure function of the key set. Writing the same content
// twice produces the same bytes, and a note added by hand anywhere in an
// object moves next to its key on the next save.
type document map[string]any

// noteSuffix is the convention's trailing marker: "//model_note" annotates
// "model". Optional, so "//model" pins too.
const noteSuffix = "_note"

// notePrefix marks an annotation key. Two slashes rather than one, because a
// path-like key is a plausible thing to store and a comment is not.
const notePrefix = "//"

// pinnedTo reports the sibling key an annotation belongs to. A note naming no
// sibling is object-level commentary rather than a pin: "//why" in an object
// with no "why" key documents the object itself.
func pinnedTo(key string, siblings map[string]any) (string, bool) {
	if !strings.HasPrefix(key, notePrefix) {
		return "", false
	}
	name := strings.TrimPrefix(key, notePrefix)
	if target := strings.TrimSuffix(name, noteSuffix); target != name {
		if _, ok := siblings[target]; ok {
			return target, true
		}
	}
	if _, ok := siblings[name]; ok {
		return name, true
	}
	return "", false
}

// orderedKeys returns an object's keys in the order they are written:
// free-standing notes first, as the commentary on the object they are, then
// everything else by sort name with each pinned note ahead of its key.
func orderedKeys(obj map[string]any) []string {
	type entry struct {
		key      string
		sortName string
		pinned   bool // a note sorts ahead of the key it shares a name with
		loose    bool // commentary on the object, so it leads
	}
	entries := make([]entry, 0, len(obj))
	for key := range obj {
		e := entry{key: key, sortName: key}
		if target, ok := pinnedTo(key, obj); ok {
			e.sortName, e.pinned = target, true
		} else if strings.HasPrefix(key, notePrefix) {
			e.loose = true
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.loose != b.loose {
			return a.loose
		}
		if a.sortName != b.sortName {
			return a.sortName < b.sortName
		}
		if a.pinned != b.pinned {
			return a.pinned
		}
		return a.key < b.key
	})
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.key)
	}
	return keys
}

// MarshalJSON writes the object in that order. Compact: creds.Store hands the
// result to json.MarshalIndent, which re-indents a marshaler's output, so the
// file keeps its two-space shape without this knowing about it.
func (d document) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range orderedKeys(d) {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		value, err := marshalOrdered(d[key])
		if err != nil {
			return nil, err
		}
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalOrdered applies the same ordering at every depth, including objects
// inside arrays -- a rule or an override is an object like any other.
func marshalOrdered(v any) ([]byte, error) {
	switch value := v.(type) {
	case map[string]any:
		return document(value).MarshalJSON()
	case []any:
		var b bytes.Buffer
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteByte(',')
			}
			encoded, err := marshalOrdered(item)
			if err != nil {
				return nil, err
			}
			b.Write(encoded)
		}
		b.WriteByte(']')
		return b.Bytes(), nil
	default:
		return json.Marshal(v)
	}
}

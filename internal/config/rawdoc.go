package config

// Raw document access: the config file as the map it literally is, rather
// than as the struct it parses into.
//
// Everything else in this package goes through Read/Write, which is the right
// default and is exactly why this exists: the struct is the parsed SUBSET of
// the document, so anything the schema has no field for is invisible to it --
// and is dropped by the next Write, which marshals the struct. Naming a stray
// key (unknown.go), showing what it holds, or removing just that one while
// leaving the rest of the file intact all need the document itself.

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
)

// decodeDoc parses a config document into the map it literally is.
func decodeDoc(data []byte) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// readDoc loads the config file as a raw document. Callers differ on what a
// failure means -- the reporting paths treat a missing or corrupt file as
// nothing to say, the write path refuses -- so the error is returned rather
// than decided here.
func readDoc() (map[string]any, error) {
	data, err := os.ReadFile(filePath())
	if err != nil {
		return nil, err
	}
	return decodeDoc(data)
}

// ReadUnknown returns the raw value at a dotted path, for a key the schema
// has no field for. The registry's get cannot answer for these: it resolves
// against a fixed list of known keys, so a key that is in the FILE but not in
// the schema reports as if it were never written.
func ReadUnknown(path string) (string, bool) {
	doc, err := readDoc()
	if err != nil {
		return "", false
	}
	value, ok := lookupPath(doc, strings.Split(path, "."))
	if !ok {
		return "", false
	}
	return render(value), true
}

// UnsetUnknown removes a dotted path from the config document, reporting
// whether it was there. It rewrites the RAW document rather than the parsed
// struct, so everything the schema cannot see -- the "//note" annotations,
// and any other unknown key -- survives having one of them removed. Held
// under the store lock, like every other write.
func UnsetUnknown(path string) (bool, error) {
	var removed bool
	err := store().WithLock(func() error {
		doc, err := readDoc()
		if err != nil {
			return err
		}
		if removed = deletePath(doc, strings.Split(path, ".")); !removed {
			return nil
		}
		return store().Save(doc)
	})
	return removed, err
}

// lookupPath walks a dotted path through nested objects.
func lookupPath(doc map[string]any, path []string) (any, bool) {
	value, ok := doc[path[0]]
	if !ok {
		return nil, false
	}
	if len(path) == 1 {
		return value, true
	}
	child, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return lookupPath(child, path[1:])
}

// deletePath removes a dotted path, leaving its parents in place: an object
// that is empty afterwards was still written deliberately, and pruning it
// would delete more than was asked for.
func deletePath(doc map[string]any, path []string) bool {
	if len(path) == 1 {
		if _, ok := doc[path[0]]; !ok {
			return false
		}
		delete(doc, path[0])
		return true
	}
	child, ok := doc[path[0]].(map[string]any)
	if !ok {
		return false
	}
	return deletePath(child, path[1:])
}

// Writing: the struct's view laid OVER the stored document, rather than in
// place of it.
//
// Write used to marshal the struct and be done, which meant every write threw
// away everything the struct could not see: the "//note" annotations
// config init ships, and any key from another version. Editing one unrelated
// setting silently stripped all of it -- a config init file lost its entire
// documentation the first time anyone ran config set.
//
// The overlay keeps them. What makes it precise rather than a guess is
// knowing which keys the SCHEMA owns (see jsonFields): a schema key missing
// from the struct's view was unset and must go, while a key the schema has no
// field for was never ours to remove.

// overlay merges the struct's view of one object over what is stored there.
// stored may be nil, which is simply a first write.
func overlay(stored, fresh map[string]any, t reflect.Type) map[string]any {
	fields := jsonFields(t)
	out := make(map[string]any, len(fresh)+len(stored))

	// Everything the struct cannot speak for, kept as found. A schema key is
	// deliberately not copied: whether it survives is decided below, by
	// whether the struct still states it.
	for key, value := range stored {
		if _, isSchema := fields[key]; isSchema && !strings.HasPrefix(key, notePrefix) {
			continue
		}
		out[key] = value
	}

	for key, value := range fresh {
		field, known := fields[key]
		child, freshIsObject := value.(map[string]any)
		prior, storedIsObject := stored[key].(map[string]any)
		if !known || !freshIsObject || !storedIsObject {
			// A scalar, an array, or a fresh object with nothing under it to
			// preserve. Arrays replace wholesale: their elements have no
			// identity to merge on, so "the struct's list" is the answer.
			out[key] = value
			continue
		}
		switch next := deref(field); next.Kind() {
		case reflect.Struct:
			out[key] = overlay(prior, child, next)
		case reflect.Map:
			out[key] = overlayEntries(prior, child, deref(next.Elem()))
		default:
			out[key] = value
		}
	}
	return out
}

// overlayEntries merges a map-typed field, where the KEYS are data rather than
// schema: a cohort may be called anything. The struct round-trips the whole
// map, so an entry it no longer lists was deleted and does not survive --
// unlike an unknown key in a struct, which the schema never claimed. Inside an
// entry the element's own schema applies again, so a stray key there lives on.
func overlayEntries(stored, fresh map[string]any, elem reflect.Type) map[string]any {
	out := make(map[string]any, len(fresh))
	for key, value := range fresh {
		child, freshIsObject := value.(map[string]any)
		prior, storedIsObject := stored[key].(map[string]any)
		if freshIsObject && storedIsObject && elem.Kind() == reflect.Struct {
			out[key] = overlay(prior, child, elem)
			continue
		}
		out[key] = value
	}
	// Annotations sit beside the entries they describe, and the struct has no
	// field for them, so they are not entries the struct dropped.
	for key, value := range stored {
		if strings.HasPrefix(key, notePrefix) {
			if _, taken := out[key]; !taken {
				out[key] = value
			}
		}
	}
	return out
}

// structDoc is the config as the struct sees it: the subset that has fields.
func structDoc(cfg Config) (map[string]any, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return decodeDoc(data)
}

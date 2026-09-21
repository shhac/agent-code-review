package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
)

// Unknown keys: what the document says that the schema does not.
//
// Read tolerates them silently (encoding/json ignores fields it has no home
// for), which is right for reading and wrong as the only signal: a typo, a
// key from a newer version, or a key this version renamed all behave as if
// they were never written. The value is simply not in effect, and nothing
// says so.
//
// Worse, they are not merely inert: Write marshals the STRUCT, so the next
// `config set` of anything at all drops every unknown key from the file. A
// warning therefore has to carry the value, because by the time anyone
// investigates it may no longer be on disk.

// UnknownKey is one key in the document that the schema has no field for.
type UnknownKey struct {
	Path  string // dotted path as it would be typed, e.g. "schedule.usage_floor.5h_percent"
	Value string // its value, rendered compactly; empty for an empty object
	// Moved describes where this key's setting lives now, when it is one this
	// version renamed rather than one nobody recognises.
	Moved *rename
}

// rename is where a key we retired went. Note carries anything the
// replacement paths do not say on their own, such as a window key that was
// spelled differently under the old parent.
type rename struct {
	To   []string
	Note string
}

// renamedKeys maps a key this version no longer reads to the keys that
// replaced it, so a rename reports as a migration rather than as a shrug:
// "not a known key" is equally true of a typo and of a key we moved
// ourselves, and only one of those is ours to explain.
//
// Keyed by the path actually REPORTED. The walk stops at the outermost key
// the schema cannot account for, so a whole retired object reports once and a
// hint on one of its leaves would never fire.
var renamedKeys = map[string]*rename{
	"schedule.usage_floor": {
		To:   []string{"codex.usage_floor", "claude.usage_floor"},
		Note: `the weekly window's key is "1w_percent" there`,
	},
}

// UnknownKeys reports every key in the config file that the schema has no
// field for, sorted by path so the report is stable between runs. A missing
// or unparseable file reports nothing: Read already treats both as an empty
// config, and a parse error is a different complaint than a key nobody
// recognises.
func UnknownKeys() []UnknownKey {
	data, err := os.ReadFile(filePath())
	if err != nil {
		return nil
	}
	return unknownKeysIn(data)
}

// unknownKeysIn is UnknownKeys against bytes, so the walk tests without a
// filesystem.
func unknownKeysIn(data []byte) []UnknownKey {
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var found []UnknownKey
	walkUnknown(doc, reflect.TypeOf(Config{}), "", &found)
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found
}

// walkUnknown descends the document alongside the type that should describe
// it, collecting what the type cannot account for.
func walkUnknown(doc map[string]any, t reflect.Type, prefix string, found *[]UnknownKey) {
	fields := jsonFields(t)
	for key, value := range doc {
		// Annotation keys. The starter config documents itself in "//note"
		// entries, so the schema deliberately has no field for them and
		// reporting them would make the shipped config warn about itself.
		if strings.HasPrefix(key, "//") {
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		ft, ok := fields[key]
		if !ok {
			*found = append(*found, UnknownKey{Path: path, Value: render(value), Moved: renamedKeys[path]})
			continue
		}
		child, isObject := value.(map[string]any)
		if !isObject {
			continue
		}
		switch next := deref(ft); next.Kind() {
		case reflect.Struct:
			walkUnknown(child, next, path, found)
		case reflect.Map:
			// A map's keys are data, not field names (authors.groups.<name>),
			// so only its VALUES are described by the schema.
			for name, entry := range child {
				if sub, ok := entry.(map[string]any); ok {
					walkUnknown(sub, deref(next.Elem()), path+"."+name, found)
				}
			}
		}
	}
}

// jsonFields maps a struct's json names to their types, flattening embedded
// structs exactly as encoding/json does -- EngineCommon is embedded, so its
// dials are spelled at the engine's own level in the document.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	if t.Kind() != reflect.Struct {
		return out
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			for k, v := range jsonFields(deref(f.Type)) {
				out[k] = v
			}
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// render prints a value compactly enough for a log line: scalars as
// themselves, containers as their JSON. An empty object renders empty, which
// reads as "set, but holding nothing" in the report.
func render(v any) string {
	switch value := v.(type) {
	case nil:
		return "null"
	case string:
		return value
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%.6f", value), ".000000")
	case bool:
		return fmt.Sprintf("%t", value)
	case map[string]any:
		if len(value) == 0 {
			return ""
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(out)
}

// UnknownKeyProblems phrases the unknown keys as config problems, in the
// voice doctor and the boot preflight report everything else in. A key with a
// value says what that value was: the next write drops it from the file, so
// this line may be the last record of it.
func UnknownKeyProblems(keys []UnknownKey) []string {
	problems := make([]string, 0, len(keys))
	for _, k := range keys {
		var b strings.Builder
		fmt.Fprintf(&b, "%s is not a config key this version reads", k.Path)
		if k.Value != "" {
			fmt.Fprintf(&b, " (its value, %s, is not in effect)", k.Value)
		}
		if k.Moved != nil {
			fmt.Fprintf(&b, "; it moved to %s", strings.Join(k.Moved.To, " and "))
			if k.Moved.Note != "" {
				fmt.Fprintf(&b, " (%s)", k.Moved.Note)
			}
		}
		problems = append(problems, b.String())
	}
	return problems
}

// ReadUnknown returns the raw value at a dotted path, for a key the schema
// has no field for. The registry's get cannot answer for these: it resolves
// against a fixed list of known keys, so a key that is in the FILE but not in
// the schema reports as if it were never written.
func ReadUnknown(path string) (string, bool) {
	data, err := os.ReadFile(filePath())
	if err != nil {
		return "", false
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
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
		data, err := os.ReadFile(filePath())
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
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

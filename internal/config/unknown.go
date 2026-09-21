package config

import (
	"fmt"
	"strings"

	"github.com/shhac/lib-agent-cli/creds"
)

// Keys this version does not read, and what to say about them.
//
// Finding them is creds.Store.UnknownKeys: it walks the document against the
// schema's own json tags, so nothing here has to. What is ours is the
// SENTENCE -- specifically, whether a key nobody recognises is a typo or one
// we moved ourselves, which only this package knows.
//
// They are no longer destroyed by the next write: the store preserves what
// the struct cannot see (creds.Store.Overlay). So a warning can point at a
// key that will still be there when somebody goes to look, and `config unset`
// can remove it without taking the annotations with it.

// rename is where a key we retired went. Note carries anything the
// replacement paths do not say on their own, such as a window key spelled
// differently under the old parent.
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

// UnknownKeys is every key in the config file that the schema has no field
// for, sorted by path so a report is stable between runs.
func UnknownKeys() []creds.UnknownKey { return store().UnknownKeys(Config{}) }

// UnknownKeyProblems reports them already phrased as problems, in the voice
// doctor and the boot preflight report everything else in. One exported call
// rather than two, because both callers compose exactly this pair and the
// slice between them buys nothing outside the package.
func UnknownKeyProblems() []string { return unknownKeyProblems(UnknownKeys()) }

// unknownKeyProblems is the phrasing over keys already read, so it tests
// without a filesystem.
func unknownKeyProblems(keys []creds.UnknownKey) []string {
	problems := make([]string, 0, len(keys))
	for _, k := range keys {
		var b strings.Builder
		fmt.Fprintf(&b, "%s is not a config key this version reads", k.Path)
		if k.Value != "" {
			fmt.Fprintf(&b, " (its value, %s, is not in effect)", k.Value)
		}
		if moved := renamedKeys[k.Path]; moved != nil {
			fmt.Fprintf(&b, "; it moved to %s", strings.Join(moved.To, " and "))
			if moved.Note != "" {
				fmt.Fprintf(&b, " (%s)", moved.Note)
			}
		}
		problems = append(problems, b.String())
	}
	return problems
}

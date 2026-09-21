package cli

import (
	libcli "github.com/shhac/lib-agent-cli/cli"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

// The `config` command's escape hatch for keys that are in the FILE but not
// in the schema.
//
// libcli.ConfigCommand resolves against a fixed key list, so without this the
// one command that could show what a warned-about key held answers "unknown
// config key" -- true of the schema, and unhelpful about the document.
//
// Registering the stray keys as extra libcli.ConfigKeys would be the other
// route, and is worse: they would leak into `config list` and shell
// completion, and `config set <stray>` would change from "unknown config key"
// to "config key is read-only". Wrapping the generated RunE keeps all of that
// intact and changes only what happens after the library gives up.

// allowUnknownKeys lets `get` and `unset` reach a key the schema has no field
// for. `set` is deliberately NOT extended: writing a key nothing reads would
// manufacture the very state the warning exists to clear.
//
// The registry decides what "unknown" means -- the same slice handed to
// ConfigCommand, so the two can never disagree. An earlier version asked the
// library's error instead, classifying any FixableByAgent as an unknown key;
// but the library returns that same class when a KNOWN key's Unset fails, so
// a failed unset of a key whose CLI name equals its file path (store.path,
// schedule.max_parallel) would fall through to the raw-document path and
// delete it, reporting as success the write the library had just refused.
func allowUnknownKeys(cmd *cobra.Command, keys []libcli.ConfigKey) {
	known := make(map[string]bool, len(keys))
	for _, k := range keys {
		known[k.Name] = true
	}
	fallback := map[string]func(key, value string) error{"get": getUnknownKey, "unset": unsetUnknownKey}
	for _, sub := range cmd.Commands() {
		handle, ok := fallback[sub.Name()]
		if !ok {
			continue
		}
		sub.RunE = withUnknownKeyFallback(sub.RunE, known, handle)
	}
}

// withUnknownKeyFallback returns lib's RunE with one extra path: a key the
// registry does not hold, but the document does. Anything else -- a known
// key, or a name in neither -- runs the library's exact path, so its error
// text (including the list of valid keys, which is what an outright typo
// needs) and its output shape are untouched.
//
// Named rather than inlined so the rule is exercisable without building a
// cobra tree, and so the snapshot of the original RunE is structural: taking
// it as a parameter makes it impossible for the wrapper to reach for the
// field it is about to overwrite, which would loop forever.
func withUnknownKeyFallback(
	lib func(*cobra.Command, []string) error,
	known map[string]bool,
	handle func(key, value string) error,
) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		key := args[0] // cobra.ExactArgs(1) on every one of these subcommands
		if known[key] {
			return lib(c, args)
		}
		value, found := config.ReadUnknown(key)
		if !found {
			return lib(c, args)
		}
		return handle(key, value)
	}
}

// getUnknownKey reports a value the caller already read, so `config get` on a
// stray key parses the document once rather than twice.
func getUnknownKey(key, value string) error {
	return emit(map[string]any{"key": key, "set": true, "value": value, "known_key": false})
}

func unsetUnknownKey(key, _ string) error {
	removed, err := config.UnsetUnknown(key)
	if err != nil {
		return err
	}
	return emit(map[string]any{"key": key, "unset": removed, "known_key": false})
}

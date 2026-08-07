// Package config owns ~/.config/agent-code-review/config.json: the repos to
// watch, the approval allow-list, candidate age thresholds, schedule cadence,
// the review engine + prompt/rules, the DuckDB store location, and the
// dashboard/Tailscale settings. Everything the CLI treats as tunable lives
// here; no GitHub handles, repos, or prompts are hardcoded in code.
//
// The package is split by concern: schema.go (the on-disk structs), defaults.go
// (resolved getters that fill in zero values), validate.go (value validators +
// enums), and this file (locating and reading/writing the document).
package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shhac/lib-agent-cli/creds"
	"github.com/shhac/lib-agent-cli/xdg"
)

const appName = "agent-code-review"

// starterJSON is the annotated starter config written by `config init`. It is
// the same content as the repo's config.example.json (a test keeps them in
// lockstep).
//
//go:embed starter.json
var starterJSON []byte

// Dir is ~/.config/agent-code-review (respects XDG_CONFIG_HOME).
func Dir() string { return xdg.ConfigDir(appName) }

func filePath() string { return filepath.Join(Dir(), "config.json") }

func store() creds.Store { return creds.Store{Path: filePath()} }

// Read returns the parsed config, or a zero Config when the file is missing or
// unparseable; a corrupt file behaves like an empty one rather than wedging
// every command.
func Read() Config {
	var cfg Config
	if err := store().Load(&cfg); err != nil {
		return Config{}
	}
	return cfg
}

// Write persists the config (0600 file, 0700 dirs, via creds.Store).
func Write(cfg Config) error { return store().Save(cfg) }

// Update applies mutate to one current config snapshot, then persists it, with
// the whole read-mutate-write held under the store's exclusive lock so
// concurrent callers serialize instead of overwriting each other.
//
// Without the lock this raced: two invocations (say `repos add` alongside
// `rules add`, or the serve daemon's operator running either while a cycle is
// live) each read the same snapshot, and whichever wrote second erased the
// other's edit — an entire repo or rule silently gone from config.json.
//
// It wraps Read/Write rather than using creds.Store.Update so that Read's
// deliberate corrupt-file tolerance survives: Store.Update surfaces the unmarshal
// error, where Read treats an unparseable file as empty. Read has no cache to
// go stale, so reading inside the lock is genuinely a fresh load from disk.
func Update(mutate func(*Config) error) error {
	return store().WithLock(func() error {
		cfg := Read()
		if err := mutate(&cfg); err != nil {
			return err
		}
		return Write(cfg)
	})
}

// Path exposes the config file location for the `config path` command.
func Path() string { return filePath() }

// Init writes the annotated starter config, refusing to overwrite an existing
// file; `config init` must never clobber a live setup.
//
// The check and the write are one critical section under the store's lock,
// because separately they are a TOCTOU: `config init` could stat an empty
// directory, a concurrent `repos add` could create config.json, and init would
// then overwrite the new file with the starter. The lock also puts init on the
// same object every other writer contends for, so the stat sees a settled file.
//
// It writes the embedded bytes rather than going through creds.Store.Save,
// which would marshal a Config and drop the starter's comments and key order —
// the whole point of shipping an annotated file. WithLock creates the parent
// directory 0700, so the local MkdirAll it replaced is not needed.
func Init() (string, error) {
	path := filePath()
	err := store().WithLock(func() error {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("Config already exists at %s: edit it directly, or remove it first", path)
		}
		return os.WriteFile(path, starterJSON, 0o600)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// StarterJSON exposes the embedded starter for the lockstep test.
func StarterJSON() []byte { return starterJSON }

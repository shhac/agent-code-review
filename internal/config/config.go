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

// Update applies mutate to one current config snapshot, then persists it. It
// keeps every config command on the same read-once/write-once transaction
// shape without implying cross-process locking.
func Update(mutate func(*Config) error) error {
	cfg := Read()
	if err := mutate(&cfg); err != nil {
		return err
	}
	return Write(cfg)
}

// Path exposes the config file location for the `config path` command.
func Path() string { return filePath() }

// Init writes the annotated starter config, refusing to overwrite an existing
// file; `config init` must never clobber a live setup.
func Init() (string, error) {
	path := filePath()
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("Config already exists at %s: edit it directly, or remove it first", path)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, starterJSON, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// StarterJSON exposes the embedded starter for the lockstep test.
func StarterJSON() []byte { return starterJSON }

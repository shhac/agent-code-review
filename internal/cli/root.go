// Package cli assembles the agent-code-review root command on lib-agent-cli's
// shared scaffolding. The CLI is a queue manager + scheduler + dashboard for
// PR reviews: `serve` runs the daemon (scheduler + web UI + optional Tailscale),
// `run --once` performs a single cycle, and `queue` manages candidates by hand.
package cli

import (
	"context"

	libcli "github.com/shhac/lib-agent-cli/cli"
	_ "github.com/shhac/lib-agent-cli/yaml" // registers the --format yaml encoder
	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

type rootFlags struct {
	libcli.Globals
}

func newRootCmd(version string) *cobra.Command {
	g := &rootFlags{}
	root := libcli.NewRoot(libcli.Options{
		Use:           "agent-code-review",
		Short:         "PR review queue + scheduler for AI agents",
		Version:       version,
		Globals:       &g.Globals,
		DefaultFormat: output.FormatNDJSON,
		UnknownHint:   "run 'agent-code-review usage' to see the available commands",
	})

	registerServe(root)
	registerRun(root)
	registerQueue(root)
	registerAuthors(root)
	registerConfig(root)
	registerUsage(root)

	return root
}

// Run executes the CLI; errors render as {error, fixable_by, hint} on stderr
// with exit code 1.
func Run(version string) { libcli.Run(newRootCmd(version)) }

// openStore opens and initializes the configured store. Callers own Close.
func openStore(cfg config.Config) (store.Store, error) {
	s, err := store.Open(cfg.Store.Engine, cfg.StorePath())
	if err != nil {
		return nil, err
	}
	if err := s.Init(context.Background()); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

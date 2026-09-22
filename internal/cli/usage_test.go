package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The cards are embedded files now, so a renamed or emptied file would still
// compile and print nothing. Every usage command in the tree must say
// something, and every group the root card points at must have one.
func TestEveryUsageCardPrints(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := newRootCmd("test")
	printed := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			if child.Name() != "usage" {
				walk(child)
				continue
			}
			var buf bytes.Buffer
			child.SetOut(&buf)
			if err := child.RunE(child, nil); err != nil {
				t.Errorf("%s: %v", child.CommandPath(), err)
			}
			group := strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), root.Name()))
			if strings.TrimSpace(buf.String()) == "" {
				t.Errorf("%q usage printed nothing", group)
			}
			printed[group] = true
		}
	}
	walk(root)

	for _, group := range []string{"", "queue", "repos", "authors", "score", "prompts", "prompts rules", "config"} {
		if !printed[group] {
			t.Errorf("%q has no usage card", group)
		}
	}
}

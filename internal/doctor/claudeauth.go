package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
)

// claudeAuthStatus is what `claude auth status --json` reports: the one
// structured login probe Claude Code offers, read here for the preflight auth
// check. (The usage meter used to read it too, for a plan name; it now gets
// that from the same CLI through lib-agent-harness.)
type claudeAuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	SubscriptionType string `json:"subscriptionType"`
}

// errClaudeAuthUnreadable means the probe ran but its output did not parse,
// which is a different diagnosis from the probe failing to run at all.
var errClaudeAuthUnreadable = errors.New("claude auth status was not readable JSON")

// readClaudeAuthStatus runs the local auth probe. The CLI exits 0 while
// logged out and reports it in the payload, so callers must check LoggedIn
// rather than trusting the exit code.
func readClaudeAuthStatus(ctx context.Context, bin string) (claudeAuthStatus, error) {
	out, err := exec.CommandContext(ctx, bin, "auth", "status", "--json").Output()
	if err != nil {
		return claudeAuthStatus{}, err
	}
	var status claudeAuthStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return claudeAuthStatus{}, errClaudeAuthUnreadable
	}
	return status, nil
}

package review

// Static configuration checks: the combinations that would fail EVERY review
// without any single setting being invalid on its own. These live here rather
// than in the doctor package because they encode engine knowledge, and the
// engine's own package is where that has to stay accurate.

import (
	"fmt"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
)

// autoModeUnsupportedModels are model identifiers Claude Code's permission
// classifier cannot run against: auto mode needs Opus 4.6+, Sonnet 4.6+, or
// Fable 5. Matched as substrings so both aliases ("haiku") and full ids
// ("claude-haiku-4-5") are caught.
//
// Pairing one of these with auto mode is the nastiest misconfiguration this
// engine has: nothing rejects it at config time, and every review then fails
// identically at run time.
var autoModeUnsupportedModels = []string{
	"haiku", "claude-3", "sonnet-4-5", "sonnet-4-0", "opus-4-5", "opus-4-1", "opus-4-0",
}

// claudeAutoModeSupports reports whether auto mode can run against model.
// An empty model means the engine default, which is pinned to a supported one.
func claudeAutoModeSupports(model string) bool {
	if model == "" {
		model = defaultModel
	}
	for _, bad := range autoModeUnsupportedModels {
		if strings.Contains(model, bad) {
			return false
		}
	}
	return true
}

// Preflight reports configuration problems that would make every review fail,
// found without running one. Empty means nothing statically detectable is
// wrong; it cannot vouch for anything that only shows up at run time.
func Preflight(cfg config.ReviewSettings) []string {
	engine := cfg.Engine
	if engine == "" {
		engine = Engines[0]
	}
	if engine != "claude" {
		return nil
	}

	var problems []string
	mode := cfg.Claude.PermissionMode
	if mode == "" {
		mode = defaultPermissionMode
	}
	model := cfg.Claude.Model
	if mode == autoPermissionMode && !claudeAutoModeSupports(model) {
		problems = append(problems, fmt.Sprintf(
			"claude.model %q is not supported in %q permission mode (needs Opus 4.6+, Sonnet 4.6+, or Fable 5), so every review would fail; pin a supported model or switch claude.permission_mode to a static one",
			model, mode))
	}
	if mode == autoPermissionMode && len(cfg.Claude.AllowedTools) > 0 {
		problems = append(problems, fmt.Sprintf(
			"claude.allowed_tools is set (%s) while permission_mode is %q; allow rules resolve BEFORE the classifier, so those tools bypass the vetting auto mode exists to do",
			strings.Join(cfg.Claude.AllowedTools, ", "), mode))
	}
	return problems
}

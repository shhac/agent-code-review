package review

import (
	"cmp"
	"path/filepath"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/lib-agent-harness/native"
)

// newCodex drives `codex exec` non-interactively with the assembled prompt.
// codex reports its verdict through a file (--output-schema +
// --output-last-message); the session id and the token split come off the
// --json event stream, which the harness renders into the shared marker format
// as it goes.
func newCodex(c config.CodexSettings, resumePrompt string) *nativeEngine {
	return &nativeEngine{
		label:        "codex exec",
		cfg:          resolveCodex(c),
		maxResumes:   resolveMaxResumes(c.MaxResumes),
		resumePrompt: resumePrompt,
		template:     codexTemplate,
	}
}

// resolveCodex applies codex's defaults: the binary's own name, and the
// sandbox. Model and effort have none of ours; empty leaves them to codex.
func resolveCodex(c config.CodexSettings) native.Config {
	return native.Config{
		Engine: "codex",
		Binary: config.DefaultBin("codex", c.Bin),
		Model:  c.Model,
		Effort: c.Effort,
		// The agent needs to write scratch files and run gh; workspace-write
		// scopes that to the per-PR workdir.
		Sandbox: cmp.Or(c.Sandbox, "workspace-write"),
		Args:    c.Args,
	}
}

// codexTemplate hands codex the schema as a file and names the file its
// report lands in. The harness clears that file before every invocation,
// resume included, so a failed turn cannot reuse an earlier verdict.
func codexTemplate(workDir string) (native.Request, error) {
	schemaPath, err := writeVerdictSchema(workDir)
	if err != nil {
		return native.Request{}, err
	}
	return native.Request{SchemaPath: schemaPath, OutputPath: filepath.Join(workDir, "verdict.json")}, nil
}

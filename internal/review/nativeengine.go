package review

import (
	"context"

	"github.com/shhac/lib-agent-harness/native"
)

// nativeEngine is the one review driver, and codex.go and claude.go are its two
// configurations. The agent performs the review itself (posting to GitHub and
// running any post-approve steps) and then REPORTS BACK what it did through the
// shared verdict schema, which Review parses into a Verdict. The engine never
// posts the review; it only launches the agent and reads the report.
//
// Everything that differs between the CLIs is data: the resolved harness
// config, how the schema goes in and the report comes out (the request
// template), and which directory the process runs in. lib-agent-harness turns
// that into argv, runs it, and normalises the stream.
type nativeEngine struct {
	label        string // names the invocation in error text: "codex exec", "claude -p"
	cfg          native.Config
	maxResumes   int
	resumePrompt string

	// template builds what every invocation of one review shares, from its
	// workspace. It may write into the workspace, so it runs once per review.
	template func(workDir string) (native.Request, error)
}

func (e *nativeEngine) Provenance(ctx context.Context) Provenance {
	return Provenance{Engine: e.cfg.Engine, Model: e.cfg.Model, Effort: e.cfg.Effort, EngineVersion: e.version(ctx)}
}

// version probes the CLI's --version uncached: the engine is rebuilt from live
// config for every candidate and reviews take minutes, so one cheap exec per
// Provenance call needs no cache (and recording the version at review end
// stays accurate across a mid-cycle upgrade). "" on a failed probe.
func (e *nativeEngine) version(ctx context.Context) string {
	version, _ := native.Version(ctx, e.cfg)
	return version
}

func (e *nativeEngine) Review(ctx context.Context, req Request) (Verdict, error) {
	workDir, err := prepareWorkspace(req.WorkDir)
	if err != nil {
		return Verdict{Decision: DecisionError}, err
	}
	template, err := e.template(workDir)
	if err != nil {
		return Verdict{Decision: DecisionError}, err
	}
	template.WorkDir = workDir

	sink, buf, closeSink := newAgentSink(workDir)
	defer closeSink()

	// One stream spans every invocation of the review, so a resumed run keeps
	// appending to the same transcript and its usage accumulates by the
	// engine's own rule.
	stream, _ := native.NewStream(e.cfg.Engine, sink, native.StreamOptions{Structured: true})
	cfg := e.cfg
	var latest native.Result
	invoke := func(session, prompt string) (err error) {
		r := template
		r.ResumeSession, r.Prompt = session, prompt
		latest, err = native.Run(ctx, cfg, r, stream)
		return err
	}

	return resumableRun{
		engine: e.label,
		max:    e.maxResumes,
		start: func() error {
			// An interrupted attempt left a live session; continuing it costs
			// the nudge instead of the whole review again.
			if req.ResumeSession != "" {
				return invoke(req.ResumeSession, e.resumePrompt)
			}
			return invoke("", req.Prompt+reportingInstruction)
		},
		resume: func(id string) error { return invoke(id, e.resumePrompt) },
		report: func() (Verdict, error) { return parseVerdict(latest.Report) },
		raw:    buf.String,
		stream: stream,
	}.do()
}

package review

// This file is the engine-agnostic half of a review driver. Every engine
// reports through the same verdict contract (the output schema and the
// reporting instruction appended to the prompt), tees its transcript into the
// same workdir log, and yields to the same bounded resume policy when a run
// ends before reporting a real outcome. codex.go and claude.go supply only
// what genuinely differs: how to spawn the CLI, and how to read a session id
// and a report back out of its output.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// verdictSchema constrains the agent's report. codex applies the schema to
// EVERY assistant message in a run, not just the final one, so WORKING exists
// as the honest value for intermediate progress notes; without it the agent
// overloads SKIPPED for "I'm still investigating". claude applies it only to
// the final structured output, where WORKING instead means the run stopped
// early. ERROR is deliberately absent: it is a driver's own value for "the
// invocation failed", never something the agent reports.
const verdictSchema = `{
  "type": "object",
  "properties": {
    "decision": {
      "type": "string",
      "enum": ["WORKING", "APPROVED", "COMMENTED", "REQUESTED_CHANGES", "SKIPPED"],
      "description": "WORKING = you are not finished yet (use for every intermediate progress note; NEVER as your final message). The rest report what you actually did: APPROVED = submitted an approving review; COMMENTED = left a review or comments without approving; REQUESTED_CHANGES = submitted a request-changes review; SKIPPED = did not review this PR."
    },
    "summary": {
      "type": "string",
      "description": "One or two sentences: your progress note (WORKING), or what you did and why (final message)."
    }
  },
  "required": ["decision", "summary"],
  "additionalProperties": false
}`

// reportingInstruction is appended to every prompt so the agent knows its final
// message is a machine-read report, not prose.
const reportingInstruction = `

Every message you emit matches the provided output schema. While you are still working, use {"decision": "WORKING", "summary": "<progress note>"} for intermediate updates. When you are completely finished, your FINAL message must report the outcome: {"decision": "APPROVED"|"COMMENTED"|"REQUESTED_CHANGES"|"SKIPPED", "summary": "..."}. The final decision must reflect what you ACTUALLY did on GitHub: APPROVED only if you submitted an approving review, COMMENTED if you left a review or comments without approving, REQUESTED_CHANGES if you submitted a request-changes review, SKIPPED if you did not review this PR (explain why in the summary). Never end on WORKING.`

// agentLogName is the live log file every engine tees its output into inside
// the review workdir; consumers locate it through LogPath.
const agentLogName = "agent.log"

// engineWaitDelay bounds how long a forced shutdown waits after killing the
// engine's process group before giving up on draining its output. Generous,
// because the normal path never reaches it: it only applies once cancellation
// has already killed the group, and exists so one wedged descendant still
// holding the transcript pipe cannot hang the daemon's exit.
const engineWaitDelay = 10 * time.Second

// appendPositionals adds a command's positional arguments behind a "--"
// terminator. Both drivers end their argv this way.
//
// This is load-bearing, not decoration. claude's `--allowedTools` is VARIADIC
// (`<tools...>`), so it keeps consuming argv until the next flag: with the
// prompt appended plainly it was swallowed as one more tool name and every
// review in a static permission mode died on "Input must be provided either
// through stdin or as a prompt argument".
//
// A terminator rather than a reordering, because ordering only fixes the flags
// we ship today. Both drivers end their flag list with the user's own
// codex.args / claude.args, which may hold any flag at all, including another
// variadic one (codex has `-i/--image` today). After "--" nothing downstream
// can claim a positional, and a prompt starting with "-" stops being
// ambiguous too. Verified against both live CLIs: `codex exec`, `codex exec
// resume` and `claude -p` all parse identically with and without it.
func appendPositionals(args []string, positionals ...string) []string {
	return append(append(args, "--"), positionals...)
}

// LogPath locates the review agent's live log inside its workspace. The
// engine tees its output there as the run progresses; the CLI's `queue log`
// and the dashboard's per-review page both tail it through this one contract.
// Engines that don't natively emit a readable transcript render one into this
// same shape rather than inventing a second format (see claude.go).
func LogPath(workDir string) string {
	return filepath.Join(workDir, agentLogName)
}

// newAgentSink builds the writer engine output streams into: an in-memory
// buffer (it feeds Verdict.Raw for error surfacing) teed into the workdir's
// live agent log (see LogPath) as the run progresses, so the CLI's
// `queue log` and the dashboard's per-review page can watch it. A workspace
// that can't hold the log file degrades to buffer-only; diagnostics must
// survive even when the live view can't.
func newAgentSink(workDir string) (io.Writer, *bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	logFile, err := os.Create(LogPath(workDir))
	if err != nil {
		return buf, buf, func() {}
	}
	return io.MultiWriter(buf, logFile), buf, func() { _ = logFile.Close() }
}

// errEndedOnWorking marks a run whose final report was an intermediate
// WORKING note: the agent yielded early, and the driver may resume it.
var errEndedOnWorking = errors.New("agent ended on an intermediate WORKING report (run truncated?)")

// parseVerdict validates one agent report against the decision vocabulary.
func parseVerdict(data []byte) (Verdict, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Verdict{}, fmt.Errorf("empty verdict report")
	}
	var v Verdict
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return Verdict{}, fmt.Errorf("parse verdict report: %w", err)
	}
	switch v.Decision {
	case DecisionApproved, DecisionCommented, DecisionRequestedChanges, DecisionSkipped:
		return v, nil
	case DecisionWorking:
		// WORKING is only legal mid-run; ending on it means the run stopped
		// short before a real outcome was reported.
		return Verdict{}, errEndedOnWorking
	default:
		return Verdict{}, fmt.Errorf("verdict report has invalid decision %q", v.Decision)
	}
}

// defaultMaxResumes bounds the resume-on-WORKING nudges per review when an
// engine's max_resumes setting is unset.
const defaultMaxResumes = 2

// resolveMaxResumes applies the shared nil-or-negative-means-default policy to
// an engine's max_resumes setting. Both drivers carry the same *int and the
// same rule, so it lives beside the default it falls back to.
func resolveMaxResumes(configured *int) int {
	if configured != nil && *configured >= 0 {
		return *configured
	}
	return defaultMaxResumes
}

// resumableRun is one review expressed as the parts the resume policy needs.
// Every field is engine-specific; the policy that combines them is not, which
// is the whole reason this lives here instead of in each driver.
type resumableRun struct {
	engine string // names the engine in error text
	max    int    // resume attempts allowed

	start    func() error                 // the initial invocation; error means the process failed
	resume   func(sessionID string) error // one nudge against an existing session
	report   func() (Verdict, error)      // latest report; errEndedOnWorking when the agent yielded early
	session  func() string                // session id to resume, "" when the run didn't expose one
	raw      func() string                // full transcript, for Verdict.Raw and error surfacing
	cost     func() float64               // total API-rate valuation, nil when the engine reports none
	usage    func() TokenUsage            // token spend across every invocation, nil when unknown
	rawUsage func() string                // engine usage payloads verbatim, nil when the engine exposes none
}

// do drives the initial invocation and, when a clean exit's report is WORKING
// (the agent yielded its turn without a tool call and the CLI took that as
// the final answer, so the session is intact and nothing was posted), resumes
// with a nudge up to max times instead of burning the whole run as an ERROR.
// The finished run resolves through resolve, so callers see one ordinary
// (Verdict, error).
func (r resumableRun) do() (Verdict, error) {
	runErr := r.start()
	verdict, parseErr := r.report()
	for resumed := 0; resumed < r.max && runErr == nil && errors.Is(parseErr, errEndedOnWorking); resumed++ {
		sessionID := r.session()
		if sessionID == "" {
			break
		}
		runErr = r.resume(sessionID)
		verdict, parseErr = r.report()
	}
	return r.resolve(verdict, parseErr, runErr)
}

// resolve applies the precedence rules for one finished run: a valid report
// wins even over a non-zero exit (a partial run may still have reported its
// outcome); otherwise the process failure, and last a clean exit that never
// produced a report.
// A failed run still carries its spend: the tokens and cost were incurred
// whether or not a report came back, and an ERROR that hides what it cost is
// exactly the row you want to see when the budget looks wrong.
func (r resumableRun) resolve(verdict Verdict, parseErr, runErr error) (Verdict, error) {
	raw, cost, usage, rawUsage := r.raw(), r.costUSD(), r.tokenUsage(), r.usageRaw()
	if parseErr == nil {
		verdict.Raw = raw
		verdict.CostUSD = cost
		verdict.Tokens = usage
		verdict.UsageRaw = rawUsage
		return verdict, nil
	}
	failed := Verdict{Decision: DecisionError, Raw: raw, CostUSD: cost, Tokens: usage, UsageRaw: rawUsage}
	if runErr != nil {
		return failed, fmt.Errorf("%s: %w", r.engine, runErr)
	}
	return failed, fmt.Errorf("%s succeeded but no verdict report: %w", r.engine, parseErr)
}

// tokenUsage reads the run's token spend, treating an absent accessor as
// "this engine reports none" rather than requiring a driver to supply a stub.
func (r resumableRun) tokenUsage() TokenUsage {
	if r.usage == nil {
		return TokenUsage{}
	}
	return r.usage()
}

// costUSD does the same for the run's API-rate valuation, which only some
// engines report at all.
// usageRaw reads the engine's verbatim usage payloads, absent accessor meaning
// the engine exposes none.
func (r resumableRun) usageRaw() string {
	if r.rawUsage == nil {
		return ""
	}
	return r.rawUsage()
}

func (r resumableRun) costUSD() float64 {
	if r.cost == nil {
		return 0
	}
	return r.cost()
}

// joinRawUsage renders the collected per-invocation usage payloads as one
// JSON array. "" when the engine reported none, so an absent value stays
// absent in the store rather than becoming a misleading empty array.
func joinRawUsage(payloads []json.RawMessage) string {
	if len(payloads) == 0 {
		return ""
	}
	out, err := json.Marshal(payloads)
	if err != nil {
		return ""
	}
	return string(out)
}

// prepareWorkspace resolves the review's workspace, creating a temp one when
// the caller supplied none, and writes the shared verdict schema into it.
// Both drivers hand the schema to their CLI as a file path.
func prepareWorkspace(workDir string) (string, error) {
	if workDir != "" {
		return workDir, nil
	}
	return os.MkdirTemp("", "agent-code-review-")
}

// writeVerdictSchema puts the output schema on disk for the engine that takes
// a PATH to one. Split out because claude takes the schema inline and never
// reads a file, so writing it unconditionally made a workspace that could not
// hold it fail claude reviews over something claude does not use.
func writeVerdictSchema(workDir string) (string, error) {
	path := filepath.Join(workDir, "verdict.schema.json")
	if err := os.WriteFile(path, []byte(verdictSchema), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// logSessionPattern matches the session-id banner both transcoders render at
// the top of every invocation.
var logSessionPattern = regexp.MustCompile(`(?m)^session id: (\S+)\s*$`)

// maxLogScan bounds how much of a previous agent log is read looking for a
// session to resume. Generous next to a real review log, and a ceiling either
// way so a runaway log cannot be pulled into memory at claim time.
const maxLogScan = 8 << 20

// SessionFromLog recovers the engine session a previous attempt was running,
// from the transcript it left in workDir. "" when there is nothing to resume:
// no log, no banner, or a log too mangled to read.
//
// This is what lets an interrupted review continue instead of starting over.
// The transcript is the only place the session id survives a daemon death —
// it lives in the transcoder's memory otherwise — which is a good reason for
// both engines to render it into the shared format rather than keep it.
//
// The LAST banner wins: a log that already covers a resume holds several, and
// the most recent names the session still open.
func SessionFromLog(workDir string) string {
	f, err := os.Open(LogPath(workDir))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxLogScan))
	if err != nil {
		return ""
	}
	matches := logSessionPattern.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		return ""
	}
	return string(matches[len(matches)-1][1])
}

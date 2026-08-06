package review

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
)

func logWith(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(LogPath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The session id lives in the transcoder's memory and dies with the daemon.
// The transcript is the only place it survives, which is what lets an
// interrupted review continue instead of paying for the whole thing again.
func TestSessionFromLogRecoversAnInterruptedSession(t *testing.T) {
	codex := logWith(t, "session id: 019f6f77-3c3d-7ce3-966d-d4b2083f4459\nuser\nReview it\nexec\ngh pr diff\n")
	if got := SessionFromLog(codex); got != "019f6f77-3c3d-7ce3-966d-d4b2083f4459" {
		t.Errorf("codex session = %q", got)
	}
	claude := logWith(t, "session id: 9f1c2d3e-0000-4444-8888-abcdefabcdef\nuser\nReview it\n")
	if got := SessionFromLog(claude); got != "9f1c2d3e-0000-4444-8888-abcdefabcdef" {
		t.Errorf("claude session = %q", got)
	}
}

// A log that already covers a resume holds several banners. The last names the
// session still open; resuming an earlier one would rejoin a dead conversation.
func TestSessionFromLogTakesTheMostRecent(t *testing.T) {
	dir := logWith(t, strings.Join([]string{
		"session id: first-session",
		"user", "Review it", "codex", `{"decision":"WORKING","summary":"still going"}`,
		"session id: second-session",
		"user", "keep going", "",
	}, "\n"))
	if got := SessionFromLog(dir); got != "second-session" {
		t.Errorf("session = %q, want the most recent banner", got)
	}
}

// Nothing to resume must be silent and empty, never an error: a first-ever
// claim has no previous log, and a mangled one must fall back to a fresh
// review rather than block it.
func TestSessionFromLogDegradesToEmpty(t *testing.T) {
	if got := SessionFromLog(filepath.Join(t.TempDir(), "no-such-dir")); got != "" {
		t.Errorf("missing log = %q, want empty", got)
	}
	if got := SessionFromLog(logWith(t, "user\nno banner here\n")); got != "" {
		t.Errorf("log without a banner = %q, want empty", got)
	}
	if got := SessionFromLog(logWith(t, "")); got != "" {
		t.Errorf("empty log = %q, want empty", got)
	}
}

// The point of recovering a session is that the driver picks it up instead of
// paying for the review again. Both engines must open with a resume, carrying
// the nudge rather than the full prompt: the session already holds the
// context, so re-sending it would buy nothing and cost a fresh read of it.
func TestReviewResumesTheGivenSessionInsteadOfStartingFresh(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		e := newCodex(config.CodexSettings{}, "keep going")
		var argv [][]string
		e.runCmd = func(_ context.Context, args []string, sink io.Writer) error {
			argv = append(argv, args)
			_, _ = io.WriteString(sink, `{"type":"turn.completed","usage":{"input_tokens":10}}`+"\n")
			return nil
		}
		workDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(workDir, "verdict.json"),
			[]byte(`{"decision":"APPROVED","summary":"finished after the interruption"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		v, err := e.Review(context.Background(), Request{
			Prompt: "THE FULL PROMPT", WorkDir: workDir, ResumeSession: "prev-session"})
		if err != nil {
			t.Fatal(err)
		}
		if v.Decision != DecisionApproved {
			t.Errorf("verdict = %+v", v)
		}
		first := strings.Join(argv[0], " ")
		if !strings.Contains(first, "exec resume") || !strings.Contains(first, "prev-session") {
			t.Errorf("first invocation = %q, want it to resume the recovered session", first)
		}
		if strings.Contains(first, "THE FULL PROMPT") {
			t.Error("a resumed session already holds the context; re-sending the prompt pays for it twice")
		}
	})

	t.Run("claude", func(t *testing.T) {
		e := newClaude(config.ClaudeSettings{}, "keep going")
		var argv [][]string
		e.runCmd = func(_ context.Context, args []string, _ string, stream io.Writer, _ io.Writer) error {
			argv = append(argv, args)
			_, _ = io.WriteString(stream, `{"type":"result","subtype":"success","structured_output":`+
				`{"decision":"APPROVED","summary":"finished after the interruption"},"usage":{"input_tokens":10}}`+"\n")
			return nil
		}
		v, err := e.Review(context.Background(), Request{
			Prompt: "THE FULL PROMPT", WorkDir: t.TempDir(), ResumeSession: "prev-session"})
		if err != nil {
			t.Fatal(err)
		}
		if v.Decision != DecisionApproved {
			t.Errorf("verdict = %+v", v)
		}
		first := strings.Join(argv[0], " ")
		if !strings.Contains(first, "--resume") || !strings.Contains(first, "prev-session") {
			t.Errorf("first invocation = %q, want it to resume the recovered session", first)
		}
		if strings.Contains(first, "THE FULL PROMPT") {
			t.Error("a resumed session already holds the context; re-sending the prompt pays for it twice")
		}
	})
}

// TestDriverCancellationRecordsErrorWithSpendSoFar covers the path a FORCED
// shutdown actually takes. The second Ctrl-C cancels reviewCtx mid-run, which
// kills the engine's process group; the driver then has to turn a killed
// subprocess into an ordinary recorded outcome rather than a panic or a lost
// figure. Nothing exercised it before, so the deliberate cost of a force
// (this review burns as an ERROR) was assumed rather than verified.
//
// The tokens matter as much as the verdict: the run really did spend them, so
// dropping them would under-report what a forced shutdown cost.
func TestDriverCancellationRecordsErrorWithSpendSoFar(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, ctx context.Context, workDir string) (Verdict, error)
	}{
		{
			name: "codex",
			run: func(t *testing.T, ctx context.Context, workDir string) (Verdict, error) {
				e := newCodex(config.CodexSettings{}, "nudge")
				e.runCmd = func(ctx context.Context, _ []string, sink io.Writer) error {
					_, _ = sink.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4242,"cached_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0}}` + "\n"))
					<-ctx.Done()
					return ctx.Err()
				}
				return e.Review(ctx, Request{Prompt: "p", WorkDir: workDir})
			},
		},
		{
			name: "claude",
			run: func(t *testing.T, ctx context.Context, workDir string) (Verdict, error) {
				e := newClaude(config.ClaudeSettings{}, "nudge")
				e.runCmd = func(ctx context.Context, _ []string, _ string, stream, _ io.Writer) error {
					_, _ = stream.Write([]byte(resultLine(t, "sess-1", DecisionWorking, 4242) + "\n"))
					<-ctx.Done()
					return ctx.Err()
				}
				return e.Review(ctx, Request{Prompt: "p", WorkDir: workDir})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				time.Sleep(20 * time.Millisecond)
				cancel()
			}()

			v, err := tc.run(t, ctx, t.TempDir())
			if err == nil {
				t.Fatal("a cancelled run must report its error, not a clean outcome")
			}
			if v.Decision != DecisionError {
				t.Errorf("decision = %q, want %q for a killed run", v.Decision, DecisionError)
			}
			if v.Tokens.Total() == 0 {
				t.Error("tokens the run had already spent must survive the cancellation")
			}
			// A cancelled context must not be nudged into resuming: the point
			// of the force is to stop, and a resume would spend again.
			if strings.Count(v.Raw, "session id:") > 1 {
				t.Errorf("a cancelled run must not be resumed:\n%s", v.Raw)
			}
		})
	}
}

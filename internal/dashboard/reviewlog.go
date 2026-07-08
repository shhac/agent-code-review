package dashboard

// This file is the per-review log surface: /api/review-log resolves a PR's
// engine workspace (live queue row or history postmortem) and serves the
// tail of its agent.log. Kept apart from the thin read handlers the same way
// queue.go is — it owns a response contract the ReviewLog page types against.

import (
	"io"
	"net/http"
	"os"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// reviewLogTailBytes caps how much of an agent log one response carries; the
// interesting part of a stuck review is the tail.
const reviewLogTailBytes = 128 * 1024

// prInfo is the PR header block of a review-log response. The frontend's
// ReviewLog page types against this shape; changing a tag here means
// changing its PrInfo mirror.
type prInfo struct {
	Repo         string     `json:"repo"`
	Number       int        `json:"number"`
	Title        string     `json:"title,omitempty"`
	Author       string     `json:"author,omitempty"`
	URL          string     `json:"url,omitempty"`
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`
	Verdict      string     `json:"verdict,omitempty"`
	DurationSecs int        `json:"duration_secs,omitempty"`
	TokensUsed   int        `json:"tokens_used,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
}

// reviewLogResp is the /api/review-log envelope. The zero value is the
// "nothing recorded" answer.
type reviewLogResp struct {
	Available bool    `json:"available"`
	State     string  `json:"state,omitempty"` // queued|reviewing|finished
	PR        *prInfo `json:"pr,omitempty"`
	WorkDir   string  `json:"work_dir,omitempty"`
	Size      int64   `json:"size,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	Content   string  `json:"content,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// reviewLogView derives the response's state and PR header from a resolved
// workspace: a queued row wears claimStatus (reviewing under a live lease,
// queued otherwise); a history row is "finished". Pure — the states are
// table-tested directly.
func reviewLogView(repo string, number int, ws store.Workspace, now time.Time, lease time.Duration) (string, prInfo) {
	pr := prInfo{Repo: repo, Number: number}
	if c := ws.Queued; c != nil {
		pr.Title, pr.Author, pr.URL, pr.ClaimedAt = c.Title, c.Author, c.URL, c.ClaimedAt
		return claimStatus(*c, now, lease), pr
	}
	last := ws.Finished
	pr.Title, pr.Author, pr.Verdict = last.Title, last.Author, last.Verdict
	pr.DurationSecs = last.DurationSecs
	pr.TokensUsed = last.TokensUsed
	pr.ReviewedAt = &last.ReviewedAt
	return "finished", pr
}

// handleReviewLog streams the tail of one review agent's log: the live queue
// row's workspace for an in-flight review, else the most recent history
// row's for a postmortem.
func (s *Server) handleReviewLog(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	number := queryInt(r, "number", 0, 1<<30)
	if !config.ValidRepoName(repo) || number <= 0 {
		httpError(w, http.StatusBadRequest, "need ?repo=owner/name&number=N")
		return
	}
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()

	ws, found, err := store.FindReviewWorkspace(ctx, s.store, store.ReviewLogRef{
		Repo:   repo,
		Number: number,
		LogKey: r.URL.Query().Get("review"),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, reviewLogResp{})
		return
	}
	state, pr := reviewLogView(repo, number, ws, time.Now(), s.config().LeaseWindow())
	content, size, err := tailFile(review.LogPath(ws.Dir), reviewLogTailBytes)
	if err != nil {
		writeJSON(w, http.StatusOK, reviewLogResp{State: state, PR: &pr, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, reviewLogResp{
		Available: true,
		State:     state,
		PR:        &pr,
		WorkDir:   ws.Dir,
		Size:      size,
		Truncated: size > reviewLogTailBytes,
		Content:   content,
	})
}

// tailFile returns up to limit bytes from the end of the file plus its size.
func tailFile(path string, limit int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	start := int64(0)
	if info.Size() > limit {
		start = info.Size() - limit
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", 0, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", 0, err
	}
	return string(b), info.Size(), nil
}

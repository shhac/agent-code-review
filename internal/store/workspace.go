package store

import "context"

// Workspace is where a PR's review agent ran, resolved by FindWorkspace.
// Exactly one of Queued/Finished is set: Queued while the PR still has a
// queue row (in-flight review or reclaimable claim), Finished for a
// postmortem from history.
type Workspace struct {
	Dir      string
	Queued   *Candidate
	Finished *Review
}

// FindWorkspace resolves a PR's recorded engine workspace: the live queue
// row first, then the most recent history row. false means no workspace was
// ever recorded (reviews predating workdir tracking have none). The CLI's
// `queue log` and the dashboard's review-log endpoint share this resolution
// so the two surfaces cannot drift.
func FindWorkspace(ctx context.Context, s Store, repo string, number int) (Workspace, bool, error) {
	return FindReviewWorkspace(ctx, s, ReviewLogRef{Repo: repo, Number: number})
}

// FindReviewWorkspace resolves a review log workspace. With ref.LogKey set,
// it selects that exact history row and never falls back to the live/latest PR
// log. With ref.LogKey empty, it preserves the normal live-then-latest
// behavior.
func FindReviewWorkspace(ctx context.Context, s Store, ref ReviewLogRef) (Workspace, bool, error) {
	if ref.LogKey != "" {
		r, ok, err := s.ReviewByLogKey(ctx, ref.Repo, ref.Number, ref.LogKey)
		if err != nil || !ok || r.WorkDir == "" {
			return Workspace{}, false, err
		}
		return Workspace{Dir: r.WorkDir, Finished: &r}, true, nil
	}
	queue, err := s.ListQueue(ctx, ref.Repo)
	if err != nil {
		return Workspace{}, false, err
	}
	for _, c := range queue {
		if c.Number == ref.Number && c.WorkDir != "" {
			return Workspace{Dir: c.WorkDir, Queued: &c}, true, nil
		}
	}
	last, ok, err := s.LastOutcome(ctx, ref.Repo, ref.Number)
	if err != nil {
		return Workspace{}, false, err
	}
	if ok && last.WorkDir != "" {
		return Workspace{Dir: last.WorkDir, Finished: &last}, true, nil
	}
	return Workspace{}, false, nil
}

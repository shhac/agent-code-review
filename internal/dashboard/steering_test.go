package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore is the roster + queue + steering surface the identity and
// steering handlers touch. Embeds the full Store so an unexpected call panics.
func steerServer(fs *fakeStore, trust bool) *Server {
	opts := []serverOpt{withStore(fs), withConfig(config.Config{GHUser: "paul-gh"})}
	if trust {
		opts = append(opts, withTrustedProxy())
	}
	return testServer(opts...)
}

// post drives the steering handler with a chosen peer address and headers,
// which is the whole point: the two spoofing defences are about WHERE the
// request came from, not what it says.
func post(t *testing.T, s *Server, remote, login, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/steering", strings.NewReader(body))
	r.RemoteAddr = remote
	if login != "" {
		r.Header.Set(tailscaleLoginHeader, login)
	}
	w := httptest.NewRecorder()
	s.handleSteering(w, r)
	return w
}

const octoPR = `{"repo":"o/r","number":1,"message":"focus on the rollback path"}`

func queuedPR() *fakeStore {
	return &fakeStore{
		queue: []store.Candidate{{Repo: "o/r", Number: 1, Author: "octocat", HeadSHA: "s1"}},
		byLogin: map[string]store.Author{
			"octo@example.com":    {GitHubHandle: "octocat"},
			"paul@example.com":    {GitHubHandle: "paul-gh"},
			"mallory@example.com": {GitHubHandle: "mallory"},
		},
	}
}

// TestSteeringRejectsForgedIdentity is the security property: the header is
// proof only because Tailscale attached it, and the only requests Tailscale
// attached anything to are the ones it proxied. Everything else must be
// anonymous, however convincing the header looks.
func TestSteeringRejectsForgedIdentity(t *testing.T) {
	t.Run("a non-loopback peer is never identified", func(t *testing.T) {
		// What a direct hit on the port looks like if the listener is ever
		// bound wider than loopback: a real tailnet address, a header the
		// client wrote itself, and nothing in between to strip it.
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "100.101.66.81:54321", "paul@example.com", octoPR)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401: a header on a direct connection is client-supplied", w.Code)
		}
		if len(fs.steered) != 0 {
			t.Errorf("nothing may be written, got %+v", fs.steered)
		}
	})

	t.Run("a public address is never identified", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "203.0.113.7:443", "paul@example.com", octoPR)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	t.Run("funnel mode trusts no header at all", func(t *testing.T) {
		// Funnel carries public traffic Tailscale attaches no identity to, so
		// even a loopback peer (the funnel proxy itself) proves nothing.
		fs := queuedPR()
		w := post(t, steerServer(fs, false), "127.0.0.1:54321", "paul@example.com", octoPR)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 when serving over funnel", w.Code)
		}
		if len(fs.steered) != 0 {
			t.Errorf("nothing may be written, got %+v", fs.steered)
		}
	})

	t.Run("no header is anonymous, not permissive", func(t *testing.T) {
		fs := queuedPR()
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "", octoPR); w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})
}

// TestSteeringAuthorisation pins who may steer what, once identity is proven.
func TestSteeringAuthorisation(t *testing.T) {
	t.Run("the PR author may steer their own", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "octo@example.com", octoPR)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body)
		}
		if len(fs.steered) != 1 || fs.steered[0].SetBy != "octocat" {
			t.Fatalf("steering = %+v, want one row attributed to octocat", fs.steered)
		}
		// And against the PR that was named. Recording the target without
		// checking it would let a handler steer the wrong row unnoticed.
		if got := fs.steered[0]; got.repo != "o/r" || got.number != 1 {
			t.Errorf("steered %s#%d, want o/r#1", got.repo, got.number)
		}
	})

	t.Run("a rostered stranger may not steer someone else's", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "mallory@example.com", octoPR)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
		if len(fs.steered) != 0 {
			t.Errorf("nothing may be written, got %+v", fs.steered)
		}
	})

	t.Run("the account reviews post as may steer any PR", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "paul@example.com", octoPR)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body)
		}
		if len(fs.steered) != 1 || fs.steered[0].SetBy != "paul-gh" {
			t.Fatalf("steering = %+v", fs.steered)
		}
		if got := fs.steered[0]; got.repo != "o/r" || got.number != 1 {
			t.Errorf("steered %s#%d, want o/r#1", got.repo, got.number)
		}
	})

	t.Run("an identified but unrostered person steers nothing", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "stranger@example.com", octoPR)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403: authenticated but no roster row", w.Code)
		}
	})

	t.Run("authorisation reads the author from the store, not the request", func(t *testing.T) {
		// A caller cannot widen their rights by describing the PR differently:
		// the author comes from the queued row.
		fs := queuedPR()
		body := `{"repo":"o/r","number":1,"message":"x","author":"mallory"}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "mallory@example.com", body); w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("a mis-cased repo is not a different PR", func(t *testing.T) {
		// The store matches repo exactly, so this is a 404 rather than a hit.
		// Pinned because the handler used to carry a case-insensitive
		// comparison that the SQL filter made unreachable, reading as a
		// promise the store never kept.
		fs := queuedPR()
		body := `{"repo":"O/R","number":1,"message":"x"}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "paul@example.com", body); w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("an unqueued PR is a 404", func(t *testing.T) {
		fs := queuedPR()
		body := `{"repo":"o/r","number":99,"message":"x"}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "paul@example.com", body); w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("an empty message clears", func(t *testing.T) {
		fs := queuedPR()
		body := `{"repo":"o/r","number":1,"message":"   "}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "octo@example.com", body); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if len(fs.cleared) != 1 || len(fs.steered) != 0 {
			t.Fatalf("cleared=%+v steered=%+v, want a clear and no write", fs.cleared, fs.steered)
		}
		if got := fs.cleared[0]; got.Repo != "o/r" || got.Number != 1 {
			t.Errorf("cleared %s#%d, want o/r#1", got.Repo, got.Number)
		}
	})

	t.Run("an over-long message is refused", func(t *testing.T) {
		fs := queuedPR()
		long, _ := json.Marshal(strings.Repeat("x", store.SteeringMaxLen+1))
		body := `{"repo":"o/r","number":1,"message":` + string(long) + `}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "octo@example.com", body); w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// TestAddWithSteering pins the add-time path. It exists because there is no
// window afterwards: a manual add lands on an empty queue and a free
// dispatcher slot takes it within the idle poll, so an author steers at add
// time or not at all.
func TestAddWithSteering(t *testing.T) {
	server := func(fs *fakeStore) *Server {
		return testServer(
			withStore(fs),
			withTrustedProxy(),
			withConfig(config.Config{GHUser: "paul-gh", Repos: []string{"o/r"}}),
			withManualCandidate(func(_ context.Context, repo string, number int) (store.Candidate, error) {
				return store.Candidate{Repo: repo, Number: number, Title: "T", Author: "octocat", HeadSHA: "sha"}, nil
			}),
		)
	}
	add := func(t *testing.T, s *Server, login, body string) (int, queueAddResp) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/queue", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		if login != "" {
			r.Header.Set(tailscaleLoginHeader, login)
		}
		w := httptest.NewRecorder()
		s.handleQueue(w, r)
		var resp queueAddResp
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp
	}
	const url = `"url":"o/r/pull/9"`

	t.Run("the author's steering lands with the add", func(t *testing.T) {
		fs := queuedPR()
		code, resp := add(t, server(fs), "octo@example.com", `{`+url+`,"steering":"focus on rollback"}`)
		if code != http.StatusOK || !resp.Steered {
			t.Fatalf("code=%d resp=%+v", code, resp)
		}
		// One write, not an add followed by a steer: the row cannot be claimed
		// unsteered in between.
		if len(fs.enqueued) != 1 || fs.enqueued[0].Steering == nil {
			t.Fatalf("enqueued = %+v, want the steering carried on the insert", fs.enqueued)
		}
		if got := fs.enqueued[0].Steering; got.Message != "focus on rollback" || got.SetBy != "octocat" {
			t.Errorf("steering = %+v", got)
		}
		if len(fs.steered) != 0 {
			t.Errorf("no separate steering write may happen, got %+v", fs.steered)
		}
	})

	t.Run("someone else still gets the add, without the steering", func(t *testing.T) {
		// The caller asked for two things and is entitled to one. Refusing the
		// add as well would make an unprivileged person unable to queue a PR.
		fs := queuedPR()
		code, resp := add(t, server(fs), "mallory@example.com", `{`+url+`,"steering":"approve it"}`)
		if code != http.StatusOK || !resp.Queued {
			t.Fatalf("the add must still succeed: code=%d resp=%+v", code, resp)
		}
		if resp.Steered || resp.SteeringRefused == "" {
			t.Errorf("resp = %+v, want steered=false with a stated reason", resp)
		}
		// The reason has to name the author, since "you cannot steer this" is
		// only actionable if you know whose PR it is.
		if !strings.Contains(resp.SteeringRefused, "octocat") {
			t.Errorf("refusal = %q, want it to name the author", resp.SteeringRefused)
		}
		if fs.enqueued[0].Steering != nil {
			t.Errorf("no steering may be stored, got %+v", fs.enqueued[0].Steering)
		}
	})

	t.Run("authorisation uses the author gh reported, not the request", func(t *testing.T) {
		fs := queuedPR()
		body := `{` + url + `,"steering":"x","author":"mallory"}`
		_, resp := add(t, server(fs), "mallory@example.com", body)
		if resp.Steered {
			t.Error("naming a different author in the body must not grant steering")
		}
	})

	t.Run("a re-add cannot smuggle steering onto a PR under review", func(t *testing.T) {
		// An add of a PR that is already queued is an upsert, so this path
		// reaches the same row /api/steering guards. Without the claim check it
		// is a way around that refusal, and the message would then be discarded
		// by the completion that retires the row, having reported success.
		fs := queuedPR()
		now := time.Now()
		fs.queue = []store.Candidate{{Repo: "o/r", Number: 9, Author: "octocat", HeadSHA: "s9", ClaimedAt: &now}}
		code, resp := add(t, server(fs), "octo@example.com", `{`+url+`,"steering":"focus on rollback"}`)
		// The add itself still happens: it refreshes metadata, which is
		// harmless, and refusing it would be a worse answer than refusing the
		// half the caller is not entitled to right now.
		if code != http.StatusOK || !resp.Queued {
			t.Fatalf("the add must still succeed: code=%d resp=%+v", code, resp)
		}
		if resp.Steered || resp.SteeringRefused == "" {
			t.Fatalf("resp = %+v, want the steering refused with a reason", resp)
		}
		if !strings.Contains(resp.SteeringRefused, "once this review finishes") {
			t.Errorf("refusal = %q, want it to say when to try again", resp.SteeringRefused)
		}
		if fs.enqueued[0].Steering != nil {
			t.Errorf("no steering may ride along, got %+v", fs.enqueued[0].Steering)
		}
	})

	t.Run("an over-long message is refused, not a 400", func(t *testing.T) {
		// The two paths share the rungs but not the consequence: /api/steering
		// returns 400 for this, the add returns 200 and still queues the PR.
		// Pinned because a future "share one validator" reading could quietly
		// turn an add into a failed request.
		fs := queuedPR()
		long := strings.Repeat("x", store.SteeringMaxLen+1)
		code, resp := add(t, server(fs), "octo@example.com", `{`+url+`,"steering":"`+long+`"}`)
		if code != http.StatusOK || !resp.Queued {
			t.Fatalf("the add must still succeed: code=%d resp=%+v", code, resp)
		}
		if resp.Steered || !strings.Contains(resp.SteeringRefused, "longer than") {
			t.Errorf("resp = %+v, want the message refused for length", resp)
		}
		if fs.enqueued[0].Steering != nil {
			t.Errorf("no steering may ride along, got %+v", fs.enqueued[0].Steering)
		}
	})

	t.Run("permission outranks the claim on the add path too", func(t *testing.T) {
		// The same precedence steerableRow uses. Both paths run one ladder now,
		// so a stranger is told they may not steer rather than that a review is
		// running on somebody else's PR.
		fs := queuedPR()
		now := time.Now()
		fs.queue = []store.Candidate{{Repo: "o/r", Number: 9, Author: "octocat", HeadSHA: "s9", ClaimedAt: &now}}
		_, resp := add(t, server(fs), "mallory@example.com", `{`+url+`,"steering":"x"}`)
		if !strings.Contains(resp.SteeringRefused, "octocat") {
			t.Errorf("refusal = %q, want the permission answer, not the in-flight one", resp.SteeringRefused)
		}
	})

	t.Run("an anonymous caller may add but not steer", func(t *testing.T) {
		fs := queuedPR()
		code, resp := add(t, server(fs), "", `{`+url+`,"steering":"x"}`)
		if code != http.StatusOK || !resp.Queued || resp.Steered {
			t.Errorf("code=%d resp=%+v, want a plain add", code, resp)
		}
	})

	t.Run("preflight reports the author and the answer", func(t *testing.T) {
		fs := queuedPR()
		probe := func(login string) queuePreflightResp {
			r := httptest.NewRequest(http.MethodPost, "/api/queue/preflight", strings.NewReader(`{`+url+`}`))
			r.RemoteAddr = "127.0.0.1:1"
			if login != "" {
				r.Header.Set(tailscaleLoginHeader, login)
			}
			w := httptest.NewRecorder()
			server(fs).handleQueuePreflight(w, r)
			var resp queuePreflightResp
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			return resp
		}
		if got := probe("octo@example.com"); got.Author != "octocat" || !got.MaySteer {
			t.Errorf("the author must be told they may steer: %+v", got)
		}
		if got := probe("mallory@example.com"); got.Author != "octocat" || got.MaySteer {
			t.Errorf("a stranger must be told they may not: %+v", got)
		} else if !strings.Contains(got.Refusal, "octocat") {
			// The client renders this verbatim, so it has to name the author.
			t.Errorf("refusal = %q, want it to name the author", got.Refusal)
		}
		if got := probe(""); got.MaySteer {
			t.Errorf("anonymous must be told they may not: %+v", got)
		}
		// Preflight must not queue anything.
		if len(fs.enqueued) != 0 {
			t.Errorf("preflight must not mutate, got %+v", fs.enqueued)
		}
	})
}

// hold drives the editing-hold endpoint the way post drives the steering one.
func hold(t *testing.T, s *Server, method, login, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/steering/hold", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5000"
	if login != "" {
		r.Header.Set(tailscaleLoginHeader, login)
	}
	w := httptest.NewRecorder()
	s.handleSteeringHold(w, r)
	return w
}

const octoRef = `{"repo":"o/r","number":1}`

// editingPair is the two names one session owns, in the sorted order
// heldNames reports. They are asserted together on purpose: the whole reason
// they share a write is that neither is correct without the other.
var editingPair = []string{store.HoldEditing, store.MarkEditingSince}

func decodeHold(t *testing.T, w *httptest.ResponseRecorder) steeringHoldResp {
	t.Helper()
	var got steeringHoldResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode hold response %q: %v", w.Body.String(), err)
	}
	return got
}

// TestSteeringRefusedWhileReviewing is the timing rung of the authorisation
// ladder. A running review built its prompt from the candidate the dispatcher
// pulled and never re-reads the row, and completion retires the row along with
// any message on it, so a write accepted now would return 200 for an
// instruction that reaches nothing.
func TestSteeringRefusedWhileReviewing(t *testing.T) {
	claimed := func() *fakeStore {
		fs := queuedPR()
		now := time.Now()
		fs.queue[0].ClaimedAt = &now
		return fs
	}

	t.Run("the author's own steering is refused with 409", func(t *testing.T) {
		fs := claimed()
		w := post(t, steerServer(fs, true), "127.0.0.1:5000", "octo@example.com", octoPR)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", w.Code)
		}
		if len(fs.steered) != 0 {
			t.Errorf("nothing may be written for a refused steer, got %+v", fs.steered)
		}
		if !strings.Contains(w.Body.String(), "once this review finishes") {
			t.Errorf("the refusal must say what to do instead, got %q", w.Body.String())
		}
	})

	t.Run("an editing hold is refused too: there is nothing left to defer", func(t *testing.T) {
		fs := claimed()
		w := hold(t, steerServer(fs, true), http.MethodPost, "octo@example.com", octoRef)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", w.Code)
		}
		if len(fs.holdsSet) != 0 {
			t.Errorf("a claimed row must not be held, got %+v", fs.holdsSet)
		}
	})

	t.Run("a stale claim is not a running review", func(t *testing.T) {
		// Past the lease window: a crashed daemon's leftovers, which the
		// dispatcher will reclaim. The author is not competing with anything.
		fs := queuedPR()
		old := time.Now().Add(-3 * time.Hour)
		fs.queue[0].ClaimedAt = &old
		w := post(t, steerServer(fs, true), "127.0.0.1:5000", "octo@example.com", octoPR)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
	})
}

// TestSteeringLadderOrder pins the ORDER of the authorisation ladder, which is
// the security property steerableRow's comment names. Outcome-only tests miss
// this entirely: each rung was previously asserted with the others held
// non-competing, so hoisting one above another kept every test green while
// changing what the endpoint discloses to whom.
//
// Each case makes two rungs disagree and states which must answer.
func TestSteeringLadderOrder(t *testing.T) {
	claimed := func(fs *fakeStore) *fakeStore {
		now := time.Now()
		fs.queue[0].ClaimedAt = &now
		return fs
	}
	unqueued := `{"repo":"o/r","number":404,"message":"x"}`

	cases := []struct {
		name  string
		fs    *fakeStore
		login string
		body  string
		want  int
		why   string
	}{
		{
			name: "identity before existence", fs: queuedPR(), login: "", body: unqueued,
			want: http.StatusUnauthorized,
			why:  "an anonymous caller must not learn which PRs are queued",
		},
		{
			name: "existence before permission", fs: queuedPR(), login: "mallory@example.com", body: unqueued,
			want: http.StatusNotFound,
			why:  "a 403 would confirm the PR exists to somebody who may not steer it",
		},
		{
			name: "permission before claim", fs: claimed(queuedPR()), login: "mallory@example.com", body: octoPR,
			want: http.StatusForbidden,
			why:  "a 409 would tell a stranger a review is running on this PR",
		},
		{
			name: "claim answers once the caller is entitled to an answer",
			fs:   claimed(queuedPR()), login: "octo@example.com", body: octoPR,
			want: http.StatusConflict,
			why:  "the author may know, and needs to",
		},
	}
	for _, tc := range cases {
		w := post(t, steerServer(tc.fs, true), "127.0.0.1:5000", tc.login, tc.body)
		if w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s): %s", tc.name, w.Code, tc.want, tc.why, w.Body.String())
		}
	}
}

// TestEditingSession pins the pure cap decision, including the self-healing
// rule. A client that fails to release is the NORMAL case here (a closed tab, a
// dropped network, a capped session that stopped renewing), so a mark that
// outlived its hold must never be allowed to cap the next session.
func TestEditingSession(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const cap = 20 * time.Minute
	live, dead := now.Add(time.Minute), now.Add(-time.Minute)

	cases := []struct {
		name       string
		holds      map[string]time.Time
		wantSince  time.Time
		wantCapped bool
	}{
		{"no session at all", nil, time.Time{}, false},
		{"a fresh session is not capped", map[string]time.Time{
			store.HoldEditing: live, store.MarkEditingSince: now.Add(-time.Minute),
		}, now.Add(-time.Minute), false},
		{"at the cap exactly is not yet capped", map[string]time.Time{
			store.HoldEditing: live, store.MarkEditingSince: now.Add(-cap),
		}, now.Add(-cap), false},
		{"one tick past the cap is capped", map[string]time.Time{
			store.HoldEditing: live, store.MarkEditingSince: now.Add(-cap - time.Second),
		}, now.Add(-cap - time.Second), true},
		// The self-healing rule. Without it this row could never be parked
		// again: every future session would read this mark and cap instantly.
		{"a mark whose hold expired is an abandoned session", map[string]time.Time{
			store.HoldEditing: dead, store.MarkEditingSince: now.Add(-24 * time.Hour),
		}, time.Time{}, false},
		{"a mark with no hold at all is ignored", map[string]time.Time{
			store.MarkEditingSince: now.Add(-24 * time.Hour),
		}, time.Time{}, false},
		{"a live hold with no mark starts a session", map[string]time.Time{
			store.HoldEditing: live,
		}, time.Time{}, false},
	}
	for _, tc := range cases {
		since, capped := editingSession(tc.holds, now, cap)
		if !since.Equal(tc.wantSince) || capped != tc.wantCapped {
			t.Errorf("%s: since=%v capped=%v, want since=%v capped=%v",
				tc.name, since, capped, tc.wantSince, tc.wantCapped)
		}
	}
}

// TestSteeringHoldBounds pins the two independent bounds that keep "parked
// briefly" honest: the hold expires on its own, and renewal stops at the cap.
func TestSteeringHoldBounds(t *testing.T) {
	// editing seeds a live session on the queued row: a hold in the future and
	// a mark dating it, which is the shape the handler reads.
	editing := func(fs *fakeStore, since time.Time) *fakeStore {
		fs.queue[0].Holds = map[string]time.Time{
			store.HoldEditing: time.Now().Add(time.Minute), store.MarkEditingSince: since,
		}
		return fs
	}

	t.Run("a first hold marks the session and parks the row", func(t *testing.T) {
		fs := queuedPR()
		w := hold(t, steerServer(fs, true), http.MethodPost, "octo@example.com", octoRef)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		// One write, not two: the hold and the mark dating it have to agree, so
		// a half-failure must not be able to leave a mark with no hold.
		if got := heldNames(fs.holdsSet); !slices.Equal(got, editingPair) {
			t.Fatalf("want hold and mark written together, got %v", got)
		}
		var until time.Time
		for _, c := range fs.holdsSet {
			if c.name == store.HoldEditing {
				until = c.until
			}
		}
		// The server picks the window; the client never said one.
		if got := time.Until(until); got < 4*time.Minute || got > 5*time.Minute {
			t.Errorf("hold window = %v, want the configured 5m", got)
		}
	})

	t.Run("renewal keeps the original mark", func(t *testing.T) {
		fs := editing(queuedPR(), time.Now().Add(-time.Minute))
		if w := hold(t, steerServer(fs, true), http.MethodPost, "octo@example.com", octoRef); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if got := heldNames(fs.holdsSet); !slices.Equal(got, []string{store.HoldEditing}) {
			t.Errorf("renewal must re-impose only the hold, or the cap could never be reached: %v", got)
		}
	})

	t.Run("past the cap, renewal stops", func(t *testing.T) {
		// The editor left open: the client is still talking, so the expiry
		// alone would keep re-parking the PR indefinitely.
		fs := editing(queuedPR(), time.Now().Add(-time.Hour))
		w := hold(t, steerServer(fs, true), http.MethodPost, "octo@example.com", octoRef)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if len(fs.holdsSet) != 0 {
			t.Errorf("a capped session must not renew, got %+v", fs.holdsSet)
		}
		if got := decodeHold(t, w); got.State != holdCapped {
			t.Errorf("state = %q, want capped: the client must stop believing the PR is parked", got.State)
		}
	})

	t.Run("an abandoned session does not cap the next one", func(t *testing.T) {
		// The bug this rule exists for: a mark left behind by a tab that closed
		// without releasing. Read literally it is hours past the cap, and every
		// future session on this row would be refused a hold forever.
		fs := queuedPR()
		fs.queue[0].Holds = map[string]time.Time{
			store.HoldEditing:      time.Now().Add(-time.Hour), // expired: nobody is editing
			store.MarkEditingSince: time.Now().Add(-24 * time.Hour),
		}
		w := hold(t, steerServer(fs, true), http.MethodPost, "octo@example.com", octoRef)
		if got := decodeHold(t, w); got.State != holdHeld {
			t.Fatalf("state = %q, want held: an expired hold means the session is over", got.State)
		}
		if got := heldNames(fs.holdsSet); !slices.Equal(got, editingPair) {
			t.Errorf("a new session must restamp its own mark, got %v", got)
		}
	})

	t.Run("holds configured off say so, so the client stops asking", func(t *testing.T) {
		fs := queuedPR()
		s := testServer(withStore(fs), withTrustedProxy(), withConfig(config.Config{
			GHUser: "paul-gh", Candidates: config.CandidateSettings{SteeringHold: "0s"},
		}))
		w := hold(t, s, http.MethodPost, "octo@example.com", octoRef)
		if got := decodeHold(t, w); got.State != holdDisabled {
			t.Errorf("state = %q, want disabled: an empty body would read as a release", got.State)
		}
		if len(fs.holdsSet) != 0 {
			t.Errorf("nothing may be written when holds are off, got %+v", fs.holdsSet)
		}
	})

	t.Run("release retires the hold and its mark together", func(t *testing.T) {
		fs := queuedPR()
		w := hold(t, steerServer(fs, true), http.MethodDelete, "octo@example.com", octoRef)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if got := heldNames(fs.holdsGone); !slices.Equal(got, editingPair) {
			t.Fatalf("want both retired, got %v", got)
		}
		if got := decodeHold(t, w); got.State != holdReleased {
			t.Errorf("state = %q, want released", got.State)
		}
	})

	t.Run("saving steering releases the session", func(t *testing.T) {
		fs := queuedPR()
		if w := post(t, steerServer(fs, true), "127.0.0.1:5000", "octo@example.com", octoPR); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if got := heldNames(fs.holdsGone); !slices.Equal(got, editingPair) {
			t.Errorf("a save is the end of editing, so the session goes with it: %v", got)
		}
	})

	t.Run("someone else's PR cannot be parked", func(t *testing.T) {
		fs := queuedPR()
		w := hold(t, steerServer(fs, true), http.MethodPost, "mallory@example.com", octoRef)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", w.Code)
		}
		if len(fs.holdsSet) != 0 {
			t.Errorf("no hold may be written for a refused caller, got %+v", fs.holdsSet)
		}
	})
}

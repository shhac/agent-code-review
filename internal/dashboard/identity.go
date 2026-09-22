// Who is asking. The dashboard has no login of its own: `tailscale serve`
// authenticates the person and asserts it in a header, and the roster says
// which GitHub handle that identity belongs to.

package dashboard

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

// tailscaleLoginHeader is what `tailscale serve` attaches to every proxied
// request, naming the tailnet USER (not the device, so someone with a laptop
// and a desktop presents the same value from both). Tailscale strips any
// incoming copy before proxying, which is the only reason this can be trusted
// at all: a request that reaches the listener without passing through the
// proxy can set it freely, which is why the dashboard binds loopback.
//
// Absent for tagged devices and for public Funnel traffic, both of which
// therefore resolve to an anonymous viewer and can steer nothing.
const tailscaleLoginHeader = "Tailscale-User-Login"

// viewer is who the dashboard believes is asking.
//
// Login is proof of a PERSON. Handle is an assertion by the roster that the
// person owns a GitHub account, and is empty for a tailnet user with no roster
// row: they browse exactly as before and can steer nothing, which is the right
// default when the alternative is granting rights to everyone on the tailnet.
type viewer struct {
	Login  string // Tailscale-User-Login, "" when unproxied or a tagged device
	Handle string // GitHub handle the roster maps that login to, "" if unmapped
	IsGH   bool   // the login maps to the handle reviews are posted as
}

// anonymous reports whether nothing about the caller was proven.
func (v viewer) anonymous() bool { return v.Login == "" }

// maySteer reports whether this viewer may steer the review of a PR authored
// by author. Two rules, both requiring a proven identity: you may steer your
// own PR, and the account reviews are posted as may steer any of them (it is
// already acting on every one of these PRs).
func (v viewer) maySteer(author string) bool {
	if v.Handle == "" {
		return false
	}
	return v.IsGH || strings.EqualFold(v.Handle, author)
}

// fromLoopback reports whether the connection arrived on the loopback
// interface, which for this daemon means it came through the local
// `tailscale serve` proxy (or from a process already on the machine).
//
// This is the control that does not depend on configuration being right. The
// listener binds loopback by default, but a wider bind is one config edit
// away, and the moment it happens the header stops being proof: anything that
// can reach the port directly sets it freely, because Tailscale only strips
// forged copies on traffic that goes THROUGH it. Checking the peer per
// request means a wider bind degrades to "nobody is identified" rather than
// to "everybody is whoever they say".
func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// cannotSteer is the refusal, worded once. It sits beside maySteer because it
// is that rule stated in prose, and it had drifted into three copies across
// two languages ("that PR" in Go, "this PR" in TypeScript) within an hour of
// being written.
func cannotSteer(author string) string {
	return "only @" + author + " (or the account reviews are posted as) can steer that PR"
}

// identify resolves the caller. A header naming nobody the roster knows still
// yields a viewer with a Login: the person is authenticated, just unmapped.
//
// Three things must hold before the header counts as proof, and only the
// third is Tailscale's to keep:
//
//  1. This daemon is not serving over Funnel. Funnel carries public internet
//     traffic and Tailscale attaches no identity to it, so any header on such
//     a request came from the client. Refused wholesale rather than reasoned
//     about per request.
//  2. The connection arrived on loopback, i.e. through the local proxy.
//  3. Tailscale strips any client-supplied copy before proxying, which is what
//     makes the value it attaches trustworthy in the first place.
func (s *Server) identify(ctx context.Context, r *http.Request) (viewer, error) {
	if !s.trustProxyIdentity || !fromLoopback(r) {
		return viewer{}, nil
	}
	login := strings.TrimSpace(r.Header.Get(tailscaleLoginHeader))
	if login == "" {
		return viewer{}, nil
	}
	v := viewer{Login: login}
	a, ok, err := s.store.AuthorByTailscaleLogin(ctx, login)
	if err != nil {
		return viewer{}, err
	}
	if ok {
		v.Handle = a.GitHubHandle
	}
	if v.Handle != "" {
		v.IsGH = strings.EqualFold(v.Handle, s.reviewingAs(ctx))
	}
	return v, nil
}

// viewerResp tells the UI who it is talking to. Deliberately narrow: the chip
// renders an identity, and whether a given PR is steerable is answered per row
// by queueView.MaySteer, so nothing here describes permissions.
//
// State is the classification, named once here rather than re-derived by the
// client from a combination of booleans. The English prose that used to ride
// along was assembled by a switch in Go and consumed only as a tooltip;
// wording belongs to the client.
type viewerResp struct {
	State  viewerState `json:"state"`
	Login  string      `json:"login,omitempty"`
	Handle string      `json:"handle,omitempty"`
}

// viewerState is the four ways the dashboard can know a caller.
type viewerState string

const (
	// viewerAnonymous: nothing was proved. Either the request did not come
	// through the tailscale proxy, or it is a tagged device or Funnel traffic,
	// for which Tailscale attaches no identity at all.
	viewerAnonymous viewerState = "anonymous"
	// viewerUnmapped: authenticated, but no roster row claims that login.
	viewerUnmapped viewerState = "unmapped"
	// viewerAuthor: a rostered person, who may steer their own PRs.
	viewerAuthor viewerState = "author"
	// viewerOperator: the account reviews are posted as, which may steer any.
	viewerOperator viewerState = "operator"
)

// state classifies a viewer. Pure, so the four cases are table-testable
// without building a request.
func (v viewer) state() viewerState {
	switch {
	case v.anonymous():
		return viewerAnonymous
	case v.Handle == "":
		return viewerUnmapped
	case v.IsGH:
		return viewerOperator
	default:
		return viewerAuthor
	}
}

func (s *Server) handleViewer(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 5*time.Second)
	defer cancel()
	v, err := s.identify(ctx, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewerResp{State: v.state(), Login: v.Login, Handle: v.Handle})
}

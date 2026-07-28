package usage

// Claude Code has no `claude usage` command and reports no headroom in its
// run output, so subscription limits come from the same OAuth endpoint the
// interactive /usage panel reads. The credential is the one `claude auth
// login` already stored; this package only ever reads it, sends it to
// api.anthropic.com, and drops it. It is never logged, never copied into a
// Snapshot, and never included in an error string.
//
// The endpoint is undocumented and may change without notice. That is
// tolerable precisely because every failure path here is fail-open: an error
// becomes Snapshot.Error, and BelowFloor treats an errored snapshot as "no
// opinion", so a broken meter can never wedge the review loop.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	claudeUsageURL  = "https://api.anthropic.com/api/oauth/usage"
	claudeOAuthBeta = "oauth-2025-04-20"
	// keychainService is where the macOS build of Claude Code stores the
	// credential blob; other platforms use credentialsFile under $HOME.
	keychainService = "Claude Code-credentials"
	credentialsFile = ".claude/.credentials.json"
)

// Claude reports a rolling session window and a weekly one, matching the two
// windows Snapshot already models for codex.
const (
	claudeSessionMins = 300
	claudeWeeklyMins  = 10080
)

// claudeUsageResp is the subset of the endpoint's payload this reads. The
// response carries several other windows (scoped weekly limits, promotional
// pools) that no floor acts on, plus an extra_usage block describing paid
// overage; none of it is mapped, because Snapshot deliberately models only
// what the usage floor consumes.
type claudeUsageResp struct {
	FiveHour *claudeWindow `json:"five_hour"`
	SevenDay *claudeWindow `json:"seven_day"`
}

type claudeWindow struct {
	Utilization float64 `json:"utilization"` // percent USED, matching Window.UsedPercent
	ResetsAt    string  `json:"resets_at"`   // RFC3339
}

// fetchClaude reads subscription headroom for the claude engine. bin locates
// the CLI for the plan-name probe only; the usage read itself needs just the
// stored credential.
func fetchClaude(ctx context.Context, bin string) (Snapshot, error) {
	token, err := claudeOAuthToken()
	if err != nil {
		return Snapshot{}, err
	}
	snap, err := claudeUsage(ctx, claudeUsageURL, token)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Plan = claudePlan(ctx, bin)
	return snap, nil
}

// claudeUsage is the endpoint read and its mapping onto a Snapshot, split from
// credential loading and the plan probe so tests can drive it against a local
// server without touching a real credential store.
func claudeUsage(ctx context.Context, url, token string) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", claudeOAuthBeta)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Status only: a body could echo request details, and this string is
		// rendered on the dashboard.
		return Snapshot{}, fmt.Errorf("claude usage endpoint returned %s", resp.Status)
	}

	var payload claudeUsageResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Primary:   toClaudeWindow(payload.FiveHour, claudeSessionMins),
		Secondary: toClaudeWindow(payload.SevenDay, claudeWeeklyMins),
		FetchedAt: time.Now(),
	}, nil
}

func toClaudeWindow(w *claudeWindow, windowMins int) *Window {
	if w == nil {
		return nil
	}
	resets := int64(0)
	if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
		resets = t.Unix()
	}
	return &Window{UsedPercent: w.Utilization, WindowMins: windowMins, ResetsAt: resets}
}

// claudePlan reads the subscription tier from `claude auth status --json`,
// which is a local call against the same stored credential. "" when the probe
// fails: the plan name is display sugar, never a floor input.
func claudePlan(ctx context.Context, bin string) string {
	if bin == "" {
		bin = "claude"
	}
	out, err := exec.CommandContext(ctx, bin, "auth", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var status struct {
		SubscriptionType string `json:"subscriptionType"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return ""
	}
	return status.SubscriptionType
}

// claudeOAuthToken loads the stored credential: the macOS keychain first,
// then the file store used elsewhere.
func claudeOAuthToken() (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-w").Output()
		if err == nil {
			if token, err := accessTokenFrom(out); err == nil {
				return token, nil
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(credentialsFile)))
	if err != nil {
		return "", fmt.Errorf("no stored claude credential; run `claude auth login`")
	}
	return accessTokenFrom(data)
}

// accessTokenFrom pulls the bearer token out of the credential blob. Pure, so
// the wire shape is table-tested without touching a real credential store.
func accessTokenFrom(data []byte) (string, error) {
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("stored claude credential is not readable JSON")
	}
	if strings.TrimSpace(creds.ClaudeAiOauth.AccessToken) == "" {
		return "", fmt.Errorf("stored claude credential has no access token; run `claude auth login`")
	}
	return creds.ClaudeAiOauth.AccessToken, nil
}

package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
)

// row is one result row plus the first decode failure it produced.
//
// The getters used to swallow a value they could not interpret and return a
// zero, so a renamed or retyped column read as legitimate data: a review with
// no tokens, a run with no pid, a candidate at the zero time. That is the
// worst shape a storage bug can take, because nothing anywhere reports it.
//
// An ABSENT column is still not an error. Queries select subsets and several
// columns are genuinely optional, so absent means "not asked for" and yields
// the zero value as before. Only a value that is PRESENT and uninterpretable
// is drift, and that is what this records.
type row struct {
	values map[string]any
	err    error
}

func (r *row) fail(key string, v any, err error) {
	if r.err == nil {
		r.err = fmt.Errorf("column %q: cannot read %T (%v): %w", key, v, v, err)
	}
}

// present reports the raw value when the column was selected and non-null.
func (r *row) present(key string) (any, bool) {
	v, ok := r.values[key]
	return v, ok && v != nil
}

func (r *row) str(key string) string {
	v, ok := r.present(key)
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func (r *row) int(key string) int {
	v, ok := r.present(key)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, err := strconv.Atoi(n)
		if err != nil {
			r.fail(key, v, err)
		}
		return i
	default:
		r.fail(key, v, errUnexpectedType)
		return 0
	}
}

func (r *row) float(key string) float64 {
	v, ok := r.present(key)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			r.fail(key, v, err)
		}
		return f
	default:
		r.fail(key, v, errUnexpectedType)
		return 0
	}
}

func (r *row) time(key string) time.Time {
	v, ok := r.present(key)
	if !ok {
		return time.Time{}
	}
	s := r.str(key)
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := parseStoredTime(s)
	if err != nil {
		r.fail(key, v, err)
	}
	return t
}

// timePtr is time for the nullable columns, where absent is meaningful
// (unclaimed, not yet finished, no hold) rather than a zero instant.
func (r *row) timePtr(key string) *time.Time {
	if t := r.time(key); !t.IsZero() {
		return &t
	}
	return nil
}

var errUnexpectedType = errors.New("unexpected type")

func scanReview(m map[string]any) (Review, error) {
	r := &row{values: m}
	review := Review{
		Repo:             r.str("repo"),
		Number:           r.int("number"),
		Title:            r.str("title"),
		Author:           r.str("author"),
		HeadSHA:          r.str("head_sha"),
		Verdict:          r.str("verdict"),
		Engine:           r.str("engine"),
		Model:            r.str("model"),
		Effort:           r.str("effort"),
		EngineVersion:    r.str("engine_version"),
		ReviewedAt:       r.time("reviewed_at"),
		DurationSecs:     r.int("duration_secs"),
		WorkDir:          r.str("work_dir"),
		TokensUsed:       r.int("tokens_used"),
		CostUSD:          r.float("cost_usd"),
		EstCostUSD:       r.float("est_cost_usd"),
		FreshTokens:      r.int("fresh_tokens"),
		InputTokens:      r.int("input_tokens"),
		OutputTokens:     r.int("output_tokens"),
		CacheWriteTokens: r.int("cache_write_tokens"),
		CacheReadTokens:  r.int("cache_read_tokens"),
		ReasoningTokens:  r.int("reasoning_tokens"),
		UsageRaw:         r.str("usage_raw"),
	}
	review.LogKey = ReviewLogKey(review)
	return review, r.err
}

func scanAuthor(m map[string]any) (Author, error) {
	r := &row{values: m}
	a := Author{
		Repo:         r.str("repo"),
		GitHubHandle: r.str("github_handle"),
		Group:        r.str("group_name"),
		Name:         r.str("name"),
		Email:        r.str("email"),
		SlackID:      r.str("slack_id"),

		TailscaleLogin: r.str("tailscale_login"),
	}
	// A row written before group_name existed still means what it meant then.
	if a.Group == "" {
		a.Group = config.GroupApprover
	}
	return a, r.err
}

func scanCandidate(m map[string]any) (Candidate, error) {
	r := &row{values: m}
	c := Candidate{
		Repo:         r.str("repo"),
		Number:       r.int("number"),
		Type:         r.str("type"),
		Title:        r.str("title"),
		Author:       r.str("author"),
		URL:          r.str("url"),
		HeadSHA:      r.str("head_sha"),
		CreatedAt:    r.time("created_at"),
		UpdatedAt:    r.time("updated_at"),
		QueuePos:     r.int("queue_pos"),
		DiscoveredAt: r.time("discovered_at"),
		Source:       r.str("source"),
		WorkDir:      r.str("work_dir"),
		HoldReason:   r.str("hold_reason"),
		ClaimHost:    r.str("claim_host"),
		ClaimPID:     r.int("claim_pid"),
		ClaimedAt:    r.timePtr("claimed_at"),
		EligibleAt:   r.timePtr("eligible_at"),
	}
	// Steering is present only when a message is: set_by and set_at ride with
	// it, so a row with no instruction carries no empty struct to be mistaken
	// for one.
	if msg := r.str("steering_message"); msg != "" {
		c.Steering = &Steering{Message: msg, SetBy: r.str("steering_by"), SetAt: r.time("steering_at")}
	}
	return c, r.err
}

// text renders a SQL string literal (single quotes doubled). An empty string
// stays an empty string.
func text(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// nullText is text for columns where empty MEANS absent, so it renders NULL.
//
// Named for the policy it applies rather than for being short. As `q` it was
// the only quoting helper, so every call site inherited "empty becomes NULL"
// whether or not that was right for the column, and nothing at the call site
// said so. The distinction is not cosmetic: a NULL group_name is deliberately
// read back as the approver group for legacy rows, so a column that quietly
// became NULL could hand someone approve-level policy.
func nullText(s string) string {
	if s == "" {
		return "NULL"
	}
	return text(s)
}

// ts renders a TIMESTAMP literal in UTC, or NULL for the zero time.
func ts(t time.Time) string {
	if t.IsZero() {
		return "NULL"
	}
	return "'" + t.UTC().Format("2006-01-02 15:04:05") + "'"
}

// tsp is ts for optional timestamps: NULL for nil.
func tsp(t *time.Time) string {
	if t == nil {
		return "NULL"
	}
	return ts(*t)
}

// storedTimeLayouts are the shapes DuckDB's JSON output uses for a TIMESTAMP,
// most specific first.
var storedTimeLayouts = []string{"2006-01-02 15:04:05.999", "2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339}

// parseStoredTime is the one timestamp rule. A value that matches no layout is
// a decode failure rather than the zero instant: a review "completed" in year
// zero is not a fact, it is a parse that went wrong quietly.
func parseStoredTime(s string) (time.Time, error) {
	for _, layout := range storedTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("no known timestamp layout matched %q", s)
}

// getString reads one column off an ad-hoc single-value query (a count, a
// DISTINCT list), where there is no struct to scan and no drift to detect.
func getString(r map[string]any, key string) string {
	return (&row{values: r}).str(key)
}

// getInt is getString for the count queries.
func getInt(r map[string]any, key string) int {
	return (&row{values: r}).int(key)
}

// num renders a float as a SQL literal. Never scientific notation: DuckDB
// accepts it, but a literal that reads as `1e-05` in a logged statement is
// needlessly hard to eyeball.
func num(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// scanCount reads a single-column count(*) result.
func scanCount(m map[string]any) (int, error) {
	r := &row{values: m}
	return r.int("n"), r.err
}

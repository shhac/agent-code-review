package store

import (
	"errors"
	"fmt"
	"slices"
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

// holds decodes the named-hold map. DuckDB's jsonlines output renders a JSON
// column as a nested object rather than as a string, so the value arrives
// already parsed and this only has to interpret the timestamps.
//
// An unparseable entry is drift, recorded like any other: a hold that silently
// read as the zero time would make a held row look reviewable, which is the
// one direction this must never fail in.
func (r *row) holds(key string) map[string]time.Time {
	v, ok := r.present(key)
	if !ok {
		return nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		r.fail(key, v, errUnexpectedType)
		return nil
	}
	out := make(map[string]time.Time, len(raw))
	for name, val := range raw {
		s, ok := val.(string)
		if !ok {
			r.fail(key, v, errUnexpectedType)
			continue
		}
		t, err := parseStoredTime(s)
		if err != nil {
			r.fail(key, v, err)
			continue
		}
		out[name] = t
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clearHoldsJSON renders a merge patch that removes exactly the named holds.
// A JSON null is how merge-patch says "delete this key"; anything else it says
// means set-or-leave, which is why retiring a hold needs its own renderer.
func clearHoldsJSON(names ...string) string {
	parts := make([]string, 0, len(names))
	for _, name := range slices.Sorted(slices.Values(names)) {
		parts = append(parts, fmt.Sprintf("%q:null", name))
	}
	return text("{" + strings.Join(parts, ",") + "}")
}

// holdsJSON renders a hold map as a JSON object literal for SQL. Zero
// timestamps are dropped rather than written: they are not holds, and a zero
// instant in the column would read back as one more expired entry to explain.
func holdsJSON(holds map[string]time.Time) string {
	names := make([]string, 0, len(holds))
	for name, t := range holds {
		if !t.IsZero() {
			names = append(names, name)
		}
	}
	// Sorted so the same map always renders the same SQL, which keeps the
	// statements readable in a log and the tests free of map-order flakes.
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%q:%q", name, holds[name].UTC().Format("2006-01-02 15:04:05")))
	}
	return text("{" + strings.Join(parts, ",") + "}")
}

// bool reads a BOOLEAN column. Absent or NULL is false, which is the right
// reading for every row written before a flag column existed.
func (r *row) bool(key string) bool {
	v, ok := r.present(key)
	if !ok {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true"
	default:
		r.fail(key, v, errUnexpectedType)
		return false
	}
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
		PolicyViolation:  r.bool("policy_violation"),
		Diff: DiffStats{
			Additions:       r.int("additions"),
			Deletions:       r.int("deletions"),
			ChangedFiles:    r.int("changed_files"),
			ScoredAdditions: r.int("scored_additions"),
			ScoredDeletions: r.int("scored_deletions"),
			ExcludedFiles:   r.int("excluded_files"),
			DiffSHA:         r.str("diff_sha"),
		},
		// Score and Attempt through intPtr, not int: NULL here means never
		// scored, which a 0 would silently claim to be a score of zero.
		Score: ScoreRecord{
			Score:   r.intPtr("score"),
			Source:  r.str("score_source"),
			Rules:   r.str("score_rules"),
			Bucket:  r.str("score_bucket"),
			Note:    r.str("score_note"),
			Attempt: r.intPtr("score_attempt"),
			At:      r.time("scored_at"),
		},
	}
	// Present only when a message is, exactly as scanCandidate does: a row with
	// no instruction must not carry an empty struct that reads as one, and a
	// row predating the columns must not read as "not steered".
	if msg := r.str("steering_message"); msg != "" {
		review.Steering = &Steering{Message: msg, SetBy: r.str("steering_by"), SetAt: r.time("steering_at")}
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
		ClaimHost:    r.str("claim_host"),
		ClaimPID:     r.int("claim_pid"),
		ClaimedAt:    r.timePtr("claimed_at"),
		Holds:        r.holds("holds"),
		Additions:    r.int("additions"),
		Deletions:    r.int("deletions"),
		ChangedFiles: r.int("changed_files"),
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

// tsExact renders a TIMESTAMP literal at MICROSECOND precision, which is
// DuckDB's own resolution for the type.
//
// ts() truncates to the second, which is right for the columns it writes
// (holds, claims, discovery instants: nobody asks whether a debounce expired
// 300 microseconds ago) and wrong for addressing a row BY its timestamp. A
// history row inserted by anything that kept sub-second precision reads back
// as 10:33:06.022613, and `WHERE reviewed_at = '2026-09-11 10:33:06'` then
// matches nothing at all — the lookup fails rather than finding the wrong row,
// but a scoring command that cannot find any row it just listed is no better.
//
// Comparison is by TIMESTAMP value and not by string, so this also matches a
// row whose stored value genuinely has no sub-second part.
func tsExact(t time.Time) string {
	if t.IsZero() {
		return "NULL"
	}
	return "'" + t.UTC().Format("2006-01-02 15:04:05.000000") + "'"
}

// storedTimeLayouts are the shapes DuckDB's JSON output uses for a TIMESTAMP,
// most specific first.
var storedTimeLayouts = []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05.999", "2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339}

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

// intOrNull renders an optional integer: nil becomes NULL.
//
// Needed because num() and %d have no NULL form, so a score of nil would be
// written as 0 — and 0 is a LEGITIMATE score (a PR whose every line is
// generated), so conflating the two would hide exactly the rows a recompute
// sweep needs to find.
func intOrNull(v *int) string {
	if v == nil {
		return "NULL"
	}
	return strconv.Itoa(*v)
}

// intPtr reads a nullable integer column, distinguishing absent from zero.
func (r *row) intPtr(key string) *int {
	if _, ok := r.present(key); !ok {
		return nil
	}
	v := r.int(key)
	return &v
}

// scanCount reads a single-column count(*) result.
func scanCount(m map[string]any) (int, error) {
	r := &row{values: m}
	return r.int("n"), r.err
}

package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func scanReview(r map[string]any) Review {
	review := Review{
		Repo:         getString(r, "repo"),
		Number:       getInt(r, "number"),
		Title:        getString(r, "title"),
		Author:       getString(r, "author"),
		HeadSHA:      getString(r, "head_sha"),
		Verdict:      getString(r, "verdict"),
		Engine:       getString(r, "engine"),
		Model:        getString(r, "model"),
		Effort:       getString(r, "effort"),
		ReviewedAt:   getTime(r, "reviewed_at"),
		DurationSecs: getInt(r, "duration_secs"),
		WorkDir:      getString(r, "work_dir"),
		TokensUsed:   getInt(r, "tokens_used"),
	}
	review.LogKey = ReviewLogKey(review)
	return review
}

func scanAuthor(r map[string]any) AllowedAuthor {
	return AllowedAuthor{
		Repo:         getString(r, "repo"),
		GitHubHandle: getString(r, "github_handle"),
		Name:         getString(r, "name"),
		Email:        getString(r, "email"),
		SlackID:      getString(r, "slack_id"),
	}
}

func scanRun(r map[string]any) Run {
	run := Run{
		ID:        getString(r, "id"),
		StartedAt: getTime(r, "started_at"),
		Status:    getString(r, "status"),
		Host:      getString(r, "host"),
		PID:       getInt(r, "pid"),
	}
	if t := getTime(r, "finished_at"); !t.IsZero() {
		run.FinishedAt = &t
	}
	return run
}

func scanCandidate(r map[string]any) Candidate {
	c := Candidate{
		Repo:         getString(r, "repo"),
		Number:       getInt(r, "number"),
		Type:         getString(r, "type"),
		Title:        getString(r, "title"),
		Author:       getString(r, "author"),
		URL:          getString(r, "url"),
		HeadSHA:      getString(r, "head_sha"),
		CreatedAt:    getTime(r, "created_at"),
		UpdatedAt:    getTime(r, "updated_at"),
		QueuePos:     getInt(r, "queue_pos"),
		DiscoveredAt: getTime(r, "discovered_at"),
		Source:       getString(r, "source"),
		WorkDir:      getString(r, "work_dir"),
		HoldReason:   getString(r, "hold_reason"),
		ClaimHost:    getString(r, "claim_host"),
		ClaimPID:     getInt(r, "claim_pid"),
	}
	if t := getTime(r, "claimed_at"); !t.IsZero() {
		c.ClaimedAt = &t
	}
	if t := getTime(r, "eligible_at"); !t.IsZero() {
		c.EligibleAt = &t
	}
	return c
}

// q renders a SQL string literal (single quotes doubled). NULL for empty.
func q(s string) string {
	if s == "" {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
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

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func getString(r map[string]any, key string) string {
	v, ok := r[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func getInt(r map[string]any, key string) int {
	v, ok := r[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func getTime(r map[string]any, key string) time.Time {
	s := getString(r, key)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999", "2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

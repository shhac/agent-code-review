package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// Defaults and ceiling for one page of history. The ceiling is a transfer
// bound, not a search bound: a search matches the whole table server-side and
// only its current page crosses the wire, so capping the page no longer caps
// what can be found.
const (
	defaultReviewLimit = 50
	maxReviewLimit     = 500
)

// ReviewSort names an ordering. One value today; it is a field rather than an
// assumption so adding "oldest first" or "most expensive first" later is a new
// constant, not a new cursor format.
type ReviewSort string

const SortNewest ReviewSort = "newest"

// Normalise resolves the empty sort to the default and rejects one this build
// does not implement. Rejecting matters: an unrecognised sort silently served
// as "newest" is a page that answers a different question than it was asked,
// and the ordering is invisible in the rows themselves.
func (s ReviewSort) Normalise() (ReviewSort, error) {
	switch s {
	case "", SortNewest:
		return SortNewest, nil
	default:
		return "", fmt.Errorf("unknown sort %q", string(s))
	}
}

// cursorVersion is bumped when a field's MEANING changes, not when one is
// added: an added field is absent from an old cursor and takes its zero value,
// which is why the payload is JSON rather than a positional string.
const cursorVersion = 1

// ReviewCursor is a self-describing position in a result set: which row to
// resume after, and which query and ordering produced it.
//
// It carries the query and the sort, not just the row, so the server can
// refuse a cursor that belongs to a different search. Without that, a cursor
// outliving the search it came from (a reload, a shared URL, a stale tab)
// pages through rows from the old result set under the new query's heading,
// and nothing on screen says so.
//
// All three position fields, because reviewed_at is not unique: 676 of this
// history's timestamps are shared by two or more rows, so a cursor carrying
// the timestamp alone would either skip the rest of a tied group or repeat it.
// Repo and Number break the tie, and the ORDER BY names the same three columns
// in the same order so the comparison and the sort cannot disagree.
type ReviewCursor struct {
	Version    int        `json:"v"`
	Sort       ReviewSort `json:"sort,omitempty"`
	Text       string     `json:"q,omitempty"`
	ReviewedAt time.Time  `json:"at"`
	Repo       string     `json:"repo"`
	Number     int        `json:"n"`
}

// IsZero reports the first page: no row to resume after.
func (c ReviewCursor) IsZero() bool { return c.ReviewedAt.IsZero() }

// String encodes a cursor for a URL. Opaque to the browser on purpose: it
// holds the value and hands it back, and nothing outside this file should be
// composing one out of a timestamp it guessed.
func (c ReviewCursor) String() string {
	if c.IsZero() {
		return ""
	}
	c.Version = cursorVersion
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ParseReviewCursor decodes what String produced, and checks it belongs to the
// search being run. A malformed or foreign cursor is an error rather than a
// silent first page: paging is the one place where quietly ignoring a
// parameter shows the reader a page they did not ask for and gives them no way
// to tell.
func ParseReviewCursor(s, text string, sort ReviewSort) (ReviewCursor, error) {
	if s == "" {
		return ReviewCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ReviewCursor{}, fmt.Errorf("cursor is not valid base64: %w", err)
	}
	var c ReviewCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return ReviewCursor{}, fmt.Errorf("cursor is not valid JSON: %w", err)
	}
	if c.Version != cursorVersion {
		return ReviewCursor{}, fmt.Errorf("cursor is version %d, want %d", c.Version, cursorVersion)
	}
	if c.Text != text {
		return ReviewCursor{}, fmt.Errorf("cursor belongs to a different search")
	}
	want, err := sort.Normalise()
	if err != nil {
		return ReviewCursor{}, err
	}
	if c.Sort != want {
		return ReviewCursor{}, fmt.Errorf("cursor is sorted %q, want %q", string(c.Sort), string(want))
	}
	return c, nil
}

// ReviewQuery selects one page of history. The zero value is a valid query:
// the newest defaultReviewLimit rows, unfiltered.
type ReviewQuery struct {
	// Text matches anywhere in "repo#number title author verdict",
	// case-insensitively. Empty matches every row.
	Text string
	// Limit is the page size, clamped to maxReviewLimit. Zero or negative
	// takes the default rather than returning nothing, so a caller that
	// forgets to set it gets a page instead of an empty result that reads as
	// "no matches".
	Limit int
	// Sort is the ordering. Empty takes SortNewest.
	Sort ReviewSort
	// After starts the page strictly after a row from the previous page.
	// The zero cursor means the first page.
	//
	// A cursor rather than an offset because history grows while it is being
	// read: a review completing between two page fetches shifts every later
	// offset by one, so page 2 re-shows the row that page 1 ended on. The
	// cursor names a position in the data instead of a distance from the top,
	// so an insertion above it changes nothing.
	After ReviewCursor
}

// sort is the query's effective ordering. An invalid one cannot reach here:
// the caller normalises before building the query, and the zero value is the
// default rather than an error, so the zero ReviewQuery stays valid.
func (q ReviewQuery) sort() ReviewSort {
	s, err := q.Sort.Normalise()
	if err != nil {
		return SortNewest
	}
	return s
}

func (q ReviewQuery) limit() int {
	if q.Limit <= 0 {
		return defaultReviewLimit
	}
	return min(q.Limit, maxReviewLimit)
}

// ReviewPage is one page of matching history plus the size of the whole match.
type ReviewPage struct {
	Reviews []Review `json:"reviews"`
	// Total counts every row the query matches, not the rows in this page,
	// so a pager can show the last page and the page can say how many results
	// the search actually found.
	Total int `json:"total"`
	// NextCursor starts the following page, empty on the last page.
	NextCursor string `json:"next_cursor,omitempty"`
}

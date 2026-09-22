package store

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

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

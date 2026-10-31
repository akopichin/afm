package accounting

import (
	"strings"
	"time"
)

// compareOptional orders two optional values so that a missing one is last in both directions.
func compareOptional[T int64 | uint64 | float64](a, b *T, desc bool) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case *a == *b:
		return 0
	case (*a < *b) != desc:
		return -1
	default:
		return 1
	}
}

func compareStartThenID(aAt time.Time, aID string, cAt time.Time, cID string) int {
	if d := aAt.Compare(cAt); d != 0 {
		return d
	}
	return strings.Compare(aID, cID)
}

func compareText(a, b string, desc bool) int {
	d := strings.Compare(strings.ToLower(a), strings.ToLower(b))
	if d == 0 {
		d = strings.Compare(a, b)
	}
	if desc {
		return -d
	}
	return d
}

func tokenTotal(m TokenMetrics) *uint64 {
	if m.Counts == nil {
		return nil
	}
	total := m.Counts.Total()
	return &total
}

func compareJobs(a, c JobItem, sortBy SortKey, desc bool) int {
	var d int
	switch sortBy {
	case SortTime:
		d = compareOptional(a.DurationMS, c.DurationMS, desc)
	case SortTokens:
		d = compareOptional(tokenTotal(a.Tokens), tokenTotal(c.Tokens), desc)
	case SortCost:
		d = compareOptional(a.Money.EstimatedCostUSD, c.Money.EstimatedCostUSD, desc)
	case SortName:
		d = compareText(a.Title, c.Title, desc)
	case SortModel:
		d = compareModelNames(a.Model, c.Model, desc)
	default: // SortStarted
		d = a.StartedAt.Compare(c.StartedAt)
		if desc {
			d = -d
		}
	}
	if d != 0 {
		return d
	}
	return compareStartThenID(a.StartedAt, a.ID, c.StartedAt, c.ID)
}

// compareModelNames puts jobs without a model last in both directions.
func compareModelNames(a, c *ModelRef, desc bool) int {
	switch {
	case a == nil && c == nil:
		return 0
	case a == nil:
		return 1
	case c == nil:
		return -1
	default:
		return compareText(a.Key, c.Key, desc)
	}
}

func compareRows(a, c Row, sortBy SortKey, desc bool) int {
	var d int
	switch sortBy {
	case SortCost:
		d = compareOptional(a.Money.EstimatedCostUSD, c.Money.EstimatedCostUSD, desc)
	case SortTokens:
		d = compareOptional(tokenTotal(a.Tokens), tokenTotal(c.Tokens), desc)
	case SortTime:
		d = compareOptional(a.Time.DurationMS, c.Time.DurationMS, desc)
	case SortJobs:
		d = compareInts(a.Jobs, c.Jobs, desc)
	default: // SortName and the job-only keys
		d = compareText(a.Label, c.Label, desc)
	}
	if d != 0 {
		return d
	}
	return strings.Compare(a.Key, c.Key)
}

func compareInts(a, b int, desc bool) int {
	switch {
	case a == b:
		return 0
	case (a < b) != desc:
		return -1
	default:
		return 1
	}
}

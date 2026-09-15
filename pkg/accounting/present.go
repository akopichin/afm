package accounting

import (
	"sort"
)

type Coverage string

const (
	CoverageFull    Coverage = "full"
	CoveragePartial Coverage = "partial"
	CoverageNone    Coverage = "none"
)

type AttrKind string

const (
	AttrStage      AttrKind = "stage"
	AttrRunOverhead AttrKind = "run_overhead"
	AttrUnknown    AttrKind = "unknown"
)

type Attribution struct {
	Kind    AttrKind `json:"kind"`
	StageID string   `json:"stage_id,omitempty"`
}

type CoverageIssue struct {
	Kind        string      `json:"kind"`
	Attribution Attribution `json:"attribution"`
	Phase       string      `json:"phase"`
	Channel     string      `json:"channel"`
	Model       string      `json:"model"`
	Reason      string      `json:"reason"`
	Count       int         `json:"count"`
}

func coverageOf(s Summary) Coverage {
	priced := s.Metered - s.Unpriced
	gaps := s.Unpriced + s.Unmetered
	switch {
	case priced >= 1 && gaps == 0:
		return CoverageFull
	case priced >= 1:
		return CoveragePartial
	default:
		return CoverageNone
	}
}

// DisplayCost is the one cost string every surface renders. Full/partial reuse
// FormatUSD's tiny-value handling for the priced sum; partial appends "+"; none
// is the em dash (dashUnknown). Never gates on the money value.
func DisplayCost(s Summary) string {
	switch coverageOf(s) {
	case CoverageFull:
		return FormatUSD(s.CostUSD, true)
	case CoveragePartial:
		return FormatUSD(s.CostUSD, true) + "+"
	default:
		return dashUnknown
	}
}

// attributionOf maps a UsageRecord to its Attribution:
// - if Scope == ScopeRunOverhead, return AttrRunOverhead
// - else if StageID is non-empty, return AttrStage with that ID
// - else return AttrUnknown
func attributionOf(r UsageRecord) Attribution {
	if r.Scope == ScopeRunOverhead {
		return Attribution{Kind: AttrRunOverhead}
	}
	if r.StageID != "" {
		return Attribution{Kind: AttrStage, StageID: r.StageID}
	}
	return Attribution{Kind: AttrUnknown}
}

// Label renders the Attribution as a display string:
// - AttrRunOverhead → "run_overhead"
// - AttrStage → the stage id
// - AttrUnknown → dashUnknown ("—")
func (a Attribution) Label() string {
	switch a.Kind {
	case AttrRunOverhead:
		return "run_overhead"
	case AttrStage:
		return a.StageID
	default:
		return dashUnknown
	}
}

// aggregateIssues processes a slice of UsageRecords and returns a sorted list
// of CoverageIssues. A record is an issue if it is unpriced (!Priced && Metered)
// or unmetered (!Metered). Issues are grouped by a composite key
// (attribution kind, stage id, phase, channel, model, reason), with counts
// accumulated. The result is sorted by a stable total order:
// attribution kind, stage id, phase, issue kind, channel, model, reason.
func aggregateIssues(recs []UsageRecord) []CoverageIssue {
	type issueKey struct {
		attrKind  AttrKind
		stageID   string
		phase     string
		channel   string
		model     string
		reason    string
		issueKind string
	}

	m := make(map[issueKey]*CoverageIssue)

	for _, r := range recs {
		// Determine if this record is an issue
		var issueKind string
		if !r.Metered {
			issueKind = "unmetered"
		} else if !r.Priced {
			issueKind = "unpriced"
		} else {
			// Record is priced and metered, not an issue
			continue
		}

		// Get attribution
		attr := attributionOf(r)

		// Build the issue key
		key := issueKey{
			attrKind:  attr.Kind,
			stageID:   attr.StageID,
			phase:     r.Phase,
			channel:   r.Channel,
			model:     r.Model,
			reason:    r.Reason,
			issueKind: issueKind,
		}

		// Either increment existing or create new
		if issue, exists := m[key]; exists {
			issue.Count++
		} else {
			m[key] = &CoverageIssue{
				Kind:        issueKind,
				Attribution: attr,
				Phase:       r.Phase,
				Channel:     r.Channel,
				Model:       r.Model,
				Reason:      r.Reason,
				Count:       1,
			}
		}
	}

	// Collect and sort
	issues := make([]CoverageIssue, 0, len(m))
	for _, issue := range m {
		issues = append(issues, *issue)
	}

	// Sort by: attrKind, stageID, phase, issueKind, channel, model, reason
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]

		// Compare attribution kind
		if a.Attribution.Kind != b.Attribution.Kind {
			return a.Attribution.Kind < b.Attribution.Kind
		}

		// Compare stage id
		if a.Attribution.StageID != b.Attribution.StageID {
			return a.Attribution.StageID < b.Attribution.StageID
		}

		// Compare phase
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}

		// Compare issue kind
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}

		// Compare channel
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}

		// Compare model
		if a.Model != b.Model {
			return a.Model < b.Model
		}

		// Compare reason
		return a.Reason < b.Reason
	})

	return issues
}

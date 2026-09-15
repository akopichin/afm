package accounting

import (
	"slices"
	"strings"
)

type Coverage string

const (
	CoverageFull    Coverage = "full"
	CoveragePartial Coverage = "partial"
	CoverageNone    Coverage = "none"
)

type AttrKind string

const (
	AttrStage       AttrKind = "stage"
	AttrRunOverhead AttrKind = "run_overhead"
	AttrUnknown     AttrKind = "unknown"
)

type Attribution struct {
	Kind    AttrKind `json:"kind"`
	StageID string   `json:"stage_id,omitempty"`
}

// CostView is the JSON DTO for cost information displayed in the UI.
type CostView struct {
	DisplayCost       string         `json:"display_cost"`
	Coverage          string         `json:"coverage"`
	EstimatedCostUSD  float64        `json:"estimated_cost_usd"`
	Metered           int            `json:"metered"`
	PricedInvocations int            `json:"priced_invocations"`
	Unpriced          int            `json:"unpriced"`
	Unmetered         int            `json:"unmetered"`
	Models            []string       `json:"models"`
	UncachedInput     uint64         `json:"uncached_input"`
	CacheRead         uint64         `json:"cache_read"`
	CacheWrite5m      uint64         `json:"cache_write_5m"`
	CacheWrite1h      uint64         `json:"cache_write_1h"`
	CacheWriteOther   uint64         `json:"cache_write_other"`
	Output            uint64         `json:"output"`
	ReasoningOutput   uint64         `json:"reasoning_output"`
	TotalTokens       uint64         `json:"total_tokens"`
	CacheWriteTotal   uint64         `json:"cache_write_total"`
	CacheHitRatio     *float64       `json:"cache_hit_ratio"`
	Phases            map[string]int `json:"phases"`
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

// The two values CoverageIssue.Kind ever takes — exported so every consumer
// (this package's own renderCoverage, cmd/afm's coverageNote) compares
// against the same identifier instead of re-typing the literal.
const (
	IssueKindUnmetered = "unmetered"
	IssueKindUnpriced  = "unpriced"
)

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
		return string(AttrRunOverhead)
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
			issueKind = IssueKindUnmetered
		} else if !r.Priced {
			issueKind = IssueKindUnpriced
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
	slices.SortStableFunc(issues, func(a, b CoverageIssue) int {
		// Compare attribution kind
		if a.Attribution.Kind != b.Attribution.Kind {
			return strings.Compare(string(a.Attribution.Kind), string(b.Attribution.Kind))
		}

		// Compare stage id
		if a.Attribution.StageID != b.Attribution.StageID {
			return strings.Compare(a.Attribution.StageID, b.Attribution.StageID)
		}

		// Compare phase
		if a.Phase != b.Phase {
			return strings.Compare(a.Phase, b.Phase)
		}

		// Compare issue kind
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}

		// Compare channel
		if a.Channel != b.Channel {
			return strings.Compare(a.Channel, b.Channel)
		}

		// Compare model
		if a.Model != b.Model {
			return strings.Compare(a.Model, b.Model)
		}

		// Compare reason
		return strings.Compare(a.Reason, b.Reason)
	})

	return issues
}

// cacheHitRatioPtr returns a pointer to the cache hit ratio if InputTotal > 0,
// otherwise returns nil.
func cacheHitRatioPtr(t Tokens) *float64 {
	if t.InputTotal() == 0 {
		return nil
	}
	ratio := t.CacheHitRatio()
	return &ratio
}

// BuildCostView constructs a CostView DTO from a Summary and phases map.
func BuildCostView(s Summary, phases map[string]int) *CostView {
	return &CostView{
		DisplayCost:       DisplayCost(s),
		Coverage:          string(coverageOf(s)),
		EstimatedCostUSD:  s.CostUSD,
		Metered:           s.Metered,
		PricedInvocations: s.Metered - s.Unpriced,
		Unpriced:          s.Unpriced,
		Unmetered:         s.Unmetered,
		Models:            s.Models,
		UncachedInput:     s.Tokens.UncachedInput,
		CacheRead:         s.Tokens.CacheRead,
		CacheWrite5m:      s.Tokens.CacheWrite5m,
		CacheWrite1h:      s.Tokens.CacheWrite1h,
		CacheWriteOther:   s.Tokens.CacheWriteOther,
		Output:            s.Tokens.Output,
		ReasoningOutput:   s.Tokens.ReasoningOutput,
		TotalTokens:       s.Tokens.Total(),
		CacheWriteTotal:   s.Tokens.CacheWriteTotal(),
		CacheHitRatio:     cacheHitRatioPtr(s.Tokens),
		Phases:            phases,
	}
}

package memory

import (
	"regexp"
	"strings"
)

type Status string

const (
	StatusNew        Status = "new"
	StatusReinforced Status = "reinforced"
	StatusUnchanged  Status = "unchanged"
)

type MergedFinding struct {
	Finding `yaml:",inline"`
	Status  Status `yaml:"status"`
}

type MergedStore struct {
	Findings []MergedFinding `yaml:"findings"`
}

// slugFromStatement creates a slug from a statement string.
func slugFromStatement(s string) string {
	// Lowercase
	s = strings.ToLower(s)
	// Replace non-alphanumeric with dash
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	// Trim dashes from both ends
	s = strings.Trim(s, "-")
	// Cap length at ~40 chars
	if len(s) > 40 {
		s = s[:40]
		// Trim trailing dash if present
		s = strings.TrimRight(s, "-")
	}
	return s
}

// Reconcile merges findings from a previous store and a merged store.
func Reconcile(prev Store, merged MergedStore, runID string) Store {
	// Build a map of previous findings by ID for quick lookup
	prevByID := make(map[string]Finding)
	for _, f := range prev.Findings {
		prevByID[f.ID] = f
	}

	// Track emitted IDs to detect collisions
	emittedIDs := make(map[string]bool)

	var result []Finding

	for _, mf := range merged.Findings {
		f := mf.Finding

		// For StatusNew or missing ID, generate ID before validity check
		if (mf.Status == StatusNew || mf.Status == StatusUnchanged) && (f.ID == "" || emittedIDs[f.ID]) {
			baseSlug := slugFromStatement(f.Statement)
			f.ID = baseSlug
			suffix := 2
			for emittedIDs[f.ID] {
				f.ID = baseSlug + "-" + string(rune('0'+suffix-2)) // -2, -3, etc.
				suffix++
			}
		}

		// Drop invalid findings
		if !f.Valid() {
			continue
		}

		switch mf.Status {
		case StatusNew:
			f.FirstSeen = runID
			f.LastSeen = runID
			f.ConfirmCount = 1

		case StatusReinforced:
			if prev, ok := prevByID[f.ID]; ok {
				f.FirstSeen = prev.FirstSeen
			} else {
				// Treat as new if no previous match
				f.FirstSeen = runID
			}
			f.LastSeen = runID
			if prev, ok := prevByID[f.ID]; ok {
				f.ConfirmCount = prev.ConfirmCount + 1
			} else {
				f.ConfirmCount = 1
			}

		case StatusUnchanged:
			if prev, ok := prevByID[f.ID]; ok {
				f = prev
			} else {
				// Treat as new if no previous match
				// ID should already be generated above for StatusNew
				f.FirstSeen = runID
				f.LastSeen = runID
				f.ConfirmCount = 1
			}
		default:
			// Unknown status, skip this finding
			continue
		}

		emittedIDs[f.ID] = true
		result = append(result, f)
	}

	return Store{Findings: result}
}

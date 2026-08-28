package memory

import (
	"slices"
)

// Evict keeps only the maxSize highest-value findings, dropping the lowest-confidence ones.
// Value order: ConfirmCount desc, then LastSeen desc (lexical), then stable by original index.
// Returns findings in their original order.
func Evict(s Store, maxSize int) Store {
	if len(s.Findings) <= maxSize {
		return s
	}

	// Create a slice of indices to sort
	type indexedFinding struct {
		index int
		f     Finding
	}

	indexed := make([]indexedFinding, len(s.Findings))
	for i, f := range s.Findings {
		indexed[i] = indexedFinding{index: i, f: f}
	}

	// Sort by value: ConfirmCount desc, then LastSeen desc, then original index asc
	slices.SortFunc(indexed, func(i, j indexedFinding) int {
		fi, fj := i.f, j.f
		// Compare ConfirmCount (descending)
		if fi.ConfirmCount != fj.ConfirmCount {
			if fi.ConfirmCount > fj.ConfirmCount {
				return -1
			}
			return 1
		}
		// Compare LastSeen (descending, lexically)
		if fi.LastSeen != fj.LastSeen {
			if fi.LastSeen > fj.LastSeen {
				return -1
			}
			return 1
		}
		// Stable by original index (ascending)
		if i.index < j.index {
			return -1
		}
		if i.index > j.index {
			return 1
		}
		return 0
	})

	// Keep top maxSize
	keep := make(map[int]bool)
	for i := 0; i < maxSize; i++ {
		keep[indexed[i].index] = true
	}

	// Return in original order
	var result []Finding
	for i, f := range s.Findings {
		if keep[i] {
			result = append(result, f)
		}
	}

	return Store{Findings: result}
}

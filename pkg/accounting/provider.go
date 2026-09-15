package accounting

// Health reports whether a run's usage accounting is currently trustworthy —
// distinct from Coverage (which describes how completely a single group of
// records is priced): Health describes the Store itself (can it still be
// written to / trusted at all).
type Health string

const (
	HealthOK          Health = "ok"
	HealthUnavailable Health = "unavailable"
)

// CostBundle is the complete, ready-to-render snapshot of a run's cost data:
// one CostView per stage, one for the whole run, one for run-level overhead
// (nil when there is none), plus the coverage issues surfaced across every
// record. It is what the server layer hands to the dashboard/API — built
// once per Store revision and cached (see Store.CostSnapshot).
type CostBundle struct {
	Stages   map[string]*CostView
	Run      *CostView
	Overhead *CostView
	Issues   []CoverageIssue
	Health   Health
	HasData  bool

	// builtAt is the Store revision this bundle's contents were computed
	// from. It has no meaning outside this package — tests use it to assert
	// that repeated CostSnapshot calls between Appends reuse one build
	// instead of re-aggregating.
	builtAt uint64
}

// CostProvider is how callers outside this package (the server layer) read
// cost data, without depending on *Store directly — so a run whose
// accounting Store could not even be opened can still serve cost data
// through the same interface, via StaticUnavailable.
type CostProvider interface {
	CostSnapshot() CostBundle
}

// StaticUnavailable returns a CostProvider whose CostSnapshot is always the
// fixed "no data, unavailable" bundle. Used when a run's accounting Store
// failed to open at all (accounting.Open returned no usable *Store) — the
// server still needs SOME CostProvider to call, not a nil check at every
// call site.
func StaticUnavailable() CostProvider { return staticUnavailableProvider{} }

type staticUnavailableProvider struct{}

func (staticUnavailableProvider) CostSnapshot() CostBundle {
	return CostBundle{Health: HealthUnavailable, HasData: false}
}

// CostSnapshot returns the current cost bundle for the run this Store
// backs. If nothing has changed (no Append, no unavailable-latch) since the
// last build, the cached bundle is returned as-is — no ledger copy, no
// re-aggregation. Otherwise it rebuilds: the raw records/summaries/health
// are copied out under the lock, the CostViews/issues are computed from that
// local copy without holding the lock, and the result is cached under the
// lock before being returned.
//
// The returned CostBundle's Stages map and Issues slice are always fresh —
// a caller mutating them can never corrupt a later CostSnapshot call.
func (s *Store) CostSnapshot() CostBundle {
	s.mu.Lock()
	if s.cachedBundle != nil && s.cachedRevision == s.revision {
		b := cloneBundle(*s.cachedBundle)
		s.mu.Unlock()
		return b
	}
	recs := append([]UsageRecord(nil), s.ledger.recs...)
	byStage := s.ledger.SummaryByStage()
	run := s.ledger.RunSummary()
	unavailable := s.unavailable
	rev := s.revision
	s.mu.Unlock()

	built := buildCostBundle(recs, byStage, run, unavailable, rev)

	s.mu.Lock()
	// Only cache if nothing changed while we were building outside the
	// lock — otherwise we'd cache a bundle that's already stale, and the
	// NEXT unchanged call would wrongly reuse it instead of rebuilding.
	if rev == s.revision {
		cached := built
		s.cachedBundle = &cached
		s.cachedRevision = rev
	}
	s.mu.Unlock()

	return cloneBundle(built)
}

// buildCostBundle is the pure aggregation step: given an immutable snapshot
// of records/summaries/health, it produces the CostBundle. It touches no
// Store state and takes no lock.
func buildCostBundle(recs []UsageRecord, byStage map[string]Summary, run Summary, unavailable bool, rev uint64) CostBundle {
	health := HealthOK
	if unavailable {
		health = HealthUnavailable
	}

	bundle := CostBundle{
		Health:  health,
		HasData: len(recs) > 0,
		builtAt: rev,
	}
	if !bundle.HasData {
		return bundle
	}

	// One pass over the records builds phase→count maps for every stage,
	// for run-overhead, and for the run as a whole — instead of re-scanning
	// recs once per stage.
	stagePhases := make(map[string]map[string]int, len(byStage))
	overheadPhases := make(map[string]int)
	runPhases := make(map[string]int)
	var overheadRecs []UsageRecord

	for _, r := range recs {
		runPhases[r.Phase]++
		switch {
		case r.Scope == ScopeRunOverhead:
			overheadPhases[r.Phase]++
			overheadRecs = append(overheadRecs, r)
		case r.StageID != "":
			m := stagePhases[r.StageID]
			if m == nil {
				m = make(map[string]int)
				stagePhases[r.StageID] = m
			}
			m[r.Phase]++
		default:
			// No stage attribution and not run-overhead — an unattributed
			// record. It still counts toward runPhases (already added
			// above); it just doesn't belong to any per-stage/overhead
			// group.
		}
	}

	stages := make(map[string]*CostView, len(byStage))
	for id, sum := range byStage {
		stages[id] = BuildCostView(sum, stagePhases[id])
	}
	bundle.Stages = stages
	bundle.Run = BuildCostView(run, runPhases)

	if len(overheadRecs) > 0 {
		bundle.Overhead = BuildCostView(summarize(overheadRecs), overheadPhases)
	}

	bundle.Issues = aggregateIssues(recs)
	return bundle
}

// cloneBundle returns a copy of b whose Stages map and Issues slice are
// fresh — safe to hand to a caller who might mutate them. The *CostView
// values themselves are shared (they're never mutated once built).
func cloneBundle(b CostBundle) CostBundle {
	clone := b
	if b.Stages != nil {
		stages := make(map[string]*CostView, len(b.Stages))
		for k, v := range b.Stages {
			stages[k] = v
		}
		clone.Stages = stages
	}
	if b.Issues != nil {
		clone.Issues = append([]CoverageIssue(nil), b.Issues...)
	}
	return clone
}

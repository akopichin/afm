package accounting

// Health reports whether a run's usage accounting is currently trustworthy — distinct from Coverage:
// Health describes the Store itself.
type Health string

const (
	HealthOK          Health = "ok"
	HealthUnavailable Health = "unavailable"
)

// CostBundle holds cached cost views for stages, run, optional overhead, and coverage issues.
type CostBundle struct {
	Stages   map[string]*CostView
	Run      *CostView
	Overhead *CostView
	Issues   []CoverageIssue
	Health   Health
	HasData  bool

	// builtAt is the Store revision this bundle's contents were computed from.
	builtAt uint64
}

// CostProvider exposes cost snapshots, including StaticUnavailable when the Store cannot open.
type CostProvider interface {
	CostSnapshot() CostBundle
}

// StaticUnavailable returns a CostProvider whose CostSnapshot is always the fixed "no data, unavailable"
// bundle.
func StaticUnavailable() CostProvider { return staticUnavailableProvider{} }

type staticUnavailableProvider struct{}

func (staticUnavailableProvider) CostSnapshot() CostBundle {
	return CostBundle{Health: HealthUnavailable, HasData: false}
}

// CostSnapshot returns the current cost bundle for the run this Store backs. The result is cached under
// the lock before being returned.
func (s *Store) CostSnapshot() CostBundle {
	s.mu.Lock()
	if s.cachedBundle != nil && s.cachedRevision == s.revision {
		b := cloneBundle(*s.cachedBundle)
		s.mu.Unlock()
		return b
	}
	recs := append([]UsageRecord(nil), s.ledger.recs...)
	unavailable := s.unavailable
	rev := s.revision
	s.mu.Unlock()

	// Aggregate the copied records outside the lock — SummaryByStage/ RunSummary are both O scans, and Append
	// must not wait on them.
	tmp := &Ledger{recs: recs}
	byStage := tmp.SummaryByStage()
	run := tmp.RunSummary()

	built := buildCostBundle(recs, byStage, run, unavailable, rev)

	s.mu.Lock()
	// Cache only if the revision still matches; a concurrent change would make this bundle stale.
	if rev == s.revision {
		cached := built
		s.cachedBundle = &cached
		s.cachedRevision = rev
	}
	s.mu.Unlock()

	return cloneBundle(built)
}

// buildCostBundle is the pure aggregation step: given an immutable snapshot of records/summaries/health,
// it produces the CostBundle. It touches no Store state and takes no lock.
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

	// One pass over the records builds phase→count maps for every stage, for run-overhead, and for the run as
	// a whole — instead of re-scanning recs once per stage.
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
			// No stage attribution and not run-overhead — an unattributed record. It still counts toward runPhases
			// (already added above); it just doesn't belong to any per-stage/overhead group.
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

// cloneBundle deep-copies maps, slices, and pointers so callers cannot mutate cached snapshots.
func cloneBundle(b CostBundle) CostBundle {
	clone := b
	if b.Stages != nil {
		stages := make(map[string]*CostView, len(b.Stages))
		for k, v := range b.Stages {
			stages[k] = cloneCostView(v)
		}
		clone.Stages = stages
	}
	clone.Run = cloneCostView(b.Run)
	clone.Overhead = cloneCostView(b.Overhead)
	if b.Issues != nil {
		clone.Issues = append([]CoverageIssue(nil), b.Issues...)
	}
	return clone
}

// cloneCostView returns a deep copy of v: a fresh CostView whose Phases map, Models slice and
// CacheHitRatio pointer are all independent of v's. Nil in, nil out.
func cloneCostView(v *CostView) *CostView {
	if v == nil {
		return nil
	}
	clone := *v
	if v.Models != nil {
		clone.Models = append([]string(nil), v.Models...)
	}
	if v.Phases != nil {
		phases := make(map[string]int, len(v.Phases))
		for k, n := range v.Phases {
			phases[k] = n
		}
		clone.Phases = phases
	}
	if v.CacheHitRatio != nil {
		ratio := *v.CacheHitRatio
		clone.CacheHitRatio = &ratio
	}
	return &clone
}

// Package graph holds the orchestrator's runtime registry of stage definitions: a stable-order stage list,
// an id index and the dependency DAG, versioned so a change always produces a brand-new, immutable Registry
// rather than mutating one that other goroutines may be reading concurrently (dev-plan.md Этап 8, T8.1).
package graph

import (
	"slices"
	"sync/atomic"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// Registry is an immutable, versioned snapshot of a run's stage definitions. Every accessor returns a copy —
// callers can freely mutate what they get back without ever reaching the registry's own state, and a
// Registry handle obtained once stays internally consistent forever (a later Replace on the owning Store
// builds a NEW Registry one revision ahead; it never touches this one).
type Registry struct {
	revision int64
	order    []string // stage ids, in stable declaration order
	byID     map[string]flow.Stage
	deps     map[string][]string // id → depends_on, defensively copied out of byID for O(1) reuse
}

// NewRegistry builds revision 1 from stages in declaration order (the same order flow.yaml listed them, the
// order opts.Stages iteration used to rely on). The input slice/its stage structs are never retained by
// reference — later mutation of the caller's backing array never reaches the registry.
func NewRegistry(stages []flow.Stage) *Registry {
	return newRegistryAt(1, stages)
}

func newRegistryAt(revision int64, stages []flow.Stage) *Registry {
	order := make([]string, 0, len(stages))
	byID := make(map[string]flow.Stage, len(stages))
	deps := make(map[string][]string, len(stages))
	for _, s := range stages {
		order = append(order, s.ID)
		// DependsOn is cloned before storing: flow.Stage's assignment above only copies the slice header, so
		// without this, a slice returned later by Stages()/Stage() would still alias the SAME backing array
		// as the registry's own internal copy — mutating it through a caller's returned value would corrupt
		// ReadyStages' dependency walk for every future reader.
		s.DependsOn = slices.Clone(s.DependsOn)
		s.Skills = slices.Clone(s.Skills)
		byID[s.ID] = s
		deps[s.ID] = slices.Clone(s.DependsOn)
	}
	return &Registry{revision: revision, order: order, byID: byID, deps: deps}
}

// Revision identifies this exact Registry instance's version: 1 for the registry a run started with, +1 for
// every subsequent Store.Replace. nil-safe (see the package-level nil-receiver note on Stages).
func (r *Registry) Revision() int64 {
	if r == nil {
		return 0
	}
	return r.revision
}

// cloneStage copies s AND re-clones its DependsOn and Skills slices: a plain struct copy only copies the
// slice headers, which would still alias the registry's own stored backing arrays. Every accessor that hands
// a flow.Stage (or a pointer to one) to a caller must go through this, not a bare struct copy.
func cloneStage(s flow.Stage) flow.Stage {
	s.DependsOn = slices.Clone(s.DependsOn)
	s.Skills = slices.Clone(s.Skills)
	return s
}

// Stages returns every stage, as a fresh copy, in the registry's stable order. nil-safe — a nil *Registry
// (a test fixture that never needed stage definitions, same "nil-receiver → empty" convention already used
// by assistants.Registry.Resolve) behaves like an empty registry instead of panicking; orchestrator.New
// always installs a real, non-nil Registry in production.
func (r *Registry) Stages() []flow.Stage {
	if r == nil {
		return nil
	}
	out := make([]flow.Stage, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, cloneStage(r.byID[id]))
	}
	return out
}

// IDs returns every stage id, as a fresh copy, in the registry's stable order. nil-safe (see Stages).
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	return slices.Clone(r.order)
}

// AllIDs is an alias for IDs kept for call sites migrating off the old graph.Graph.AllIDs name; order is not
// part of AllIDs' contract (unlike IDs/Stages), callers needing a stable order should prefer IDs.
func (r *Registry) AllIDs() []string { return r.IDs() }

// Stage returns a pointer to a COPY of the stage with id, or nil if it is not registered. Because the
// pointer addresses a fresh local copy (not the registry's internal map storage), mutating *Stage(id) can
// never corrupt the registry — unlike the old graph.Graph, which handed out pointers straight into the
// backing opts.Stages array.
func (r *Registry) Stage(id string) *flow.Stage {
	if r == nil {
		return nil
	}
	s, ok := r.byID[id]
	if !ok {
		return nil
	}
	s = cloneStage(s)
	return &s
}

// StageByID is Stage's non-pointer form, matching the (value, ok) idiom used elsewhere in this codebase
// (e.g. assistants.Registry.Get). nil-safe (see Stages).
func (r *Registry) StageByID(id string) (flow.Stage, bool) {
	if r == nil {
		return flow.Stage{}, false
	}
	s, ok := r.byID[id]
	if !ok {
		return flow.Stage{}, false
	}
	return cloneStage(s), true
}

// DependsOn returns a fresh copy of id's depends_on list (nil/empty if id is unknown or has no
// dependencies). nil-safe (see Stages).
func (r *Registry) DependsOn(id string) []string {
	if r == nil {
		return nil
	}
	return slices.Clone(r.deps[id])
}

// ReadyStages returns the ids of stages that are in StatusReady and whose dependencies are all StatusDone —
// identical semantics to the old graph.Graph.ReadyStages. nil-safe (see Stages).
func (r *Registry) ReadyStages(statuses map[string]state.StageStatus) []string {
	if r == nil {
		return nil
	}
	var ready []string
	for _, id := range r.order {
		if statuses[id] != state.StatusReady {
			continue
		}
		allDone := true
		for _, dep := range r.deps[id] {
			if statuses[dep] != state.StatusDone {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, id)
		}
	}
	return ready
}

// WithStages returns a NEW Registry one revision ahead of r, built from stages. r itself is left completely
// untouched — any goroutine still holding r keeps observing its original, consistent view forever.
func (r *Registry) WithStages(stages []flow.Stage) *Registry {
	return newRegistryAt(r.revision+1, stages)
}

// Store holds the single current Registry for a run behind an atomic pointer. Load never blocks and never
// observes a partially built Registry; Replace installs a new revision in one atomic store, so concurrent
// readers either see the whole old graph or the whole new one, never a mix of both fields from two versions.
//
// Stage 8 does not yet wire any production caller into Replace — no UI/API can edit the graph until Этап 9 —
// but the type is fully implemented and tested here so that future work only has to call it, not invent it.
type Store struct {
	ptr atomic.Pointer[Registry]
}

// NewStore creates a Store seeded with initial (normally revision 1 from NewRegistry).
func NewStore(initial *Registry) *Store {
	s := &Store{}
	s.ptr.Store(initial)
	return s
}

// Load returns the current Registry. Safe to call concurrently with Replace. nil-safe: a nil *Store (a test
// fixture built as a bare &Orchestrator{} literal, never routed through orchestrator.New — the same "nil
// receiver behaves like empty" convention Registry's own accessors use) returns a nil Registry instead of
// panicking; every Registry method already tolerates that nil.
func (s *Store) Load() *Registry {
	if s == nil {
		return nil
	}
	return s.ptr.Load()
}

// Replace builds a new Registry one revision ahead of the current one and atomically installs it, returning
// the new Registry. Safe to call concurrently with Load and with other Replace calls: a CompareAndSwap loop
// means two overlapping Replace calls can never both land on the same revision number or silently lose one
// of their updates — the loser recomputes its new Registry against whatever the winner just installed and
// retries.
func (s *Store) Replace(stages []flow.Stage) *Registry {
	for {
		cur := s.Load()
		next := cur.WithStages(stages)
		if s.ptr.CompareAndSwap(cur, next) {
			return next
		}
	}
}

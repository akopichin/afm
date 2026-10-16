package graph_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func stagesFor(ids ...string) []flow.Stage {
	stages := make([]flow.Stage, len(ids))
	for i, id := range ids {
		stages[i] = flow.Stage{ID: id}
	}
	return stages
}

// TestRegistry_SnapshotImmutable: T8.1 — mutating a slice returned by the registry (Stages/IDs/DependsOn)
// must never reach the registry's own internal state; a concurrent reader racing a Replace must also never
// observe a torn/mixed registry (run with -race).
func TestRegistry_SnapshotImmutable(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a", DependsOn: nil},
		{ID: "b", DependsOn: []string{"a"}},
	}
	store := graph.NewStore(graph.NewRegistry(stages))

	// Mutate what Stages() returns; the registry must not change.
	got := store.Load().Stages()
	got[0].ID = "mutated"
	got[1].DependsOn[0] = "mutated"

	again := store.Load().Stages()
	if again[0].ID != "a" {
		t.Fatalf("Stages()[0].ID = %q, want %q (mutation leaked into registry)", again[0].ID, "a")
	}
	if again[1].DependsOn[0] != "a" {
		t.Fatalf("Stages()[1].DependsOn[0] = %q, want %q (mutation leaked into registry)", again[1].DependsOn[0], "a")
	}

	// Mutate what IDs()/DependsOn() return.
	ids := store.Load().IDs()
	ids[0] = "mutated-id"
	if again2 := store.Load().IDs(); again2[0] != "a" {
		t.Fatalf("IDs()[0] = %q after external mutation, want %q", again2[0], "a")
	}
	deps := store.Load().DependsOn("b")
	deps[0] = "mutated-dep"
	if again3 := store.Load().DependsOn("b"); again3[0] != "a" {
		t.Fatalf("DependsOn(b)[0] = %q after external mutation, want %q", again3[0], "a")
	}

	// Concurrent reader during a Replace: -race must find nothing, and every observed registry must be
	// internally consistent (len(order) == len(byID)).
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var readErr atomic.Value
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			reg := store.Load()
			if len(reg.IDs()) != len(reg.Stages()) {
				readErr.Store("mixed/torn registry observed")
				return
			}
		}
	})
	for i := 0; i < 50; i++ {
		store.Replace(stagesFor("a", "b", "c"))
	}
	close(stop)
	wg.Wait()
	if v := readErr.Load(); v != nil {
		t.Fatalf("concurrent reader error: %v", v)
	}
}

// TestRegistry_RevisionIncrementsOnChange: a replace never mutates the old Registry in place — it produces a
// new one, one revision ahead.
func TestRegistry_RevisionIncrementsOnChange(t *testing.T) {
	store := graph.NewStore(graph.NewRegistry(stagesFor("a")))
	first := store.Load()
	if first.Revision() != 1 {
		t.Fatalf("initial revision = %d, want 1", first.Revision())
	}
	second := store.Replace(stagesFor("a", "b"))
	if second.Revision() != 2 {
		t.Fatalf("revision after Replace = %d, want 2", second.Revision())
	}
	// The old registry handle is untouched by the replace.
	if first.Revision() != 1 {
		t.Fatalf("old registry handle's revision changed in place: %d", first.Revision())
	}
	if len(first.IDs()) != 1 {
		t.Fatalf("old registry handle mutated: IDs = %v", first.IDs())
	}
	third := store.Replace(stagesFor("a", "b", "c"))
	if third.Revision() != 3 {
		t.Fatalf("revision after second Replace = %d, want 3", third.Revision())
	}
}

// TestRegistry_ConcurrentReadersNeverSeeMixedVersion: every concurrent Load() during a burst of Replace
// calls must return a self-consistent registry (coherent revision ↔ stage set, i.e. the stage set is exactly
// the one that was installed for that revision), never a mix of two versions' data.
func TestRegistry_ConcurrentReadersNeverSeeMixedVersion(t *testing.T) {
	store := graph.NewStore(graph.NewRegistry(stagesFor("a")))
	revisionLen := map[int64]int{1: 1} // revision -> expected stage count, filled in as Replace runs
	var mu sync.Mutex

	idSets := make([][]string, 20)
	for i := range idSets {
		ids := make([]string, i+2) // distinct length per step, always != previous
		for j := range ids {
			ids[j] = string(rune('a' + j))
		}
		idSets[i] = ids
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan string, 100)
	for r := 0; r < 8; r++ {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				reg := store.Load()
				mu.Lock()
				wantLen, known := revisionLen[reg.Revision()]
				mu.Unlock()
				if known && len(reg.IDs()) != wantLen {
					select {
					case errs <- "revision/stage-count mismatch: a reader observed a torn/mixed registry":
					default:
					}
					return
				}
			}
		})
	}
	for _, ids := range idSets {
		next := store.Replace(stagesFor(ids...))
		mu.Lock()
		revisionLen[next.Revision()] = len(ids)
		mu.Unlock()
	}
	close(stop)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// TestRegistry_StableOrderMatchesDeclaration: Stages()/IDs() preserve declaration order from flow.yaml, the
// same order the old opts.Stages iteration relied on.
func TestRegistry_StableOrderMatchesDeclaration(t *testing.T) {
	reg := graph.NewRegistry(stagesFor("c", "a", "b"))
	want := []string{"c", "a", "b"}
	if got := reg.IDs(); !equalStrings(got, want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	stages := reg.Stages()
	for i, s := range stages {
		if s.ID != want[i] {
			t.Fatalf("Stages()[%d].ID = %q, want %q", i, s.ID, want[i])
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- Behavioural parity with the old graph.Graph (ReadyStages/Stage/AllIDs semantics preserved). ---

func TestReadyStagesNoDeps(t *testing.T) {
	stages := stagesFor("a", "b", "c")
	statuses := map[string]state.StageStatus{
		"a": state.StatusReady,
		"b": state.StatusReady,
		"c": state.StatusDone,
	}
	g := graph.NewRegistry(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 2 {
		t.Errorf("expected 2 ready stages, got %d: %v", len(ready), ready)
	}
}

func TestReadyStagesBlockedByDep(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
	}
	statuses := map[string]state.StageStatus{
		"a": state.StatusRunning,
		"b": state.StatusReady,
	}
	g := graph.NewRegistry(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 0 {
		t.Errorf("b should be blocked: got %v", ready)
	}
}

func TestReadyStagesDepDone(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
	}
	statuses := map[string]state.StageStatus{
		"a": state.StatusDone,
		"b": state.StatusReady,
	}
	g := graph.NewRegistry(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 1 || ready[0] != "b" {
		t.Errorf("b should be ready when a is done: got %v", ready)
	}
}

func TestRegistry_StageByPointerReturnsCopyNotSharedState(t *testing.T) {
	g := graph.NewRegistry([]flow.Stage{{ID: "a", DependsOn: []string{"x"}}})
	s := g.Stage("a")
	if s == nil {
		t.Fatal("Stage(a) = nil")
	}
	s.DependsOn[0] = "mutated"
	s.ID = "mutated"

	again := g.Stage("a")
	if again.ID != "a" {
		t.Fatalf("Stage(a).ID = %q after external mutation via previous pointer, want %q", again.ID, "a")
	}
	if again.DependsOn[0] != "x" {
		t.Fatalf("Stage(a).DependsOn[0] = %q after external mutation via previous pointer, want %q", again.DependsOn[0], "x")
	}
}

func TestRegistry_StageMissingReturnsNil(t *testing.T) {
	g := graph.NewRegistry(stagesFor("a"))
	if g.Stage("missing") != nil {
		t.Fatal("Stage(missing) != nil")
	}
}

func TestRegistry_AllIDs(t *testing.T) {
	g := graph.NewRegistry(stagesFor("a", "b"))
	ids := g.AllIDs()
	if len(ids) != 2 {
		t.Fatalf("AllIDs() = %v, want 2 ids", ids)
	}
}

// Store.Load never returns nil once constructed, and NewStore seeds revision 1 untouched.
func TestStore_LoadReturnsInitial(t *testing.T) {
	reg := graph.NewRegistry(stagesFor("a"))
	store := graph.NewStore(reg)
	if store.Load() != reg {
		t.Fatal("Store.Load() did not return the exact initial registry instance")
	}
}

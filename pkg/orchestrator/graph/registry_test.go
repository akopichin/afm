package graph_test

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
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

// fullStage fills every reference field of flow.Stage with a value distinct enough to notice a write.
func fullStage() flow.Stage {
	on := true
	limit := 3
	return flow.Stage{
		ID:        "a",
		Agents:    []flow.AgentType{flow.AgentPlanning, flow.AgentReview},
		Skills:    []string{"tdd", "docs"},
		DependsOn: []string{"dep"},
		Artifacts: []flow.Artifact{{Name: "plan", Path: "plan.md", Inline: &on}},
		Inputs:    []flow.Input{{Ref: "dep.plan"}},
		Verify: flow.VerifySpec{
			Steps:           []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "make test"}},
			MaxFailures:     &limit,
			ReviewPatience:  &limit,
			FreshCorrection: &on,
		},
		AutoRun:   &on,
		Buttons:   flow.Buttons{{Label: "go", Prompt: "do it"}},
		Reflect:   &flow.Reflect{File: "memory.md", Mode: flow.ReflectModeRW},
		MemoryUse: &on,
		Hooks: []lifecyclehooks.Hook{{
			ID:          "h",
			Events:      lifecyclehooks.EventSelector{Events: []lifecyclehooks.EventType{lifecyclehooks.EventStageFinished}},
			SkipEvents:  []lifecyclehooks.EventType{lifecyclehooks.EventStageFailed},
			Env:         map[string]lifecyclehooks.SecretRef{"TOKEN": "env:TOKEN"},
			ResolvedEnv: map[string]string{"TOKEN": "secret"},
		}},
		Browser:    &on,
		Assistants: flow.StageAssistants{{Profile: "reviewer", Mode: assistants.Mode("advisory")}},
	}
}

// scribble overwrites every value reachable through s's reference fields, the way a careless caller could.
func scribble(s *flow.Stage) {
	s.Agents[0] = "x"
	s.Skills[0] = "x"
	s.DependsOn[0] = "x"
	s.Artifacts[0].Name = "x"
	*s.Artifacts[0].Inline = false
	s.Inputs[0].Ref = "x"
	s.Verify.Steps[0].Run = "x"
	*s.Verify.MaxFailures = 99
	*s.Verify.ReviewPatience = 99
	*s.Verify.FreshCorrection = false
	*s.AutoRun = false
	s.Buttons[0].Prompt = "x"
	s.Reflect.File = "x"
	*s.MemoryUse = false
	s.Hooks[0].Events.Events[0] = "x"
	s.Hooks[0].SkipEvents[0] = "x"
	s.Hooks[0].Env["TOKEN"] = "x"
	s.Hooks[0].ResolvedEnv["TOKEN"] = "x"
	*s.Browser = false
	s.Assistants[0].Profile = "x"
}

// Every way a stage enters or leaves a Registry must hand out an independent copy of all reference fields.
func TestRegistry_StageReferenceFieldsAreIsolated(t *testing.T) {
	// each case builds a registry from a fresh fullStage and returns the stage a caller could scribble on
	cases := map[string]func(in []flow.Stage) (*graph.Registry, flow.Stage){
		"input slice": func(in []flow.Stage) (*graph.Registry, flow.Stage) {
			return graph.NewRegistry(in), in[0]
		},
		"Stages": func(in []flow.Stage) (*graph.Registry, flow.Stage) {
			reg := graph.NewRegistry(in)
			return reg, reg.Stages()[0]
		},
		"Stage": func(in []flow.Stage) (*graph.Registry, flow.Stage) {
			reg := graph.NewRegistry(in)
			return reg, *reg.Stage("a")
		},
		"StageByID": func(in []flow.Stage) (*graph.Registry, flow.Stage) {
			reg := graph.NewRegistry(in)
			s, _ := reg.StageByID("a")
			return reg, s
		},
		"WithStages input": func(in []flow.Stage) (*graph.Registry, flow.Stage) {
			return graph.NewRegistry(nil).WithStages(in), in[0]
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			reg, victim := build([]flow.Stage{fullStage()})
			scribble(&victim)

			if got, _ := reg.StageByID("a"); !reflect.DeepEqual(got, fullStage()) {
				t.Fatalf("registry stage changed through a caller's copy:\n got %+v\nwant %+v", got, fullStage())
			}
		})
	}
}

// A stage re-registered through WithStages must not share references with the snapshot it came from.
func TestRegistry_WithStagesIsolatesOldAndNewSnapshots(t *testing.T) {
	old := graph.NewRegistry([]flow.Stage{fullStage()})
	next := old.WithStages(old.Stages())

	newStage := next.Stages()[0]
	scribble(&newStage)
	oldStage := old.Stages()[0]
	scribble(&oldStage)

	for name, reg := range map[string]*graph.Registry{"old": old, "new": next} {
		if got, _ := reg.StageByID("a"); !reflect.DeepEqual(got, fullStage()) {
			t.Fatalf("%s snapshot changed after the other one was scribbled on: %+v", name, got)
		}
	}
}

// Dependencies used by ReadyStages must come from the registry's own copy, not the caller's slice.
func TestRegistry_ReadyStagesIgnoresLaterInputMutation(t *testing.T) {
	in := []flow.Stage{{ID: "a"}, {ID: "b", DependsOn: []string{"a"}}}
	reg := graph.NewRegistry(in)
	in[1].DependsOn[0] = "missing"

	statuses := map[string]state.StageStatus{"a": state.StatusDone, "b": state.StatusReady}
	if got := reg.ReadyStages(statuses); len(got) != 1 || got[0] != "b" {
		t.Fatalf("ReadyStages = %v, want [b]", got)
	}
	if got := reg.DependsOn("b"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("DependsOn(b) = %v, want [a]", got)
	}
}

// nil and empty non-nil references must come back exactly as they went in: callers (persistence, API) tell
// them apart.
func TestRegistry_PreservesNilAndEmptyReferences(t *testing.T) {
	cases := map[string]flow.Stage{
		"zero":  {ID: "a"},
		"empty": fillStage(t, false),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			reg := graph.NewRegistry([]flow.Stage{in})
			if got, _ := reg.StageByID("a"); !reflect.DeepEqual(got, in) {
				t.Fatalf("stage round trip changed nil/empty shape:\n got %#v\nwant %#v", got, in)
			}
		})
	}
}

// Guards cloneStage against a reference-typed field (at any depth) added to flow.Stage later: the walk finds
// it by type, so no list of field names has to be kept in sync.
func TestRegistry_ClonesEveryReferenceField(t *testing.T) {
	in := fillStage(t, true)
	reg := graph.NewRegistry([]flow.Stage{in})
	got, _ := reg.StageByID("a")

	if !reflect.DeepEqual(got, in) {
		t.Fatalf("clone differs from the input:\n got %#v\nwant %#v", got, in)
	}
	assertNoSharedReferences(t, "Stage", reflect.ValueOf(in), reflect.ValueOf(got))
}

// fillStage returns a flow.Stage whose every exported field, at any depth, is set: populated=true gives
// non-empty slices/maps and non-nil pointers; populated=false gives empty non-nil slices/maps and nil pointers.
func fillStage(t *testing.T, populated bool) flow.Stage {
	t.Helper()
	var s flow.Stage
	fillValue(t, "Stage", reflect.ValueOf(&s).Elem(), populated)
	s.ID = "a"
	return s
}

func fillValue(t *testing.T, path string, v reflect.Value, populated bool) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("v")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() {
				if hasReference(field.Type) {
					t.Fatalf("%s.%s is an unexported reference field: the guard cannot fill or verify it", path, field.Name)
				}
				continue
			}
			fillValue(t, path+"."+field.Name, v.Field(i), populated)
		}
	case reflect.Slice:
		if !populated {
			v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		for i := range 2 {
			fillValue(t, path+"[]", v.Index(i), populated)
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		if !populated {
			return
		}
		key, elem := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillValue(t, path+"{key}", key, populated)
		fillValue(t, path+"{}", elem, populated)
		v.SetMapIndex(key, elem)
	case reflect.Pointer:
		if !populated {
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fillValue(t, path+"*", v.Elem(), populated)
	default:
		t.Fatalf("%s has kind %s: teach fillValue about it", path, v.Kind())
	}
}

// hasReference reports whether a value of type typ can share memory with a copy of it.
func hasReference(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Chan, reflect.Func, reflect.Interface, reflect.UnsafePointer:
		return true
	case reflect.Array:
		return hasReference(typ.Elem())
	case reflect.Struct:
		for i := range typ.NumField() {
			if hasReference(typ.Field(i).Type) {
				return true
			}
		}
	default:
	}
	return false
}

// assertNoSharedReferences fails when a and b (equal-shaped values) share any slice backing array, map or
// pointer target.
func assertNoSharedReferences(t *testing.T, path string, a, b reflect.Value) {
	t.Helper()
	switch a.Kind() {
	case reflect.Struct:
		for i := range a.NumField() {
			assertNoSharedReferences(t, path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i))
		}
	case reflect.Slice:
		if a.Len() == 0 {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s shares its backing array with the clone", path)
			return
		}
		for i := range a.Len() {
			assertNoSharedReferences(t, path+"[]", a.Index(i), b.Index(i))
		}
	case reflect.Map:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s shares its map with the clone", path)
			return
		}
		for _, key := range a.MapKeys() {
			assertNoSharedReferences(t, path+"{}", a.MapIndex(key), b.MapIndex(key))
		}
	case reflect.Pointer:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Errorf("%s shares its target with the clone", path)
			return
		}
		assertNoSharedReferences(t, path+"*", a.Elem(), b.Elem())
	default:
	}
}

package graph_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func draft(id string) graph.StageDraft {
	return graph.StageDraft{ID: id, Name: "New stage", Command: "claude", Prompt: "do it"}
}

func stageWithDeps(id string, deps ...string) flow.Stage {
	return flow.Stage{ID: id, DependsOn: deps}
}

// TestInsert_LinearDag — T9.2: a -> b -> c, insert n before b. n inherits b's deps (a), b gets n added
// additionally, c (downstream of b) is untouched.
func TestInsert_LinearDag(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{
		stageWithDeps("a"),
		stageWithDeps("b", "a"),
		stageWithDeps("c", "b"),
	})
	plan, err := reg.PlanInsertBefore(draft("n"), "b", nil)
	if err != nil {
		t.Fatalf("PlanInsertBefore: %v", err)
	}
	if !slices.Equal(plan.Stage.DependsOn, []string{"a"}) {
		t.Fatalf("new stage deps = %v, want [a]", plan.Stage.DependsOn)
	}
	if !slices.Equal(plan.ExtraDeps["b"], []string{"n"}) {
		t.Fatalf("extra deps for b = %v, want [n]", plan.ExtraDeps["b"])
	}
	if want := []string{"a", "n", "b", "c"}; !slices.Equal(plan.Order, want) {
		t.Fatalf("order = %v, want %v", plan.Order, want)
	}

	next := reg.WithPlan(plan)
	b, _ := next.StageByID("b")
	if !slices.Equal(b.DependsOn, []string{"a", "n"}) {
		t.Fatalf("b.DependsOn after insert = %v, want [a n]", b.DependsOn)
	}
	c, _ := next.StageByID("c")
	if !slices.Equal(c.DependsOn, []string{"b"}) {
		t.Fatalf("c (downstream of b) must be untouched: %v", c.DependsOn)
	}
	n, ok := next.StageByID("n")
	if !ok || !slices.Equal(n.DependsOn, []string{"a"}) {
		t.Fatalf("n not registered correctly: %+v ok=%v", n, ok)
	}
}

// TestInsert_BranchedDagKeepsOtherBranches — T9.2: a branches into b and d; inserting n before b must leave
// the independent d branch completely untouched.
func TestInsert_BranchedDagKeepsOtherBranches(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{
		stageWithDeps("a"),
		stageWithDeps("b", "a"),
		stageWithDeps("d", "a"),
		stageWithDeps("e", "d"),
	})
	plan, err := reg.PlanInsertBefore(draft("n"), "b", nil)
	if err != nil {
		t.Fatalf("PlanInsertBefore: %v", err)
	}
	next := reg.WithPlan(plan)

	d, _ := next.StageByID("d")
	if !slices.Equal(d.DependsOn, []string{"a"}) {
		t.Fatalf("branch d must be untouched: %v", d.DependsOn)
	}
	e, _ := next.StageByID("e")
	if !slices.Equal(e.DependsOn, []string{"d"}) {
		t.Fatalf("branch e must be untouched: %v", e.DependsOn)
	}
	a, _ := next.StageByID("a")
	if len(a.DependsOn) != 0 {
		t.Fatalf("a must be untouched: %v", a.DependsOn)
	}
}

// TestInsert_PreservesTargetDirectDependencies — T9.2's key semantics test: target's ORIGINAL dependencies
// (D1, D2) are kept, not replaced by the new stage alone — they may still be needed for the target's own
// direct artifact inputs.
func TestInsert_PreservesTargetDirectDependencies(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{
		stageWithDeps("d1"),
		stageWithDeps("d2"),
		stageWithDeps("t", "d1", "d2"),
	})
	plan, err := reg.PlanInsertBefore(draft("n"), "t", nil)
	if err != nil {
		t.Fatalf("PlanInsertBefore: %v", err)
	}
	next := reg.WithPlan(plan)
	tgt, ok := next.StageByID("t")
	if !ok {
		t.Fatal("target stage missing after insert")
	}
	want := []string{"d1", "d2", "n"}
	if !slices.Equal(tgt.DependsOn, want) {
		t.Fatalf("t.DependsOn = %v, want %v (original deps preserved, n added)", tgt.DependsOn, want)
	}
}

// TestInsert_RejectedWhenTargetStartedBetweenPreviewAndApply — T9.2: the orchestrator-level commit path
// re-checks revision/activation under the state.Store lock (AddStageToGraph's Expect.TargetPending); the pure
// graph.Registry layer itself has no notion of live stage status, so this exercises the state-layer half of
// that guarantee directly: a target that left pending between preview and commit is rejected stale_revision.
func TestInsert_RejectedWhenTargetStartedBetweenPreviewAndApply(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"t"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	reg := graph.NewRegistry([]flow.Stage{stageWithDeps("t")})
	plan, err := reg.PlanInsertBefore(draft("n"), "t", nil)
	if err != nil {
		t.Fatalf("PlanInsertBefore: %v", err)
	}

	// Target starts running AFTER preview computed plan, BEFORE commit.
	if err := store.Apply(&state.Transition{StageID: "t", From: state.StatusPending, To: state.StatusReady, Event: "ready"}); err != nil {
		t.Fatalf("Apply ready: %v", err)
	}
	if err := store.Apply(&state.Transition{StageID: "t", From: state.StatusReady, To: state.StatusRunning, Event: "start_run"}); err != nil {
		t.Fatalf("Apply start_run: %v", err)
	}

	op := state.StageGraphOperation{
		ID:      "op-1",
		Payload: state.StageGraphPayload{Stage: plan.Stage, ExtraDeps: plan.ExtraDeps, Order: plan.Order},
		Expect:  state.StageGraphExpect{TargetPending: "t"},
	}
	_, err = store.AddStageToGraph(op)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || string(rej.Code) != "invalid_status" {
		t.Fatalf("expected invalid_status rejection, got %v", err)
	}
}

// TestAppend_DependsOnAllSinks — T9.3: appending with no explicit deps depends on every current sink.
func TestAppend_DependsOnAllSinks(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{
		stageWithDeps("a"),
		stageWithDeps("b", "a"),
		stageWithDeps("c", "a"),
	})
	sinks := reg.Sinks()
	if want := []string{"b", "c"}; !slices.Equal(sinks, want) {
		t.Fatalf("Sinks() = %v, want %v", sinks, want)
	}

	plan, err := reg.PlanAppend(draft("n"), nil, nil, nil)
	if err != nil {
		t.Fatalf("PlanAppend: %v", err)
	}
	if !slices.Equal(plan.Stage.DependsOn, []string{"b", "c"}) {
		t.Fatalf("appended stage deps = %v, want [b c]", plan.Stage.DependsOn)
	}
	if want := []string{"a", "b", "c", "n"}; !slices.Equal(plan.Order, want) {
		t.Fatalf("order = %v, want %v", plan.Order, want)
	}
}

// TestAppend_ExplicitDependencySetHonored — T9.3: an explicit dependency set overrides the sinks default.
func TestAppend_ExplicitDependencySetHonored(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{
		stageWithDeps("a"),
		stageWithDeps("b", "a"),
		stageWithDeps("c", "a"),
	})
	plan, err := reg.PlanAppend(draft("n"), []string{"a"}, nil, nil)
	if err != nil {
		t.Fatalf("PlanAppend: %v", err)
	}
	if !slices.Equal(plan.Stage.DependsOn, []string{"a"}) {
		t.Fatalf("appended stage deps = %v, want [a] (explicit set, not sinks)", plan.Stage.DependsOn)
	}
}

// TestAppend_WarnsOnFailedDependency — T9.3: depending on a failed/blocked stage is allowed but surfaces a
// warning, not a hard error.
func TestAppend_WarnsOnFailedDependency(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{
		stageWithDeps("a"),
		stageWithDeps("failed_stage"),
	})
	failedOrBlocked := func(id string) bool { return id == "failed_stage" }
	plan, err := reg.PlanAppend(draft("n"), []string{"a", "failed_stage"}, nil, failedOrBlocked)
	if err != nil {
		t.Fatalf("PlanAppend: %v (dependency on failed stage must be ALLOWED)", err)
	}
	if len(plan.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one warning", plan.Warnings)
	}
}

// TestAppend_UnknownExplicitDependencyRejected: an explicit dependency naming a non-existent stage id is a
// hard error (unlike the failed/blocked case above).
func TestAppend_UnknownExplicitDependencyRejected(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{stageWithDeps("a")})
	_, err := reg.PlanAppend(draft("n"), []string{"ghost"}, nil, nil)
	if err == nil {
		t.Fatal("expected an error for unknown dependency")
	}
	ve, ok := err.(*graph.ValidationError)
	if !ok || ve.Code != graph.CodeUnknownDependency {
		t.Fatalf("err = %v, want ValidationError{Code: unknown_dependency}", err)
	}
}

// TestDynamic_CommandMustBePrepared mirrors T8.5's TestDynamic_CanOnlyPickPreparedExecutor at the draft-
// validation layer: a command not in the prepared set is rejected regardless of insertion kind.
func TestDynamic_CommandMustBePrepared(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{stageWithDeps("a")})
	isPrepared := func(cmd string) bool { return cmd == "claude" }

	if _, err := reg.PlanAppend(graph.StageDraft{ID: "n", Command: "unprepared-cmd"}, nil, isPrepared, nil); err == nil {
		t.Fatal("expected command_not_prepared rejection")
	} else if ve, ok := err.(*graph.ValidationError); !ok || ve.Code != graph.CodeCommandNotPrepared {
		t.Fatalf("err = %v, want command_not_prepared", err)
	}

	if _, err := reg.PlanAppend(graph.StageDraft{ID: "n", Command: "claude"}, nil, isPrepared, nil); err != nil {
		t.Fatalf("PlanAppend with a prepared command should succeed: %v", err)
	}
}

// TestValidateDraftShape_RejectsUnsafeOrDuplicateID covers id-safety and uniqueness, shared by both insertion
// kinds through validateDraftShape.
func TestValidateDraftShape_RejectsUnsafeOrDuplicateID(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{stageWithDeps("existing")})

	if _, err := reg.PlanAppend(graph.StageDraft{ID: "has/slash"}, nil, nil, nil); err == nil {
		t.Fatal("expected invalid_stage_id rejection for unsafe id")
	}
	if _, err := reg.PlanAppend(graph.StageDraft{ID: "existing"}, nil, nil, nil); err == nil {
		t.Fatal("expected duplicate_stage_id rejection")
	}
}

// TestInsert_TargetInputReferencesSurviveInsertion documents why T9.1's "references to inputs/artifacts stay
// valid" condition is satisfied by construction: insertion only ADDS a dependency to the target, it never
// removes one, so any existing Input{From: depID} on the target keeps resolving to the same (still-present)
// dependency stage after insertion.
func TestInsert_TargetInputReferencesSurviveInsertion(t *testing.T) {
	reg := graph.NewRegistry([]flow.Stage{
		stageWithDeps("d1"),
		{ID: "t", DependsOn: []string{"d1"}, Inputs: []flow.Input{{Ref: "d1/*.md"}}},
	})
	plan, err := reg.PlanInsertBefore(draft("n"), "t", nil)
	if err != nil {
		t.Fatalf("PlanInsertBefore: %v", err)
	}
	next := reg.WithPlan(plan)
	tgt, _ := next.StageByID("t")
	if len(tgt.Inputs) != 1 || tgt.Inputs[0].Ref != "d1/*.md" {
		t.Fatalf("target's input reference was not preserved: %+v", tgt.Inputs)
	}
	if _, ok := next.StageByID("d1"); !ok {
		t.Fatal("d1 (the input's source stage) must still exist")
	}
}

// TestMaterialize_ReplaysDynamicAdditionsInOrder — T9.5's structural half: rebuilding from frozen+dynamic
// must reproduce exactly what WithPlan produced live, for a chain of two additions.
func TestMaterialize_ReplaysDynamicAdditionsInOrder(t *testing.T) {
	frozen := []flow.Stage{stageWithDeps("a"), stageWithDeps("t", "a")}
	reg := graph.NewRegistry(frozen)

	plan1, err := reg.PlanInsertBefore(draft("n1"), "t", nil)
	if err != nil {
		t.Fatalf("plan1: %v", err)
	}
	reg = reg.WithPlan(plan1)

	plan2, err := reg.PlanInsertBefore(draft("n2"), "t", nil)
	if err != nil {
		t.Fatalf("plan2: %v", err)
	}
	reg = reg.WithPlan(plan2)

	dynamic := []state.DynamicStageAdded{
		{Stage: plan1.Stage, ExtraDeps: plan1.ExtraDeps},
		{Stage: plan2.Stage, ExtraDeps: plan2.ExtraDeps},
	}
	rebuilt := graph.Materialize(frozen, dynamic, reg.IDs())
	rebuiltReg := graph.NewRegistry(rebuilt)

	for _, id := range reg.IDs() {
		want, _ := reg.StageByID(id)
		got, ok := rebuiltReg.StageByID(id)
		if !ok {
			t.Fatalf("rebuilt registry missing stage %q", id)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("stage %q diverged: live=%+v rebuilt=%+v", id, want, got)
		}
	}
}

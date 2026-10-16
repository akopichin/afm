package state

import (
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func testStageGraphOp(id, newID string, order []string) StageGraphOperation {
	return StageGraphOperation{
		ID: id,
		Payload: StageGraphPayload{
			Stage: flow.Stage{ID: newID, Name: "New stage", Agents: []flow.AgentType{flow.AgentAuto}, Prompt: "do it", Command: "claude"},
			Order: order,
		},
	}
}

func TestAddStageToGraph_CommitsSingleRecordAndProjectsState(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	op := testStageGraphOp("op-1", "n1", []string{"a", "n1", "b"})
	op.Payload.ExtraDeps = map[string][]string{"b": {"n1"}}
	res, err := store.AddStageToGraph(op)
	if err != nil {
		t.Fatalf("AddStageToGraph: %v", err)
	}
	if res.Seq == 0 {
		t.Fatal("expected non-zero seq")
	}

	snap := store.Snapshot()
	if st, ok := snap.Stages["n1"]; !ok || st.Status != StatusPending {
		t.Fatalf("new stage not projected as pending: %+v", snap.Stages["n1"])
	}
	if got := snap.StageOrder; len(got) != 3 || got[1] != "n1" {
		t.Fatalf("StageOrder = %v, want [a n1 b]", got)
	}
	if len(snap.DynamicStages) != 1 || snap.DynamicStages[0].Stage.ID != "n1" {
		t.Fatalf("DynamicStages = %+v", snap.DynamicStages)
	}
	if snap.DynamicStages[0].ExtraDeps["b"][0] != "n1" {
		t.Fatalf("ExtraDeps not recorded: %+v", snap.DynamicStages[0])
	}
}

func TestAddStageToGraph_DuplicateOperationIDReturnsOriginal(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	op := testStageGraphOp("op-1", "n1", []string{"a", "n1"})
	res1, err := store.AddStageToGraph(op)
	if err != nil {
		t.Fatalf("first AddStageToGraph: %v", err)
	}
	res2, err := store.AddStageToGraph(op)
	if err != nil {
		t.Fatalf("repeat AddStageToGraph: %v", err)
	}
	if res1.Seq != res2.Seq {
		t.Fatalf("repeat got a different seq: %d vs %d", res1.Seq, res2.Seq)
	}
	if len(store.Snapshot().DynamicStages) != 1 {
		t.Fatalf("duplicate operation must not be applied twice: %+v", store.Snapshot().DynamicStages)
	}
}

func TestAddStageToGraph_SameIDDifferentPayloadConflicts(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	op := testStageGraphOp("op-1", "n1", []string{"a", "n1"})
	if _, err := store.AddStageToGraph(op); err != nil {
		t.Fatalf("first AddStageToGraph: %v", err)
	}
	op2 := testStageGraphOp("op-1", "n2", []string{"a", "n2"})
	if _, err := store.AddStageToGraph(op2); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("expected ErrOperationIDConflict, got %v", err)
	}
}

func TestAddStageToGraph_RejectsWhenRunFinalizing(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	store.SetRunFinalizing(true)

	op := testStageGraphOp("op-1", "n1", []string{"a", "n1"})
	op.Expect.RunNotFinalizing = true
	_, err = store.AddStageToGraph(op)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != "run_finalizing" {
		t.Fatalf("expected run_finalizing rejection, got %v", err)
	}
}

func TestAddStageToGraph_RejectsStaleGraphRevision(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	// Land a first addition, bumping the "revision" (len(DynamicStages)+1) to 2.
	if _, err := store.AddStageToGraph(testStageGraphOp("op-1", "n1", []string{"a", "n1"})); err != nil {
		t.Fatalf("op-1: %v", err)
	}

	op2 := testStageGraphOp("op-2", "n2", []string{"a", "n1", "n2"})
	op2.Expect.GraphRevision = 1 // stale — a concurrent addition already landed
	_, err = store.AddStageToGraph(op2)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != "stale_revision" {
		t.Fatalf("expected stale_revision rejection, got %v", err)
	}
}

func TestAddStageToGraph_RejectsTargetAlreadyLeftPending(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"t"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.Apply(&Transition{StageID: "t", From: StatusPending, To: StatusPlanning, Event: "start_planning"}); err != nil {
		t.Fatalf("Apply start_planning: %v", err)
	}

	op := testStageGraphOp("op-1", "n1", []string{"n1", "t"})
	op.Expect.TargetPending = "t"
	_, err = store.AddStageToGraph(op)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != "invalid_status" {
		t.Fatalf("expected invalid_status rejection, got %v", err)
	}
}

// TestAddStageToGraph_RejectsTargetWithNonZeroGeneration reproduces §11.3's "a bare status=pending is not
// enough if this is a return from Retry": EvManualRetry returns a FAILED stage to StatusPending, but the
// stage's StageGeneration (first entered running at least once) is non-zero — insertion must still be
// rejected.
func TestAddStageToGraph_RejectsTargetWithNonZeroGeneration(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"t"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	for _, tr := range []Transition{
		{StageID: "t", From: StatusPending, To: StatusReady, Event: "ready"},
		{StageID: "t", From: StatusReady, To: StatusRunning, Event: "start_run"},
		{StageID: "t", From: StatusRunning, To: StatusFailed, Event: "fail"},
		{StageID: "t", From: StatusFailed, To: StatusPending, Event: "manual_retry"},
	} {
		if err := store.Apply(&tr); err != nil {
			t.Fatalf("Apply %s: %v", tr.Event, err)
		}
	}
	if got := store.Snapshot().Stages["t"].Status; got != StatusPending {
		t.Fatalf("precondition: stage t status = %s, want pending", got)
	}

	op := testStageGraphOp("op-1", "n1", []string{"n1", "t"})
	op.Expect.TargetPending = "t"
	_, err = store.AddStageToGraph(op)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != "stale_revision" {
		t.Fatalf("expected stale_revision rejection (non-zero generation), got %v", err)
	}
}

func TestAddStageToGraph_SurvivesRestartReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	op := testStageGraphOp("op-1", "n1", []string{"a", "n1", "b"})
	op.Payload.ExtraDeps = map[string][]string{"b": {"n1"}}
	if _, err := store.AddStageToGraph(op); err != nil {
		t.Fatalf("AddStageToGraph: %v", err)
	}
	store.Close()

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if _, ok := rs.Stages["n1"]; !ok {
		t.Fatalf("restored state is missing dynamically added stage: %+v", rs.Stages)
	}
	if len(rs.DynamicStages) != 1 || rs.DynamicStages[0].Stage.ID != "n1" {
		t.Fatalf("DynamicStages not restored: %+v", rs.DynamicStages)
	}
	if got := rs.StageOrder; len(got) != 3 || got[1] != "n1" {
		t.Fatalf("StageOrder not restored: %v", got)
	}

	store2, err := Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer store2.Close()
	if _, ok := store2.Snapshot().Stages["n1"]; !ok {
		t.Fatal("Open-driven replay lost the dynamically added stage")
	}
}

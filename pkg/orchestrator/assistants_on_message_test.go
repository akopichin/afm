package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

func onMessageAttachRequest(opID string) AttachRequest {
	return AttachRequest{
		OperationID: opID, StageID: assistantsTestStageID, ProfileID: "reviewer",
		Instruction: "answer each request", Trigger: assistants.TriggerOnMessage, Actor: state.Actor{Kind: "user"},
	}
}

// countHelperRuns replaces the helper runner with one that only counts its calls.
func countHelperRuns(o *Orchestrator) *atomic.Int32 {
	var runs atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		runs.Add(1)
		return passResultOutcome(), nil
	}
	return &runs
}

// journaledTaskStatuses lists every status the journal ever recorded for taskID, in order.
func journaledTaskStatuses(t *testing.T, store *state.Store, taskID assistants.TaskID) []string {
	t.Helper()
	var statuses []string
	for _, e := range store.TaskEvents(taskID) {
		if e.Kind != taskStatusChangedEffectKind {
			continue
		}
		var p state.TaskStatusChangedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode task_status_changed: %v", err)
		}
		statuses = append(statuses, p.Status)
	}
	return statuses
}

// assertOnMessageAtRest checks that taskID is the run's only task, an on_message helper resting in watching that
// never started a turn and reserved one unit of the task budget.
func assertOnMessageAtRest(t *testing.T, store *state.Store, taskID assistants.TaskID) {
	t.Helper()
	rs := store.Snapshot()
	if len(rs.Assistants.Tasks) != 1 {
		t.Fatalf("tasks = %d, want only the on_message helper", len(rs.Assistants.Tasks))
	}
	tk, ok := rs.Assistants.Tasks[taskID]
	if !ok {
		t.Fatalf("task %s not found", taskID)
	}
	if tk.Trigger != assistants.TriggerOnMessage || tk.Status != assistants.StatusWatching || tk.Turn != 0 {
		t.Fatalf("task = trigger %q, status %q, turn %d; want on_message resting in watching before any turn", tk.Trigger, tk.Status, tk.Turn)
	}
	if got := journaledTaskStatuses(t, store, taskID); !slices.Equal(got, []string{string(assistants.StatusWatching)}) {
		t.Fatalf("journaled statuses = %v, want only watching (no queued/running start)", got)
	}
	if rs.Assistants.QuotaReserved != 1 {
		t.Fatalf("QuotaReserved = %d, want 1: one helper is one task however many requests it serves", rs.Assistants.QuotaReserved)
	}
}

func assertRejected(t *testing.T, err error, want assistants.ErrorCode) {
	t.Helper()
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != want {
		t.Fatalf("err = %v (%T), want *OperationRejectedError{Code: %s}", err, err, want)
	}
}

// Attach returns after its commit's work ran, so a start it caused would already be journaled; a repeated Attach
// with the same operation id answers with the same task and writes nothing.
func TestOnMessageAdmission_AttachRestsInWatchingWithoutStarting(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	runs := countHelperRuns(o)

	taskID, err := o.Attach(context.Background(), onMessageAttachRequest("op-on-message"))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	assertOnMessageAtRest(t, store, taskID)

	again, err := o.Attach(context.Background(), onMessageAttachRequest("op-on-message"))
	if err != nil {
		t.Fatalf("Attach replay: %v", err)
	}
	if again != taskID {
		t.Fatalf("Attach replay = %s, want the same task %s", again, taskID)
	}
	assertOnMessageAtRest(t, store, taskID)
	if n := runs.Load(); n != 0 {
		t.Fatalf("helper runs = %d, want 0 without a message", n)
	}
}

// A helper declared in flow.yaml is attached the same way when its stage's agent spawns, and a respawn of the same
// attempt keeps the one task.
func TestOnMessageAdmission_DeclaredHelperRestsInWatching(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	runs := countHelperRuns(o)
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude", Assistants: flow.StageAssistants{
		{Profile: "reviewer", Instruction: "answer each request", Trigger: assistants.TriggerOnMessage},
	}}

	o.attachDeclaredAssistants(context.Background(), stage, kindImplementation)
	o.attachDeclaredAssistants(context.Background(), stage, kindImplementation)

	taskIDs := store.Snapshot().Assistants.ByRootStage[assistantsTestStageID]
	if len(taskIDs) != 1 {
		t.Fatalf("stage tasks = %v, want one declared helper", taskIDs)
	}
	assertOnMessageAtRest(t, store, taskIDs[0])
	if n := runs.Load(); n != 0 {
		t.Fatalf("helper runs = %d, want 0 without a message", n)
	}
}

// Before its stage starts, an on_message helper would have nothing to answer to: the attach is refused and
// writes nothing.
func TestOnMessageAdmission_PendingStageRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)

	_, err := o.Attach(context.Background(), onMessageAttachRequest("op-on-message"))
	assertRejected(t, err, assistants.CodeInvalidStatus)
	if rs := store.Snapshot(); len(rs.Assistants.Tasks) != 0 || rs.Assistants.QuotaReserved != 0 {
		t.Fatalf("tasks = %d, QuotaReserved = %d; want nothing written", len(rs.Assistants.Tasks), rs.Assistants.QuotaReserved)
	}
}

func TestOnMessageAdmission_RunAgentRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)

	_, err := o.admitTopLevelTask(store.Snapshot(), topLevelTaskRequest{
		OperationID: "op-run-agent", ProfileID: "reviewer", Scope: assistants.ScopeRun,
		Trigger: assistants.TriggerOnMessage, Actor: state.Actor{Kind: "user"},
	})
	assertRejected(t, err, assistants.CodeInvalidRequest)
}

// Only a stage's top-level helper waits for messages: neither a delegated child nor the delegating parent's
// continuation may use on_message, and a rejected group creates no child.
func TestOnMessageAdmission_DelegationRejected(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	defer func() {
		close(release)
		o.concurrency.WaitAgents()
	}()
	parentID := runningParentTask(t, o, store, release)
	actor := state.Actor{Kind: string(assistants.OriginAssistant), ID: string(parentID)}

	child := oneChild()
	child[0].Trigger = assistants.TriggerOnMessage
	_, err := o.admitChildren(context.Background(), "op-child", parentID, child, actor, "", nil)
	assertRejected(t, err, assistants.CodeInvalidRequest)

	_, err = o.admitChildren(context.Background(), "op-continuation", parentID, oneChild(), actor,
		assistants.StatusWaitingChildren, &assistants.Continuation{Trigger: assistants.TriggerOnMessage})
	assertRejected(t, err, assistants.CodeInvalidRequest)

	rs := store.Snapshot()
	if len(rs.Assistants.ByParent[parentID]) != 0 || rs.Assistants.QuotaReserved != 1 {
		t.Fatalf("children = %v, QuotaReserved = %d; want no child and only the parent's reservation",
			rs.Assistants.ByParent[parentID], rs.Assistants.QuotaReserved)
	}
	if tk := rs.Assistants.Tasks[parentID]; tk.Status != assistants.StatusRunning || tk.Continuation != nil {
		t.Fatalf("parent = status %q, continuation %+v; want it still running with no continuation saved", tk.Status, tk.Continuation)
	}
}

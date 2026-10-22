package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

func TestWaiver_RequiredByUserOnly(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"fix it"}`}, nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	// A model's own result (OriginAssistant) must never be able to waive its own requirement (§19.5).
	err := o.WaiveTask(context.Background(), taskID, state.Actor{Kind: string(assistants.OriginAssistant)}, "self-waived")
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeActorNotAllowed {
		t.Fatalf("WaiveTask(actor=assistant) err = %v, want *OperationRejectedError{Code: actor_not_allowed}", err)
	}
	if blocker, open := store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); !open || blocker.ID != taskID {
		t.Fatalf("RequiredBlocker = (%v, %v), want (%s, true) — rejected waiver must not close the gate", blocker, open, taskID)
	}

	// Side-agent origin is also rejected — only a real human (OriginUser) may waive.
	err = o.WaiveTask(context.Background(), taskID, state.Actor{Kind: string(assistants.OriginSideAgent)}, "side-agent waived")
	if !errors.As(err, &rej) || rej.Code != assistants.CodeActorNotAllowed {
		t.Fatalf("WaiveTask(actor=side_agent) err = %v, want *OperationRejectedError{Code: actor_not_allowed}", err)
	}

	// A human actor succeeds.
	if err := o.WaiveTask(context.Background(), taskID, state.Actor{Kind: string(assistants.OriginUser)}, "accepted risk"); err != nil {
		t.Fatalf("WaiveTask(actor=user): %v", err)
	}
}

func TestWaiver_IsSeparateAuditedOperation(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"fix it"}`}, nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	// Cancel (a DIFFERENT operation) must never implicitly waive — the gate stays open after it.
	if err := o.CancelTask(context.Background(), taskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if blocker, open := store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); !open || blocker.ID != taskID {
		t.Fatalf("RequiredBlocker after Cancel = (%v, %v), want (%s, true) — Cancel must not be a side-effect waiver", blocker, open, taskID)
	}

	// WaiveTask is idempotent — calling it twice is a safe no-op, not a second audit entry's worth of error.
	if err := o.WaiveTask(context.Background(), taskID, state.Actor{Kind: string(assistants.OriginUser)}, "accepted"); err != nil {
		t.Fatalf("WaiveTask (first): %v", err)
	}
	if err := o.WaiveTask(context.Background(), taskID, state.Actor{Kind: string(assistants.OriginUser)}, "accepted again"); err != nil {
		t.Fatalf("WaiveTask (second, idempotent): %v", err)
	}
}

func TestWaiver_RecordedWithActorAndReason(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"fix it"}`}, nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	if err := o.WaiveTask(context.Background(), taskID, state.Actor{Kind: "user", ID: "alice"}, "accepted the stale lint warning risk"); err != nil {
		t.Fatalf("WaiveTask: %v", err)
	}
	ts, ok := store.Snapshot().Assistants.Tasks[taskID]
	if !ok {
		t.Fatal("task not found")
	}
	if !ts.Waived {
		t.Fatal("TaskState.Waived = false, want true")
	}
	if ts.WaiverReason != "accepted the stale lint warning risk" {
		t.Fatalf("WaiverReason = %q, want the given reason", ts.WaiverReason)
	}
	if ts.WaivedBy.Kind != "user" || ts.WaivedBy.ID != "alice" {
		t.Fatalf("WaivedBy = %+v, want {Kind:user ID:alice}", ts.WaivedBy)
	}
	if ts.WaivedAt.IsZero() {
		t.Fatal("WaivedAt is zero, want a timestamp")
	}
	if waived, reason, ok := o.TaskWaiver(taskID); !ok || !waived || reason != "accepted the stale lint warning risk" {
		t.Fatalf("TaskWaiver(%s) = (%v, %q, %v), want (true, the given reason, true) — pkg/server's AssistantTaskView reads through this", taskID, waived, reason, ok)
	}

	// Reject an empty reason — a waiver must always explain itself.
	taskID2 := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-2")
	if err := o.WaiveTask(context.Background(), taskID2, state.Actor{Kind: "user"}, ""); err == nil {
		t.Fatal("WaiveTask with empty reason succeeded, want an error")
	}
}

func TestWaiver_UnblocksStageThroughNormalCascade(t *testing.T) {
	upstream := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	downstream := flow.Stage{ID: "downstream", Command: "claude", DependsOn: []string{assistantsTestStageID}}
	o, store := newMultiStageBarrierTestOrchestrator(t, []flow.Stage{upstream, downstream})
	activateStage(t, o, assistantsTestStageID)

	// release is closed explicitly mid-test (below) once the task should resolve — no t.Cleanup double-close.
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"fix it"}`}, nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status = %v, want awaiting_assistants", got)
	}
	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	// needs_changes does not close the gate on its own — still awaiting_assistants.
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status after needs_changes = %v, want still awaiting_assistants", got)
	}

	if err := o.WaiveTask(context.Background(), taskID, state.Actor{Kind: string(assistants.OriginUser)}, "accepted"); err != nil {
		t.Fatalf("WaiveTask: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.currentStatus(assistantsTestStageID) == state.StatusDone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusDone {
		t.Fatalf("status after waiver = %v, want done", got)
	}
	// The waiver must have run the NORMAL completion cascade — downstream becomes ready, not left pending.
	// "done" is committed before its cascade runs, and the cascade may be driven by the helper task's own
	// post-commit hook on another goroutine (slow fsync on macOS makes that common) — wait for it.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := o.currentStatus("downstream"); got == state.StatusReady || got == state.StatusRunning {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := o.currentStatus("downstream"); got != state.StatusReady && got != state.StatusRunning {
		t.Fatalf("downstream status = %v, want ready/running — waiver must drive the normal cascade", got)
	}
}

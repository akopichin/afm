package orchestrator

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
)

// TestInterruptedRun_TaskMidTurnIsRetriedOnResume: Ctrl+C cancels the run while a task's agent is mid-turn.
// The cut-short invocation is not a failure — the task stays running, and the resumed run re-queues and
// retries it.
func TestInterruptedRun_TaskMidTurnIsRetriedOnResume(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	runCtx, cancelRun := context.WithCancel(context.Background())
	o.runMu.Lock()
	o.runCtx = runCtx
	o.runMu.Unlock()

	var calls atomic.Int32
	started := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return executor.HelperOutcome{ProcessErr: ctx.Err()}, nil
		}
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-interrupted")
	<-started
	cancelRun()
	o.concurrency.WaitAgents()

	if got := store.Snapshot().Assistants.Tasks[taskID].Status; got != assistants.StatusRunning {
		t.Fatalf("task status after the run was interrupted = %s, want running (not failed)", got)
	}

	resumed, cancelResumed := context.WithCancel(context.Background())
	defer cancelResumed()
	o.runMu.Lock()
	o.runCtx = resumed
	o.runMu.Unlock()
	o.recoverAssistants(resumed)

	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := calls.Load(); got != 2 {
		t.Fatalf("helper invoked %d times, want 2 (the interrupted turn and its retry)", got)
	}
	o.concurrency.WaitAgents()
}

// TestInterruptedRun_RetriedTurnKeepsItsInput: a message wakes a run agent, its process starts (the message is
// marked delivered) and the run is interrupted mid-turn. The retried turn after resume is handed the same
// message again — the input of the cut-short turn is not lost. A turn that ran to its end consumed its input.
func TestInterruptedRun_RetriedTurnKeepsItsInput(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)

	runCtx, cancelRun := context.WithCancel(context.Background())
	o.runMu.Lock()
	o.runCtx = runCtx
	o.runMu.Unlock()

	var calls atomic.Int32
	started := make(chan struct{})
	var retriedPrompt atomic.Value
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			o.helperExecutorConfig(assistants.TaskID(taskID), cmd).OnDelivered() // what the executor does at process start
			close(started)
			<-ctx.Done()
			return executor.HelperOutcome{ProcessErr: ctx.Err()}, nil
		}
		retriedPrompt.Store(prompt)
		return passResultOutcome(), nil
	}

	msg := "please review the checkout flow"
	res := addTestRunAgent(t, o, "op-int-input", &msg)
	<-started
	cancelRun()
	o.concurrency.WaitAgents()
	if got := receiptOf(store, *res.MessageID, res.ParticipantID); got != agentcomm.DeliveryDelivered {
		t.Fatalf("receipt after the interrupted turn started = %s, want delivered", got)
	}

	resumed, cancelResumed := context.WithCancel(context.Background())
	defer cancelResumed()
	o.runMu.Lock()
	o.runCtx = resumed
	o.runMu.Unlock()
	o.recoverAssistants(resumed)

	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()
	if prompt, _ := retriedPrompt.Load().(string); !strings.Contains(prompt, msg) {
		t.Fatalf("retried turn prompt does not carry the interrupted turn's message:\n%s", prompt)
	}
	if ids := o.loadTurnInbox(res.TaskID); len(ids) != 0 {
		t.Fatalf("turn input after a finished turn = %v, want none", ids)
	}
	if block, _, _ := o.agentCommInboxForTask(res.TaskID); block != "" {
		t.Fatalf("the next turn after a finished one must not see the message again, got:\n%s", block)
	}
}

// TestInterruptedRun_WaitsStayPendingAndResolveAfterResume: an interrupted run leaves every pending wait
// pending; only a run that really finished closes them as run_closed.
func TestInterruptedRun_WaitsStayPendingAndResolveAfterResume(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	waiter := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-int-waiter"))
	deadline := time.Now().Add(time.Hour)

	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "op-int-wait", Waiter: waiter, Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "tests/checkout", Status: "done"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	o.closeMeshWaitsIfFinalized() // the run was interrupted, not finalized
	if got := store.Snapshot().Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitPending {
		t.Fatalf("wait after an interrupted run = %s, want pending", got)
	}

	if _, err := o.Publish(ctx, PublishRequest{OperationID: "op-int-publish", Author: meshUserParticipantID, Key: "tests/checkout", Status: "done"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := store.Snapshot().Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait after its condition held = %s, want ready", got)
	}
}

// TestFinalizedRun_ClosesPendingWaits: a run that really finished closes what is still pending.
func TestFinalizedRun_ClosesPendingWaits(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	waiter := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-fin-waiter"))
	deadline := time.Now().Add(time.Hour)
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-fin-wait", Waiter: waiter, Deadline: &deadline,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "never", Status: "done"},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	o.finalizing.Store(true)
	o.closeMeshWaitsIfFinalized()
	if got := store.Snapshot().Assistants.Mesh.Waits[receipt.WaitID].Status; got != agentcomm.WaitRunClosed {
		t.Fatalf("wait after the run finished = %s, want run_closed", got)
	}
}

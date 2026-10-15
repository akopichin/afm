package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// TestFollowUp_StoredAsNextTurnInput — dev-plan.md T4.7: a follow-up stored while a task is still queued
// (i.e. before its one turn ever runs) must show up in that turn's own assembled context — "stored as the
// next turn's input", not injected into anything already live (there is nothing live yet in this case).
func TestFollowUp_StoredAsNextTurnInput(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	blockSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{}
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{"helper-cmd": blockSem}, "claude")

	var gotPrompt string
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		gotPrompt = prompt
		return passResultOutcome(), nil
	}

	// Attach commits the task straight to "running" even though its actual subprocess is still queued behind
	// the saturated semaphore (status reflects having WON the schedule race, not having actually started) —
	// FollowUp only requires non-terminal, so calling it here (before the semaphore ever frees) is exactly
	// the "queued behind the scenes" case this test means to cover.
	taskID := attachTestTask(context.Background(), t, o, "op-1")

	if err := o.FollowUp(context.Background(), taskID, "please also check the mobile layout"); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}

	<-blockSem // free the slot so the goroutine actually runs
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	if !strings.Contains(gotPrompt, "please also check the mobile layout") {
		t.Fatalf("prompt does not contain the stored follow-up text: %q", gotPrompt)
	}
}

// TestFollowUp_DoesNotLeakToSiblings — a follow-up stored for one task must never appear in a DIFFERENT
// task's context, even a sibling attached to the same stage around the same time.
func TestFollowUp_DoesNotLeakToSiblings(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	blockSem := concurrency.ChannelSemaphore(make(chan struct{}, 2))
	blockSem <- struct{}{}
	blockSem <- struct{}{}
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{"helper-cmd": blockSem}, "claude")

	var mu sync.Mutex
	prompts := map[assistants.TaskID]string{}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		mu.Lock()
		prompts[assistants.TaskID(taskID)] = prompt
		mu.Unlock()
		return passResultOutcome(), nil
	}

	taskA := attachTestTask(context.Background(), t, o, "op-a")
	taskB := attachTestTask(context.Background(), t, o, "op-b")

	if err := o.FollowUp(context.Background(), taskA, "A's secret follow-up"); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}

	<-blockSem
	<-blockSem
	waitForTaskStatus(t, store, taskA, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, taskB, assistants.StatusCompleted, 2*time.Second)

	mu.Lock()
	promptA, promptB := prompts[taskA], prompts[taskB]
	mu.Unlock()
	if !strings.Contains(promptA, "A's secret follow-up") {
		t.Fatalf("task A's own prompt is missing its follow-up: %q", promptA)
	}
	if strings.Contains(promptB, "A's secret follow-up") {
		t.Fatalf("task B's prompt leaked task A's follow-up: %q", promptB)
	}
}

// TestStopAndClarify_EndsAttemptAndQueuesNew — a RUNNING task's current attempt is ended and a new one
// started with the clarified instruction, with a visible effect (the second attempt's own prompt).
func TestStopAndClarify_EndsAttemptAndQueuesNew(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	var gotPrompt string
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-ctx.Done()
			return executor.HelperOutcome{}, nil
		}
		gotPrompt = prompt
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	<-started
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	if err := o.StopAndClarify(context.Background(), taskID, "actually, focus on X instead"); err != nil {
		t.Fatalf("StopAndClarify: %v", err)
	}

	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if calls.Load() != 2 {
		t.Fatalf("runHelperAgent called %d times, want 2 (ended attempt + clarified attempt)", calls.Load())
	}
	if !strings.Contains(gotPrompt, "actually, focus on X instead") {
		t.Fatalf("second attempt's prompt missing the clarify text: %q", gotPrompt)
	}
}

// TestFollowUp_RejectedForTerminalTask — a one-shot task that already finished has no future turn left to
// read a follow-up back on; both FollowUp and StopAndClarify must reject it with CodeInvalidStatus rather
// than silently storing text nothing will ever read, or resurrecting a terminal task.
func TestFollowUp_RejectedForTerminalTask(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	assertInvalidStatus := func(t *testing.T, err error) {
		t.Helper()
		var rej *state.OperationRejectedError
		if !errors.As(err, &rej) || rej.Code != assistants.CodeInvalidStatus {
			t.Fatalf("error = %v, want *state.OperationRejectedError{Code: CodeInvalidStatus}", err)
		}
	}
	assertInvalidStatus(t, o.FollowUp(context.Background(), taskID, "too late"))
	assertInvalidStatus(t, o.StopAndClarify(context.Background(), taskID, "too late"))
}

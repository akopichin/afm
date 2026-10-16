// Этап 7 (T7.8): Revise from awaiting_assistants — the main executor's own process has ALREADY exited, so
// Revise here is an explicit "start a fresh execution pass with feedback" action, never a blind interrupt.
package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// enterAwaitingAssistantsForRevise drives assistantsTestStageID into awaiting_assistants with one blocked
// required task, wiring a fake agent runner so a subsequent Revise's spawned runImplementationWithFeedback
// never touches a real subprocess.
func enterAwaitingAssistantsForRevise(t *testing.T, stage flow.Stage) (*Orchestrator, *state.Store, assistants.TaskID) {
	t.Helper()
	o, store := newBarrierTestOrchestrator(t, stage)
	fake := &fakeAgentRunner{}
	o.opts.Runner = fake
	o.runner = fake

	stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"fix it"}`}, nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("setup: status = %v, want awaiting_assistants", got)
	}
	return o, store, taskID
}

func TestRevise_FromAwaitingAssistantsStartsExecutorWithFeedback(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store, _ := enterAwaitingAssistantsForRevise(t, stage)

	applied, _, err := o.Revise(context.Background(), assistantsTestStageID, "please also update the docs")
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	if !applied {
		t.Fatal("Revise from awaiting_assistants: applied = false, want true")
	}

	stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
	raw, err := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if !strings.Contains(string(raw), "please also update the docs") {
		t.Fatalf("feedback.md = %q, want it to contain the given feedback", raw)
	}

	// runImplementationWithFeedback's own FIRST action is EvStartRun (Revising -> Running) — proof that a
	// genuine fresh execution pass was actually started, not a blind interrupt-and-wait (there was no live
	// process to interrupt). Checked against the durable HISTORY, not the current (possibly already-moved-on)
	// status: the fake agent runner completes synchronously with no real file output, so the stage can race
	// past Running into retrying/failed within the same instant — the transition still happened either way.
	o.concurrency.WaitAgents()
	hist, err := store.History()
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	sawStartRun := false
	for _, tr := range hist {
		if tr.StageID == assistantsTestStageID && tr.From == state.StatusRevising && tr.To == state.StatusRunning {
			sawStartRun = true
			break
		}
	}
	if !sawStartRun {
		t.Fatalf("no revising->running transition found in history = %+v — Revise must actually start a fresh execution pass", hist)
	}
}

func TestRevise_InvalidatesPriorPass(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store, taskID := enterAwaitingAssistantsForRevise(t, stage)

	// The OLD task completed with needs_changes in this fixture — but even a PASS from the old generation must
	// stop counting once Revise bumps the root generation (mirrors TestBarrier_GenerationAwareCas's read-side
	// proof, exercised here through the real Revise call path).
	rsBefore := store.Snapshot()
	genBefore := rsBefore.Assistants.RootGenerations[assistantsTestStageID]

	if _, _, err := o.Revise(context.Background(), assistantsTestStageID, "try again"); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	o.concurrency.WaitAgents()

	rsAfter := store.Snapshot()
	genAfter := rsAfter.Assistants.RootGenerations[assistantsTestStageID]
	if genAfter <= genBefore {
		t.Fatalf("RootGenerations[%s] = %d after Revise, want > %d (before)", assistantsTestStageID, genAfter, genBefore)
	}
	// The old task is no longer a current-generation root child — RequiredBlocker sees nothing from it.
	if _, open := rsAfter.Assistants.RequiredBlocker(assistantsTestStageID); open {
		t.Fatal("RequiredBlocker open after Revise with no new required task attached — the stale pre-Revise task must not block the new attempt")
	}
	// The old task itself is still visible in history (never erased) but is no longer current-generation.
	old, ok := rsAfter.Assistants.Tasks[taskID]
	if !ok {
		t.Fatal("old task missing from history — BumpRootGeneration must not erase it")
	}
	if old.RootGeneration == genAfter {
		t.Fatalf("old task's RootGeneration = %d, want it to remain at the OLD generation (not silently promoted)", old.RootGeneration)
	}
}

func TestRevise_CancelsStaleAssistantTurns(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	fake := &fakeAgentRunner{}
	o.opts.Runner = fake
	o.runner = fake
	stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return executor.HelperOutcome{}, context.Canceled
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status = %v, want awaiting_assistants", got)
	}

	if _, _, err := o.Revise(context.Background(), assistantsTestStageID, "stop waiting, try differently"); err != nil {
		t.Fatalf("Revise: %v", err)
	}

	// The still-running required task must be cancelled by Revise's BumpRootGeneration — not left to finish
	// into a now-irrelevant generation. The underlying blocked goroutine is drained by t.Cleanup (close(release)
	// runs before newBarrierTestOrchestrator's own WaitAgents cleanup, LIFO) — calling WaitAgents HERE would
	// deadlock, since release is only closed by that cleanup.
	waitForTaskStatus(t, store, taskID, assistants.StatusCancelled, 2*time.Second)
}

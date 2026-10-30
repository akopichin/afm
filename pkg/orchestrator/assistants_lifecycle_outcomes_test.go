package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
)

// Outcome table of one helper turn: what the agent call returned -> the task's resting status. It pins the
// order in which runHelperTask classifies outcomes, so splitting the function cannot change who wins.
func TestHelperTurnOutcomes_TerminalStatusAndReason(t *testing.T) {
	cases := []struct {
		name       string
		outcome    executor.HelperOutcome
		err        error
		wantStatus assistants.TaskStatus
		wantReason string
	}{
		{
			name:       "runner error",
			err:        errors.New("spawn failed"),
			wantStatus: assistants.StatusFailed,
			wantReason: "assistants: helper execution failed: spawn failed",
		},
		{
			name:       "timeout",
			outcome:    executor.HelperOutcome{TimedOut: true},
			wantStatus: assistants.StatusFailed,
			wantReason: "helper agent timed out",
		},
		{
			name:       "process crash carries its error",
			outcome:    executor.HelperOutcome{ProcessErr: errors.New("exit status 2")},
			wantStatus: assistants.StatusFailed,
			wantReason: "helper execution failed: exit status 2",
		},
		{
			name:       "executor protocol error",
			outcome:    executor.HelperOutcome{ProcessOK: true, ProtocolErr: errors.New("no final answer")},
			wantStatus: assistants.StatusFailed,
			wantReason: protocolErrorPrefix + "no final answer",
		},
		{
			name:       "undecodable answer",
			outcome:    executor.HelperOutcome{ProcessOK: true, RawText: "not json"},
			wantStatus: assistants.StatusFailed,
			wantReason: protocolErrorPrefix,
		},
		{
			name:       "pass",
			outcome:    passResultOutcome(),
			wantStatus: assistants.StatusCompleted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				return tc.outcome, tc.err
			}

			taskID := attachTestTask(context.Background(), t, o, "op-1")
			tk := waitForTaskStatus(t, store, taskID, tc.wantStatus, 2*time.Second)
			if !strings.Contains(tk.StatusReason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", tk.StatusReason, tc.wantReason)
			}
			o.concurrency.WaitAgents()
		})
	}
}

// An interrupted subprocess is reported by Pause/Cancel themselves; the turn must not add a status of its own.
func TestHelperTurnOutcomes_InterruptedLeavesTaskRunning(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	var calls atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		calls.Add(1)
		return executor.HelperOutcome{Interrupted: true}, nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-1")
	o.concurrency.WaitAgents()

	if got := calls.Load(); got != 1 {
		t.Fatalf("agent calls = %d, want 1", got)
	}
	if tk := store.Snapshot().Assistants.Tasks[taskID]; tk.Status != assistants.StatusRunning {
		t.Fatalf("status = %s, want running (interrupt is recorded by whoever interrupted)", tk.Status)
	}
}

// A run shutdown that cut the agent short is no failure: the task stays running for recovery to re-queue. This
// wins over a usage limit surfacing from the same cut-short call.
func TestHelperTurnOutcomes_ShutdownBeatsFailureAndQuota(t *testing.T) {
	cases := []struct {
		name    string
		outcome executor.HelperOutcome
		err     error
	}{
		{name: "not ok", outcome: executor.HelperOutcome{}},
		{name: "runner error", err: errors.New("killed")},
		{name: "usage limit", outcome: executor.HelperOutcome{ProcessErr: helperLimit(time.Now().Add(time.Hour))}, err: helperLimit(time.Now().Add(time.Hour))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			runCtx, cancelRun := context.WithCancel(context.Background())
			o.runMu.Lock()
			o.runCtx = runCtx
			o.runMu.Unlock()
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				cancelRun()
				return tc.outcome, tc.err
			}

			taskID := attachTestTask(context.Background(), t, o, "op-1")
			o.concurrency.WaitAgents()

			tk := store.Snapshot().Assistants.Tasks[taskID]
			if tk.Status != assistants.StatusRunning {
				t.Fatalf("status = %s (reason %q), want running", tk.Status, tk.StatusReason)
			}
			if tk.Quota != nil {
				t.Fatalf("quota = %+v, want none: a shutdown never parks the task", tk.Quota)
			}
		})
	}
}

// CancelTask commits "cancelled" while the agent runs; whatever the cut-short call then reports — an error, a
// usage limit or even a finished answer — must not add a second status or a result. Every finish is a CAS on
// "running", so the journal cannot show a skipped guard: result.json written into the terminal task's directory does.
func TestHelperTurnOutcomes_CancelledByOtherWins(t *testing.T) {
	cases := []struct {
		name    string
		outcome executor.HelperOutcome
		err     error
	}{
		{name: "runner error", err: errors.New("killed")},
		{name: "timeout", outcome: executor.HelperOutcome{TimedOut: true}},
		{name: "usage limit", outcome: executor.HelperOutcome{ProcessErr: helperLimit(time.Now().Add(time.Hour))}, err: helperLimit(time.Now().Add(time.Hour))},
		{name: "late pass", outcome: passResultOutcome()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			started := make(chan struct{})
			o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
				close(started)
				<-ctx.Done()
				// CancelTask commits right after cancelling the context; give that commit time to land.
				for range 200 {
					if o.taskTerminal(deriveTaskID("op-1")) {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				return tc.outcome, tc.err
			}

			taskID := attachTestTask(context.Background(), t, o, "op-1")
			<-started
			if err := o.CancelTask(context.Background(), taskID); err != nil {
				t.Fatalf("CancelTask: %v", err)
			}
			o.concurrency.WaitAgents()

			tk := store.Snapshot().Assistants.Tasks[taskID]
			if tk.Status != assistants.StatusCancelled {
				t.Fatalf("status = %s (reason %q), want cancelled", tk.Status, tk.StatusReason)
			}
			for _, kind := range taskEventKinds(t, o, taskID) {
				if kind == "task_result" {
					t.Fatal("a cancelled task must not get a task_result event")
				}
			}
			resultFile := filepath.Join(o.taskDir(taskID), "result.json")
			if _, err := os.Stat(resultFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a cancelled task must not get %s (stat err = %v)", resultFile, err)
			}
		})
	}
}

// Preparation failures end the task as failed before the agent is ever called.
func TestHelperTurnOutcomes_PreparationFailureSkipsAgent(t *testing.T) {
	cases := []struct {
		name       string
		sabotage   func(t *testing.T, taskDir string)
		wantReason string
	}{
		{
			name: "workspace cannot be created",
			sabotage: func(t *testing.T, taskDir string) {
				if err := os.MkdirAll(filepath.Dir(taskDir), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(taskDir, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "assistants: create task workspace:",
		},
		{
			name: "stale raw answer cannot be removed",
			sabotage: func(t *testing.T, taskDir string) {
				stale := filepath.Join(taskDir, rawResultFileName)
				if err := os.MkdirAll(filepath.Join(stale, "keep"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "assistants: clear stale raw answer:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			var calls atomic.Int32
			o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				calls.Add(1)
				return passResultOutcome(), nil
			}
			tc.sabotage(t, o.taskDir(deriveTaskID("op-1")))

			taskID := attachTestTask(context.Background(), t, o, "op-1")
			tk := waitForTaskStatus(t, store, taskID, assistants.StatusFailed, 2*time.Second)
			if !strings.Contains(tk.StatusReason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", tk.StatusReason, tc.wantReason)
			}
			o.concurrency.WaitAgents()
			if got := calls.Load(); got != 0 {
				t.Fatalf("agent calls = %d, want 0: preparation failed first", got)
			}
		})
	}
}

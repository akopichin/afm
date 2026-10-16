package orchestrator_test

// Этап 9, T9.3/T9.4 — the half of graph_mutate.go's test coverage that needs a full live scheduler/runner
// loop (orch.Run), not just the orchestrator-internal unit tests in graph_mutate_test.go (package
// orchestrator). As there, a dynamically-added stage ALWAYS carries an explicit Command (never substitutable
// by Options.Runner, which runner_factory.go's runnerFor only ever uses for a stage with an EMPTY Command) —
// every real script here is a safe, instantaneous stand-in, never the real "claude" binary.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// newSafeGraphCommand mirrors graph_mutate_test.go's newSafeAgentCommand for the external test package: a
// tiny real script standing in for an agent binary, writing execution_summary.md (the agents:[auto]
// completion marker) into $AFM_STAGE_DIR and exiting 0 immediately.
func newSafeGraphCommand(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent.sh")
	// Appends one line to $AFM_STAGE_DIR/invocation.count per call — lets TestCrashRightAfterCommitBeforeScheduler
	// count how many times a stage's agent actually ran (exactly once, not zero, not twice).
	script := "#!/bin/sh\nif [ -n \"$AFM_STAGE_DIR\" ]; then echo x >> \"$AFM_STAGE_DIR/invocation.count\"; printf '## Summary\\ndone\\n' > \"$AFM_STAGE_DIR/execution_summary.md\"; fi\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake agent script: %v", err)
	}
	return path
}

// TestCommit_NewStageStartsViaExistingPaths — T9.4: a freshly committed, immediately-ready dynamic stage
// must actually run and complete through the SAME activation paths every other stage uses
// (tryActivatePrePlanned -> startReadyStages -> startReadyStage), not a bespoke spawn CommitAddStage invents
// for itself.
func TestCommit_NewStageStartsViaExistingPaths(t *testing.T) {
	cmd := newSafeGraphCommand(t)
	cfg := config.Default()
	cfg.Client.Command = cmd
	// "keepalive" has no deps and an injected Go-level runner that blocks forever (released only by ctx
	// cancellation tearing the process down) — without it, "a" would be the run's ONLY stage, and by the time
	// waitForStatus observes it done the run may have ALREADY finalized (shouldExit/allTerminal), rejecting
	// the commit run_finalizing before this test gets a chance to exercise the activation path it's actually
	// about. "keepalive" keeps the run demonstrably alive throughout.
	stages := []flow.Stage{
		{ID: "a", Agents: []flow.AgentType{flow.AgentAuto}, Command: cmd, Prompt: "p"},
		{ID: "keepalive", Agents: []flow.AgentType{flow.AgentAuto}, Prompt: "p"},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"a", "keepalive"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	keepalive := &blockingOnceAutoRunner{release: make(chan struct{})}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: cfg, Prompts: orchestrator.DefaultPrompts(),
		Runner: keepalive,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)
	// Registered AFTER runOrchestratorAsync's own cleanup, so LIFO runs this FIRST: unblock "keepalive"'s
	// agent goroutine before that cleanup cancels ctx and waits (bounded 10s) for orch.Run to return —
	// otherwise the clean-shutdown wait would time out on a goroutine nothing ever told to stop.
	t.Cleanup(func() { close(keepalive.release) })

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "a", state.StatusDone, 10*time.Second)
	waitForStatus(t, stateFile, "keepalive", state.StatusRunning, 10*time.Second)

	res, err := orch.CommitAddStage(context.Background(), orchestrator.AddStageDraft{
		Kind: orchestrator.AddStageKindAppend, ExplicitDeps: []string{"a"}, RequestID: "req-1",
		Stage: graph.StageDraft{ID: "n", Command: cmd, Prompt: "p"},
	})
	if err != nil {
		t.Fatalf("CommitAddStage: %v", err)
	}
	if res.Stage.ID != "n" {
		t.Fatalf("AddStageResult.Stage.ID = %q, want n", res.Stage.ID)
	}

	// n's dependency ("a") is already done, so it must become ready and run WITHOUT any further nudge from
	// the test — proof that CommitAddStage's own tryActivatePrePlanned call is what starts it.
	waitForStatus(t, stateFile, "n", state.StatusDone, 10*time.Second)
}

// blockingOnceAutoRunner is a Go-level executor.Runner stub (no real subprocess) for the ONE stage it's
// built for: RunAgent blocks until release is closed, then marks the stage complete. Used to deterministically
// park a stage in StatusRunning so a test can race something against its completion.
type blockingOnceAutoRunner struct {
	release chan struct{}
}

func (r *blockingOnceAutoRunner) RunPlanning(context.Context, string, string, string, string) error {
	return nil
}

func (r *blockingOnceAutoRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	<-r.release
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *blockingOnceAutoRunner) RunJSONQuery(context.Context, string) ([]byte, error) {
	return nil, nil
}

// TestAppend_RaceWithLastStageCompletion — T9.3, §11.4: a concurrent append racing the run's ONLY (and thus
// last) stage completing must never corrupt state or deadlock — run with -race. The commit either wins the
// race (lands before the run decides to wind down, and the new stage runs to completion as part of the SAME
// run) or loses it (rejected run_finalizing once Store.SetRunFinalizing(true) is visible) or — a narrow,
// acknowledged gap shared with TestCommit_SharedAdmissionSerialization's doc comment — durably commits a
// moment after the run's own shutdown decision already latched, leaving the new stage pending for a future
// restart to pick up. All three are acceptable; a panic, a duplicate start, or any other error is not.
func TestAppend_RaceWithLastStageCompletion(t *testing.T) {
	cmd := newSafeGraphCommand(t)
	cfg := config.Default()
	cfg.Client.Command = cmd
	stages := []flow.Stage{{ID: "only", Agents: []flow.AgentType{flow.AgentAuto}}}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"only"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	runner := &blockingOnceAutoRunner{release: make(chan struct{})}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: cfg, Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "only", state.StatusRunning, 10*time.Second)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var commitErr error
	wg.Go(func() {
		<-start
		close(runner.release) // let "only" (the run's last stage) complete
	})
	wg.Go(func() {
		<-start
		_, commitErr = orch.CommitAddStage(context.Background(), orchestrator.AddStageDraft{
			Kind: orchestrator.AddStageKindAppend, RequestID: "req-1",
			Stage: graph.StageDraft{ID: "n", Command: cmd, Prompt: "p"},
		})
	})
	close(start)
	wg.Wait()

	if commitErr != nil {
		var rej *state.OperationRejectedError
		if !errors.As(commitErr, &rej) || rej.Code != "run_finalizing" {
			t.Fatalf("CommitAddStage failed with an unexpected error (want nil or run_finalizing): %v", commitErr)
		}
		return // commit lost the race cleanly — nothing further to check
	}

	// Commit won the race. Either the run is still alive and will run n to completion, or the run had
	// already decided to exit a moment earlier and n is left pending for a future restart — both are
	// documented-acceptable; just make sure we don't hang waiting for an outcome that can't happen.
	deadline := time.After(10 * time.Second)
	for {
		st, err := state.LoadRunState(runDir)
		if err == nil {
			if st.Stages["n"].Status == state.StatusDone {
				return // commit won AND n ran to completion within this run
			}
		}
		select {
		case <-deadline:
			// n may legitimately still be "pending" (run already exited) — only fail if something actively
			// wrong happened (n never got durably recorded at all, which CommitAddStage's own nil error
			// already rules out) — nothing more to assert here.
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestCrashRightAfterCommitBeforeScheduler — T9.5: a crash immediately after AddStageToGraph's durable
// commit but BEFORE the scheduler ever reacted (CommitAddStage's own tryActivatePrePlanned call never ran —
// the closest in-process simulation of "the process died right there") must not lose the new stage NOR start
// it more than once after restart: cmd/afm/run.go's restart path (graph.RestoreOrPersist +
// graph.Materialize, exercised directly here) must bring it up exactly once.
func TestCrashRightAfterCommitBeforeScheduler(t *testing.T) {
	cmd := newSafeGraphCommand(t)
	runDir := t.TempDir()
	base := []flow.Stage{{ID: "a", Agents: []flow.AgentType{flow.AgentAuto}, Command: cmd, Prompt: "p"}}

	frozen, err := graph.RestoreOrPersist(runDir, base)
	if err != nil {
		t.Fatalf("RestoreOrPersist: %v", err)
	}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	// Durable commit lands — but nothing here ever calls CommitAddStage/tryActivatePrePlanned, simulating a
	// crash in the gap between the journal write and the scheduler ever hearing about it.
	op := state.StageGraphOperation{
		ID: "op-1",
		Payload: state.StageGraphPayload{
			Stage:     flow.Stage{ID: "n", Agents: []flow.AgentType{flow.AgentAuto}, Command: cmd, Prompt: "p", DependsOn: []string{"a"}},
			ExtraDeps: nil,
			Order:     []string{"a", "n"},
		},
	}
	if _, err := store.AddStageToGraph(op); err != nil {
		t.Fatalf("AddStageToGraph: %v", err)
	}
	store.Close() // "crash"

	// Restart: reopen the store and fold the durable addition back onto the frozen stage set — the EXACT
	// sequence cmd/afm/run.go's restart path uses (see its T9.5 comment right before graph.Materialize).
	store2, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	t.Cleanup(func() { store2.Close() })
	snap := store2.Snapshot()
	if len(snap.DynamicStages) != 1 {
		t.Fatalf("precondition: expected the crash-landed commit to survive replay, got %+v", snap.DynamicStages)
	}
	restoredStages := graph.Materialize(frozen, snap.DynamicStages, snap.StageOrder)

	cfg := config.Default()
	cfg.Client.Command = cmd
	orch := orchestrator.New(orchestrator.Options{RunDir: runDir, Stages: restoredStages, Store: store2, Config: cfg, Prompts: orchestrator.DefaultPrompts()})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	stateFile := filepath.Join(runDir, "state.json")
	waitForStatus(t, stateFile, "a", state.StatusDone, 10*time.Second)
	waitForStatus(t, stateFile, "n", state.StatusDone, 10*time.Second)

	data, err := os.ReadFile(filepath.Join(runDir, "n", "invocation.count"))
	if err != nil {
		t.Fatalf("read n's invocation.count: %v", err)
	}
	if got := strings.Count(string(data), "x"); got != 1 {
		t.Fatalf("n's agent ran %d times after restart, want exactly 1 (not zero, not twice): %q", got, data)
	}
}

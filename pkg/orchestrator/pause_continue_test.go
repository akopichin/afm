package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestIntegration_AutoRunGatesScriptStage(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Script: "true", AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "s1", state.StatusPaused, 3*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].PausedFrom != state.StatusPending {
		t.Errorf("PausedFrom = %q, want %q", final.Stages["s1"].PausedFrom, state.StatusPending)
	}
}

func TestIntegration_AutoRunGatesRegularStage(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}, AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	// No Runner injected: the gate must fire before any agent would ever be
	// spawned. If it doesn't, orch.Run would hang on a nil Runner and this
	// test would time out instead of failing cleanly on the wrong status.
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "s1", state.StatusPaused, 3*time.Second)
}

// TestIntegration_AutoRunGateFiresOnceOnly locks in the "только при первой
// активации" requirement: a stage that already completed one pause/continue
// cycle (PausedFrom permanently non-empty, see state.SetStageStatusAt) must
// not re-pause when a later failure sends it back through Pending.
func TestIntegration_AutoRunGateFiresOnceOnly(t *testing.T) {
	runDir := t.TempDir()
	disabled := false
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Script: "true", AutoRun: &disabled},
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPaused, To: state.StatusPending, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("expected done (gate must not re-fire on second pending pass), got %v", final.Stages["s1"].Status)
	}
}

// TestIntegration_ResumeLeavesPausedStageUntouched — afm restarting must not
// auto-resume a paused stage; only an explicit Continue may.
func TestIntegration_ResumeLeavesPausedStageUntouched(t *testing.T) {
	stages := []flow.Stage{
		{ID: "paused-stage", Name: "Paused", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"paused-stage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "paused-stage", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "paused-stage", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = orch.Run(ctx) // expected to idle out on the ctx deadline — nothing else to complete

	final := loadStateJSON(t, stateFile)
	if final.Stages["paused-stage"].Status != state.StatusPaused {
		t.Errorf("expected paused stage to remain untouched by recovery, got %v", final.Stages["paused-stage"].Status)
	}
}

// TestContinue_FromPending_StartsScriptStage covers cases 1/3: a stage
// gated by auto_run:false (never actually started — PausedFrom=pending) is
// resumed by Continue exactly like a normal first activation.
func TestContinue_FromPending_StartsScriptStage(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Script: "true"}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// The gate never fired live (status was seeded directly into paused) —
	// nothing to wait for before calling Continue.
	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "s1", state.StatusDone, 3*time.Second)
}

// TestContinue_FromRevising_ResumesWithFeedback covers case 2: a stage
// paused mid-revision resumes via resumeStageAtStatus, exactly like an afm
// restart would.
func TestContinue_FromRevising_ResumesWithFeedback(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "revise-stuck")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.v1.md"), []byte("# Plan v1\n\nold content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte("please add error handling for edge case X"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "revise-stuck", Name: "Revise Stuck", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	store, err := state.Open(runDir, []string{"revise-stuck"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "revise-stuck", From: state.StatusPending, To: state.StatusRevising, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "revise-stuck", From: state.StatusRevising, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	capture := &capturingPlanningRunner{delegate: mockRunner(t, mockPlanningScript)}
	runner := &doneCreatingRunner{delegate: capture}

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})
	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, ctxCancel)

	if err := orch.Continue(ctx, "revise-stuck"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "revise-stuck", state.StatusDone, 8*time.Second)

	capture.mu.Lock()
	prompts := append([]string{}, capture.prompts...)
	capture.mu.Unlock()
	if len(prompts) == 0 || !strings.Contains(prompts[0], "please add error handling for edge case X") {
		t.Fatalf("expected planning prompt to include feedback.md content, got: %v", prompts)
	}
}

func TestContinue_NotPaused_IsNoOp(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Script: "true"}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "s1", state.StatusDone, 3*time.Second)

	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue on a non-paused stage should be a no-op, got error: %v", err)
	}
	final := loadStateJSON(t, stateFile)
	if final.Stages["s1"].Status != state.StatusDone {
		t.Errorf("Continue on a done stage must not change its status, got %v", final.Stages["s1"].Status)
	}
}

// blockingRunner: RunPlanning writes a minimally valid plan.md so the stage
// reaches running (headless auto-approve carries it from
// awaiting_approval->ready->running); RunAgent blocks on the same
// interruptChans channel Revise() already uses for a running stage (see
// orchestrator.InterruptChanForTest, used identically in
// agent_suggest_test.go's blockingThenFeedbackRunner) and returns
// executor.ErrUserInterrupted when signaled — simulating what a real
// executor does after SIGINT.
type blockingRunner struct {
	orch    *orchestrator.Orchestrator
	stageID string
}

func (r *blockingRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	plan := "## Tasks\n\n- [ ] implement feature\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] feature works\n"
	return os.WriteFile(outFile, []byte(plan), 0644)
}

func (r *blockingRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	ch, ok := orchestrator.InterruptChanForTest(r.orch, r.stageID)
	if !ok {
		<-ctx.Done()
		return context.Canceled
	}
	select {
	case <-ch:
		return executor.ErrUserInterrupted
	case <-ctx.Done():
		return context.Canceled
	}
}

func (r *blockingRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*blockingRunner)(nil)

func TestPause_RunningStage_StopsAgentAndTransitionsToPaused(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "impl", Name: "Impl", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}}

	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	runner := &blockingRunner{stageID: "impl"}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})
	runner.orch = orch

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "impl", state.StatusRunning, 10*time.Second)

	if err := orch.Pause(ctx, "impl"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	waitForStatus(t, stateFile, "impl", state.StatusPaused, 5*time.Second)

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl"].PausedFrom != state.StatusRunning {
		t.Errorf("PausedFrom = %q, want %q", final.Stages["impl"].PausedFrom, state.StatusRunning)
	}
}

// alwaysRetryableAgentRunner: planning succeeds via delegate; every RunAgent
// call fails with a retryable error, keeping the stage in the
// running->retrying backoff loop so the test can Pause it mid-backoff.
type alwaysRetryableAgentRunner struct {
	delegate executor.Runner
}

func (r *alwaysRetryableAgentRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *alwaysRetryableAgentRunner) RunAgent(_ context.Context, _, _, _, _ string) error {
	return errors.New("rate limit exceeded")
}

func (r *alwaysRetryableAgentRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

var _ executor.Runner = (*alwaysRetryableAgentRunner)(nil)

func TestPause_RetryingStage_CancelsBackoffImmediately(t *testing.T) {
	origBackoff := orchestrator.RetryBackoff
	origMax := orchestrator.MaxRetries
	orchestrator.RetryBackoff = 30 * time.Second
	orchestrator.MaxRetries = 15
	t.Cleanup(func() { orchestrator.RetryBackoff = origBackoff; orchestrator.MaxRetries = origMax })

	stages := []flow.Stage{
		{ID: "impl", Name: "Impl", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}
	runner := &alwaysRetryableAgentRunner{delegate: mockRunner(t, mockPlanningScript)}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	cancel := autoApprove(orch)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 15*time.Second)
	runOrchestratorAsync(ctx, t, orch, ctxCancel)

	waitForStatus(t, stateFile, "impl", state.StatusRetrying, 10*time.Second)

	if err := orch.Pause(ctx, "impl"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	// RetryBackoff is 30s — if Pause didn't cancel the backoff wait, this
	// would time out instead of passing quickly.
	waitForStatus(t, stateFile, "impl", state.StatusPaused, 3*time.Second)
}

func TestPause_AwaitingApproval_IsNoOp(t *testing.T) {
	srv := setupPauseNoOpOrchestrator(t) // see helper below
	if err := srv.orch.Pause(context.Background(), srv.stageID); err != nil {
		t.Fatalf("Pause on awaiting_approval should be a no-op, got error: %v", err)
	}
	if got := srv.store.Get(srv.stageID); got != state.StatusAwaitingApproval {
		t.Errorf("status changed to %v, want unchanged awaiting_approval", got)
	}
}

type pauseNoOpFixture struct {
	orch    *orchestrator.Orchestrator
	store   *state.Store
	stageID string
}

func setupPauseNoOpOrchestrator(t *testing.T) pauseNoOpFixture {
	t.Helper()
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentPlanning}}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusAwaitingApproval, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})
	return pauseNoOpFixture{orch: orch, store: store, stageID: "s1"}
}

// autonomousDoneCreatingRunner mirrors doneCreatingRunner but writes
// execution_summary.md (the autonomous completion marker) instead of .done.
type autonomousDoneCreatingRunner struct {
	delegate executor.Runner
}

func (r *autonomousDoneCreatingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *autonomousDoneCreatingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	if err := r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile); err != nil {
		return err
	}
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *autonomousDoneCreatingRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

var _ executor.Runner = (*autonomousDoneCreatingRunner)(nil)

// TestContinue_RecoveredCompletion_UnblocksDependent is a regression test for
// a bug found live: resumeStageAtStatus's "already complete, recovered from
// disk" fast paths (autonomous execution_summary.md / script .done) finalize
// the resumed stage via a bare Trigger(EvComplete) + maybeRunAfterHook,
// skipping the failBlockedStages/startPlanningForUnblocked/startReadyStages/
// tryActivatePrePlanned cascade that completeStage (used by the normal
// onAgentCompleted path) always runs afterward. Called from
// startPlanningForPending that's harmless — the bootstrap loop runs that
// cascade once after processing every stage regardless. But Continue()
// resumes exactly one stage and returns — if that stage happens to hit the
// recovered-from-disk fast path, nothing ever re-evaluates stages waiting on
// it, and they hang in pending forever.
func TestContinue_RecoveredCompletion_UnblocksDependent(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	// The agent already wrote its completion artifact before Pause() landed —
	// exactly the race a real run hit: Pause fires after the file is written
	// but before the FSM would have naturally reached done on its own.
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "s2", Name: "S2", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"s1"}},
	}
	store, err := state.Open(runDir, []string{"s1", "s2"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusRunning, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	runner := &autonomousDoneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// Let Run's synchronous startPlanningForPending bootstrap finish and the
	// event loop settle into its select before calling Continue — otherwise
	// this test races: if Continue ran first (plausible, since go func()
	// doesn't guarantee the new goroutine starts before this one continues),
	// the bootstrap's OWN post-loop scheduling cascade (called once after it
	// finishes processing every stage) would see s1 already done and activate
	// s2 itself, passing for the wrong reason and masking the real bug this
	// test targets: Continue's single-stage resume path skipping that cascade.
	time.Sleep(200 * time.Millisecond)

	if err := orch.Continue(ctx, "s1"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "s1", state.StatusDone, 3*time.Second)
	// The real assertion: s2 depends on s1 and must be unblocked by s1's
	// completion, exactly like it would be after any other stage's natural
	// completion — a hang here means Continue's fast path skipped scheduling.
	waitForStatus(t, stateFile, "s2", state.StatusDone, 5*time.Second)
}

// TestContinue_FromRetrying_AutonomousStage is a regression test for a bug
// found live: resumeStageAtStatus's StatusRetrying branch didn't check for
// autonomous stages the way its StatusRunning branch already does — Continue
// after a manual pause during retrying misrouted an agents:[auto] stage into
// EvStartPlanning + runPlanningAgent (a real planning agent, even though
// autonomous stages have no plan.md and never go through planning at all).
func TestContinue_FromRetrying_AutonomousStage(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "auto-retry")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{ID: "auto-retry", Name: "Auto Retry", Agents: []flow.AgentType{flow.AgentAuto}}}
	store, err := state.Open(runDir, []string{"auto-retry"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: "auto-retry", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "auto-retry", From: state.StatusRetrying, To: state.StatusPaused, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(runDir, "state.json")

	runner := &autonomousDoneCreatingRunner{delegate: mockRunner(t, mockPlanningScript)}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	if err := orch.Continue(ctx, "auto-retry"); err != nil {
		t.Fatalf("Continue: %v", err)
	}

	waitForStatus(t, stateFile, "auto-retry", state.StatusDone, 8*time.Second)

	// The critical assertion: reaching "done" alone isn't proof of the fix —
	// the buggy path (EvStartPlanning -> runPlanningAgent -> auto-approve ->
	// ready -> EvStartRun) also eventually reaches "done", because
	// startReadyStages (a different, already-correct call site) detects
	// autonomous.flag and spawns runAutonomousAgent regardless of how the
	// stage arrived at "ready". What distinguishes the buggy path is that it
	// writes a real plan.md along the way — something an autonomous stage
	// must never do. Its absence proves resumeStageAtStatus's Retrying branch
	// routed straight to runAutonomousAgent instead of through planning.
	if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err == nil {
		t.Error("plan.md was created — autonomous stage was incorrectly routed through planning on resume from retrying")
	}
}

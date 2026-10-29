package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// The owner cancels the wait of a stage that wrote its marker but yielded; end to end through Run it then takes
// the ordinary completion path: verify, required-helper gate, dependent-stage ordering.

const (
	recoveryImpl = "impl"
	recoveryDocs = "docs"
)

// stageScriptRunner is the author of every stage of a recovery run: script plays one turn of one stage, then the
// runner writes the completion markers a real agent would.
type stageScriptRunner struct {
	script func(stage string, turn int) error
	// noCompletion: the agent exits without writing any completion marker.
	noCompletion bool
	mu           sync.Mutex
	turns        map[string]int
	order        []string
}

func (r *stageScriptRunner) RunPlanning(context.Context, string, string, string, string) error {
	return errors.New("unexpected planning turn")
}

func (r *stageScriptRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	stage := filepath.Base(stageDir)
	r.mu.Lock()
	if r.turns == nil {
		r.turns = map[string]int{}
	}
	r.turns[stage]++
	turn := r.turns[stage]
	r.order = append(r.order, fmt.Sprintf("%s#%d", stage, turn))
	r.mu.Unlock()
	if r.script != nil {
		if err := r.script(stage, turn); err != nil {
			return err
		}
	}
	if r.noCompletion {
		return nil
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("summary"), 0o644)
}

func (r *stageScriptRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (r *stageScriptRunner) turnsOf(stage string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[stage]
}

func (r *stageScriptRunner) started() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// recoveryRig is impl (an autonomous stage with agent verify) and docs, which depends on it.
type recoveryRig struct {
	t           *testing.T
	o           *Orchestrator
	store       *state.Store
	runner      *stageScriptRunner
	runDir      string
	verifyCalls atomic.Int32
	verdict     atomic.Value // verify.Verdict the verifier answers with
	runDone     chan struct{}
	stopRun     context.CancelFunc
}

// newRecoveryRig builds the rig over runDir; beforeRecords sets the run up before its first journal record.
func newRecoveryRig(t *testing.T, runDir string, runner *stageScriptRunner, beforeRecords ...func(*state.Store)) *recoveryRig {
	t.Helper()
	return newRecoveryRigVerifying(t, runDir, runner, flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}, beforeRecords...)
}

// newRecoveryRigVerifying is newRecoveryRig with impl gated by verify.
func newRecoveryRigVerifying(t *testing.T, runDir string, runner *stageScriptRunner, verifySpec flow.VerifySpec, beforeRecords ...func(*state.Store)) *recoveryRig {
	t.Helper()
	store, err := state.Open(runDir, []string{recoveryImpl, recoveryDocs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, setUp := range beforeRecords {
		setUp(store)
	}
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatal(err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "qa", Title: "QA", Command: "helper-cmd"}}, nil, "claude",
		func(string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{recoveryImpl, recoveryDocs} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rig := &recoveryRig{t: t, store: store, runner: runner, runDir: runDir}
	rig.verdict.Store(verify.VerdictPass)
	rig.o = New(Options{
		RunDir: runDir, Store: store, Config: config.Default(), Runner: runner,
		Stages: []flow.Stage{
			{ID: recoveryImpl, Name: "Impl", Agents: []flow.AgentType{flow.AgentAuto}, Verify: verifySpec},
			{ID: recoveryDocs, Name: "Docs", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{recoveryImpl}},
		},
		AssistantsEnabled: true, AssistantsRegistry: reg,
	})
	rig.o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		rig.verifyCalls.Add(1)
		result := &verify.ModelResult{SchemaVersion: verify.SupportedSchemaVersion, Verdict: rig.verdict.Load().(verify.Verdict), Summary: "checked"}
		if result.Verdict == verify.VerdictNeedsChanges {
			result.Findings = []verify.Finding{{Blocking: true, Title: "broken", Requirement: "it works", Evidence: "it does not", MinimalFix: "fix it"}}
		}
		return verify.RunOutcome{ProcessOK: true, Result: result}, nil
	}
	return rig
}

// start runs the orchestrator until stop; the cleanup waits for Run, so the Store is closed after it.
func (r *recoveryRig) start() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	r.runDone = make(chan struct{})
	r.stopRun = cancel
	go func() { _ = r.o.Run(ctx); close(r.runDone) }()
	r.t.Cleanup(r.stop)
}

func (r *recoveryRig) stop() {
	r.stopRun()
	select {
	case <-r.runDone:
	case <-time.After(10 * time.Second):
		r.t.Error("Run did not return within 10s of cancellation")
	}
}

func (r *recoveryRig) waitStage(stage string, want state.StageStatus) {
	r.t.Helper()
	waitUntilFor(r.t, 10*time.Second, fmt.Sprintf("stage %s to be %s (is %s)", stage, want, r.store.Get(stage)), func() bool { return r.store.Get(stage) == want })
}

// waitUntilFor is waitUntil with a longer budget: a whole Run takes longer than a single commit.
func waitUntilFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// yieldOnce is turn 1 of impl: it registers a wait nobody will answer and ends the turn.
func (r *recoveryRig) yieldOnce(stage string, turn int) error {
	if stage != recoveryImpl || turn != 1 {
		return nil
	}
	_, err := r.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-recovery-wait", Waiter: meshStageParticipantID(recoveryImpl), Deadline: testFarDeadline(),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-nobody-answers"},
	})
	return err
}

func (r *recoveryRig) stuckWait() agentcomm.WaitID { return deriveMeshWaitID("op-recovery-wait") }

// ownerCancels is the owner's cancellation of the stage's stuck wait, under a request id.
func (r *recoveryRig) ownerCancels(requestID string) {
	r.t.Helper()
	err := r.o.CancelWait(context.Background(), CancelWaitRequest{
		OperationID: requestID, WaitIDs: []agentcomm.WaitID{r.stuckWait()}, StageID: recoveryImpl, Reason: "nobody will answer", Actor: ownerActor,
	})
	if err != nil {
		r.t.Fatalf("owner cancel: %v", err)
	}
}

// requireParked checks the state a conflicting completion marker must leave: parked, nothing verified, nothing
// downstream started, the wait still open.
func (r *recoveryRig) requireParked() {
	r.t.Helper()
	r.waitStage(recoveryImpl, state.StatusAwaitingPeer)
	if got := r.verifyCalls.Load(); got != 0 {
		r.t.Fatalf("verify ran %d times while the stage waits for peers", got)
	}
	if got := r.runner.turnsOf(recoveryDocs); got != 0 || r.store.Get(recoveryDocs) == state.StatusDone {
		r.t.Fatalf("docs started (%d turns, %s) before impl completed", got, r.store.Get(recoveryDocs))
	}
	if st := r.store.Snapshot().Assistants.Mesh.Waits[r.stuckWait()].Status; st != agentcomm.WaitPending {
		r.t.Fatalf("the stuck wait is %s, want pending", st)
	}
}

func TestOwnerRecovery_CancelThenCompletionRunsVerifyOnceAndReleasesTheDependentStage(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	runner.script = rig.yieldOnce
	rig.start()

	rig.requireParked()
	if n := len(readMarkerNotices(t, rig.runDir)); n != 1 {
		t.Fatalf("completion-marker notices = %d, want exactly 1 while parked", n)
	}

	rig.ownerCancels("req-cancel-1")
	rig.ownerCancels("req-cancel-1") // a repeat of the request is a no-op: one wake

	rig.waitStage(recoveryDocs, state.StatusDone)
	if got := rig.store.Get(recoveryImpl); got != state.StatusDone {
		t.Fatalf("impl = %s, want done", got)
	}
	if got := rig.verifyCalls.Load(); got != 1 {
		t.Fatalf("verify calls = %d, want exactly 1 (after the cancellation)", got)
	}
	order := runner.started()
	want := []string{"impl#1", "impl#2", "docs#1"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("turn order = %v, want %v (one continuation, docs strictly after impl)", order, want)
	}
	if n := len(readMarkerNotices(t, rig.runDir)); n != 1 {
		t.Fatalf("completion-marker notices = %d at the end, want still 1: the continuation turn waited on nothing", n)
	}
}

// A rejection by verify still stops the stage: cancelling the wait is not a pass.
func TestOwnerRecovery_VerifyRejectionAfterCancelStillBlocksCompletion(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	rig.verdict.Store(verify.VerdictNeedsChanges)
	runner.script = rig.yieldOnce
	rig.start()
	rig.requireParked()

	rig.ownerCancels("req-cancel-reject")

	rig.waitStage(recoveryImpl, state.StatusFailed)
	if rig.verifyCalls.Load() == 0 {
		t.Fatal("verify never ran after the cancellation")
	}
	time.Sleep(200 * time.Millisecond)
	if got := runner.turnsOf(recoveryDocs); got != 0 || rig.store.Get(recoveryDocs) == state.StatusDone {
		t.Fatalf("docs ran (%d turns, %s) although impl was rejected", got, rig.store.Get(recoveryDocs))
	}
}

// A required helper that has not passed still holds the stage after the cancellation and a passing verify.
func TestOwnerRecovery_RequiredHelperGateStillBlocksAfterCancel(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	release := make(chan struct{})
	rig.o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		select {
		case <-release:
			return passResultOutcome(), nil
		case <-ctx.Done():
			return executor.HelperOutcome{}, ctx.Err()
		}
	}
	runner.script = func(stage string, turn int) error {
		if stage == recoveryImpl && turn == 1 {
			if _, err := rig.o.Attach(context.Background(), AttachRequest{
				OperationID: "op-recovery-required", StageID: recoveryImpl, ProfileID: "qa", Mode: assistants.ModeRequired,
				Instruction: "review", Actor: state.Actor{Kind: "user"},
			}); err != nil {
				return err
			}
		}
		return rig.yieldOnce(stage, turn)
	}
	rig.start()
	rig.requireParked()

	rig.ownerCancels("req-cancel-gate")

	rig.waitStage(recoveryImpl, state.StatusAwaitingAssistants)
	time.Sleep(200 * time.Millisecond)
	if got := rig.store.Get(recoveryImpl); got != state.StatusAwaitingAssistants || runner.turnsOf(recoveryDocs) != 0 {
		t.Fatalf("impl = %s, docs turns = %d: the open required helper must hold the stage", got, runner.turnsOf(recoveryDocs))
	}

	close(release)
	rig.waitStage(recoveryDocs, state.StatusDone)
	if got := rig.verifyCalls.Load(); got != 1 {
		t.Fatalf("verify calls = %d, want 1", got)
	}
}

// Pause keeps the park; a cancellation made meanwhile is durable and Continue then finishes the stage.
func TestOwnerRecovery_CancelWhilePausedThenContinueCompletes(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	runner.script = rig.yieldOnce
	rig.start()
	rig.requireParked()

	if err := rig.o.Pause(context.Background(), recoveryImpl); err != nil {
		t.Fatal(err)
	}
	rig.waitStage(recoveryImpl, state.StatusPaused)
	rig.ownerCancels("req-cancel-paused")
	time.Sleep(200 * time.Millisecond)
	if got := rig.store.Get(recoveryImpl); got != state.StatusPaused || runner.turnsOf(recoveryImpl) != 1 || rig.verifyCalls.Load() != 0 {
		t.Fatalf("impl = %s after %d turns, %d verifies: a cancel must not lift a pause", got, runner.turnsOf(recoveryImpl), rig.verifyCalls.Load())
	}

	if err := rig.o.Continue(context.Background(), recoveryImpl); err != nil {
		t.Fatal(err)
	}

	rig.waitStage(recoveryDocs, state.StatusDone)
	if got := rig.verifyCalls.Load(); got != 1 || runner.turnsOf(recoveryImpl) != 2 {
		t.Fatalf("verify calls = %d, impl turns = %d, want 1 and 2", got, runner.turnsOf(recoveryImpl))
	}
}

// A Revise that lands in the yielding turn leaves the old wait open: the feedback turn parks again and nothing
// completes or verifies until the owner cancels it.
func TestOwnerRecovery_ReviseDoesNotLetTheOldWaitPass(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	runner.script = func(stage string, turn int) error {
		if err := rig.yieldOnce(stage, turn); err != nil {
			return err
		}
		if stage == recoveryImpl && turn == 1 {
			applied, _, err := rig.o.Revise(context.Background(), recoveryImpl, "use YAML instead")
			if err != nil || !applied {
				return fmt.Errorf("Revise = %v, %v; want applied", applied, err)
			}
		}
		return nil
	}
	rig.start()

	waitUntilFor(t, 10*time.Second, "the feedback turn to run", func() bool { return runner.turnsOf(recoveryImpl) >= 2 })
	rig.requireParked()
	if got := runner.turnsOf(recoveryImpl); got != 2 {
		t.Fatalf("impl turns = %d while parked, want 2 (the turn and the feedback turn)", got)
	}

	rig.ownerCancels("req-cancel-revise")

	rig.waitStage(recoveryDocs, state.StatusDone)
	if got := rig.verifyCalls.Load(); got != 1 {
		t.Fatalf("verify calls = %d, want 1", got)
	}
}

// The process stops after the cancellation was committed and before the stage was woken. The restarted run
// replays the journal and continues the stage once, through the normal completion.
func TestOwnerRecovery_RestartAfterCancellationContinuesOnce(t *testing.T) {
	runDir := t.TempDir()
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, runDir, runner)
	runner.script = rig.yieldOnce
	rig.start()
	rig.requireParked()
	rig.stop() // the process goes away; the park and its waits are in the journal
	rig.ownerCancels("req-cancel-restart")
	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}

	second := &stageScriptRunner{}
	restarted := newRecoveryRig(t, runDir, second)
	restarted.start()

	restarted.waitStage(recoveryDocs, state.StatusDone)
	if got := second.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns after the restart = %d, want 1 (the single continuation)", got)
	}
	if got := restarted.verifyCalls.Load(); got != 1 {
		t.Fatalf("verify calls after the restart = %d, want 1", got)
	}
	if n := len(readMarkerNotices(t, runDir)); n != 1 {
		t.Fatalf("completion-marker notices = %d, want only the first process's", n)
	}
}

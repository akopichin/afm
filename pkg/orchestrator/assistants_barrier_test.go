// Этап 7 (T7.2/T7.3): tests for the durable completion barrier and every fast-path that must now go through
// it instead of bypassing it. See AGENTS.md's "AI-verify" and "Paused Stage Status" sections for the
// established conventions these tests extend (CAS-protected transitions, a single completion funnel).
package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// newBarrierTestOrchestrator mirrors newAssistantsTestOrchestrator (assistants_test.go) but takes the full
// main-stage definition, so barrier tests can attach a ScriptAfter marker or a Verify spec to it — something
// the shared helper's fixed {ID: assistantsTestStageID, Command: "claude"} stage can't express.
func newBarrierTestOrchestrator(t *testing.T, mainStage flow.Stage) (*Orchestrator, *state.Store) {
	t.Helper()
	return newMultiStageBarrierTestOrchestrator(t, []flow.Stage{mainStage})
}

// newMultiStageBarrierTestOrchestrator is newBarrierTestOrchestrator's general form — needed whenever a test
// must observe the completion CASCADE (a downstream stage becoming ready), not just the barrier's own
// stage-local decision.
func newMultiStageBarrierTestOrchestrator(t *testing.T, stages []flow.Stage) (*Orchestrator, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	stageIDs := make([]string, len(stages))
	for i, s := range stages {
		stageIDs[i] = s.ID
	}
	store, err := state.Open(dir, stageIDs)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd"}}, nil, "claude",
		func(cmd string) (bool, string, assistants.ReadonlyEnforcement) { return true, "", assistants.ReadonlyEnforcementOS })
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	o := &Orchestrator{
		opts: Options{
			RunDir: dir, Stages: stages, Store: store, RunID: "run1",
			AssistantsEnabled: true, AssistantsRegistry: reg,
		},
		graph:         graph.NewGraph(stages),
		ui:            bus.NewUIBus(),
		critical:      bus.NewCriticalBus(16),
		fsm:           bus.NewFSM(store),
		concurrency:   concurrency.New(bus.NewCriticalBus(16), stages, "claude", 0, nil),
		fingerprintFn: func(string) (string, bool) { return "", false },
	}
	// Registered AFTER the store-close cleanup above, so LIFO runs it FIRST: a background task goroutine's
	// own finishTaskFrom/completeStage call (triggered by this test releasing a blocked runHelperAgent) must
	// fully settle BEFORE store.Close() runs, or it races store.Apply against a nilled-out eventsLog — exactly
	// the "write events.jsonl: invalid argument" flake AGENTS.md documents.
	t.Cleanup(o.concurrency.WaitAgents)
	return o, store
}

func attachRequiredTask(ctx context.Context, t *testing.T, o *Orchestrator, stageID, opID string) assistants.TaskID {
	t.Helper()
	taskID, err := o.Attach(ctx, AttachRequest{
		OperationID: opID, StageID: stageID, ProfileID: "reviewer", Mode: assistants.ModeRequired,
		Instruction: "block until resolved", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach(required): %v", err)
	}
	return taskID
}

func needsChangesOutcome() executor.HelperOutcome {
	return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"please fix X"}`}
}

// --- T7.2: durable completion barrier ---

func TestBarrier_AfterHookNotRunEarly(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "after-ran")
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude", ScriptAfter: "touch " + marker}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status = %v, want awaiting_assistants while the required task is still running", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("after-hook ran EARLY — it must not fire until the required task's gate actually closes")
	}

	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.currentStatus(assistantsTestStageID) == state.StatusDone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusDone {
		t.Fatalf("status after required task passes = %v, want done", got)
	}
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("after-hook never ran once the required task passed and the stage completed")
}

func TestBarrier_VerifyStillIndependentGate(t *testing.T) {
	// completeStage must never itself invoke verify — verify is a SEPARATE, upstream gate (gateWithVerify,
	// inside runWithRetry's completionCheck, which runs BEFORE completeAgent/EventAgentCompleted is ever
	// published — completeStage is only ever reached afterward). A verify step configured to fail loudly if
	// somehow re-run here ("exit 1") must have NO effect on completeStage's own decision once a required task
	// has already passed — proving the barrier neither replaces nor re-runs verify.
	stage := flow.Stage{
		ID: assistantsTestStageID, Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "exit 1"}}},
	}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)

	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := o.currentStatus(assistantsTestStageID); got != state.StatusDone {
		t.Fatalf("status = %v, want done — completeStage must not re-run/replace the independent verify gate", got)
	}
}

func TestBarrier_SlotReleasedWhileWaiting(t *testing.T) {
	// Entering awaiting_assistants must never acquire/hold a concurrency slot of its own — the main executor's
	// own process/goroutine has already exited by the time completeStage runs. Saturate the stage's OWN
	// command semaphore with an unrelated holder and confirm completeStage still settles into
	// awaiting_assistants synchronously (never blocks trying to acquire anything).
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)

	blockSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	blockSem <- struct{}{} // saturate "claude"'s only slot
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{
		"claude": blockSem, "helper-cmd": concurrency.ChannelSemaphore(make(chan struct{}, 2)),
	}, "claude")

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	done := make(chan struct{})
	go func() {
		o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("completeStage blocked entering awaiting_assistants — it must never wait on a concurrency slot")
	}
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status = %v, want awaiting_assistants", got)
	}
	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
}

func TestBarrier_RestoredAfterRestart(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	dir := t.TempDir()
	store, err := state.Open(dir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd"}}, nil, "claude",
		func(cmd string) (bool, string, assistants.ReadonlyEnforcement) { return true, "", assistants.ReadonlyEnforcementOS })
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	o := &Orchestrator{
		opts:          Options{RunDir: dir, Stages: []flow.Stage{stage}, Store: store, RunID: "run1", AssistantsEnabled: true, AssistantsRegistry: reg},
		graph:         graph.NewGraph([]flow.Stage{stage}),
		ui:            bus.NewUIBus(),
		critical:      bus.NewCriticalBus(16),
		fsm:           bus.NewFSM(store),
		concurrency:   concurrency.New(bus.NewCriticalBus(16), []flow.Stage{stage}, "claude", 0, nil),
		fingerprintFn: func(string) (string, bool) { return "", false },
	}
	activateStage(t, o, assistantsTestStageID)
	// release stays open for the "running at crash time" portion of the test. t.Cleanup (not a bare defer)
	// runs BEFORE t.TempDir()'s own removal cleanup (LIFO: TempDir's cleanup was registered first, at the top
	// of this test) — releasing and draining the background goroutine here avoids racing TempDir's RemoveAll
	// against a still-running task goroutine still touching files under dir.
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		o.concurrency.WaitAgents()
	})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return executor.HelperOutcome{}, context.Canceled
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status before restart = %v, want awaiting_assistants", got)
	}
	store.Close()

	// Simulate a restart: reopen the SAME run directory from scratch.
	reopened, err := state.Open(dir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer reopened.Close()

	rs := reopened.Snapshot()
	if rs.Stages[assistantsTestStageID].Status != state.StatusAwaitingAssistants {
		t.Fatalf("status after replay = %v, want awaiting_assistants (durable)", rs.Stages[assistantsTestStageID].Status)
	}
	blocker, open := rs.Assistants.RequiredBlocker(assistantsTestStageID)
	if !open || blocker.ID != taskID {
		t.Fatalf("RequiredBlocker after replay = (%v, %v), want (%s, true) — the barrier itself must survive the restart untouched", blocker, open, taskID)
	}
}

func TestBarrier_GenerationAwareCas(t *testing.T) {
	// The CAS protecting entry into awaiting_assistants is the ordinary FSM CAS on stage STATUS (EvAwaitAssistants's
	// From allowlist) — but the barrier's OWN decision (open/closed) is additionally generation-aware: once the
	// root stage's task-tree generation bumps (Revise invalidates the old attempt), a stale pass from the
	// PREVIOUS generation must not satisfy the CURRENT attempt's gate. Exercises AssistantsState.RequiredBlocker
	// (pkg/state, already unit-tested) through completeStage's real call path for extra confidence.
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)

	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	// Manually bump the root generation the way T4a.6's Revise invalidation does — op shape mirrors existing
	// kindRootGenerationBumped usage.
	bumpEff := state.Effect{Kind: "root_generation_bumped", Target: state.Target{StageID: assistantsTestStageID}}
	if _, err := store.ApplyRunOperation(state.Operation{
		ID: "bump-1", Kind: "bump_root_generation", Effects: []state.Effect{bumpEff},
	}, nil); err != nil {
		t.Fatalf("bump root generation: %v", err)
	}

	rs := store.Snapshot()
	if _, open := rs.Assistants.RequiredBlocker(assistantsTestStageID); open {
		t.Fatal("RequiredBlocker = open after bumping generation with no new required task attached — a stale-generation pass must simply stop being considered, not block forever")
	}

	// completeStage must now complete normally: the old task's pass is invisible to the new generation, and no
	// NEW required task was attached, so the barrier is vacuously satisfied.
	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusDone {
		t.Fatalf("status = %v, want done", got)
	}
}

// --- T7.3: every completion fast-path must go through the barrier ---

func TestResumeByDoneMarkerDoesNotBypassBarrier(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, assistantsTestStageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	store, err := state.Open(dir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd"}}, nil, "claude",
		func(cmd string) (bool, string, assistants.ReadonlyEnforcement) { return true, "", assistants.ReadonlyEnforcementOS })
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	o := &Orchestrator{
		opts:          Options{RunDir: dir, Stages: []flow.Stage{stage}, Store: store, RunID: "run1", AssistantsEnabled: true, AssistantsRegistry: reg},
		graph:         graph.NewGraph([]flow.Stage{stage}),
		ui:            bus.NewUIBus(),
		critical:      bus.NewCriticalBus(16),
		fsm:           bus.NewFSM(store),
		concurrency:   concurrency.New(bus.NewCriticalBus(16), []flow.Stage{stage}, "claude", 0, nil),
		fingerprintFn: func(string) (string, bool) { return "", false },
	}
	if err := store.Apply(&state.Transition{StageID: assistantsTestStageID, From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	// Attach schedules immediately (parentActive = status != pending, true for retrying) — block it so the task
	// stays open, simulating a required task mid-flight at crash time. t.Cleanup (LIFO, runs before TempDir's
	// own cleanup registered earlier) drains the background goroutine before the test directory is removed.
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		o.concurrency.WaitAgents()
	})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return executor.HelperOutcome{}, context.Canceled
	}
	_ = attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")

	o.startPlanningForPending(context.Background())

	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status = %v, want awaiting_assistants — an existing .done marker must not bypass the barrier on recovery", got)
	}
}

func TestPlanningOnlyStageUnaffected(t *testing.T) {
	// A planning-only stage (no implementation agent) completes through approveStage's own EvComplete path —
	// Этап 7's barrier must never touch it (it never calls completeStage at all).
	stage := flow.Stage{ID: "plan-only", Agents: []flow.AgentType{flow.AgentPlanning}}
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"plan-only"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	o := &Orchestrator{
		opts:          Options{RunDir: dir, Stages: []flow.Stage{stage}, Store: store, RunID: "run1"},
		graph:         graph.NewGraph([]flow.Stage{stage}),
		ui:            bus.NewUIBus(),
		critical:      bus.NewCriticalBus(16),
		fsm:           bus.NewFSM(store),
		concurrency:   concurrency.New(bus.NewCriticalBus(16), []flow.Stage{stage}, "claude", 0, nil),
		fingerprintFn: func(string) (string, bool) { return "", false },
	}
	if _, ok := o.Trigger("plan-only", bus.EvStartPlanning, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvStartPlanning failed")
	}
	if _, ok := o.Trigger("plan-only", bus.EvPlanReady, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvPlanReady failed")
	}
	o.approveStage(context.Background(), "plan-only")

	if got := o.currentStatus("plan-only"); got != state.StatusDone {
		t.Fatalf("status = %v, want done — planning-only completion must be unaffected by the assistants barrier", got)
	}
}

func TestCompletionGoesThroughCascade(t *testing.T) {
	// Once the barrier is satisfied, completion must run the SAME cascade as any other completeStage call
	// (failBlockedStages/startPlanningForUnblocked/startReadyStages/tryActivatePrePlanned) — proven here by a
	// dependent stage becoming ready immediately once the blocking stage completes.
	upstream := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	downstream := flow.Stage{ID: "downstream", Command: "claude", DependsOn: []string{assistantsTestStageID}}
	o, store := newMultiStageBarrierTestOrchestrator(t, []flow.Stage{upstream, downstream})
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")

	if got := o.currentStatus(assistantsTestStageID); got != state.StatusDone {
		t.Fatalf("status = %v, want done", got)
	}
	if got := o.currentStatus("downstream"); got != state.StatusReady && got != state.StatusRunning {
		t.Fatalf("downstream status = %v, want ready/running — completion must drive the normal cascade", got)
	}
}

func TestAttachRequiredRacesWithComplete(t *testing.T) {
	// -race: attaching a NEW required task concurrently with the parent stage's own completion attempt must
	// serialize through one coordinator — either Attach's CAS commits first (the stage must then go to
	// awaiting_assistants, never done, until the task resolves) or completeStage's CAS commits first (Attach
	// must then be cleanly rejected — Этап 7's new terminal-status guard in Attach — rather than silently
	// admitting a required task nothing will ever check again).
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		o.concurrency.WaitAgents()
	})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}

	var attachErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, attachErr = o.Attach(context.Background(), AttachRequest{
			OperationID: "op-race", StageID: assistantsTestStageID, ProfileID: "reviewer",
			Mode: assistants.ModeRequired, Instruction: "race", Actor: state.Actor{Kind: "user"},
		})
	}()
	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	<-done

	// One atomic snapshot — status and the gate must be read together, or the TEST itself would race.
	rs := store.Snapshot()
	status := rs.Stages[assistantsTestStageID].Status
	_, open := rs.Assistants.RequiredBlocker(assistantsTestStageID)

	switch {
	case attachErr == nil && open:
		if status != state.StatusAwaitingAssistants {
			t.Fatalf("Attach won the race (task admitted, still open) but status = %v, want awaiting_assistants", status)
		}
	case attachErr != nil:
		if status != state.StatusDone {
			t.Fatalf("Attach was rejected (%v) but status = %v, want done — completeStage must have won the race", attachErr, status)
		}
	default:
		t.Fatalf("unexpected combination: attachErr=%v open=%v status=%v", attachErr, open, status)
	}
}

func TestCollaborationDisabledBeforeResumeDoesNotSkipRequirement(t *testing.T) {
	// A required task was attached while the feature was enabled; collaboration is now disabled (e.g. config
	// changed before a restart) — AssistantsEnabled=false must NOT turn an unresolved requirement into a
	// silent "done": RequiredBlocker is a pure, always-on read of durable state, independent of the flag.
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	// Simulate the flag flipping off for the NEXT process (resume) — the durable requirement predates it.
	o.opts.AssistantsEnabled = false

	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status = %v, want awaiting_assistants even with AssistantsEnabled=false", got)
	}
	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
}

// --- T7.4/T7.5 at the orchestrator level: who exactly is blocked ---

func TestChain_ExplainedInApi(t *testing.T) {
	stage := flow.Stage{ID: assistantsTestStageID, Command: "claude"}
	o, store := newBarrierTestOrchestrator(t, stage)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}
	taskID := attachRequiredTask(context.Background(), t, o, assistantsTestStageID, "op-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)

	rs := store.Snapshot()
	blocker, open := rs.Assistants.RequiredBlocker(assistantsTestStageID)
	if !open || blocker.ID != taskID {
		t.Fatalf("RequiredBlocker = (%v, %v), want (%s, true)", blocker, open, taskID)
	}
	// blocker has no ParentTaskID -> it blocks the STAGE itself (not another helper).
	if blocker.ParentTaskID != nil {
		t.Fatalf("blocker.ParentTaskID = %v, want nil — a direct child of the stage blocks the stage, not a helper", blocker.ParentTaskID)
	}
	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
}

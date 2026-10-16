// T3 (dev-plan.md): declarative stage assistants auto-attach — attachDeclaredAssistants is wired into
// spawnKind (startReadyStage, Revise, every crash recovery path — see its own doc comment for the one known
// exception, review-pause's resumeOwner), strictly BEFORE the agent itself is spawned.
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
	"github.com/akopichin/afm/pkg/state"
)

// wireHelperAgentFake stands in for the real helper-task subprocess runner: Attach auto-runs a freshly
// created non-watcher task SYNCHRONOUSLY inside its own commit callback (maybeScheduleTask), so every test
// below that attaches to an already-active stage needs this wired, or the background goroutine panics on a
// nil o.runHelperAgent (production wires it to execHelperAgent; these test Orchestrators build the struct
// literal directly and skip that).
func wireHelperAgentFake(o *Orchestrator) {
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
}

// declStageSetup writes the on-disk fixture a non-interactive execution-track stage needs: a valid plan.md
// (so runImplementationAttempt never fails on a missing-plan read before attachDeclaredAssistants's effect
// is even checkable) and nothing else — no session files, so detectInterruptedPhase behaves exactly as it
// does for a real non-interactive stage (returns "").
func declStageSetup(t *testing.T, o *Orchestrator, stageID string) string {
	t.Helper()
	stageDir := filepath.Join(o.opts.RunDir, stageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := "## Tasks\n- x\n\n## Assumptions\n- y\n\n## Acceptance Criteria\n- z\n"
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	return stageDir
}

// readyAndStart triggers EvReady then calls startReadyStage, failing the test on either step — the common
// "stage is ready, start its execution agent" sequence shared by most of the tests below.
func readyAndStart(t *testing.T, o *Orchestrator, id string, stage flow.Stage) {
	t.Helper()
	if _, ok := o.Trigger(id, bus.EvReady, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvReady failed")
	}
	if !o.startReadyStage(context.Background(), stage) {
		t.Fatal("startReadyStage returned false")
	}
}

// waitForAssistantCount polls the store until stageID's ByRootStage index reaches exactly want tasks.
func waitForAssistantCount(t *testing.T, store *state.Store, stageID string, want int, timeout time.Duration) []assistants.TaskID {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ids := store.Snapshot().Assistants.ByRootStage[stageID]; len(ids) == want {
			return ids
		}
		time.Sleep(5 * time.Millisecond)
	}
	ids := store.Snapshot().Assistants.ByRootStage[stageID]
	t.Fatalf("ByRootStage[%s] = %v (len %d), want len %d", stageID, ids, len(ids), want)
	return nil
}

// 1. Happy path: a scalar-form assistant attaches exactly once, with Attach's own defaults.
func TestDeclaredAssistants_HappyPath(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "reviewer"}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents) // registered after store.Close (LIFO): drains agents before the store closes
	declStageSetup(t, o, id)
	o.opts.Runner = &fakeAgentRunner{}
	o.runner = &fakeAgentRunner{}
	wireHelperAgentFake(o)

	readyAndStart(t, o, id, stage)

	ids := waitForAssistantCount(t, store, id, 1, 2*time.Second)
	tk := store.Snapshot().Assistants.Tasks[ids[0]]
	if tk.RootStageID != id {
		t.Errorf("RootStageID = %q, want %q", tk.RootStageID, id)
	}
	if tk.ProfileID != "reviewer" {
		t.Errorf("ProfileID = %q, want %q", tk.ProfileID, "reviewer")
	}
	// Not "queued": the stage is already active (parentActive), so Attach's own post-commit callback
	// synchronously auto-schedules a plain one_shot task straight to "running" — the same behavior every
	// other Attach test in this package already relies on (see e.g. assistants_barrier_test.go).
	if tk.Status != assistants.StatusRunning {
		t.Errorf("Status = %q, want %q", tk.Status, assistants.StatusRunning)
	}
	if tk.Mode != assistants.ModeAdvisory {
		t.Errorf("Mode = %q, want %q (Attach's default)", tk.Mode, assistants.ModeAdvisory)
	}
	if tk.Trigger != assistants.TriggerOneShot {
		t.Errorf("Trigger = %q, want %q (Attach's default)", tk.Trigger, assistants.TriggerOneShot)
	}
}

// 2. Object form: explicit title/instruction/mode/trigger all reach Attach, and an on_completion trigger
// lands the task in "watching" (not "queued") — exactly as a manual dashboard Attach would.
func TestDeclaredAssistants_ObjectFormReachesAttach(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{
		Profile: "reviewer", Title: "QA gate", Instruction: "check coverage",
		Mode: assistants.ModeRequired, Trigger: assistants.TriggerOnCompletion,
	}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents) // registered after store.Close (LIFO): drains agents before the store closes
	declStageSetup(t, o, id)
	o.opts.Runner = &fakeAgentRunner{}
	o.runner = &fakeAgentRunner{}

	readyAndStart(t, o, id, stage)

	ids := waitForAssistantCount(t, store, id, 1, 2*time.Second)
	tk := store.Snapshot().Assistants.Tasks[ids[0]]
	if tk.Title != "QA gate" || tk.Instruction != "check coverage" {
		t.Errorf("Title/Instruction = %q/%q, want %q/%q", tk.Title, tk.Instruction, "QA gate", "check coverage")
	}
	if tk.Mode != assistants.ModeRequired {
		t.Errorf("Mode = %q, want %q", tk.Mode, assistants.ModeRequired)
	}
	if tk.Trigger != assistants.TriggerOnCompletion {
		t.Errorf("Trigger = %q, want %q", tk.Trigger, assistants.TriggerOnCompletion)
	}
	if tk.Status != assistants.StatusWatching {
		t.Errorf("Status = %q, want %q (on_completion watcher starts watching, not queued)", tk.Status, assistants.StatusWatching)
	}
}

// 3. Multiple declared assistants attach in declaration order.
func TestDeclaredAssistants_OrderPreserved(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "reviewer"}, {Profile: "qa"}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents) // registered after store.Close (LIFO): drains agents before the store closes
	declStageSetup(t, o, id)
	o.opts.Runner = &fakeAgentRunner{}
	o.runner = &fakeAgentRunner{}
	wireHelperAgentFake(o)

	readyAndStart(t, o, id, stage)

	ids := waitForAssistantCount(t, store, id, 2, 2*time.Second)
	snap := store.Snapshot()
	if got := snap.Assistants.Tasks[ids[0]].ProfileID; got != "reviewer" {
		t.Errorf("ids[0].ProfileID = %q, want %q", got, "reviewer")
	}
	if got := snap.Assistants.Tasks[ids[1]].ProfileID; got != "qa" {
		t.Errorf("ids[1].ProfileID = %q, want %q", got, "qa")
	}
}

// 4. Idempotency on a restarted attempt: retryStage re-enters ready -> startReadyStage WITHOUT bumping the
// generation — the SAME operation id must replay, not create a second task.
func TestDeclaredAssistants_IdempotentOnRetry(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "reviewer"}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents) // registered after store.Close (LIFO): drains agents before the store closes
	declStageSetup(t, o, id)
	// fakeAgentRunner.RunAgent returns nil with no .done written -> CheckCompletion reports incomplete work;
	// attempt 0 == maxRetries (0, the Orchestrator literal's zero value) so the stage fails immediately, with
	// no backoff sleep.
	fake := &fakeAgentRunner{}
	o.opts.Runner = fake
	o.runner = fake
	wireHelperAgentFake(o)

	readyAndStart(t, o, id, stage)
	waitForAssistantCount(t, store, id, 1, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := o.currentStatus(id); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed (so retryStage below has something to retry)", got)
	}

	o.retryStage(context.Background(), id)
	o.concurrency.WaitAgents()

	waitForAssistantCount(t, store, id, 1, 2*time.Second) // still ONE task: same generation, idempotent replay
}

// 5. A bumped generation makes the next attempt attach a genuinely NEW task; the old one remains in history.
func TestDeclaredAssistants_NewGenerationAttachesNewTask(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "reviewer"}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents) // registered after store.Close (LIFO): drains agents before the store closes
	declStageSetup(t, o, id)
	fake := &fakeAgentRunner{}
	o.opts.Runner = fake
	o.runner = fake
	wireHelperAgentFake(o)

	readyAndStart(t, o, id, stage)
	waitForAssistantCount(t, store, id, 1, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := o.currentStatus(id); got != state.StatusFailed {
		t.Fatalf("status = %v, want failed", got)
	}

	if err := o.BumpRootGeneration(id); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	o.retryStage(context.Background(), id)
	o.concurrency.WaitAgents()

	waitForAssistantCount(t, store, id, 2, 2*time.Second) // new generation -> second task; first stays in history
}

// 6. Collaboration disabled: declaring assistants is a silent no-op, the stage still runs normally.
func TestDeclaredAssistants_DisabledCollaborationIsNoOp(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "reviewer"}}}
	o, store := newAssistantsTestOrchestrator(t, false, stage)
	t.Cleanup(o.concurrency.WaitAgents)
	declStageSetup(t, o, id)
	fake := &fakeAgentRunner{}
	o.opts.Runner = fake
	o.runner = fake

	readyAndStart(t, o, id, stage)
	if ids := store.Snapshot().Assistants.ByRootStage[id]; len(ids) != 0 {
		t.Fatalf("ByRootStage[%s] = %v, want none (collaboration disabled)", id, ids)
	}
	if got := o.currentStatus(id); got != state.StatusRunning {
		t.Fatalf("status = %v, want running (disabled collaboration must not block the stage)", got)
	}
	o.concurrency.WaitAgents()
	if err := o.loadFatal(); err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
}

// 7. An unknown profile at runtime (preflight bypassed) is best-effort: logged, not attached, the stage
// still runs.
func TestDeclaredAssistants_UnknownProfileDoesNotBlockStage(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "ghost"}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents) // registered after store.Close (LIFO): drains agents before the store closes
	declStageSetup(t, o, id)
	fake := &fakeAgentRunner{}
	o.opts.Runner = fake
	o.runner = fake

	readyAndStart(t, o, id, stage)
	if ids := store.Snapshot().Assistants.ByRootStage[id]; len(ids) != 0 {
		t.Fatalf("ByRootStage[%s] = %v, want none (unknown profile)", id, ids)
	}
	if got := o.currentStatus(id); got != state.StatusRunning {
		t.Fatalf("status = %v, want running (an unknown profile must not block the stage)", got)
	}
	o.concurrency.WaitAgents()
	if err := o.loadFatal(); err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
}

// 8. A regression guard for the first plan draft's bug: an agent that completes the stage to "done"
// SYNCHRONOUSLY from inside its own RunAgent call must not race ahead of the attach — attachDeclaredAssistants
// runs as spawnKind's first line, strictly before spawnAgentLeased ever starts the agent goroutine.
func TestDeclaredAssistants_AttachBeforeInstantCompletion(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "reviewer"}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents)
	stageDir := declStageSetup(t, o, id)
	fake := &fakeAgentRunner{onAgent: func(string) {
		_ = os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644)
		o.completeStage(context.Background(), id, state.StatusRunning, "instant completion")
	}}
	o.opts.Runner = fake
	o.runner = fake
	wireHelperAgentFake(o)

	readyAndStart(t, o, id, stage)

	waitForAssistantCount(t, store, id, 1, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := o.currentStatus(id); got != state.StatusDone {
		t.Fatalf("status = %v, want done", got)
	}
}

// 9a. Revise from awaiting_assistants cleans the stale generation and attaches the declared assistant AGAIN,
// on the new generation.
func TestDeclaredAssistants_ReviseReattaches(t *testing.T) {
	stage := flow.Stage{
		ID: assistantsTestStageID, Command: "claude",
		Assistants: flow.StageAssistants{{Profile: "reviewer"}},
	}
	o, store, _ := enterAwaitingAssistantsForRevise(t, stage)
	before := waitForAssistantCount(t, store, assistantsTestStageID, 1, 2*time.Second) // the manually-attached barrier task

	applied, _, err := o.Revise(context.Background(), assistantsTestStageID, "try again")
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	if !applied {
		t.Fatal("Revise: applied = false")
	}

	ids := waitForAssistantCount(t, store, assistantsTestStageID, len(before)+1, 2*time.Second)
	snap := store.Snapshot()
	gen := snap.Assistants.RootGenerations[assistantsTestStageID]
	newID := ids[len(ids)-1]
	tk := snap.Assistants.Tasks[newID]
	if tk.RootGeneration != gen {
		t.Fatalf("new task's RootGeneration = %d, want current generation %d", tk.RootGeneration, gen)
	}
	if tk.ProfileID != "reviewer" {
		t.Fatalf("new task's ProfileID = %q, want %q", tk.ProfileID, "reviewer")
	}
}

// 9b. Same path, but via crash recovery: the process died between EvRevise+BumpRootGeneration and the spawn
// (reviseAwaitingAssistants's own window) — resumeStageAtStatus(StatusRevising) must still attach.
func TestDeclaredAssistants_RecoveredRevisingReattaches(t *testing.T) {
	stage := flow.Stage{
		ID: assistantsTestStageID, Command: "claude",
		Assistants: flow.StageAssistants{{Profile: "reviewer"}},
	}
	o, store, _ := enterAwaitingAssistantsForRevise(t, stage)
	before := waitForAssistantCount(t, store, assistantsTestStageID, 1, 2*time.Second)

	// Simulate the crash window: EvRevise + BumpRootGeneration committed durably, but the process went down
	// before spawnKind (and thus attachDeclaredAssistants) ever ran.
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvRevise, bus.GuardCtx{}, "crash-window revise"); !ok {
		t.Fatal("EvRevise failed")
	}
	if err := o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}

	o.resumeStageAtStatus(context.Background(), stage, state.StatusRevising)

	waitForAssistantCount(t, store, assistantsTestStageID, len(before)+1, 2*time.Second)
}

// 10. The window-of-death between EvStartRun being committed and the agent actually spawning: resumption
// must attach exactly once, and a repeated resumption of the SAME crash must not duplicate it.
func TestDeclaredAssistants_CrashBeforeSpawnAttachesOnce(t *testing.T) {
	const id = "decl"
	stage := flow.Stage{ID: id, Assistants: flow.StageAssistants{{Profile: "reviewer"}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents)
	fake := &fakeAgentRunner{}
	o.opts.Runner = fake
	o.runner = fake
	wireHelperAgentFake(o)

	if _, ok := o.Trigger(id, bus.EvReady, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvReady failed")
	}
	// EvStartRun committed, but nothing was ever spawned — exactly the state a crash right after
	// startReadyStage's durable transition (and before its spawnKind call) would leave behind.
	if _, ok := o.Trigger(id, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvStartRun failed")
	}

	o.resumeExecutionStage(context.Background(), stage, state.StatusRunning)
	waitForAssistantCount(t, store, id, 1, 2*time.Second)
	o.concurrency.WaitAgents()

	// A second recovery pass over the same crash (e.g. another restart before progress) must not duplicate.
	o.resumeExecutionStage(context.Background(), stage, state.StatusRunning)
	o.concurrency.WaitAgents()
	waitForAssistantCount(t, store, id, 1, 2*time.Second)
}

// 11. revisingKind: a non-interactive stage in `revising` has no session file to read, so the phase must be
// derived from the stage itself — not default to planning (the pre-fix bug this guards against).
func TestRevisingKind_NonInteractiveWithoutSessionFile(t *testing.T) {
	t.Run("auto stage resumes autonomous", func(t *testing.T) {
		const id = "decl"
		stage := flow.Stage{ID: id, Agents: []flow.AgentType{flow.AgentAuto}, Assistants: flow.StageAssistants{{Profile: "reviewer"}}}
		o, store := newAssistantsTestOrchestrator(t, true, stage)
		t.Cleanup(o.concurrency.WaitAgents)
		stageDir := filepath.Join(o.opts.RunDir, id)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		fake := &fakeAgentRunner{}
		o.opts.Runner = fake
		o.runner = fake
		wireHelperAgentFake(o)

		if _, ok := o.Trigger(id, bus.EvReady, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvReady failed")
		}
		if _, ok := o.Trigger(id, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvStartRun failed")
		}
		if _, ok := o.Trigger(id, bus.EvRevise, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvRevise failed")
		}
		if err := o.BumpRootGeneration(id); err != nil {
			t.Fatalf("BumpRootGeneration: %v", err)
		}

		o.resumeStageAtStatus(context.Background(), stage, state.StatusRevising)
		// Read runnerKindOf IMMEDIATELY: setRunnerKind is spawnKind's synchronous first line, but the agent
		// goroutine it spawns races ahead in the background and may later clear it on EvFail/EvComplete.
		kind := o.runnerKindOf(id)
		waitForAssistantCount(t, store, id, 1, 2*time.Second)
		if kind != kindAutonomous {
			t.Fatalf("runnerKindOf = %q, want %q", kind, kindAutonomous)
		}
	})

	t.Run("implementation with intact plan.md resumes implementation", func(t *testing.T) {
		const id = "decl"
		stage := flow.Stage{
			ID: id, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
			Assistants: flow.StageAssistants{{Profile: "reviewer"}},
		}
		o, store := newAssistantsTestOrchestrator(t, true, stage)
		t.Cleanup(o.concurrency.WaitAgents)
		declStageSetup(t, o, id) // writes a valid plan.md
		fake := &fakeAgentRunner{}
		o.opts.Runner = fake
		o.runner = fake
		wireHelperAgentFake(o)

		if _, ok := o.Trigger(id, bus.EvReady, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvReady failed")
		}
		if _, ok := o.Trigger(id, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvStartRun failed")
		}
		if _, ok := o.Trigger(id, bus.EvRevise, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvRevise failed")
		}
		if err := o.BumpRootGeneration(id); err != nil {
			t.Fatalf("BumpRootGeneration: %v", err)
		}

		o.resumeStageAtStatus(context.Background(), stage, state.StatusRevising)
		kind := o.runnerKindOf(id) // see the "auto stage" subtest above for why this is read before waiting
		waitForAssistantCount(t, store, id, 1, 2*time.Second)
		if kind != kindImplementation {
			t.Fatalf("runnerKindOf = %q, want %q", kind, kindImplementation)
		}
	})

	t.Run("planning with archived plan.md resumes planning, no attach", func(t *testing.T) {
		const id = "decl"
		stage := flow.Stage{
			ID: id, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
			Assistants: flow.StageAssistants{{Profile: "reviewer"}},
		}
		o, store := newAssistantsTestOrchestrator(t, true, stage)
		t.Cleanup(o.concurrency.WaitAgents)
		stageDir := filepath.Join(o.opts.RunDir, id)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Mirrors reviseAwaitingApproval's VersionPlan call: plan.md archived to plan.v1.md, nothing named
		// plan.md left — the same on-disk shape a Revise targeting the plan leaves behind.
		if err := os.WriteFile(filepath.Join(stageDir, "plan.v1.md"), []byte("old plan"), 0o644); err != nil {
			t.Fatal(err)
		}
		fake := &fakeAgentRunner{}
		o.opts.Runner = fake
		o.runner = fake

		if _, ok := o.Trigger(id, bus.EvReady, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvReady failed")
		}
		if _, ok := o.Trigger(id, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvStartRun failed")
		}
		if _, ok := o.Trigger(id, bus.EvRevise, bus.GuardCtx{}, ""); !ok {
			t.Fatal("EvRevise failed")
		}
		if err := o.BumpRootGeneration(id); err != nil {
			t.Fatalf("BumpRootGeneration: %v", err)
		}

		o.resumeStageAtStatus(context.Background(), stage, state.StatusRevising)
		kind := o.runnerKindOf(id) // see the "auto stage" subtest above for why this is read before waiting
		ids := store.Snapshot().Assistants.ByRootStage[id]
		o.concurrency.WaitAgents()
		if kind != kindPlanning {
			t.Fatalf("runnerKindOf = %q, want %q (planning-only gate must still skip auto-attach)", kind, kindPlanning)
		}
		if len(ids) != 0 {
			t.Fatalf("ByRootStage[%s] = %v, want none (attach is gated out during planning)", id, ids)
		}
	})
}

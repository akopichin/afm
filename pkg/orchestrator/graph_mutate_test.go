package orchestrator

// Этап 9, T9.1/T9.4 — graph_mutate.go (PreviewAddStage/CommitAddStage) shipped with zero tests. These cover
// the properties that need white-box access (o.commitAddStageBarrier, raw *state.Store inspection): no
// mutation on preview, exactly one durable record per commit, idempotent replay, run_finalizing rejection,
// no lock held during the advisory preflight, and the shared-serialization guarantee AddStageToGraph's own
// doc comment promises against a concurrent scheduler admission decision. Tests that need a full live
// scheduler/runner loop live in graph_mutate_integration_test.go (package orchestrator_test).
//
// Why every stage here is agents:[auto] with an explicit, safe Command (not a bare {ID: "x"} literal nor
// Options.Runner): CommitAddStage wakes the WHOLE scheduler (tryActivatePrePlanned scans every pending
// stage, not just the newly inserted one), so ANY pending bystander stage in the fixture is scheduler-
// eligible. runner_factory.go's runnerFor only substitutes an injected Options.Runner for a stage whose OWN
// Command is empty ("test seam stands in only for the default command") — a dynamically-added stage always
// carries an explicit Command (T9.1's whole point is picking one from the prepared set), so it ALWAYS bypasses
// an injected Runner and gets a real *executor.Executor. The fix used throughout this file: point the
// default command AND every stage's own Command at one small, real, instantaneous script that writes
// execution_summary.md (the agents:[auto] completion marker) and exits — safe to actually exec, unlike
// shelling out to a real, unauthenticated "claude" binary.
import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// newSafeAgentCommand writes a tiny real script that stands in for "claude": it writes
// execution_summary.md into $AFM_STAGE_DIR (the agents:[auto] completion marker — see
// stagefiles.CheckAutonomousCompletion) and exits 0, instantly, regardless of how it's invoked. Returns its
// absolute path, usable both as the run's default Client.Command and as a dynamically-added stage's own
// Command (both must be the SAME prepared command for IsCommandPrepared to accept it).
func newSafeAgentCommand(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-agent.sh")
	script := "#!/bin/sh\nif [ -n \"$AFM_STAGE_DIR\" ]; then printf '## Summary\\ndone\\n' > \"$AFM_STAGE_DIR/execution_summary.md\"; fi\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake agent script: %v", err)
	}
	return path
}

// autoStage builds an agents:[auto] stage using cmd (newSafeAgentCommand's path) as its Command — every
// stage in this file's fixtures needs this shape, for the reasons in the package doc comment above.
func autoStage(id, cmd string, deps ...string) flow.Stage {
	return flow.Stage{ID: id, Agents: []flow.AgentType{flow.AgentAuto}, Command: cmd, Prompt: "do it", DependsOn: deps}
}

// newGraphMutateTestOrchestrator builds a real Orchestrator whose default command is cmd (newSafeAgentCommand's
// path) — deliberately NOT injecting Options.Runner: per the package doc comment, an injected Runner would
// only ever cover a bystander stage with an empty Command, never the dynamically-added stage itself, so using
// one here would be a false sense of safety. Every stage this file schedules is real-subprocess-safe by
// construction (autoStage + this same cmd).
func newGraphMutateTestOrchestrator(t *testing.T, cmd string, stages []flow.Stage) (*Orchestrator, *state.Store) {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, 0, len(stages))
	for _, s := range stages {
		ids = append(ids, s.ID)
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := config.Default()
	cfg.Client.Command = cmd
	orch := New(Options{RunDir: runDir, Stages: stages, Store: store, Config: cfg, Prompts: DefaultPrompts()})
	// CommitAddStage wakes the scheduler via tryActivatePrePlanned, which spawns agent goroutines fire-and-
	// forget (nothing in this file calls orch.Run/WaitAgents itself). Registered AFTER the store-close
	// cleanup above so LIFO runs it FIRST — every spawned agent finishes (the safe script exits in
	// milliseconds) before the store they'd otherwise write into gets closed.
	t.Cleanup(func() { orch.concurrency.WaitAgents() })
	return orch, store
}

func draftN(id, cmd string) graph.StageDraft {
	return graph.StageDraft{ID: id, Name: "New stage", Command: cmd, Prompt: "do it"}
}

// TestPreview_NoMutation — T9.1: PreviewAddStage must never touch the journal or bump the registry revision.
func TestPreview_NoMutation(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	orch, store := newGraphMutateTestOrchestrator(t, cmd, []flow.Stage{autoStage("t", cmd)})

	seqBefore := store.Snapshot().LastSeq
	revBefore := orch.GraphRevision()

	preview, err := orch.PreviewAddStage(AddStageDraft{
		Kind: AddStageKindInsertBefore, TargetID: "t", Stage: draftN("n", cmd),
	})
	if err != nil {
		t.Fatalf("PreviewAddStage: %v", err)
	}
	if preview.Plan.Stage.ID != "n" {
		t.Fatalf("preview.Plan.Stage.ID = %q, want n", preview.Plan.Stage.ID)
	}

	if got := store.Snapshot().LastSeq; got != seqBefore {
		t.Fatalf("journal advanced by preview: LastSeq %d -> %d", seqBefore, got)
	}
	if got := orch.GraphRevision(); got != revBefore {
		t.Fatalf("registry revision bumped by preview: %d -> %d", revBefore, got)
	}
}

// TestAppend_RejectedAfterRunFinalizationStarted — T9.3, §11.4: a run already winding down (the same flag
// orchestrator.go's shutdown path sets via Store.SetRunFinalizing before o.runEndOfRunMemory) must reject a
// concurrent append with run_finalizing — a closing run must never be "revived" by a late POST.
func TestAppend_RejectedAfterRunFinalizationStarted(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	orch, store := newGraphMutateTestOrchestrator(t, cmd, []flow.Stage{autoStage("a", cmd)})
	store.SetRunFinalizing(true)

	_, err := orch.CommitAddStage(context.Background(), AddStageDraft{
		Kind: AddStageKindAppend, RequestID: "req-1", Stage: draftN("n", cmd),
	})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != "run_finalizing" {
		t.Fatalf("expected run_finalizing rejection, got %v", err)
	}
}

// TestCommit_SingleOperationForDefinitionAndDependencies — T9.4: the whole insertion (new stage definition +
// the target's additional dependency + the recomputed display order) must land as EXACTLY one durable journal
// record, not two independent writes.
func TestCommit_SingleOperationForDefinitionAndDependencies(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	orch, store := newGraphMutateTestOrchestrator(t, cmd, []flow.Stage{
		autoStage("d1", cmd), autoStage("d2", cmd), autoStage("t", cmd, "d1", "d2"),
	})

	if n := len(store.Snapshot().DynamicStages); n != 0 {
		t.Fatalf("precondition: DynamicStages = %d, want 0", n)
	}
	_, err := orch.CommitAddStage(context.Background(), AddStageDraft{
		Kind: AddStageKindInsertBefore, TargetID: "t", RequestID: "req-1", Stage: draftN("n", cmd),
	})
	if err != nil {
		t.Fatalf("CommitAddStage: %v", err)
	}
	// applyStageGraphAdded appends EXACTLY one DynamicStageAdded entry per stage_graph_added control record
	// (state/stage_graph.go) — this is the authoritative count of how many such records exist, immune to
	// whatever unrelated FSM noise the scheduler wake-up this commit triggers (d1/d2 becoming ready/running)
	// adds to the journal.
	stages := store.Snapshot().DynamicStages
	if len(stages) != 1 {
		t.Fatalf("journal has %d stage_graph_added records, want exactly 1 (single op, not two independent writes): %+v", len(stages), stages)
	}

	rec, ok := store.DynamicStageByOperationID("req-1")
	if !ok {
		t.Fatal("stage_graph_added record not found under its own RequestID")
	}
	if rec.Stage.ID != "n" || rec.ExtraDeps["t"][0] != "n" {
		t.Fatalf("single record missing either the definition or the dependency: %+v", rec)
	}
	if rec.Seq != stages[0].Seq {
		t.Fatalf("DynamicStageByOperationID and the DynamicStages slice disagree on seq: %d vs %d", rec.Seq, stages[0].Seq)
	}
}

// TestCommit_DuplicateOperationIgnored — T9.4: a repeated commit with the SAME RequestID returns the
// ORIGINAL result, never attempting (or succeeding at) a second insertion.
func TestCommit_DuplicateOperationIgnored(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	orch, store := newGraphMutateTestOrchestrator(t, cmd, []flow.Stage{autoStage("a", cmd)})

	res1, err := orch.CommitAddStage(context.Background(), AddStageDraft{
		Kind: AddStageKindAppend, RequestID: "req-1", Stage: draftN("n", cmd),
	})
	if err != nil {
		t.Fatalf("first CommitAddStage: %v", err)
	}
	res2, err := orch.CommitAddStage(context.Background(), AddStageDraft{
		Kind: AddStageKindAppend, RequestID: "req-1", Stage: draftN("n", cmd),
	})
	if err != nil {
		t.Fatalf("repeat CommitAddStage: %v", err)
	}
	if res1.Stage.ID != res2.Stage.ID || res1.GraphRevision != res2.GraphRevision {
		t.Fatalf("repeat returned a different result: %+v vs %+v", res1, res2)
	}
	if len(store.Snapshot().DynamicStages) != 1 {
		t.Fatalf("duplicate RequestID must not insert twice: %+v", store.Snapshot().DynamicStages)
	}
}

// TestCommit_NoLockHeldDuringModelWorkOrPreflight — T9.4: everything before Store.AddStageToGraph (reading
// the registry, planDraft's advisory re-validation) must run with NO lock held — a concurrent, unrelated
// Store operation must not be blocked behind it. o.commitAddStageBarrier fires exactly at that point (after
// planDraft, before AddStageToGraph); while it blocks CommitAddStage's goroutine, an unrelated store.Apply on
// a DIFFERENT stage must complete promptly instead of queueing behind a lock CommitAddStage doesn't hold yet.
func TestCommit_NoLockHeldDuringModelWorkOrPreflight(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	orch, store := newGraphMutateTestOrchestrator(t, cmd, []flow.Stage{autoStage("t", cmd), autoStage("other", cmd)})

	release := make(chan struct{})
	reached := make(chan struct{})
	orch.commitAddStageBarrier = func() {
		close(reached)
		awaitRelease(t, release)
	}

	done := make(chan error, 1)
	go func() {
		_, err := orch.CommitAddStage(context.Background(), AddStageDraft{
			Kind: AddStageKindInsertBefore, TargetID: "t", RequestID: "req-1", Stage: draftN("n", cmd),
		})
		done <- err
	}()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("commitAddStageBarrier never reached")
	}

	// While CommitAddStage's goroutine is parked in the barrier (lock not yet taken), an UNRELATED store
	// operation on a different stage must complete quickly.
	applyDone := make(chan error, 1)
	go func() {
		applyDone <- store.Apply(&state.Transition{StageID: "other", From: state.StatusPending, To: state.StatusReady, Event: "ready"})
	}()
	select {
	case err := <-applyDone:
		if err != nil {
			t.Fatalf("unrelated Apply failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrelated store.Apply blocked while CommitAddStage held the barrier — a lock must be held too early")
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CommitAddStage: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CommitAddStage did not return after barrier release")
	}
}

// TestCommit_SharedAdmissionSerialization — T9.4, the most important test in this stage: AddStageToGraph's
// own precondition check (Expect.TargetPending) and a concurrent scheduler admission decision for the SAME
// target stage share Store's single lock (stage_graph.go's AddStageToGraph doc comment). The ONE guarantee
// this actually buys (and the only guarantee this test asserts — see dev-plan.md's Review decisions for why
// the narrower, fully-deterministic property is what's tested here, not "T can never start before N", which
// activatePrePlannedStage's own doc comment already documents as a narrowed-not-closed window): whichever of
// {a concurrent pending->ready transition for the target} / {this commit} reaches the lock first is strictly
// serialized ahead of the other, and AddStageToGraph can NEVER durably succeed against a target that a
// concurrent admission decision already moved out of pending. Run with -race.
func TestCommit_SharedAdmissionSerialization(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	for trial := 0; trial < 30; trial++ {
		runDir := t.TempDir()
		store, err := state.Open(runDir, []string{"t"})
		if err != nil {
			t.Fatalf("trial %d: state.Open: %v", trial, err)
		}
		cfg := config.Default()
		cfg.Client.Command = cmd
		orch := New(Options{RunDir: runDir, Stages: []flow.Stage{autoStage("t", cmd)}, Store: store, Config: cfg, Prompts: DefaultPrompts()})

		start := make(chan struct{})
		var wg sync.WaitGroup
		var commitErr error
		var commitSeq uint64
		wg.Go(func() {
			<-start
			_, err := orch.CommitAddStage(context.Background(), AddStageDraft{
				Kind: AddStageKindInsertBefore, TargetID: "t", RequestID: "req-1", Stage: draftN("n", cmd),
			})
			commitErr = err
			if err == nil {
				if rec, ok := store.DynamicStageByOperationID("req-1"); ok {
					commitSeq = rec.Seq
				}
			}
		})
		var applyErr error
		wg.Go(func() {
			<-start
			applyErr = store.Apply(&state.Transition{StageID: "t", From: state.StatusPending, To: state.StatusReady, Event: "ready"})
		})
		close(start)
		wg.Wait()

		if applyErr == nil && commitErr == nil {
			// Both succeeded: the ONLY way this is possible is if the commit's durable record predates the
			// admission transition in the journal (AddStageToGraph ran first, found t still pending, and the
			// admission transition landed afterward without AddStageToGraph ever observing it).
			hist, err := store.History()
			if err != nil {
				t.Fatalf("trial %d: History: %v", trial, err)
			}
			var readySeq uint64
			for _, h := range hist {
				if h.StageID == "t" && h.To == state.StatusReady {
					readySeq = h.Seq
				}
			}
			if readySeq != 0 && commitSeq != 0 && readySeq < commitSeq {
				t.Fatalf("trial %d: admission (seq %d) committed BEFORE the stage-graph insertion (seq %d) yet the insertion still succeeded — AddStageToGraph accepted a target that had already left pending", trial, readySeq, commitSeq)
			}
		}
		orch.concurrency.WaitAgents()
		store.Close()
		// Both failing, or exactly one succeeding, are both fine outcomes — the point is the above
		// inconsistency can never happen.
	}
}

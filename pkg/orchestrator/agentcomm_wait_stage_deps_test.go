package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A stage that is still blocked by an unfinished depends_on cannot start before that dependency finishes, so a
// wait of the dependency for the dependent stage can never end: admission refuses it as a wait cycle.

// waitForStageDone is the wait of waiter for stage to be done, with no deadline.
func waitForStageDone(o *Orchestrator, opID string, waiter agentcomm.ParticipantID, stage string) error {
	_, err := o.Wait(context.Background(), WaitRequest{
		OperationID: opID, Waiter: waiter, Actor: state.Actor{Kind: "agent", ID: string(waiter)},
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: stage, Status: string(state.StatusDone)},
	})
	return err
}

func TestWaitStageDeps_DependencyWaitingForItsPendingDependentIsACycle(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "docs", DependsOn: []string{assistantsTestStageID}})
	activateStage(t, o, assistantsTestStageID)

	err := waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs")

	requireRejected(t, err, agentcomm.CodeWaitCycle)
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 0 {
		t.Fatalf("waits = %d, want none registered", n)
	}
}

// The chain impl <- mid <- docs: impl waiting for docs closes the cycle through the intermediate stage.
func TestWaitStageDeps_CycleThroughAnIntermediateStage(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true,
		flow.Stage{ID: "mid", DependsOn: []string{assistantsTestStageID}},
		flow.Stage{ID: "docs", DependsOn: []string{"mid"}})
	activateStage(t, o, assistantsTestStageID)

	requireRejected(t, waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"), agentcomm.CodeWaitCycle)
	requireRejected(t, waitForStageDone(o, "op-impl-waits-mid", spawnStage, "mid"), agentcomm.CodeWaitCycle)
}

// The dependent being blocked by another dependency still blocks it: it cannot start before impl is done either.
func TestWaitStageDeps_DependentBlockedByAnotherDependencyToo(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true,
		flow.Stage{ID: "prep"},
		flow.Stage{ID: "docs", DependsOn: []string{"prep", assistantsTestStageID}})
	activateStage(t, o, assistantsTestStageID)

	requireRejected(t, waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"), agentcomm.CodeWaitCycle)
}

func TestWaitStageDeps_LegitimateWaitsAreAdmitted(t *testing.T) {
	t.Run("the dependent no longer depends on anything unfinished", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "prep"}, flow.Stage{ID: "docs", DependsOn: []string{"prep"}})
		activateStage(t, o, assistantsTestStageID)
		activateStage(t, o, "prep")
		finishStage(t, o, "prep")

		if err := waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"); err != nil {
			t.Fatalf("wait for a stage that only waits for the scheduler: %v", err)
		}
	})
	t.Run("the dependent already started", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "prep"}, flow.Stage{ID: "docs", DependsOn: []string{"prep"}})
		activateStage(t, o, assistantsTestStageID)
		activateStage(t, o, "prep")
		finishStage(t, o, "prep")
		activateStage(t, o, "docs") // started, as only a stage whose dependencies are done can

		if err := waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"); err != nil {
			t.Fatalf("wait for a running stage: %v", err)
		}
	})
	t.Run("a stage that does not depend on the waiter", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "other"})
		activateStage(t, o, assistantsTestStageID)

		if err := waitForStageDone(o, "op-impl-waits-other", spawnStage, "other"); err != nil {
			t.Fatalf("wait for an unrelated pending stage: %v", err)
		}
	})
	t.Run("the dependent waits for its dependency", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "docs", DependsOn: []string{assistantsTestStageID}})
		activateStage(t, o, "docs")

		// docs is running, so it may wait for impl, which is still pending and blocks nothing it needs to start.
		if err := waitForStageDone(o, "op-docs-waits-impl", meshStageParticipantID("docs"), assistantsTestStageID); err != nil {
			t.Fatalf("wait of a stage for its own dependency: %v", err)
		}
	})
}

// The edges come from the runtime registry, not from the flow the run started with: a stage added later with a
// dependency on the waiter closes the same cycle.
func TestWaitStageDeps_UsesTheRuntimeRegistry(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "docs"})
	activateStage(t, o, assistantsTestStageID)
	if err := waitForStageDone(o, "op-before", spawnStage, "docs"); err != nil {
		t.Fatalf("wait before the dependency exists: %v", err)
	}

	o.graph.Replace([]flow.Stage{{ID: assistantsTestStageID, Command: "claude"}, {ID: "docs", DependsOn: []string{assistantsTestStageID}}})

	requireRejected(t, waitForStageDone(o, "op-after", spawnStage, "docs"), agentcomm.CodeWaitCycle)
}

// The decision depends only on the journal and the registry, so a restarted run decides the same.
func TestWaitStageDeps_DecisionSurvivesARestart(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "docs", DependsOn: []string{assistantsTestStageID}})
	activateStage(t, o, assistantsTestStageID)
	requireRejected(t, waitForStageDone(o, "op-before-restart", spawnStage, "docs"), agentcomm.CodeWaitCycle)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, _ := reopenAssistantsTestOrchestrator(t, o)

	requireRejected(t, waitForStageDone(resumed, "op-after-restart", spawnStage, "docs"), agentcomm.CodeWaitCycle)
}

// An eager-planning dependent plans before its dependency is done, but it cannot be done before it: whatever status
// it has reached on the way, the dependency waiting for its completion is the same cycle.
func TestWaitStageDeps_DependentThatPlansEagerlyIsStillACycle(t *testing.T) {
	plan := []bus.FSMEvent{bus.EvStartPlanning, bus.EvPlanReady, bus.EvApprove}
	for i, status := range []state.StageStatus{state.StatusPlanning, state.StatusAwaitingApproval, state.StatusReady} {
		t.Run(string(status), func(t *testing.T) {
			o, _ := newAssistantsTestOrchestrator(t, true, eagerDocs(flow.AgentPlanning, flow.AgentImplementation))
			activateStage(t, o, assistantsTestStageID)
			for _, ev := range plan[:i+1] {
				if _, ok := o.Trigger("docs", ev, bus.GuardCtx{Stage: eagerDocs()}, ""); !ok {
					t.Fatalf("setup: %s refused", ev)
				}
			}

			requireRejected(t, waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"), agentcomm.CodeWaitCycle)
		})
	}
	t.Run("paused before its first activation", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, eagerDocs(flow.AgentPlanning, flow.AgentImplementation))
		activateStage(t, o, assistantsTestStageID)
		if _, ok := o.Trigger("docs", bus.EvPause, bus.GuardCtx{}, "auto_run: false"); !ok {
			t.Fatal("setup: pause refused")
		}

		requireRejected(t, waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"), agentcomm.CodeWaitCycle)
	})
}

// What a planning dependent can still do before its dependency is done stays waitable: answer a request, or — for a
// stage that only plans — be done.
func TestWaitStageDeps_WhatAPlanningDependentCanDoIsAdmitted(t *testing.T) {
	t.Run("a reply to a request", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, eagerDocs(flow.AgentPlanning, flow.AgentImplementation))
		activateStage(t, o, assistantsTestStageID)
		if _, ok := o.Trigger("docs", bus.EvStartPlanning, bus.GuardCtx{Stage: eagerDocs()}, ""); !ok {
			t.Fatal("setup: planning refused")
		}
		sent, err := agentSend(o, "op-ask-docs", string(spawnStage), []string{"stage:docs"}, "which format?")
		if err != nil {
			t.Fatalf("send: %v", err)
		}

		if _, err := waitReplyTo(o, "op-impl-waits-reply", spawnStage, sent.MessageID); err != nil {
			t.Fatalf("wait for the planning stage's reply: %v", err)
		}
	})
	t.Run("a planning-only stage done", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true, eagerDocs(flow.AgentPlanning))
		activateStage(t, o, assistantsTestStageID)
		if _, ok := o.Trigger("docs", bus.EvStartPlanning, bus.GuardCtx{Stage: eagerDocs(flow.AgentPlanning)}, ""); !ok {
			t.Fatal("setup: planning refused")
		}

		// Approving its plan finishes such a stage, whether impl is done or not.
		if err := waitForStageDone(o, "op-impl-waits-docs", spawnStage, "docs"); err != nil {
			t.Fatalf("wait for a planning-only stage: %v", err)
		}
	})
}

// QA waiting for docs to be done blocks nothing while impl waits for nobody, and docs, still planning, has no
// dependency edge of its own. It closes the cycle once impl waits for QA: docs cannot be done before impl, so QA's
// registered wait needs impl too. Only that registered wait carries the dependency; docs itself has no edge to carry it.
func TestWaitStageDeps_RegisteredWaitForADependentOfTheWaiterClosesACycle(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, eagerDocs(flow.AgentPlanning, flow.AgentImplementation))
	activateStage(t, o, assistantsTestStageID)
	if _, ok := o.Trigger("docs", bus.EvStartPlanning, bus.GuardCtx{Stage: eagerDocs()}, ""); !ok {
		t.Fatal("setup: planning refused")
	}
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil }) // QA's turn stays held
	qa := addTestRunAgent(t, o, "op-qa", nil).ParticipantID
	thread := openThread(t, o, "op-th", 0, spawnStage, qa)
	if err := waitForStageDone(o, "op-qa-waits-docs", qa, "docs"); err != nil {
		t.Fatalf("QA's wait for docs blocks nothing yet, but was refused: %v", err)
	}
	sent, err := o.Send(context.Background(), SendRequest{
		OperationID: "op-impl-asks-qa", ThreadID: thread, Sender: spawnStage, Recipients: []agentcomm.ParticipantID{qa},
		Kind: agentcomm.MessageKindRequest, Body: "review", Actor: spawnStageActor,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	_, err = waitReplyTo(o, "op-impl-waits-qa", spawnStage, sent.MessageID)

	requireRejected(t, err, agentcomm.CodeWaitCycle)
	if n := len(pendingWaitsOf(store.Snapshot().Assistants.Mesh, spawnStage)); n != 0 {
		t.Fatalf("pending waits of impl = %d, want none registered", n)
	}
}

// eagerDocs is stage docs, planning before impl, the stage it depends on, is done.
func eagerDocs(agents ...flow.AgentType) flow.Stage {
	return flow.Stage{ID: "docs", DependsOn: []string{assistantsTestStageID}, EagerPlanning: true, Agents: agents}
}

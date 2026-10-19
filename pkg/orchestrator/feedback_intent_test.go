package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

const feedbackIntentTestStageID = "impl"

// newFeedbackIntentTestOrchestrator builds a real (non-mocked) Store + graph + concurrency Manager with
// AssistantsEnabled, plus noopPlanningRunner (approve_test.go) so a spawned runPlanningWithFeedback completes
// harmlessly instead of trying to exec a real agent.
func newFeedbackIntentTestOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	stages := []flow.Stage{{ID: feedbackIntentTestStageID, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}}
	store, err := state.Open(dir, []string{feedbackIntentTestStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cb := bus.NewCriticalBus(16)
	o := &Orchestrator{
		opts: Options{
			RunDir: dir, Stages: stages, Store: store, RunID: "run1",
			AssistantsEnabled: true, Runner: noopPlanningRunner{},
		},
		graph:       graph.NewStore(graph.NewRegistry(stages)),
		runner:      noopPlanningRunner{},
		ui:          bus.NewUIBus(),
		critical:    cb,
		fsm:         bus.NewFSM(store),
		concurrency: concurrency.New(cb, stages, "claude", 0, nil),
	}
	return o, store
}

// seedAcceptedFeedbackProposal creates a pending ProposalFeedback proposal targeting stageID with the given
// expectedGeneration and feedback text, then accepts it — returning the proposal id and intent id.
func seedAcceptedFeedbackProposal(ctx context.Context, t *testing.T, o *Orchestrator, stageID, feedback string, expectedGeneration uint64) (proposalID string) {
	t.Helper()
	payload, err := json.Marshal(FeedbackProposalPayload{Feedback: feedback})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	proposalID, err = o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "create-" + stageID + "-" + feedback, StageID: stageID, Type: assistants.ProposalFeedback,
		Title: "test proposal", Payload: payload, Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "qaEngineer"}},
		ExpectedStageGeneration: expectedGeneration,
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	return proposalID
}

func waitForProposalStatus(t *testing.T, o *Orchestrator, id string, want assistants.ProposalStatus, timeout time.Duration) assistants.Proposal {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p, ok := o.Proposal(id); ok && p.Status == want {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	p, _ := o.Proposal(id)
	t.Fatalf("proposal %s status = %s, want %s", id, p.Status, want)
	return assistants.Proposal{}
}

func feedbackMdPath(o *Orchestrator, stageID string) string {
	return filepath.Join(o.opts.RunDir, stageID, "feedback.md")
}

// ---- T6.5: per-parent-status table ----

func TestFeedbackIntent_ParentStatus_Running(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID) // pending -> ready -> running
	gen := store.StageGeneration(feedbackIntentTestStageID)

	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "please fix the retry loop", gen)
	if err := o.AcceptProposal(ctx, proposalID, "accept-running", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}

	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)
	rs := store.Snapshot()
	if rs.Stages[feedbackIntentTestStageID].Status != state.StatusRevising {
		t.Fatalf("stage status = %s, want revising (existing durable Revise with interrupt)", rs.Stages[feedbackIntentTestStageID].Status)
	}
	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if !containsAll(string(body), "please fix the retry loop", "qaEngineer") {
		t.Fatalf("feedback.md does not carry the forwarded feedback+provenance: %s", body)
	}
}

// A Pause that lands after delivery picked the running path wins: the Revise applies nothing, so the intent
// is NOT marked delivered and no feedback is written; it stays accepted for the stage's next status.
func TestFeedbackIntent_PauseRacingDeliveryLeavesIntentPending(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)
	paused := false
	o.feedbackDeliveryDecidedHook = func(stageID string) {
		if !paused {
			paused = true
			o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "user pause")
		}
	}

	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "please fix the retry loop", gen)
	if err := o.AcceptProposal(ctx, proposalID, "accept-racing", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}

	if got := o.currentStatus(feedbackIntentTestStageID); got != state.StatusPaused {
		t.Fatalf("status = %v, want paused: the pause must not be undone", got)
	}
	if p, _ := o.Proposal(proposalID); p.Status != assistants.ProposalAccepted {
		t.Fatalf("proposal status = %s, want still accepted: nothing was delivered", p.Status)
	}
	for _, fi := range store.Snapshot().Assistants.FeedbackIntents {
		if fi.Status != state.FeedbackIntentAccepted {
			t.Fatalf("intent %s status = %s, want accepted", fi.ID, fi.Status)
		}
	}
	if _, err := os.Stat(feedbackMdPath(o, feedbackIntentTestStageID)); !os.IsNotExist(err) {
		t.Fatalf("no feedback may be written for an undelivered intent (stat err = %v)", err)
	}
}

func TestFeedbackIntent_ParentStatus_AwaitingApproval(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	o.Trigger(feedbackIntentTestStageID, bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.Trigger(feedbackIntentTestStageID, bus.EvPlanReady, bus.GuardCtx{}, "")
	stageDir := filepath.Join(o.opts.RunDir, feedbackIntentTestStageID)
	_ = os.MkdirAll(stageDir, 0755)
	_ = os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644)
	gen := store.StageGeneration(feedbackIntentTestStageID) // 0: never actually ran yet

	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "revise the plan", gen)
	if err := o.AcceptProposal(ctx, proposalID, "accept-awaiting-approval", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)
	o.concurrency.WaitAgents()

	rs := store.Snapshot()
	if rs.Stages[feedbackIntentTestStageID].Status == state.StatusAwaitingApproval {
		t.Fatal("stage must have moved out of awaiting_approval via the existing plan-revise path")
	}
}

func TestFeedbackIntent_ParentStatus_AwaitingAssistants(t *testing.T) {
	// Этап 7 (T7.8): awaiting_assistants means the main executor's own process has ALREADY exited — there is
	// nothing to interrupt, so delivery must be an explicit start-with-feedback action, never a plain defer.
	if plan := feedbackDeliveryPlanForStatus(state.StatusAwaitingAssistants); plan != deliverStartWithFeedback {
		t.Fatalf("awaiting_assistants must start a fresh execution pass with feedback, got plan=%d, want deliverStartWithFeedback", plan)
	}
}

func TestFeedbackIntent_ParentStatus_Pending(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	gen := store.StageGeneration(feedbackIntentTestStageID)

	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "consider X before starting", gen)
	if err := o.AcceptProposal(ctx, proposalID, "accept-pending", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)

	note := state.LoadPreNote(filepath.Join(o.opts.RunDir, feedbackIntentTestStageID))
	if !containsAll(note, "consider X before starting") {
		t.Fatalf("pending stage must get a pre-note, not a masked Revise: %q", note)
	}
	rs := store.Snapshot()
	if rs.Stages[feedbackIntentTestStageID].Status != state.StatusPending {
		t.Fatalf("pending stage must stay pending — a pre-note is not an FSM transition, got %s", rs.Stages[feedbackIntentTestStageID].Status)
	}
}

func TestFeedbackIntent_ParentStatus_PlanningRevisingRetrying(t *testing.T) {
	for _, tc := range []struct {
		name string
		to   func(o *Orchestrator)
	}{
		{"planning", func(o *Orchestrator) { o.Trigger(feedbackIntentTestStageID, bus.EvStartPlanning, bus.GuardCtx{}, "") }},
		{"revising", func(o *Orchestrator) {
			activateStage(t, o, feedbackIntentTestStageID)
			o.Trigger(feedbackIntentTestStageID, bus.EvRevise, bus.GuardCtx{}, "")
		}},
		{"retrying", func(o *Orchestrator) {
			activateStage(t, o, feedbackIntentTestStageID)
			o.Trigger(feedbackIntentTestStageID, bus.EvScheduleRetry, bus.GuardCtx{}, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newFeedbackIntentTestOrchestrator(t)
			ctx := context.Background()
			tc.to(o)
			gen := store.StageGeneration(feedbackIntentTestStageID)
			statusBefore := o.currentStatus(feedbackIntentTestStageID)

			proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "note for later", gen)
			if err := o.AcceptProposal(ctx, proposalID, "accept-"+tc.name, state.Actor{Kind: "user"}); err != nil {
				t.Fatalf("AcceptProposal: %v", err)
			}
			// Must NOT blindly re-interrupt — stays pending delivery, status unchanged.
			time.Sleep(20 * time.Millisecond)
			p, _ := o.Proposal(proposalID)
			if p.Status != assistants.ProposalAccepted {
				t.Fatalf("must defer, not deliver immediately while %s — proposal status = %s", tc.name, p.Status)
			}
			if o.currentStatus(feedbackIntentTestStageID) != statusBefore {
				t.Fatalf("stage status must not change just from accepting a proposal while %s", tc.name)
			}
		})
	}
}

func TestFeedbackIntent_ParentStatus_PausedAwaitingUserInputHookFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		to   func(o *Orchestrator)
	}{
		{"paused", func(o *Orchestrator) {
			activateStage(t, o, feedbackIntentTestStageID)
			o.Trigger(feedbackIntentTestStageID, bus.EvPause, bus.GuardCtx{}, "")
		}},
		{"awaiting_user_input", func(o *Orchestrator) {
			activateStage(t, o, feedbackIntentTestStageID)
			o.Trigger(feedbackIntentTestStageID, bus.EvAskUser, bus.GuardCtx{}, "")
		}},
		{"hook_failed", func(o *Orchestrator) {
			activateStage(t, o, feedbackIntentTestStageID)
			o.Trigger(feedbackIntentTestStageID, bus.EvHookFailed, bus.GuardCtx{}, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newFeedbackIntentTestOrchestrator(t)
			ctx := context.Background()
			tc.to(o)
			gen := store.StageGeneration(feedbackIntentTestStageID)
			statusBefore := o.currentStatus(feedbackIntentTestStageID)

			proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "note", gen)
			if err := o.AcceptProposal(ctx, proposalID, "accept-"+tc.name, state.Actor{Kind: "user"}); err != nil {
				t.Fatalf("AcceptProposal: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			if o.currentStatus(feedbackIntentTestStageID) != statusBefore {
				t.Fatalf("must not un-pause / bypass waiting for the user while %s", tc.name)
			}
			p, _ := o.Proposal(proposalID)
			if p.Status != assistants.ProposalAccepted {
				t.Fatalf("delivery must stay deferred while %s, got proposal status %s", tc.name, p.Status)
			}
		})
	}
}

func TestFeedbackIntent_ParentStatus_DoneFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		to   func(o *Orchestrator)
	}{
		{"done", func(o *Orchestrator) {
			activateStage(t, o, feedbackIntentTestStageID)
			o.Trigger(feedbackIntentTestStageID, bus.EvComplete, bus.GuardCtx{}, "")
		}},
		{"failed", func(o *Orchestrator) {
			activateStage(t, o, feedbackIntentTestStageID)
			o.Trigger(feedbackIntentTestStageID, bus.EvFail, bus.GuardCtx{}, "boom")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newFeedbackIntentTestOrchestrator(t)
			ctx := context.Background()
			tc.to(o)
			gen := store.StageGeneration(feedbackIntentTestStageID)

			proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "note", gen)
			if err := o.AcceptProposal(ctx, proposalID, "accept-"+tc.name, state.Actor{Kind: "user"}); err != nil {
				t.Fatalf("AcceptProposal: %v", err)
			}
			waitForProposalStatus(t, o, proposalID, assistants.ProposalDeliveryFailed, time.Second)
			wantStatus := state.StatusDone
			if tc.name == "failed" {
				wantStatus = state.StatusFailed
			}
			if o.currentStatus(feedbackIntentTestStageID) != wantStatus {
				t.Fatalf("must not silently reopen a %s stage", tc.name)
			}
		})
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}

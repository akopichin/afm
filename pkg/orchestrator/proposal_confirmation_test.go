package orchestrator

// Этап 10 (T10.4): confirmations and re-preview-on-staleness, plus a cap on assistants' own unconfirmed
// proposals so a looping/compromised helper cannot flood a human's confirmation queue.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// TestConfirmedDraftStale_ShowsNewPreviewNotHiddenApply — when the graph moved between a proposal's creation
// and its (now-stale) acceptance, AcceptProposal must neither silently apply the STALE draft nor silently
// apply some OTHER draft in its place: delivery is cleanly rejected (stale_revision), the proposed stage is
// NOT installed, and a caller can still obtain a FRESH preview of the exact same underlying stage draft (the
// same re-preview contract T9.6's manual dialog already relies on — a client re-POSTs to
// /api/run/graph/preview after a 409, it is not handed a silently-updated apply).
func TestConfirmedDraftStale_ShowsNewPreviewNotHiddenApply(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, _ := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
	ctx := context.Background()

	revAtProposalTime := o.reg().Revision()
	payload, err := json.Marshal(AddStageDraft{
		Kind: AddStageKindAppend, Stage: graph.StageDraft{ID: "proposed-stage", Command: cmd, Prompt: "x"},
		ExpectedRevision: revAtProposalTime,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	propID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-stale-propose", StageID: "impl", Type: assistants.ProposalAddStage, Title: "add a stage",
		Payload: payload, Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "planner"}},
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	// The graph moves on BEFORE the human gets to accepting the proposal above.
	if _, err := o.CommitAddStage(ctx, AddStageDraft{Kind: AddStageKindAppend, RequestID: "concurrent-commit", Stage: graph.StageDraft{ID: "other-stage", Command: cmd, Prompt: "y"}}); err != nil {
		t.Fatalf("CommitAddStage (concurrent change): %v", err)
	}

	// Accepting the now-stale proposal must be rejected, NOT silently applied.
	acceptErr := o.AcceptProposal(ctx, propID, "accept-stale", state.Actor{Kind: "user"})
	var ve *graph.ValidationError
	if !errors.As(acceptErr, &ve) || ve.Code != "stale_revision" {
		t.Fatalf("AcceptProposal on a stale add_stage proposal: err=%v, want *graph.ValidationError{Code:stale_revision}", acceptErr)
	}
	if _, ok := o.reg().StageByID("proposed-stage"); ok {
		t.Fatal("a stale proposal must never be silently applied")
	}

	// A FRESH preview of the SAME underlying stage draft (no stale ExpectedRevision pinned) must still be
	// obtainable and reflect the CURRENT graph — proving the system shows the user something NEW, not a dead
	// end and not a hidden apply.
	fresh, err := o.PreviewAddStage(AddStageDraft{Kind: AddStageKindAppend, Stage: graph.StageDraft{ID: "proposed-stage", Command: cmd, Prompt: "x"}})
	if err != nil {
		t.Fatalf("PreviewAddStage (fresh, no revision pin): %v", err)
	}
	if fresh.GraphRevision != o.reg().Revision() {
		t.Fatalf("fresh preview's GraphRevision = %d, want the CURRENT registry revision %d", fresh.GraphRevision, o.reg().Revision())
	}
	foundOther := false
	for _, dep := range fresh.Plan.Order {
		if dep == "other-stage" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatalf("fresh preview's Order does not reflect the concurrent change: %v", fresh.Plan.Order)
	}
}

// TestSelfProposals_DoNotCreateRecursiveAvalanche — Этап 10's maxPendingSelfProposals cap (proposal.go):
// once that many assistant-authored proposals are pending (unconfirmed) on the run, the next one is rejected
// outright rather than being silently queued forever — a looping/compromised helper cannot flood a human's
// confirmation queue. A human-authored proposal (no assistant author) is never counted against the cap.
func TestSelfProposals_DoNotCreateRecursiveAvalanche(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, _ := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{autoStage("impl", cmd)})
	ctx := context.Background()

	selfPayload, _ := json.Marshal(FeedbackProposalPayload{Feedback: "x"})
	for i := 0; i < maxPendingSelfProposals; i++ {
		_, err := o.CreateProposal(ctx, CreateProposalRequest{
			OperationID: fmt.Sprintf("op-avalanche-%d", i), StageID: "impl", Type: assistants.ProposalFeedback,
			Title: "idea", Payload: selfPayload,
			Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "planner"}},
		})
		if err != nil {
			t.Fatalf("CreateProposal #%d (within cap): %v", i, err)
		}
	}

	_, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-avalanche-over", StageID: "impl", Type: assistants.ProposalFeedback, Title: "idea",
		Payload: selfPayload, Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "planner"}},
	})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeBudgetExceeded {
		t.Fatalf("CreateProposal beyond the cap: err=%v, want CodeBudgetExceeded", err)
	}

	// A human-authored proposal is not self-sourced and must not be blocked by the assistants' own cap.
	humanID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-human", StageID: "impl", Type: assistants.ProposalFeedback, Title: "human idea",
		Payload: selfPayload, Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorHuman, ID: "u1"}},
	})
	if err != nil {
		t.Fatalf("CreateProposal (human-authored, cap full): %v", err)
	}
	if humanID == "" {
		t.Fatal("expected a non-empty proposal id")
	}
}

// TestConfirmation_RequiredForNewRootOrSiblingTask — §12.5: BOTH shapes of ProposalAttachAssistant (a NEW
// top-level/root task, Proposal.TaskID empty; and a SIBLING/child task under an existing one, Proposal.TaskID
// set) require an explicit human AcceptProposal — CreateProposal alone never creates either.
func TestConfirmation_RequiredForNewRootOrSiblingTask(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, store := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{{ID: assistantsTestStageIDForProposals, Command: "claude"}})
	ctx := context.Background()
	// The stage is left PENDING (not activated): Attach's own documented behavior for a pending parent stage
	// is "task_created alone, stays waiting_parent" — nothing gets scheduled, so this test needs no fake
	// runHelperAgent and has no race against the parent task completing before the sibling is delegated.

	attachPayload, _ := json.Marshal(AttachAssistantProposalPayload{ProfileID: "reviewer", Title: "t", Instruction: "x"})

	// New ROOT task: Proposal.TaskID empty.
	rootPropID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-root-propose", StageID: assistantsTestStageIDForProposals, Type: assistants.ProposalAttachAssistant,
		Title: "attach a root helper", Payload: attachPayload,
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: "planner"}},
	})
	if err != nil {
		t.Fatalf("CreateProposal (root): %v", err)
	}
	if tasks := store.Snapshot().Assistants.Tasks; len(tasks) != 0 {
		t.Fatalf("CreateProposal (root attach) must not create any task yet, got %d", len(tasks))
	}
	if err := o.AcceptProposal(ctx, rootPropID, "accept-root", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal (root): %v", err)
	}
	rootTasks := store.Snapshot().Assistants.Tasks
	if len(rootTasks) != 1 {
		t.Fatalf("AcceptProposal (root attach) must create exactly one task, got %d", len(rootTasks))
	}
	var parentID assistants.TaskID
	for id := range rootTasks {
		parentID = id
	}

	// admitChildren (unlike Attach) always queues+schedules a new child immediately, regardless of the root
	// stage's own FSM status (it assumes it is called from the parent's OWN live turn) — a fake runHelperAgent
	// stands in for the real subprocess the (never-started) "reviewer" profile command would otherwise be.
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	// SIBLING/child task: Proposal.TaskID set to the task just created above.
	siblingPropID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-sibling-propose", StageID: assistantsTestStageIDForProposals, TaskID: string(parentID),
		Type: assistants.ProposalAttachAssistant, Title: "delegate a sibling", Payload: attachPayload,
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: string(parentID)}},
	})
	if err != nil {
		t.Fatalf("CreateProposal (sibling): %v", err)
	}
	if tasks := store.Snapshot().Assistants.Tasks; len(tasks) != 1 {
		t.Fatalf("CreateProposal (sibling attach) must not create any NEW task yet, got %d total", len(tasks))
	}
	if err := o.AcceptProposal(ctx, siblingPropID, "accept-sibling", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal (sibling): %v", err)
	}
	if tasks := store.Snapshot().Assistants.Tasks; len(tasks) != 2 {
		t.Fatalf("AcceptProposal (sibling attach) must create exactly one NEW task, got %d total", len(tasks))
	}
}

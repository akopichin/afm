package orchestrator

// Этап 10 (T10.3): a stopped/cancelled task can no longer have its own task tree grown out from under it by
// a LATER confirmation of something it proposed earlier. TestStaleTurn_ActionRejected (the side-agent TURN
// half of T10.3) lives at the HTTP integration level (pkg/server/action_drafts_test.go) — turns are a
// pkg/sideagent concept this package does not model. TestActionFromToolOutput_NotExecuted (the integration
// repeat of T4.4's own unit coverage) lives there too.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// TestCancelledTask_ActionRejected proves that a ProposalAttachAssistant targeting an existing task
// (Proposal.TaskID set — "delegate a child to this task") is rejected once that task has been CANCELLED in
// the meantime, through admitChildren's EXISTING "ancestor must still be active" check (assistants_delegate.go's
// `parent.Status.Terminal()`) — not a second, hand-rolled liveness check invented for proposal delivery.
func TestCancelledTask_ActionRejected(t *testing.T) {
	cmd := newSafeAgentCommand(t)
	o, store := newProposalDeliveryTestOrchestrator(t, cmd, []flow.Stage{{ID: assistantsTestStageIDForProposals, Command: "claude"}})
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageIDForProposals)

	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		<-release
		return passResultOutcome(), nil
	}
	t.Cleanup(func() { o.concurrency.WaitAgents() })

	parentID, err := o.Attach(ctx, AttachRequest{OperationID: "op-parent-cancel", StageID: assistantsTestStageIDForProposals, ProfileID: "reviewer", Instruction: "x", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	waitForTaskStatus(t, store, parentID, assistants.StatusRunning, 2*time.Second)

	payload, _ := json.Marshal(AttachAssistantProposalPayload{ProfileID: "reviewer", Title: "child", Instruction: "do it"})
	propID, err := o.CreateProposal(ctx, CreateProposalRequest{
		OperationID: "op-cancel-proposal", StageID: assistantsTestStageIDForProposals, TaskID: string(parentID),
		Type: assistants.ProposalAttachAssistant, Title: "delegate a child", Payload: payload,
		Authors: []assistants.MessageAuthor{{Kind: assistants.AuthorAssistant, ID: string(parentID)}},
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	if err := o.CancelTask(ctx, parentID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	close(release)
	waitForTaskStatus(t, store, parentID, assistants.StatusCancelled, 2*time.Second)

	err = o.AcceptProposal(ctx, propID, "accept-after-cancel", state.Actor{Kind: "user"})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeAncestorInactive {
		t.Fatalf("AcceptProposal(attach_assistant, parent cancelled): err=%v, want CodeAncestorInactive (admitChildren's existing check)", err)
	}
}

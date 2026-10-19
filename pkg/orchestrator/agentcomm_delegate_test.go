package orchestrator

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// TestDelegate_ResultDeliveredThroughSharedMeshInbox is dev-plan.md's T6.5 "single inbox" test: a delegate
// child's result and an ordinary peer-to-peer mesh reply must BOTH surface through the exact same
// Service.ReadInbox call for the same recipient, proving there is one "a participant learns it has new
// input" primitive — not a bespoke second one for the delegation tree. See childResultMeshEffects'
// (assistants_delegate.go) own doc comment for the full reasoning: collectChildResultsJSON (the resumed
// parent's own prompt section) is left untouched; this test is about the MESH inbox specifically.
func TestDelegate_ResultDeliveredThroughSharedMeshInbox(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var calls atomic.Int32
	var resumedPrompt string
	releaseChild := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		switch calls.Add(1) {
		case 1:
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		case 2:
			awaitRelease(t, releaseChild) // the child holds until the parent got an ordinary peer message (part 2 below)
			return passResultOutcome(), nil
		default:
			resumedPrompt = prompt
			return passResultOutcome(), nil
		}
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	parentParticipant := meshHelperParticipantID(parentID)
	childParticipant := meshHelperParticipantID(childID)

	// 2) An ordinary peer message to the (still live) parent — the same Send any helper/human uses — must land
	// in the SAME inbox the delegate mirror (part 1) lands in, never a second mechanism.
	thread, err := o.OpenThread(context.Background(), OpenThreadRequest{
		OperationID: "op-thread-peer", Scope: agentcomm.ScopeStage,
		Members: []agentcomm.ParticipantID{meshUserParticipantID, parentParticipant},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(context.Background(), SendRequest{
		OperationID: "op-send-peer", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{parentParticipant}, Kind: agentcomm.MessageKindMessage,
		Body: "a plain peer reply", Actor: state.Actor{Kind: actorKindUser},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	close(releaseChild)
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)

	// 1) The child's result reached the parent through the mesh's one read primitive — Service.ReadInbox —
	// next to the ordinary peer message, not only through the bespoke collectChildResultsJSON read.
	page, err := o.ReadInbox(context.Background(), ReadInboxRequest{Participant: parentParticipant})
	if err != nil {
		t.Fatalf("ReadInbox: %v", err)
	}
	foundChild, foundPeer := false, false
	for _, m := range page.Messages {
		foundChild = foundChild || m.Sender == childParticipant
		foundPeer = foundPeer || m.Body == "a plain peer reply"
	}
	if !foundChild || !foundPeer {
		t.Fatalf("parent's inbox (via ReadInbox) = %+v, want both the child's result and the peer message", page.Messages)
	}

	// 3) The SAME prompt-injection function that would show the parent an ordinary peer reply
	// (agentCommInboxFor/agentCommInboxForTask — the "helper path" buildTaskContext always uses) is also what
	// already showed it the child's result on its resumed turn — one inbox implementation feeding both, not a
	// second bespoke one for delegation.
	if resumedPrompt == "" {
		t.Fatal("the parent's resumed turn never ran")
	}
	if !strings.Contains(resumedPrompt, "<inbox>") || !strings.Contains(resumedPrompt, string(childParticipant)) {
		t.Fatalf("resumed parent prompt does not show the child's result via the <inbox> block: %q", resumedPrompt)
	}
}

// TestDelegate_StaleChildResultNotMirroredToParentInbox: a child whose ParentGeneration no longer matches
// its parent's CURRENT TaskGeneration (the parent moved on to a new attempt — RetryTask's own
// task_generation_bumped effect, applied directly here without its cascade-cancel precondition dance, so
// THIS specific child is left running under the stale, superseded generation) must never have its outcome
// injected into the parent's LIVE mesh inbox — currentChildren's own "invisible once superseded" rule
// (assistants_delegate.go's doc comment) applies to this mirror exactly as it already does to
// collectChildResultsJSON/requiredChildOpen/tryResumeParent.
func TestDelegate_StaleChildResultNotMirroredToParentInbox(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	release := make(chan struct{})
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusWaitingChildren, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]
	waitForTaskStatus(t, store, childID, assistants.StatusRunning, 2*time.Second)

	bumpEff := state.Effect{Kind: "task_generation_bumped", Target: state.Target{TaskID: string(parentID)}}
	if _, err := store.ApplyRunOperation(state.Operation{ID: "test-bump-generation", Kind: "test_bump_generation", Effects: []state.Effect{bumpEff}}, nil); err != nil {
		t.Fatalf("bump parent generation: %v", err)
	}

	close(release)
	waitForTaskStatus(t, store, childID, assistants.StatusCompleted, 2*time.Second)

	page, err := o.ReadInbox(context.Background(), ReadInboxRequest{Participant: meshHelperParticipantID(parentID)})
	if err != nil {
		t.Fatalf("ReadInbox: %v", err)
	}
	childParticipant := meshHelperParticipantID(childID)
	for _, m := range page.Messages {
		if m.Sender == childParticipant {
			t.Fatalf("parent's inbox = %+v, want NO message from the now-stale (superseded-generation) child %s", page.Messages, childID)
		}
	}
}

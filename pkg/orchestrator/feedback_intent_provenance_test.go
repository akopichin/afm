package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestFeedUI_ShowsUserForwardedProposal asserts the feed-facing data for a delivered proposal is explicitly
// tagged as a FORWARDED proposal (origin + participants) — never collapsed into a shape indistinguishable
// from the user's own typed words.
func TestFeedUI_ShowsUserForwardedProposal(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID)
	gen := store.StageGeneration(feedbackIntentTestStageID)

	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "tighten the retry bound", gen)
	if err := o.AcceptProposal(ctx, proposalID, "accept-forwarded", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDelivered, time.Second)

	var found *rawNotice
	for _, n := range readNotices(t, o.opts.RunDir) {
		if n.Type == "feedback_delivered" {
			e := n
			found = &e
		}
	}
	if found == nil {
		t.Fatal("expected a feedback_delivered notice")
	}
	if found.Data["origin"] != "assistant_proposal" {
		t.Fatalf("notice must be tagged origin=assistant_proposal, got %+v", found.Data)
	}
	participants, _ := found.Data["participants"].([]any)
	if len(participants) == 0 || participants[0] != "qaEngineer" {
		t.Fatalf("notice must name the forwarding participant(s), got %+v", found.Data)
	}

	// The feedback.md body itself must carry a visible forwarded-provenance header, not raw indistinguishable
	// text.
	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	if !strings.Contains(string(body), "forwarded a proposal") || !strings.Contains(string(body), "@qaEngineer") {
		t.Fatalf("feedback.md must honestly mark this as a forwarded proposal, got: %s", body)
	}
}

// TestDeferredIntent_RevalidatedBeforeExecution: an intent accepted while the stage is mid-attempt
// (e.g. revising) must NOT apply its advice once the stage moves on to a genuinely DIFFERENT attempt — the
// generation check must run again right before delivery, not just once at accept time.
func TestDeferredIntent_RevalidatedBeforeExecution(t *testing.T) {
	o, store := newFeedbackIntentTestOrchestrator(t)
	ctx := context.Background()
	activateStage(t, o, feedbackIntentTestStageID) // running, generation G1
	genAtAccept := store.StageGeneration(feedbackIntentTestStageID)

	// Move to revising (deferred bucket) and accept a proposal computed against G1.
	o.Trigger(feedbackIntentTestStageID, bus.EvRevise, bus.GuardCtx{}, "")
	proposalID := seedAcceptedFeedbackProposal(ctx, t, o, feedbackIntentTestStageID, "advice for attempt 1", genAtAccept)
	if err := o.AcceptProposal(ctx, proposalID, "accept-deferred", state.Actor{Kind: "user"}); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	// Still deferred — revising is not interruptible.
	if p, _ := o.Proposal(proposalID); p.Status != assistants.ProposalAccepted {
		t.Fatalf("expected the proposal to still be accepted (deferred) while revising, got %s", p.Status)
	}

	// The stage moves on to a genuinely NEW attempt (revising -> running -> a fresh start_run bumps the
	// generation), WITHOUT the deferred intent ever having been delivered.
	o.Trigger(feedbackIntentTestStageID, bus.EvStartRun, bus.GuardCtx{}, "")
	newGen := store.StageGeneration(feedbackIntentTestStageID)
	if newGen == genAtAccept {
		t.Fatal("test setup: expected the fresh start_run to bump the stage generation")
	}

	// The deferred intent is now stale — it must be rejected, not silently delivered against the new attempt.
	waitForProposalStatus(t, o, proposalID, assistants.ProposalDeliveryFailed, time.Second)
	body, err := os.ReadFile(feedbackMdPath(o, feedbackIntentTestStageID))
	if err == nil && strings.Contains(string(body), "advice for attempt 1") {
		t.Fatalf("stale advice must never reach the NEW attempt's feedback.md: %s", body)
	}
}

package assistants

import "testing"

func TestProposal_StaleWhenGenerationChanged(t *testing.T) {
	p := Proposal{
		ID: "p-1", StageID: "build", Type: ProposalFeedback, Status: ProposalPending,
		ExpectedStageGeneration: 3,
	}
	if err := p.CheckGeneration(3); err != nil {
		t.Fatalf("matching generation must be accepted: %v", err)
	}
	if err := p.CheckGeneration(4); err == nil {
		t.Fatal("a proposal whose expected generation no longer matches the stage's current one must be rejected as stale")
	}
}

func TestProposal_DeliveredMeansAcceptedCorrection(t *testing.T) {
	// "delivered" is a documented, narrow claim: AFM durably committed the feedback into the stage's real
	// execution path. It is NOT a claim that the underlying code was actually fixed — there is no field here
	// (and must never be one) that asserts the correction succeeded.
	p := Proposal{ID: "p-1", Status: ProposalDelivered}
	if !p.Status.Terminal() {
		t.Fatal("delivered must be a terminal status")
	}
	if p.Status != ProposalDelivered {
		t.Fatal("sanity")
	}
	// The type must not expose any "fixed"/"resolved"/"verified" concept — Delivered is as far as this model
	// goes.
	if p.CanTransition(ProposalStatus("fixed")) {
		t.Fatal("there must be no such thing as a 'fixed' proposal status")
	}
}

func TestProposal_NoFixedStatusAfterHttp200(t *testing.T) {
	p := Proposal{ID: "p-1", Status: ProposalPending}
	// Accepting over HTTP only ever legally lands on 'accepted' — never jumps straight to a status implying
	// the work is done.
	if !p.CanTransition(ProposalAccepted) {
		t.Fatal("pending -> accepted must be legal")
	}
	if p.CanTransition(ProposalDelivered) {
		t.Fatal("accept (an HTTP 200) must never directly produce 'delivered' — delivery is a separate, later, durable step")
	}
}

func TestProposal_DismissedDoesNotAffectMainAgent(t *testing.T) {
	p := Proposal{ID: "p-1", Status: ProposalPending}
	if !p.CanTransition(ProposalDismissed) {
		t.Fatal("pending -> dismissed must be legal")
	}
	p.Status = ProposalDismissed
	if !p.Status.Terminal() {
		t.Fatal("dismissed must be terminal")
	}
	// Dismissed must never be able to reach delivered/queued/accepted — there is no path back into the
	// delivery pipeline once dismissed.
	for _, to := range []ProposalStatus{ProposalAccepted, ProposalQueued, ProposalDelivered} {
		if p.CanTransition(to) {
			t.Fatalf("dismissed must not transition to %s", to)
		}
	}
}

func TestProposalLifecycle_FullHappyPath(t *testing.T) {
	p := Proposal{ID: "p-1", Status: ProposalPending}
	steps := []ProposalStatus{ProposalAccepted, ProposalQueued, ProposalDelivered}
	for _, to := range steps {
		if !p.CanTransition(to) {
			t.Fatalf("%s -> %s must be legal", p.Status, to)
		}
		p.Status = to
	}
}

func TestProposalLifecycle_DeliveryFailedIsReachableFromQueued(t *testing.T) {
	p := Proposal{ID: "p-1", Status: ProposalQueued}
	if !p.CanTransition(ProposalDeliveryFailed) {
		t.Fatal("queued -> delivery_failed must be legal")
	}
}

package assistants

import (
	"errors"
	"testing"
)

func sampleDiscussion() []DiscussionMessage {
	return []DiscussionMessage{
		{ID: "m-1", Round: RoundIndependent, Author: MessageAuthor{Kind: AuthorAssistant, ID: "qa"},
			Evidence: []string{"f-1"}, Result: &Result{Verdict: VerdictNeedsChanges, Findings: []Finding{{ID: "f-1", Detail: "retry loop is unbounded"}}}},
		{ID: "m-2", Round: RoundIndependent, Author: MessageAuthor{Kind: AuthorAssistant, ID: "sec"},
			Result: &Result{Verdict: VerdictPass}},
		{ID: "m-3", Round: RoundDiscussion, Author: MessageAuthor{Kind: AuthorAssistant, ID: "qa"},
			ParentMessageID: "m-1", Body: "still unbounded after re-check", Evidence: []string{"f-1"}},
	}
}

func TestSynthesis_ProducesProposalOrExplicitNoGrounds(t *testing.T) {
	input := SynthesisInput{Messages: sampleDiscussion()}

	proposalOutcome := SynthesisOutcome{Proposal: &Proposal{
		ID: "p-1", StageID: "build", Type: ProposalFeedback, Title: "bound the retry loop",
		SourceMessageIDs: []string{"m-1", "m-3"}, SourceFindingIDs: []string{"f-1"},
	}}
	if err := ValidateSynthesis(input, proposalOutcome); err != nil {
		t.Fatalf("a proposal grounded in real evidence must validate: %v", err)
	}

	noGroundsOutcome := SynthesisOutcome{NoGrounds: true, Reason: "reviewers converged after one reply round"}
	if err := ValidateSynthesis(input, noGroundsOutcome); err != nil {
		t.Fatalf("an explicit no-grounds outcome must validate: %v", err)
	}

	ambiguous := SynthesisOutcome{}
	if err := ValidateSynthesis(input, ambiguous); !errors.Is(err, ErrSynthesisAmbiguousOutcome) {
		t.Fatalf("neither proposal nor no-grounds must be rejected as ambiguous, got %v", err)
	}
	both := SynthesisOutcome{Proposal: &Proposal{ID: "p-1"}, NoGrounds: true, Reason: "x"}
	if err := ValidateSynthesis(input, both); !errors.Is(err, ErrSynthesisAmbiguousOutcome) {
		t.Fatalf("both proposal AND no-grounds set must be rejected as ambiguous, got %v", err)
	}
	noReason := SynthesisOutcome{NoGrounds: true}
	if err := ValidateSynthesis(input, noReason); err == nil {
		t.Fatal("no-grounds without an explicit reason must be rejected")
	}
}

func TestSynthesis_DoesNotInventEvidence(t *testing.T) {
	input := SynthesisInput{Messages: sampleDiscussion()}
	outcome := SynthesisOutcome{Proposal: &Proposal{
		ID: "p-1", StageID: "build", Type: ProposalFeedback, Title: "x",
		SourceMessageIDs: []string{"m-999"}, // does not exist in the discussion
	}}
	if err := ValidateSynthesis(input, outcome); !errors.Is(err, ErrSynthesisInventedEvidence) {
		t.Fatalf("a reference to a nonexistent message must be a protocol error, got %v", err)
	}

	outcomeFinding := SynthesisOutcome{Proposal: &Proposal{
		ID: "p-2", StageID: "build", Type: ProposalFeedback, Title: "x",
		SourceFindingIDs: []string{"f-999"},
	}}
	if err := ValidateSynthesis(input, outcomeFinding); !errors.Is(err, ErrSynthesisInventedEvidence) {
		t.Fatalf("a reference to a nonexistent finding must be a protocol error, got %v", err)
	}
}

func TestSynthesis_RespectsBudget(t *testing.T) {
	input := SynthesisInput{Messages: sampleDiscussion(), Usage: RoundUsage{SynthesisUsed: true}}
	outcome := SynthesisOutcome{NoGrounds: true, Reason: "already synthesized once"}
	if err := ValidateSynthesis(input, outcome); !errors.Is(err, ErrSynthesisBudgetExceeded) {
		t.Fatalf("a second synthesis invocation must be rejected as over budget, got %v", err)
	}
}

package assistants

import (
	"errors"
	"fmt"
	"strings"
)

// SynthesisInput is everything the bounded synthesizer step is allowed to see/use: the whole discussion so
// far (both rounds) and how much of the per-discussion budget has already been spent.
type SynthesisInput struct {
	Messages []DiscussionMessage
	Usage    RoundUsage
}

// SynthesisOutcome is the synthesizer's candidate output — already decoded from whatever structured response
// produced it (same "decode untrusted model output into a typed struct first" discipline as
// pkg/orchestrator/verify's DecodeModelResult). Exactly one of Proposal/NoGrounds is set
// (TestSynthesis_ProducesProposalOrExplicitNoGrounds).
type SynthesisOutcome struct {
	Proposal  *Proposal
	NoGrounds bool
	// Reason is required when NoGrounds is true — synthesis never silently produces nothing.
	Reason string
}

var (
	// ErrSynthesisAmbiguousOutcome — the candidate outcome set neither, or both, of Proposal/NoGrounds.
	ErrSynthesisAmbiguousOutcome = errors.New("assistants: synthesis must produce exactly one of a proposal or an explicit no-grounds record")
	// ErrSynthesisInventedEvidence — a Proposal referenced a message/finding id that does not exist anywhere
	// in the discussion it was synthesized from.
	ErrSynthesisInventedEvidence = errors.New("assistants: synthesis references evidence that does not exist in the discussion")
	// ErrSynthesisBudgetExceeded — synthesis has already run once for this discussion.
	ErrSynthesisBudgetExceeded = errors.New("assistants: synthesis exceeds its one-shot budget")
)

// ValidateSynthesis enforces the bounded-synthesizer protocol (T6.3) against an already-decoded candidate
// outcome, BEFORE it is ever accepted as a real Proposal: the one-shot synthesis budget, the
// exactly-one-of-proposal-or-no-grounds invariant, and — the core anti-hallucination guard — that every
// source message/finding id the proposal cites actually resolves to something real in the discussion it
// claims to summarize.
func ValidateSynthesis(input SynthesisInput, outcome SynthesisOutcome) error {
	if err := input.Usage.Allow(RoundSynthesis); err != nil {
		return fmt.Errorf("%w: %v", ErrSynthesisBudgetExceeded, err)
	}

	hasProposal := outcome.Proposal != nil
	if hasProposal == outcome.NoGrounds {
		return ErrSynthesisAmbiguousOutcome
	}
	if outcome.NoGrounds {
		if strings.TrimSpace(outcome.Reason) == "" {
			return errors.New("assistants: a no-grounds synthesis outcome requires an explicit reason")
		}
		return nil
	}

	knownMessages, knownFindings := knownReferenceIDs(input.Messages)
	for _, id := range outcome.Proposal.SourceMessageIDs {
		if !knownMessages[id] {
			return fmt.Errorf("%w: message %q", ErrSynthesisInventedEvidence, id)
		}
	}
	for _, id := range outcome.Proposal.SourceFindingIDs {
		if !knownFindings[id] {
			return fmt.Errorf("%w: finding %q", ErrSynthesisInventedEvidence, id)
		}
	}
	return nil
}

// knownReferenceIDs returns the set of every message id present in messages, and every finding id reachable
// from them (either listed in a message's Evidence, or carried in an attached Result's own Findings).
func knownReferenceIDs(messages []DiscussionMessage) (messageIDs, findingIDs map[string]bool) {
	messageIDs = make(map[string]bool, len(messages))
	findingIDs = make(map[string]bool)
	for _, m := range messages {
		messageIDs[m.ID] = true
		for _, ev := range m.Evidence {
			findingIDs[ev] = true
		}
		if m.Result != nil {
			for _, f := range m.Result.Findings {
				findingIDs[f.ID] = true
			}
		}
	}
	return messageIDs, findingIDs
}

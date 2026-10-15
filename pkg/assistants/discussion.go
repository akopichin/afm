package assistants

import (
	"errors"
	"fmt"
	"time"
)

// MessageAuthorKind names who actually stands behind one DiscussionMessage — a human, the stage's own main
// agent, one specific helper assistant, or AFM itself (the synthesizer / system bookkeeping). This is
// deliberately distinct from Executor (the command/profile that technically produced the bytes): AFM can
// relay a human's forwarded note, or a helper's result can be attributed to that helper even though a shared
// recipe process executed it (dev-plan.md T6.1: "автор ... и фактический executor").
type MessageAuthorKind string

const (
	AuthorHuman     MessageAuthorKind = "human"
	AuthorMainAgent MessageAuthorKind = "main_agent"
	AuthorAssistant MessageAuthorKind = "assistant"
	AuthorAFM       MessageAuthorKind = "afm"
)

// MessageAuthor names who a DiscussionMessage speaks for. ID is empty for a generic main_agent/afm author and
// set to the helper task id (or profile id) for AuthorAssistant, or a user identifier for AuthorHuman.
type MessageAuthor struct {
	Kind MessageAuthorKind `json:"kind"`
	ID   string            `json:"id,omitempty"`
}

// Discussion round numbers (§9.1, dev-plan.md T6.1). The first release allows exactly one round of
// independent review (every invited participant responds without seeing anyone else's answer) plus one
// bounded round of reply/discussion — never more. RoundSynthesis is NOT a discussion round at all: it is the
// designated synthesizer's own single, separately-accounted invocation (TestRound_SynthesisCountedInLimits).
// RoundFreeform is NOT part of the structured protocol either: a human (or AFM, forwarding on a human's
// behalf) posting an ordinary comment/question into the discussion feed (T6.7's POST .../discussion) is
// unconstrained by the round budget — TestDiscussion_UserMessageIsNotAutoRevise's channel is open-ended
// conversation, not a bounded review round.
const (
	RoundFreeform    = 0
	RoundIndependent = 1
	RoundDiscussion  = 2
	RoundSynthesis   = 3
)

// DiscussionMessage is one entry in a stage's discussion log — either a participant's independent-round
// result (Result set), a bounded-round reply (Body only), a synthesis note, or a human/forwarded message.
type DiscussionMessage struct {
	ID string `json:"id"`
	// Author is who this message speaks for; Role is a short human label ("reviewer", "synthesizer", "user",
	// "main_agent"); Executor is the actual command/profile/model that ran (may differ from Author.ID when
	// AFM relays on someone's behalf).
	Author          MessageAuthor  `json:"author"`
	Role            string         `json:"role,omitempty"`
	Executor        string         `json:"executor,omitempty"`
	Round           int            `json:"round"`
	ParentMessageID string         `json:"parent_message_id,omitempty"`
	ContextVersion  ContextVersion `json:"context_version,omitempty"`
	// Evidence is a list of stable ids (finding ids and/or other message ids) this message references —
	// grouping/synthesis MUST key off these ids, never off text similarity (TestFindings_LinkedByStableIds).
	Evidence []string `json:"evidence,omitempty"`
	Body     string   `json:"body,omitempty"`
	// Result carries the full structured verdict/findings for an independent-round (Round==RoundIndependent)
	// contribution. nil for a plain prose reply/synthesis note.
	Result    *Result   `json:"result,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrRoundLimitExceeded is returned by RoundUsage.Allow when a round has already been used, or the requested
// round number is not one this build recognizes.
var ErrRoundLimitExceeded = errors.New("assistants: discussion round limit exceeded")

// RoundUsage is a durable-projection-derived tally of which bounded invocations a discussion has already
// used — never stored separately, always recomputed from the discussion's own messages (same "derive, don't
// store" discipline as assistants.ReadyContinuations/PolicyBlocked).
type RoundUsage struct {
	IndependentUsed bool
	DiscussionUsed  bool
	SynthesisUsed   bool
}

// ComputeRoundUsage derives RoundUsage from a discussion's messages.
func ComputeRoundUsage(messages []DiscussionMessage) RoundUsage {
	var u RoundUsage
	for _, m := range messages {
		switch m.Round {
		case RoundIndependent:
			u.IndependentUsed = true
		case RoundDiscussion:
			u.DiscussionUsed = true
		case RoundSynthesis:
			u.SynthesisUsed = true
		default:
			// RoundFreeform (and any other value) carries no budget — nothing to tally.
		}
	}
	return u
}

// Allow reports whether one more message/invocation at the given round is still permitted. Independent and
// discussion rounds are capped at one each (TestRound_LimitEnforced); synthesis is a separate, independently
// bounded (also one-shot) invocation (TestRound_SynthesisCountedInLimits) — using it never consumes, and is
// never consumed by, the two discussion-round slots.
func (u RoundUsage) Allow(round int) error {
	switch round {
	case RoundFreeform:
		return nil // unconstrained — see RoundFreeform's doc comment
	case RoundIndependent:
		if u.IndependentUsed {
			return fmt.Errorf("%w: independent review round already used", ErrRoundLimitExceeded)
		}
	case RoundDiscussion:
		if u.DiscussionUsed {
			return fmt.Errorf("%w: discussion round already used", ErrRoundLimitExceeded)
		}
	case RoundSynthesis:
		if u.SynthesisUsed {
			return fmt.Errorf("%w: synthesis already invoked", ErrRoundLimitExceeded)
		}
	default:
		return fmt.Errorf("assistants: unknown discussion round %d", round)
	}
	return nil
}

// CollectRoundVerdicts returns, for the given round, each RESPONDING participant's verdict keyed by
// Author.ID — strictly derived from messages that actually exist. A participant who never posted in this
// round is simply absent from the result; it is never defaulted into "agreed" (TestRound_SilentParticipantNotShownAsAgreeing).
func CollectRoundVerdicts(messages []DiscussionMessage, round int) map[string]Verdict {
	out := map[string]Verdict{}
	for _, m := range messages {
		if m.Round != round || m.Result == nil {
			continue
		}
		out[m.Author.ID] = m.Result.Verdict
	}
	return out
}

// RestoreOrder rebuilds a message slice from an ordered id list plus a lookup map — the canonical way to
// reconstruct discussion order. Order comes ENTIRELY from the id list (itself commit/append order, never
// re-derived from wall-clock data) — TestRound_OrderRestoredFromIdsNotTimestamps. Unknown ids are skipped.
func RestoreOrder(ids []string, byID map[string]*DiscussionMessage) []DiscussionMessage {
	out := make([]DiscussionMessage, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok && m != nil {
			out = append(out, *m)
		}
	}
	return out
}

// DisagreementOutcome is the result of comparing every RESPONDING participant's verdict for one round.
// Disagreement is a first-class, valid terminal outcome (TestDisagreement_IsAValidOutcome) — there is
// deliberately no field here (or anywhere derived from it) that marks a numeric majority as "the correct
// side": RequiresSynthesis is the only decision this type exposes, and a 3-vs-1 split requires synthesis
// exactly like a 1-vs-1 split (TestDisagreement_MajorityIsNotProof).
type DisagreementOutcome struct {
	Agreed   bool
	Verdicts map[string]Verdict
}

// RequiresSynthesis reports whether this outcome needs the bounded synthesizer step — true for any
// disagreement, regardless of how lopsided the split is.
func (o DisagreementOutcome) RequiresSynthesis() bool { return !o.Agreed }

// Disagree classifies a set of responding participants' verdicts. Zero or one responder is trivially
// "agreed" (nothing to disagree about yet).
func Disagree(verdicts map[string]Verdict) DisagreementOutcome {
	agreed := true
	first := VerdictInconclusive
	seen := false
	for _, v := range verdicts {
		if !seen {
			first = v
			seen = true
			continue
		}
		if v != first {
			agreed = false
		}
	}
	return DisagreementOutcome{Agreed: agreed, Verdicts: verdicts}
}

// FindingGroup is every message that referenced one stable finding id — the unit "related findings" are
// grouped into (dev-plan.md T6.2: "связанные выводы группируются по id findings, а не по похожести текста").
type FindingGroup struct {
	FindingID  string
	MessageIDs []string
}

// GroupByFindingID groups messages by the finding ids listed in their Evidence, in first-seen order. Grouping
// is a pure id-equality join — two messages with identical Body text but DIFFERENT evidence ids land in
// different groups, and two messages with unrelated text but the SAME evidence id land in the same group
// (TestFindings_LinkedByStableIds).
func GroupByFindingID(messages []DiscussionMessage) []FindingGroup {
	order := make([]string, 0)
	byID := map[string][]string{}
	for _, m := range messages {
		for _, ev := range m.Evidence {
			if _, ok := byID[ev]; !ok {
				order = append(order, ev)
			}
			byID[ev] = append(byID[ev], m.ID)
		}
	}
	groups := make([]FindingGroup, 0, len(order))
	for _, id := range order {
		groups = append(groups, FindingGroup{FindingID: id, MessageIDs: byID[id]})
	}
	return groups
}

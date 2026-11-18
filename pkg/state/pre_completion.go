package state

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// Round decisions (PreCompletionState.Decision).
const (
	PreCompletionAccepted     = "accepted"
	PreCompletionNeedsChanges = "needs_changes"
	// PreCompletionExhausted and PreCompletionFailed end the stage: no more corrections, or a required helper died.
	PreCompletionExhausted = "exhausted"
	PreCompletionFailed    = "failed"
)

// FSM events (pkg/orchestrator/bus) that carry a Transition.PreCompletion step.
const (
	transitionContinue            = "continue"
	transitionQuotaReset          = "quota_reset"
	preCompletionEventAwait       = "await_pre_completion"
	preCompletionEventDecided     = "pre_completion_decided"
	preCompletionEventAcceptEmpty = "pre_completion_accept_empty"
	preCompletionEventConsumed    = "pre_completion_consumed"
)

// preCompletionRecordEvents only record a decision of a running stage: it does not move, so the projection
// changes nothing but the pre-completion record, the seq and the timestamp.
var preCompletionRecordEvents = map[string]bool{
	preCompletionEventAcceptEmpty: true,
	preCompletionEventConsumed:    true,
}

// PreCompletionState is the stage's pre-completion record: the round in progress or decided last, and what the
// author's loop carries over a round (its budgets), so that a restart continues it without guessing from files.
// Derived on replay from Transition.PreCompletion.
type PreCompletionState struct {
	// Cycle counts the cycles of the stage; only Retry and Revise start a new one, which zeroes the rest.
	Cycle int `json:"cycle,omitempty"`
	// Round is the number of the latest round in the cycle. RoundSeq is the seq of the transition that started
	// it — it names the round and the author's finished turn.
	Round    int    `json:"round,omitempty"`
	RoundSeq uint64 `json:"round_seq,omitempty"`
	// Phase is the author's phase (implementation, review or autonomous) the stage continues in after the round.
	Phase string `json:"phase,omitempty"`
	// Tasks are the pre_completion helpers the round waits for, in creation order, fixed when it started.
	Tasks []assistants.TaskID `json:"tasks,omitempty"`
	// Decision is how the latest round ended; "" while it is open.
	Decision string `json:"decision,omitempty"`
	// Corrections counts the needs_changes rounds that sent the work back to the author in this cycle.
	Corrections int `json:"corrections,omitempty"`
	// Accepted is the one-shot intent to go on to verify (or to complete without it) after an accepted round or
	// an empty one. It lives in the decision's own transition, so a crash between the decision and the next step
	// keeps it; every other transition but Pause/Continue and a verify park consumes it.
	Accepted bool `json:"accepted,omitempty"`
	// Attempt through Feedback are the author's loop counters carried over the round.
	Attempt           int    `json:"attempt,omitempty"`
	VerifyFailures    int    `json:"verify_failures,omitempty"`
	VerifyStalemates  int    `json:"verify_stalemates,omitempty"`
	VerifyFingerprint string `json:"verify_fingerprint,omitempty"`
	// Feedback marks that the author's turn before the round was the feedback run of a Revise.
	Feedback bool `json:"feedback,omitempty"`
}

// PreCompletionStep is what one transition records: the author's loop counters, and for a decision how the
// round ended.
type PreCompletionStep struct {
	Phase             string `json:"phase,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	VerifyFailures    int    `json:"verify_failures,omitempty"`
	VerifyStalemates  int    `json:"verify_stalemates,omitempty"`
	VerifyFingerprint string `json:"verify_fingerprint,omitempty"`
	Feedback          bool   `json:"feedback,omitempty"`
	Decision          string `json:"decision,omitempty"`
}

// PreCompletionRequestID is the id of the system request that puts a round in a helper's queue: it names the round
// and is no mesh message.
func PreCompletionRequestID(roundSeq uint64) agentcomm.MessageID {
	return agentcomm.MessageID(fmt.Sprintf("pre-completion-%d", roundSeq))
}

// Open reports whether the round was started and not decided.
func (p *PreCompletionState) Open() bool {
	return p != nil && p.RoundSeq != 0 && p.Decision == ""
}

// preCompletionAfter derives the stage's pre-completion record after transition t, which was applied to rs.
func (rs *RunState) preCompletionAfter(prev *PreCompletionState, t *Transition) *PreCompletionState {
	if summaryResetEvents[t.Event] {
		next := PreCompletionState{}
		if prev != nil {
			next.Cycle = prev.Cycle + 1
		}
		return &next
	}
	if t.To == StatusPending {
		return nil
	}
	if prev == nil && t.PreCompletion == nil {
		return nil
	}
	var next PreCompletionState
	if prev != nil {
		next = *prev
		next.Tasks = slices.Clone(prev.Tasks)
	}
	if !keepsPreCompletionAccepted(prev, t) {
		next.Accepted = false
	}
	step := t.PreCompletion
	if step == nil {
		return &next
	}
	switch t.Event {
	case preCompletionEventAwait:
		next.Round++
		next.RoundSeq, next.Decision, next.Accepted = t.Seq, "", false
		next.Tasks = rs.preCompletionTaskSet(t.StageID)
	case preCompletionEventDecided:
		next.Decision = step.Decision
		next.Accepted = step.Decision == PreCompletionAccepted
		if step.Decision == PreCompletionNeedsChanges {
			next.Corrections++
		}
	case preCompletionEventAcceptEmpty:
		next.RoundSeq, next.Tasks, next.Decision, next.Accepted = t.Seq, nil, PreCompletionAccepted, true
	default:
		next.Decision = cmp.Or(step.Decision, next.Decision) // the failure that ends the stage names its round's decision
	}
	next.Phase = cmp.Or(step.Phase, next.Phase)
	if t.Event != preCompletionEventAcceptEmpty { // the gate that accepts an empty round knows no loop counters
		next.Attempt, next.Feedback = step.Attempt, step.Feedback
		next.VerifyFailures, next.VerifyStalemates, next.VerifyFingerprint = step.VerifyFailures, step.VerifyStalemates, step.VerifyFingerprint
	}
	return &next
}

// keepsPreCompletionAccepted reports whether the accepted intent survives transition t: Pause and Continue, the
// park of the verify gate on a usage limit and its wake keep it; any other transition consumes it (an unknown
// event included).
func keepsPreCompletionAccepted(prev *PreCompletionState, t *Transition) bool {
	switch t.Event {
	case "pause", transitionContinue, preCompletionEventDecided, preCompletionEventAcceptEmpty:
		return true
	case "await_quota":
		return t.Quota != nil && t.Quota.Target == QuotaTargetVerify
	case transitionQuotaReset:
		return prev != nil && prev.Accepted
	default:
		return false
	}
}

// preCompletionTaskSet is the set of helpers a round starting now waits for: top-level pre_completion tasks of the
// stage's current generation that are still alive or failed (a failed one waits for its Retry).
func (rs *RunState) preCompletionTaskSet(stageID string) []assistants.TaskID {
	var out []assistants.TaskID
	for _, id := range rs.Assistants.ByRootStage[stageID] {
		t := rs.Assistants.Tasks[id]
		if t != nil && IsPreCompletionHelper(t.Task) && t.RootGeneration == rs.Assistants.RootGenerations[stageID] &&
			(!t.Status.Terminal() || t.Status == assistants.StatusFailed) {
			out = append(out, id)
		}
	}
	return out
}

// PreCompletionTasks is the helper set a round started now would wait for.
func (rs RunState) PreCompletionTasks(stageID string) []assistants.TaskID {
	return rs.preCompletionTaskSet(stageID)
}

// queuePreCompletionRound puts the system request of the round that transition t started in each helper's queue.
func (rs *RunState) queuePreCompletionRound(stageID string, round *PreCompletionState) {
	id := PreCompletionRequestID(round.RoundSeq)
	for _, taskID := range round.Tasks {
		t := rs.Assistants.Tasks[taskID]
		if t == nil || t.queuedRequest(id) {
			continue
		}
		// Snapshots share *TaskState, so the task is replaced rather than changed in place.
		queued := *t
		queued.PendingRequests = append(slices.Clone(t.PendingRequests), id)
		rs.Assistants.Tasks[taskID] = &queued
	}
}

// PreCompletionWindowOpen reports whether a new pre_completion helper cannot join the stage now: a round is open,
// or the accepted round (or an empty one) has not led to verify or completion yet. Any stage status counts.
func PreCompletionWindowOpen(rs *RunState, stageID string) bool {
	p := rs.Stages[stageID].PreCompletion
	return p.Open() || (p != nil && p.Accepted)
}

// RoundOutcome is how one helper's request of the current round stands.
type RoundOutcome string

const (
	// RoundPending — the helper has not finished the request (or is held back from it).
	RoundPending RoundOutcome = ""
	// RoundPass, RoundNeedsChanges and RoundInconclusive are the verdict of the helper's result.
	RoundPass         RoundOutcome = "pass"
	RoundNeedsChanges RoundOutcome = "needs_changes"
	RoundInconclusive RoundOutcome = "inconclusive"
	// RoundFailed — no result: the turn failed, or the helper failed or was cancelled with the request unanswered.
	RoundFailed RoundOutcome = "failed"
	// RoundWaived — a human waived this round's request.
	RoundWaived RoundOutcome = "waived"
)

// roundOutcome reads the outcome of the round's request off helper t.
func roundOutcome(t *TaskState, request agentcomm.MessageID) RoundOutcome {
	switch {
	case t.WaivedRequest == request:
		return RoundWaived
	case t.LastRequest == request && t.LastOutcome == RequestOutcomeResult:
		switch t.LastVerdict {
		case assistants.VerdictPass:
			return RoundPass
		case assistants.VerdictNeedsChanges:
			return RoundNeedsChanges
		default:
			return RoundInconclusive
		}
	case t.LastRequest == request:
		return RoundFailed
	case t.queuedRequest(request) && (t.Status == assistants.StatusFailed || t.Status == assistants.StatusCancelled):
		return RoundFailed
	default:
		return RoundPending
	}
}

// PreCompletionTaskOutcome is the outcome of the stage's open or latest round for helper t, with the round's number;
// round 0 for a helper that is not in it.
func PreCompletionTaskOutcome(rs RunState, t *TaskState) (round int, outcome RoundOutcome) {
	p := rs.Stages[t.RootStageID].PreCompletion
	if p == nil || p.RoundSeq == 0 || !slices.Contains(p.Tasks, t.ID) {
		return 0, RoundPending
	}
	return p.Round, roundOutcome(t, PreCompletionRequestID(p.RoundSeq))
}

// RoundDecisionKind is what the round's outcomes call for.
type RoundDecisionKind string

const (
	// RoundDecisionPending — some helper is still working; nothing to decide yet.
	RoundDecisionPending RoundDecisionKind = "pending"
	// RoundDecisionBlocked — nothing more will come without a human: Continue of a held helper, Retry, Waive, Revise,
	// Pause or Fail.
	RoundDecisionBlocked  RoundDecisionKind = "blocked"
	RoundDecisionAccepted RoundDecisionKind = PreCompletionAccepted
	RoundDecisionCorrect  RoundDecisionKind = PreCompletionNeedsChanges
	// RoundDecisionExhausted and RoundDecisionFailed end the stage.
	RoundDecisionExhausted RoundDecisionKind = PreCompletionExhausted
	RoundDecisionFailed    RoundDecisionKind = PreCompletionFailed
)

// PreCompletionLimits are what the decision weighs the round against.
type PreCompletionLimits struct {
	// MaxFailures is the stage's correction budget; MaxRetries bounds the author's attempts in the cycle.
	MaxFailures int
	MaxRetries  int
	// ExitOnFail fails the stage at once when a required helper of the round died.
	ExitOnFail bool
}

// RoundDecision is the pure verdict on the stage's open round.
type RoundDecision struct {
	Kind RoundDecisionKind
	// Reason says why the stage ends (exhausted, failed); empty otherwise.
	Reason string
}

// PreCompletionDecision decides the stage's open round from the outcome of each helper's request. Waiting helpers hold
// it (a held one of any mode, as a human's decision, before anything else), required needs_changes corrects the author
// within the budget, and what a human has to settle (no pass, a dead helper) blocks. Advisory helpers never veto.
func PreCompletionDecision(rs RunState, stageID string, lim PreCompletionLimits) RoundDecision {
	p := rs.Stages[stageID].PreCompletion
	if !p.Open() {
		return RoundDecision{Kind: RoundDecisionPending}
	}
	request := PreCompletionRequestID(p.RoundSeq)
	var pending, held, needsChanges, blocked bool
	for _, id := range p.Tasks {
		t := rs.Assistants.Tasks[id]
		if t == nil {
			continue
		}
		outcome := roundOutcome(t, request)
		required := t.Mode == assistants.ModeRequired
		if required && outcome == RoundFailed && lim.ExitOnFail {
			return RoundDecision{Kind: RoundDecisionFailed, Reason: fmt.Sprintf("required pre_completion helper %s failed", t.ID)}
		}
		switch outcome {
		case RoundPending:
			if t.Paused() || t.Status == assistants.StatusPaused {
				held = true
			} else {
				pending = true
			}
		case RoundNeedsChanges:
			needsChanges = needsChanges || required
		case RoundInconclusive, RoundFailed:
			blocked = blocked || required
		default:
		}
	}
	switch {
	case pending:
		return RoundDecision{Kind: RoundDecisionPending}
	case held:
		return RoundDecision{Kind: RoundDecisionBlocked}
	case needsChanges && p.Corrections < lim.MaxFailures && p.Attempt < lim.MaxRetries:
		return RoundDecision{Kind: RoundDecisionCorrect}
	case needsChanges && p.Corrections >= lim.MaxFailures:
		return RoundDecision{Kind: RoundDecisionExhausted, Reason: "pre-completion correction budget exhausted"}
	case needsChanges:
		return RoundDecision{Kind: RoundDecisionExhausted, Reason: "retries exhausted"}
	case blocked:
		return RoundDecision{Kind: RoundDecisionBlocked}
	default:
		return RoundDecision{Kind: RoundDecisionAccepted}
	}
}

// PreCompletionNeedsHuman reports whether the stage's round is blocked on a human decision.
func PreCompletionNeedsHuman(rs *RunState, stageID string) bool {
	return rs.Stages[stageID].Status == StatusAwaitingPreCompletion &&
		PreCompletionDecision(*rs, stageID, PreCompletionLimits{}).Kind == RoundDecisionBlocked
}

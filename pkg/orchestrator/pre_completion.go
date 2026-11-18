package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// preCompletionDue is the completion gate's request for a review round: the stage's pre_completion helpers read the
// author's finished turn before verify. It is no incomplete-work error, so it never spends a retry.
type preCompletionDue struct{}

func (*preCompletionDue) Error() string { return "pre-completion review is due" }

// preCompletionPhase reports whether a phase's turn ends in the completion gate that the round precedes.
func preCompletionPhase(phase string) bool {
	return phase == phaseImplementation || phase == phaseReview || phase == phaseAutonomous
}

// gatesBeforeCompletion reports whether a finished author of s passes the pre-completion gate: an execution stage of
// a run that can have helpers, or one that already has a persisted round or helper — a restart with collaboration
// off must not complete it unreviewed. Without either the gate and the journal stay as they were.
func (o *Orchestrator) gatesBeforeCompletion(s flow.Stage) bool {
	if s.IsScript() {
		return false
	}
	if o.opts.AssistantsEnabled {
		return true
	}
	if o.opts.Store == nil {
		return false
	}
	rs := o.opts.Store.Snapshot()
	return rs.Stages[s.ID].PreCompletion != nil || len(rs.PreCompletionTasks(s.ID)) > 0
}

// preCompletionGate runs after the author's completion check and before verify. A stage with pre_completion helpers
// gets a round; one without records an empty acceptance first, so a helper attached from now on cannot slip into
// verify unreviewed. An accepted round, or an empty one, passes its next gate once, and verify follows.
func (o *Orchestrator) preCompletionGate(s flow.Stage, phase string) error {
	if !o.gatesBeforeCompletion(s) || !preCompletionPhase(phase) {
		return nil
	}
	rs := o.opts.Store.Snapshot()
	if p := rs.Stages[s.ID].PreCompletion; p != nil && p.Accepted {
		return nil
	}
	if len(rs.PreCompletionTasks(s.ID)) > 0 {
		return &preCompletionDue{}
	}
	if o.testAfterPreCompletionSnapshot != nil {
		o.testAfterPreCompletionSnapshot()
	}
	epoch := o.mintEpochOf(s.ID)
	guard := bus.GuardCtx{
		ExpectEpoch:   &epoch,
		PreCompletion: &state.PreCompletionStep{Phase: phase},
		Fence:         func(cur *state.RunState) bool { return len(cur.PreCompletionTasks(s.ID)) == 0 },
	}
	if _, ok := o.Trigger(s.ID, bus.EvPreCompletionAcceptEmpty, guard, ""); ok {
		return nil
	}
	// A helper attached first joins the round; a lost epoch is the loop's own takeover check to handle.
	if len(o.opts.Store.Snapshot().PreCompletionTasks(s.ID)) > 0 {
		return &preCompletionDue{}
	}
	return errEmptyAcceptanceNotRecorded // fail closed: verify never starts on an acceptance the journal does not hold
}

var errEmptyAcceptanceNotRecorded = errors.New("pre-completion: the empty acceptance was not recorded")

// preCompletionStep is the author's loop counters as the round carries them over the author's exit.
func (v verifyCorrection) preCompletionStep(phase string, attempt int, feedback bool) state.PreCompletionStep {
	return state.PreCompletionStep{
		Phase: phase, Attempt: attempt, VerifyFailures: v.failures, VerifyStalemates: v.unchanged,
		VerifyFingerprint: v.prevFingerprint, Feedback: feedback,
	}
}

// parkForPreCompletion parks the stage in awaiting_pre_completion and starts the round. The commit is fenced with the
// epoch of the turn that finished, so a takeover while the author was exiting wins; a Revise of this turn is then carried
// out through onUserInterrupted. The author's lease is released by the caller returning.
func (o *Orchestrator) parkForPreCompletion(ctx context.Context, s flow.Stage, epoch uint64, step state.PreCompletionStep, onUserInterrupted func()) {
	if !o.openRound(s.ID, epoch, step) {
		o.handOffRevise(s.ID, epoch, onUserInterrupted)
		return
	}
	if o.testAfterRoundOpened != nil {
		o.testAfterRoundOpened()
	}
	// The helpers start, and the round is decided at once if nobody has to answer (an empty set, or a helper that
	// finished before the wake).
	o.resumePreCompletionRound(ctx, s.ID)
}

// openRound commits the round, drops the feedback of the round before it and announces it, all under roundMu: no
// finisher can decide the round in between, so neither the cleanup nor the notice comes after its decision.
func (o *Orchestrator) openRound(stageID string, epoch uint64, step state.PreCompletionStep) bool {
	o.roundMu.Lock()
	defer o.roundMu.Unlock()
	guard := bus.GuardCtx{ExpectEpoch: &epoch, PreCompletion: &step}
	_, roundSeq, ok := o.triggerWithSeq(stageID, bus.EvAwaitPreCompletion, guard, "pre-completion review")
	if !ok {
		return false
	}
	if err := stagefiles.ClearPreCompletionFeedback(o.stageDirFor(stageID)); err != nil {
		log.Printf("WARN: stage %q: %v", stageID, err)
	}
	// A Revise right after the commit has already started a new cycle: the round it ended never got under way.
	if p := o.opts.Store.Snapshot().Stages[stageID].PreCompletion; p != nil && p.RoundSeq == roundSeq {
		o.publishNotice(bus.Event{Type: bus.EventPreCompletionRoundStarted, StageID: stageID, Data: map[string]any{"round": p.Round, "helpers": len(p.Tasks)}})
	}
	return true
}

// wakeRoundHelpers puts each helper of the open round that rests with its request due in the queue.
func (o *Orchestrator) wakeRoundHelpers(ctx context.Context, stageID string) {
	p := o.opts.Store.Snapshot().Stages[stageID].PreCompletion
	if p == nil {
		return
	}
	for _, id := range p.Tasks {
		o.wakeOnMessageTask(ctx, id)
	}
}

// preCompletionLimits are what the open round is decided against. exit_on_fail counts only while the run is alive.
func (o *Orchestrator) preCompletionLimits(s flow.Stage) state.PreCompletionLimits {
	return state.PreCompletionLimits{
		MaxFailures: s.PreCompletionMaxFailures(), MaxRetries: o.maxRetries,
		ExitOnFail: o.opts.ExitOnFail && o.runContextAlive(),
	}
}

// maybeDecideRound commits the decision of stageID's open round when its helpers' outcomes call for one. Every
// trigger that can complete a round calls it — a request's end, Waive, Retry of a helper, Continue, recovery — and
// recordRound lets exactly one of them continue the stage.
func (o *Orchestrator) maybeDecideRound(ctx context.Context, stageID string) {
	if o.opts.Store == nil {
		return
	}
	end, ok := o.recordRound(stageID)
	if !ok {
		return
	}
	if o.testAfterRoundDecided != nil {
		o.testAfterRoundDecided()
	}
	if end.failed {
		o.failBlockedStages()
		return
	}
	o.continueAfterRound(withTurnEpoch(ctx, end.epoch), end.stage, end.round)
}

// roundEnd is a round that recordRound committed, for the steps that follow its commit.
type roundEnd struct {
	stage      flow.Stage
	round      state.PreCompletionState // the round as its decision left it
	reason     string
	reportPath string
	failed     bool   // the decision ended the stage
	epoch      uint64 // the epoch the continuation owns
}

// recordRound decides and records the open round of stageID under roundMu, one finisher at a time: a late one sees the
// round decided and writes nothing, so it can neither replace a newer round's files nor fail the stage over a raced
// file. The decision's notice goes out under the same lock, ahead of the next round's start notice.
func (o *Orchestrator) recordRound(stageID string) (roundEnd, bool) {
	o.roundMu.Lock()
	defer o.roundMu.Unlock()
	rs := o.opts.Store.Snapshot()
	st := rs.Stages[stageID]
	stage := o.reg().Stage(stageID)
	if stage == nil || st.Status != state.StatusAwaitingPreCompletion || !st.PreCompletion.Open() {
		return roundEnd{}, false
	}
	lim := o.preCompletionLimits(*stage)
	decision := state.PreCompletionDecision(rs, stageID, lim)
	switch decision.Kind {
	case state.RoundDecisionAccepted, state.RoundDecisionCorrect, state.RoundDecisionExhausted, state.RoundDecisionFailed:
	default:
		return roundEnd{}, false // helpers still work, or a human has to settle the round
	}
	roundSeq := st.PreCompletion.RoundSeq
	unchanged := func(cur *state.RunState) bool {
		p := cur.Stages[stageID].PreCompletion
		return p.Open() && p.RoundSeq == roundSeq && state.PreCompletionDecision(*cur, stageID, lim).Kind == decision.Kind
	}
	end, ok := o.finishRound(*stage, rs, decision, unchanged)
	if ok {
		o.publishRoundDecided(end)
	}
	return end, ok
}

// finishRound records the round — its report and, for a correction, the feedback; any other decision drops the active
// one — and only then commits the decision, so the author is never started without what it was told. A record that
// cannot be written fails the stage: neither the author nor verify may go on without it.
func (o *Orchestrator) finishRound(s flow.Stage, rs state.RunState, decision state.RoundDecision, unchanged func(*state.RunState) bool) (roundEnd, bool) {
	p := *rs.Stages[s.ID].PreCompletion
	stageDir := o.stageDirFor(s.ID)
	reports := o.collectRoundReports(rs, s.ID)
	reportPath := stagefiles.PreCompletionReportPath(stageDir, p.RoundSeq)
	feedback := ""
	if decision.Kind == state.RoundDecisionCorrect {
		feedback = renderPreCompletionFeedback(p.Round, p.RoundSeq, reportPath, reports)
	}
	report := renderPreCompletionReport(s.ID, p.Round, p.RoundSeq, string(decision.Kind), decision.Reason, reports)
	if err := stagefiles.WritePreCompletionRound(stageDir, p.RoundSeq, report, feedback); err != nil {
		return o.failRound(s, p, state.PreCompletionFailed, unchanged, fmt.Sprintf("pre-completion: %v", err))
	}

	step := state.PreCompletionStep{
		Phase: p.Phase, Attempt: p.Attempt, VerifyFailures: p.VerifyFailures, VerifyStalemates: p.VerifyStalemates,
		VerifyFingerprint: p.VerifyFingerprint, Feedback: p.Feedback,
	}
	switch decision.Kind {
	case state.RoundDecisionAccepted:
		step.Decision = state.PreCompletionAccepted
	case state.RoundDecisionCorrect:
		step.Decision, step.Attempt = state.PreCompletionNeedsChanges, p.Attempt+1
	default:
		return o.failRound(s, p, string(decision.Kind), unchanged, fmt.Sprintf("%s; report: %s", decision.Reason, reportPath))
	}
	// The continuation owns the epoch this commit is fenced to: a Pause or Continue after it starts the turn itself.
	epoch, ok := o.grantTurn(s.ID, bus.EvPreCompletionDecided, bus.GuardCtx{Fence: unchanged, PreCompletion: &step}, "pre-completion "+step.Decision)
	if !ok {
		return roundEnd{}, false // another trigger decided the round, or a takeover moved the stage
	}
	decided := p // as the decision's transition leaves the record (state.preCompletionAfter)
	decided.Decision, decided.Accepted = step.Decision, step.Decision == state.PreCompletionAccepted
	if step.Decision == state.PreCompletionNeedsChanges {
		decided.Corrections++
	}
	return roundEnd{stage: s, round: decided, reason: decision.Reason, reportPath: reportPath, epoch: epoch}, true
}

// failRound ends the stage for a round that cannot go on: no correction left, a required helper that died under
// exit_on_fail, or a record that cannot be written. The decision is recorded with the failure. A Pause committed while
// the round was being recorded wins: the round stays open for Continue to decide again.
func (o *Orchestrator) failRound(s flow.Stage, p state.PreCompletionState, decision string, unchanged func(*state.RunState) bool, reason string) (roundEnd, bool) {
	step := state.PreCompletionStep{Decision: decision}
	guard := bus.GuardCtx{ExpectFrom: state.StatusAwaitingPreCompletion, Fence: unchanged, PreCompletion: &step}
	if _, ok := o.Trigger(s.ID, bus.EvFail, guard, reason); !ok {
		return roundEnd{}, false
	}
	p.Decision = decision
	return roundEnd{stage: s, round: p, reason: reason, failed: true,
		reportPath: stagefiles.PreCompletionReportPath(o.stageDirFor(s.ID), p.RoundSeq)}, true
}

// publishRoundDecided is the durable notice that a round ended. It names the round the decision was committed for, not
// whatever the record says by the time it goes out.
func (o *Orchestrator) publishRoundDecided(end roundEnd) {
	data := map[string]any{
		"round": end.round.Round, "decision": end.round.Decision, "corrections": end.round.Corrections,
		"max_failures": end.stage.PreCompletionMaxFailures(), "report_path": end.reportPath,
	}
	if end.reason != "" {
		data["reason"] = o.RedactSecrets(end.reason)
	}
	o.publishNotice(bus.Event{Type: bus.EventPreCompletionDecided, StageID: end.stage.ID, Data: data})
}

// continueAfterRound starts the author's turn the decision calls for: the correction, or the turn that goes on to verify
// without calling the author (the loop sees the accepted intent). Both resume the kind and the session policy the
// finished turn had.
func (o *Orchestrator) continueAfterRound(ctx context.Context, s flow.Stage, p state.PreCompletionState) {
	kind, ok := kindOfPhase(p.Phase)
	if !ok {
		kind = o.executionKind(s)
	}
	runCtx := o.runContext(ctx)
	if epoch, granted := turnEpochFrom(ctx); granted {
		runCtx = withTurnEpoch(runCtx, epoch)
	}
	run := o.turnRunner(kind, p.Feedback)
	if p.Decision == state.PreCompletionNeedsChanges {
		run = o.correctionRunner(p, run)
	}
	o.spawnKind(runCtx, s, kind, run)
}

// correctionRunner readies the correction of a needs_changes round inside its own turn, once that turn holds the stage
// and still owns the correction: done earlier, it could drop the session of a turn a Pause and Continue started
// meanwhile. A turn that lost the stage runs on unprepared, so its loop hands a Revise over as usual.
func (o *Orchestrator) correctionRunner(round state.PreCompletionState, run func(context.Context, flow.Stage)) func(context.Context, flow.Stage) {
	return func(ctx context.Context, s flow.Stage) {
		owned := o.ownsCorrection(ctx, s.ID, round.RoundSeq)
		if o.testAfterCorrectionClaim != nil {
			o.testAfterCorrectionClaim(owned)
		}
		if owned {
			o.armResumeContext(s.ID)
			dropSessionForFreshCorrection(o.verifyFreshCorrection(s), o.stageDirFor(s.ID), round.Phase)
		}
		run(ctx, s)
	}
}

// ownsCorrection reports whether the turn of ctx may prepare the correction the round roundSeq asked for: it holds the
// stage's epoch, and the stage still runs that round's correction.
func (o *Orchestrator) ownsCorrection(ctx context.Context, stageID string, roundSeq uint64) bool {
	epoch, granted := turnEpochFrom(ctx)
	st := o.opts.Store.Snapshot().Stages[stageID]
	p := st.PreCompletion
	return granted && st.Epoch == epoch && correctionStatus(st.Status) &&
		p != nil && p.Decision == state.PreCompletionNeedsChanges && p.RoundSeq == roundSeq
}

// correctionStatus is a status the stage can hold in its correction's epoch before the turn starts: running or retrying
// as the decision or a Continue left it, or awaiting_user_input if a question was asked meanwhile (questionHeldByRound
// keeps the poller from it). Any takeover (Pause, Continue, Revise) is a new epoch, which ownsCorrection refuses on its own.
func correctionStatus(s state.StageStatus) bool {
	return s == state.StatusRunning || s == state.StatusRetrying || s == state.StatusAwaitingUserInput
}

// consumeAcceptedRound ends the accepted-round intent when verify did not take the stage on: the correction that
// follows gets a new round. The record carries the author's verify counters, so a restart keeps review_patience.
func (o *Orchestrator) consumeAcceptedRound(stageID string, epoch uint64, step state.PreCompletionStep) {
	if p := o.opts.Store.Snapshot().Stages[stageID].PreCompletion; p == nil || !p.Accepted {
		return
	}
	o.Trigger(stageID, bus.EvPreCompletionConsumed, bus.GuardCtx{ExpectEpoch: &epoch, PreCompletion: &step}, "verify did not accept the work")
}

// resumePreCompletionRound continues a round that a restart or a Continue found open.
func (o *Orchestrator) resumePreCompletionRound(ctx context.Context, stageID string) {
	runCtx := o.runContext(ctx)
	o.wakeRoundHelpers(runCtx, stageID)
	o.maybeDecideRound(runCtx, stageID)
}

// afterRoundHelperChange gives the round a chance to decide after something changed one of its helpers' standing:
// Waive, Retry or Cancel of the task, the end of its request.
func (o *Orchestrator) afterRoundHelperChange(ctx context.Context, taskID assistants.TaskID) {
	t := o.opts.Store.Snapshot().Assistants.Tasks[taskID]
	if t == nil || !state.IsPreCompletionHelper(t.Task) {
		return
	}
	o.resumePreCompletionRound(ctx, t.RootStageID)
}

// questionsSettledByRound reports whether the stage goes on from an accepted round of its helpers. Its author does not
// run before verify, so an open question is one the accepted turn left behind, and nobody is asked it again.
func questionsSettledByRound(st state.StageState) bool {
	p := st.PreCompletion
	return p != nil && p.Accepted && len(p.Tasks) > 0
}

// questionHeldByRound reports whether a question found open waits for the stage's round to be carried out: settled by an
// accepted round, or left before a correction whose author is not running yet. Asked then, its answer would start a
// second turn in the correction's epoch, and a restart would resume the reviewed author instead of the correction.
func questionHeldByRound(st state.StageState, authorLive bool) bool {
	p := st.PreCompletion
	correctionOwed := p != nil && !p.Open() && p.Decision == state.PreCompletionNeedsChanges
	return questionsSettledByRound(st) || (correctionOwed && !authorLive)
}

// gateOnlyOrder asks one retry loop to skip the author and run only the completion gate: a stage whose files already
// say it is done, found by a restart, still goes through the round.
type gateOnlyOrder struct{ claimed atomic.Bool }

type gateOnlyKey struct{}

func withGateOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, gateOnlyKey{}, &gateOnlyOrder{})
}

// claimGateOnly gives the order to the first loop on this context only: a loop started later on it for another turn
// (a Revise's feedback run) runs its author.
func claimGateOnly(ctx context.Context) bool {
	order, _ := ctx.Value(gateOnlyKey{}).(*gateOnlyOrder)
	return order != nil && order.claimed.CompareAndSwap(false, true)
}

// resumeAcceptedRound restarts a stage that a restart or a Continue found with an accepted round not yet used: the turn
// goes to verify (or completion) without the author. false — no such intent.
func (o *Orchestrator) resumeAcceptedRound(ctx context.Context, s flow.Stage) bool {
	p := o.opts.Store.Snapshot().Stages[s.ID].PreCompletion
	if p == nil || !p.Accepted {
		return false
	}
	o.continueAfterRound(ctx, s, *p)
	return true
}

// resumeOwedCorrection restarts the correction a needs_changes round asked for after a restart or a Continue: the
// reviewed turn's marker on disk cannot say it is done. The correction's own turn readies it again, so a crash between
// the decision and the author's start changes nothing.
func (o *Orchestrator) resumeOwedCorrection(ctx context.Context, s flow.Stage) bool {
	p := o.opts.Store.Snapshot().Stages[s.ID].PreCompletion
	if p == nil || p.Open() || p.Accepted || p.Decision != state.PreCompletionNeedsChanges {
		return false
	}
	o.continueAfterRound(ctx, s, *p)
	return true
}

// resumeRoundContinuation restarts the turn a pre-completion round decided on: the verify-only turn after an accepted
// round, or the correction after needs_changes.
func (o *Orchestrator) resumeRoundContinuation(ctx context.Context, s flow.Stage) bool {
	if !o.gatesBeforeCompletion(s) {
		return false
	}
	return o.resumeAcceptedRound(ctx, s) || o.resumeOwedCorrection(ctx, s)
}

// recoverThroughGate completes a stage whose files say it is done like a live turn would: it passes the gate first. A
// stage with no helper records the empty acceptance and completes at once; one with helpers goes through a gate-only
// turn, which parks it for the round. Without helpers support the stage completes by files.
func (o *Orchestrator) recoverThroughGate(ctx context.Context, s flow.Stage, from state.StageStatus, reason string) {
	if !o.gatesBeforeCompletion(s) {
		o.completeRecoveredStage(ctx, s, from, reason)
		return
	}
	phase := executionPhases[o.executionKind(s)]
	if o.parkInterruptedTurn(ctx, s, from) || o.continueForUnreadMessages(ctx, s.ID, phase, nil) {
		return
	}
	if o.preCompletionGate(s, phase) == nil {
		o.completeRecoveredStage(ctx, s, from, reason)
		return
	}
	kind := o.executionKind(s)
	o.spawnKind(withGateOnly(ctx), s, kind, o.plainRunner(kind))
}

// machineFeedbackBlock is what the author's prompt carries from the stage's automatic reviews: the active feedback of
// its pre_completion helpers and of verify, each under its own heading. Fresh and resumed sessions get it alike.
func (o *Orchestrator) machineFeedbackBlock(stageID string) string {
	stageDir := o.stageDirFor(stageID)
	block := ""
	if text, ok := o.activePreCompletionFeedback(stageID); ok {
		block = "\n\n" + preCompletionFeedbackHeading + "\n\n" + text
	}
	return block + o.verifyFeedbackBlock(stageDir)
}

// activePreCompletionFeedback is the feedback of the stage's committed needs_changes round. The journal picks it, not
// the file: a finisher that lost its decision, or a round of an earlier cycle, may have left one on disk.
func (o *Orchestrator) activePreCompletionFeedback(stageID string) (string, bool) {
	if o.opts.Store == nil {
		return "", false
	}
	p := o.opts.Store.Snapshot().Stages[stageID].PreCompletion
	if p == nil || p.Decision != state.PreCompletionNeedsChanges {
		return "", false
	}
	return stagefiles.LoadPreCompletionFeedbackOf(o.stageDirFor(stageID), p.Round, p.RoundSeq)
}

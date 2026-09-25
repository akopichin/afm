package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// errReviewPausePoisoned marks a corrupt, un-recoverable resuming review-pause
// round (see recoverReviewPause). Run() checks for it via errors.Is to skip
// normal bootstrap entirely — a poisoned run must not run any scheduling side
// effects, only stay alive (dashboard + question poller) for manual operator
// recovery.
var errReviewPausePoisoned = errors.New("review-pause resume transaction poisoned")

// enterReviewPausePoison puts the orchestrator into the in-memory fail-closed
// poison state: hold new activations AND cache a synthetic resuming marker so
// reviewTxnActive()/ReviewState()/shouldExit()/rejectIfReviewPaused all treat
// the round as active. Paired with a durable state.WritePoisonMarker so the
// state survives restarts.
func (o *Orchestrator) enterReviewPausePoison() {
	o.activationHeld.Store(true)
	o.reviewMarker.Store(&state.PauseMarker{Version: 1, State: state.PauseStateResuming})
}

// recoverReviewPause re-establishes review-pause state from disk BEFORE
// ordinary bootstrap (startPlanningForPending) runs — otherwise the normal
// scheduler could resume a stage a review round had frozen, racing the
// review operator. It reads the durable notes-pause.json marker
// (state.ReadNotesPauseMarker) and reacts per state:
//
//   - absent: no review pause was in flight — nil, nothing to do.
//   - paused: PauseFlow committed the hold but afm restarted before an
//     operator finished the round — re-establish activationHeld and cache
//     the marker (o.reviewMarker) exactly as PauseFlow itself would; the
//     owned stages are already durably StatusPaused in the event log, so
//     nothing else needs to happen here.
//   - resuming: InjectNotesAndResume/CancelNotesAndResume committed the
//     durable state=resuming transition (and, for inject, wrote
//     feedback.md) but crashed before finishing the actual resume —
//     runResumeTransaction is idempotent (SaveFeedbackOnce's op-id
//     sentinel, per-owner Trigger CAS, delete-if-exists on notes/marker), so
//     re-running it to completion is always safe, including a repeat call
//     against a transaction that had actually already finished.
//
// A corrupt marker (state.ErrCorruptPauseMarker: ReadNotesPauseMarker already
// quarantined the bad file into notes-pause.json.corrupt-<ts> and returns
// found=true with a best-effort State guess) is handled asymmetrically:
//
//   - corrupt paused: fail OPEN. The owned stages are durably StatusPaused
//     in the event log regardless of the marker (PauseFlow committed EvPause
//     before ever writing the marker file) — they simply wait for a manual
//     Continue. There's no marker left to re-cache, but nothing is lost
//     either. Logged and returns nil so ordinary bootstrap proceeds.
//   - corrupt resuming: fail CLOSED. This marker may be the ONLY record that
//     a resume transaction was in flight — e.g. feedback.md may already have
//     been delivered to the target — and we cannot safely guess which
//     owners were already resumed from a best-effort state guess alone. We
//     keep activationHeld set (blocking new activations) and return an
//     error instead of silently falling through to normal scheduling, so
//     the caller can surface it prominently and leave the hold in place for
//     an operator to resolve by hand.
func (o *Orchestrator) recoverReviewPause(ctx context.Context) error {
	// A durable poison breadcrumb from a PRIOR startup that found a corrupt
	// resuming marker: re-establish the fail-closed poison state on EVERY
	// restart until an operator clears the breadcrumb. Without this the poison
	// was in-memory only and evaporated on the next restart (the corrupt
	// canonical marker having already been quarantined), silently returning the
	// run to normal scheduling.
	if state.HasPoisonMarker(o.opts.RunDir) {
		o.enterReviewPausePoison()
		return fmt.Errorf("review-pause: %w (poison breadcrumb present in %q; clear it after manual recovery)", errReviewPausePoisoned, o.opts.RunDir)
	}

	m, found, err := state.ReadNotesPauseMarker(o.opts.RunDir)
	if !found {
		return nil // no review pause was in flight
	}
	switch {
	case errors.Is(err, state.ErrCorruptPauseMarker):
		if m.State == state.PauseStateResuming {
			// Fail CLOSED with a DURABLE poison breadcrumb (survives restarts) plus
			// a real in-memory poison marker. The in-memory marker keeps
			// reviewTxnActive() true this process — so /api/status reports
			// "resuming", every public control action is rejected with
			// ErrFlowPaused, and shouldExit() refuses to finalize. The durable
			// breadcrumb makes recovery re-enter this state on every subsequent
			// restart (the corrupt canonical marker itself was already quarantined
			// by ReadNotesPauseMarker and would otherwise be gone). The run stays
			// visible to control policy and finalization until an operator resolves
			// it by hand and clears the breadcrumb.
			_ = state.WritePoisonMarker(o.opts.RunDir, "corrupt resuming review-pause marker; owners may be partially resumed")
			o.enterReviewPausePoison()
			return fmt.Errorf("review-pause: %w (corrupt resuming marker quarantined in %q; owners may be partially resumed)", errReviewPausePoisoned, o.opts.RunDir)
		}
		log.Printf("review-pause: corrupt paused marker quarantined in %q, continuing without review mode", o.opts.RunDir)
		return nil
	case err != nil:
		return fmt.Errorf("review-pause: %w", err)
	}

	switch m.State {
	case state.PauseStatePaused:
		o.activationHeld.Store(true)
		o.reviewMarker.Store(&m)
		return nil
	case state.PauseStateResuming:
		o.reviewMarker.Store(&m)
		o.activationHeld.Store(false)
		// Idempotent: SaveFeedbackOnce sentinel + per-owner status dispatch +
		// delete-if-exists. On failure keep the hold and the marker so ordinary
		// bootstrap doesn't resume owners a second time and the run stays
		// fail-closed until a later start retries the (now no-op) transaction.
		if err := o.runResumeTransaction(ctx, m); err != nil {
			o.activationHeld.Store(true)
			return fmt.Errorf("review-pause: resume transaction incomplete (hold retained): %w", err)
		}
		return nil
	default:
		return fmt.Errorf("review-pause: unknown marker state %q", m.State)
	}
}

// autoRecoverFailedStages resets every stage currently in StatusFailed back
// to Pending when auto_recover is enabled (default true), so a run
// interrupted by a killed process/container resumes automatically instead of
// requiring manual `afm retry` on each failed stage. All failed stages are
// reset regardless of failure reason (context canceled vs a genuine bug are
// treated identically — see the design doc for why). Order does not matter
// here: the reset stages re-enter the Pending flow in startPlanningForPending
// below, which already gates on depsDone(), so depends_on order falls out on
// its own without any extra bookkeeping.
func (o *Orchestrator) autoRecoverFailedStages() {
	if !o.opts.Config.IsAutoRecover() {
		return
	}
	for _, s := range o.opts.Stages {
		if o.opts.Store.Get(s.ID) != state.StatusFailed {
			continue
		}
		if s.Interactive {
			clearInteractiveSessions(filepath.Join(o.opts.RunDir, s.ID))
		}
		if _, ok := o.Trigger(s.ID, bus.EvManualRetry, bus.GuardCtx{}, "auto_recover"); ok {
			log.Printf("auto_recover: stage %q failed -> pending", s.ID)
		}
	}
}

// startPlanningForPending restores interrupted work, then schedules newly
// unblocked stages. Completed stages and human decisions remain untouched.
func (o *Orchestrator) startPlanningForPending(ctx context.Context) {
	o.autoRecoverFailedStages()
	for _, s := range o.opts.Stages {
		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		// After-hooks do not change stage status, so their recovery marker
		// takes precedence over the status-based dispatch (including done).
		if pending, ok := readHookPending(stageDir); ok && pending.Hook == "after" {
			o.resumeAfterHook(ctx, s)
			continue
		}

		current := o.opts.Store.Get(s.ID)
		switch current {
		case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady, state.StatusPaused:
			continue
		case state.StatusAwaitingUserInput:
			// These continuations may reach verify; both must register a lease.
			o.spawnAgentLeased(ctx, s, o.resumeInteractiveAgent)
		case state.StatusHookFailed:
			o.spawnAgentLeased(ctx, s, o.resumeHookFailedWait)
		case state.StatusRetrying:
			if !s.NeedsPlanning() {
				// Restart the execution path through ready, including its before-hook.
				// A completion marker alone cannot bypass a configured verify gate.
				if s.Verify.IsEmpty() && stagefiles.CheckCompletion(stageDir, ".", s) == nil {
					o.Trigger(s.ID, bus.EvComplete, bus.GuardCtx{}, "recovered .done")
					o.maybeRunAfterHook(ctx, s.ID)
				} else {
					o.Trigger(s.ID, bus.EvReady, bus.GuardCtx{}, "retry recovery")
				}
				continue
			}
			fallthrough
		case state.StatusRevising, state.StatusRunning:
			if o.activationBlocked() {
				continue
			}
			if _, done := o.reviewResumed.Load(s.ID); done {
				continue // review-pause recovery already restarted this owner
			}
			o.resumeStageAtStatus(ctx, s, current)
		default:
			if !s.NeedsPlanning() {
				o.activatePrePlannedStage(s)
				continue
			}
			if o.recoverPlan(ctx, s) {
				continue
			}
			if current == state.StatusPending {
				if !s.EagerPlanning && !o.depsDone(s) {
					continue
				}
				if o.shouldGateAutoRun(s) {
					o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
					continue
				}
			}
			if o.activationBlocked() {
				continue
			}
			o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
			o.spawnKind(ctx, s, kindPlanning, o.runPlanningAgent)
		}
	}

	o.failBlockedStages()
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
}

// recoverPlan adopts a complete plan left on disk and applies the stage's
// approval policy. Planning recovery never runs the execution verify gate.
func (o *Orchestrator) recoverPlan(ctx context.Context, s flow.Stage) bool {
	if stagefiles.CheckPlanCompletion(filepath.Join(o.opts.RunDir, s.ID)) != nil {
		return false
	}
	o.Trigger(s.ID, bus.EvPlanReady, bus.GuardCtx{}, "recovered plan.md")
	o.autoApproveIfConfigured(ctx, s)
	return true
}

func (o *Orchestrator) resumePlanningStage(ctx context.Context, s flow.Stage) {
	if o.recoverPlan(ctx, s) {
		return
	}
	o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "")
	o.spawnKind(ctx, s, kindPlanning, o.runPlanningAgent)
}

// resumeStageAtStatus is shared by startup and Continue: the recorded status
// describes work whose process is no longer running.
func (o *Orchestrator) resumeStageAtStatus(ctx context.Context, s flow.Stage, status state.StageStatus) {
	switch status {
	case state.StatusPlanning:
		o.resumePlanningStage(ctx, s)
	case state.StatusRunning, state.StatusRetrying:
		o.resumeExecutionStage(ctx, s, status)
	case state.StatusRevising:
		switch o.detectInterruptedPhase(filepath.Join(o.opts.RunDir, s.ID)) {
		case phaseImplementation:
			o.spawnKind(ctx, s, kindImplementation, o.runImplementationWithFeedback)
		case phaseReview:
			o.spawnKind(ctx, s, kindReview, o.runReviewWithFeedback)
		case phaseAutonomous:
			o.spawnKind(ctx, s, kindAutonomous, o.runAutonomousWithFeedback)
		default:
			o.spawnKind(ctx, s, kindPlanning, o.runPlanningWithFeedback)
		}
	default:
		// Other statuses either wait for a decision or use normal scheduling.
	}
}

// resumeExecutionStage probes the author's output once. A configured verify
// gate always requires a fresh execution pass, even when the output exists.
// Retrying without output may instead be an interrupted planning attempt.
func (o *Orchestrator) resumeExecutionStage(ctx context.Context, s flow.Stage, status state.StageStatus) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	kind := o.executionKind(s)
	reason := "recovered .done"
	var completionErr error
	if kind == kindAutonomous && !s.IsScript() {
		completionErr = stagefiles.CheckAutonomousCompletion(stageDir)
		reason = "recovered execution_summary.md"
	} else {
		completionErr = stagefiles.CheckCompletion(stageDir, ".", s)
	}
	if completionErr == nil && s.Verify.IsEmpty() {
		o.completeStage(ctx, s.ID, status, reason)
		return
	}

	if status == state.StatusRunning || kind == kindAutonomous || completionErr == nil {
		if s.IsScript() {
			o.concurrency.SpawnAgent(ctx, s, o.withBeforeHook(o.runScriptStage))
		} else {
			// Resumed agents continue their work without rerunning before-hooks.
			o.spawnKind(ctx, s, kind, o.plainRunner(kind))
		}
		return
	}

	if s.NeedsPlanning() && o.recoverPlan(ctx, s) {
		return
	}
	o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{}, "restart after retry")
	o.spawnKind(ctx, s, kindPlanning, o.runPlanningAgent)
}

// resumeInteractiveAgent re-runs the agent of the phase whose
// session.json exists most recently. The phase is detected by looking at
// mtimes of <phase>.session.json files in the stage directory.
func (o *Orchestrator) resumeInteractiveAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	phase := o.detectInterruptedPhase(stageDir)

	switch phase {
	case phasePlanning:
		o.Trigger(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phasePlanning}, "resume interactive")
		o.runPlanningAgent(ctx, s)
	case phaseReview:
		// Review runs after implementation completes; if we see review session
		// open, the stage was paused inside review. Fall through to implementation
		// agent which will re-trigger review at the end.
		fallthrough
	default:
		o.Trigger(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseImplementation}, "resume interactive")
		o.runImplementationAgent(ctx, s)
	}
}

func (o *Orchestrator) detectInterruptedPhase(stageDir string) string {
	var latestPhase string
	var latestMtime time.Time
	for _, p := range flow.Phases() {
		phase := string(p)
		fi, err := os.Stat(stagefiles.SessionFile(stageDir, phase))
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMtime) {
			latestMtime = fi.ModTime()
			latestPhase = phase
		}
	}
	return latestPhase
}

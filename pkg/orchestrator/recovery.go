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

const (
	reasonRecoveredDone    = "recovered .done"
	reasonRecoveredSummary = "recovered execution_summary.md"
)

// errReviewPausePoisoned marks a corrupt, un-recoverable resuming review-pause round.
var errReviewPausePoisoned = errors.New("review-pause resume transaction poisoned")

// enterReviewPausePoison holds activation and caches a synthetic resuming marker; WritePoisonMarker
// persists the hold across restarts.
func (o *Orchestrator) enterReviewPausePoison() {
	o.activationHeld.Store(true)
	o.reviewMarker.Store(&state.PauseMarker{Version: 1, State: state.PauseStateResuming})
}

// recoverReviewPause restores the hold before scheduling or resumes an interrupted transaction. Corrupt
// paused fails open; corrupt resuming stays held.
func (o *Orchestrator) recoverReviewPause(ctx context.Context) error {
	// A durable poison breadcrumb from a PRIOR startup that found a corrupt resuming marker: re-establish the
	// fail-closed poison state on EVERY restart until an operator clears the breadcrumb.
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
			// Fail CLOSED with a DURABLE poison breadcrumb plus a real in-memory poison marker.
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
		// Idempotent: SaveFeedbackOnce sentinel + per-owner status dispatch + delete-if-exists.
		if err := o.runResumeTransaction(ctx, m); err != nil {
			o.activationHeld.Store(true)
			return fmt.Errorf("review-pause: resume transaction incomplete (hold retained): %w", err)
		}
		return nil
	default:
		return fmt.Errorf("review-pause: unknown marker state %q", m.State)
	}
}

// autoRecoverFailedStages resets all failed stages to pending when enabled; normal activation reapplies
// dependencies.
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
		// An executor already started by this process owns the stage: this pass's cascade, a control action
		// accepted before Run, or review-pause recovery. Its status cannot distinguish it from crashed work.
		if o.concurrency.Spawned(s.ID) {
			continue
		}
		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		// After-hooks do not change stage status, so their recovery marker
		// takes precedence over the status-based dispatch (including done).
		if pending, ok := readHookPending(stageDir); ok && pending.Hook == hookAfter {
			o.resumeAfterHook(ctx, s)
			continue
		}

		o.continueMu.Lock()
		snapshot := o.opts.Store.Stage(s.ID)
		current := snapshot.Status
		_, continuedHere := o.continuedThisProcess.Load(s.ID)
		o.continueMu.Unlock()
		if continuedHere {
			continue // Continue already owns this stage's restart in this process
		}
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
				completionErr := stagefiles.CheckCompletion(stageDir, ".", s)
				reason := reasonRecoveredDone
				if o.executionKind(s) == kindAutonomous && !s.IsScript() {
					completionErr = stagefiles.CheckAutonomousCompletion(stageDir)
					reason = reasonRecoveredSummary
				}
				fence := o.recoveryFence(s, snapshot.OwnerEpoch)
				if s.Verify.IsEmpty() && completionErr == nil {
					o.completeStage(ctx, s.ID, current, reason, fence)
				} else {
					o.Trigger(s.ID, bus.EvReady, bus.GuardCtx{OwnerEpoch: fence}, "retry recovery")
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
			o.resumeStageAtStatus(ctx, s, current, snapshot.OwnerEpoch)
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
func (o *Orchestrator) resumeStageAtStatus(ctx context.Context, s flow.Stage, status state.StageStatus, epoch uint64) {
	switch status {
	case state.StatusPlanning:
		o.resumePlanningStage(ctx, s)
	case state.StatusRunning, state.StatusRetrying:
		o.resumeExecutionStage(ctx, s, status, epoch)
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

// recoveryFence uses the dispatch snapshot/Continue seq, never a later reread.
// A resume delayed by filesystem probing cannot adopt a successor's ownership.
func (o *Orchestrator) recoveryFence(s flow.Stage, epoch uint64) *uint64 {
	if o.executionKind(s) == kindAutonomous && !s.IsScript() {
		return &epoch
	}
	return nil
}

// resumeExecutionStage probes the author's output once. A configured verify gate always requires a fresh
// execution pass, even when the output exists (guard: completionErr == nil && s.Verify.IsEmpty()).
// Retrying without output may instead be an interrupted planning attempt.
func (o *Orchestrator) resumeExecutionStage(ctx context.Context, s flow.Stage, status state.StageStatus, epoch uint64) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	kind := o.executionKind(s)
	reason := reasonRecoveredDone
	var completionErr error
	if kind == kindAutonomous && !s.IsScript() {
		completionErr = stagefiles.CheckAutonomousCompletion(stageDir)
		reason = reasonRecoveredSummary
	} else {
		completionErr = stagefiles.CheckCompletion(stageDir, ".", s)
	}
	if completionErr == nil && s.Verify.IsEmpty() {
		o.completeStage(ctx, s.ID, status, reason, o.recoveryFence(s, epoch))
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

// resumeInteractiveAgent re-runs the agent of the phase whose session.json exists most recently.
func (o *Orchestrator) resumeInteractiveAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	phase := o.detectInterruptedPhase(stageDir)

	switch phase {
	case phasePlanning:
		if _, ok := o.Trigger(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phasePlanning, NewOwner: true}, "resume interactive"); !ok {
			return
		}
		o.runPlanningAgent(ctx, s)
	case phaseReview:
		// Review runs after implementation completes; if we see review session open, the stage was paused inside
		// review. Fall through to implementation agent which will re-trigger review at the end.
		fallthrough
	default:
		if _, ok := o.Trigger(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phaseImplementation, NewOwner: true}, "resume interactive"); !ok {
			return
		}
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

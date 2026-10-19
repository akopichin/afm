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
	m, resuming, err := o.loadReviewPause()
	if err != nil || !resuming {
		return err
	}
	return o.resumeReviewPause(ctx, m)
}

// loadReviewPause is the half of recoverReviewPause that spawns nothing: it reads the marker and restores the
// review state (poison, hold). resuming reports a round whose owners
// resumeReviewPause must still respawn.
func (o *Orchestrator) loadReviewPause() (m state.PauseMarker, resuming bool, err error) {
	// A durable poison breadcrumb from a PRIOR startup that found a corrupt resuming marker: re-establish the
	// fail-closed poison state on EVERY restart until an operator clears the breadcrumb.
	if state.HasPoisonMarker(o.opts.RunDir) {
		o.enterReviewPausePoison()
		return m, false, fmt.Errorf("review-pause: %w (poison breadcrumb present in %q; clear it after manual recovery)", errReviewPausePoisoned, o.opts.RunDir)
	}

	m, found, err := state.ReadNotesPauseMarker(o.opts.RunDir)
	if !found {
		return m, false, nil // no review pause was in flight
	}
	switch {
	case errors.Is(err, state.ErrCorruptPauseMarker):
		if m.State == state.PauseStateResuming {
			// Fail CLOSED with a DURABLE poison breadcrumb plus a real in-memory poison marker.
			_ = state.WritePoisonMarker(o.opts.RunDir, "corrupt resuming review-pause marker; owners may be partially resumed")
			o.enterReviewPausePoison()
			return m, false, fmt.Errorf("review-pause: %w (corrupt resuming marker quarantined in %q; owners may be partially resumed)", errReviewPausePoisoned, o.opts.RunDir)
		}
		log.Printf("review-pause: corrupt paused marker quarantined in %q, continuing without review mode", o.opts.RunDir)
		return m, false, nil
	case err != nil:
		return m, false, fmt.Errorf("review-pause: %w", err)
	}

	switch m.State {
	case state.PauseStatePaused:
		o.activationHeld.Store(true)
		o.reviewMarker.Store(&m)
		return m, false, nil
	case state.PauseStateResuming:
		o.reviewMarker.Store(&m)
		o.activationHeld.Store(false)
		return m, true, nil
	default:
		return m, false, fmt.Errorf("review-pause: unknown marker state %q", m.State)
	}
}

// resumeReviewPause finishes a round loadReviewPause found in "resuming": it respawns the owners. Idempotent:
// SaveFeedbackOnce sentinel + per-owner status dispatch + delete-if-exists.
func (o *Orchestrator) resumeReviewPause(ctx context.Context, m state.PauseMarker) error {
	if err := o.runResumeTransaction(ctx, m); err != nil {
		o.activationHeld.Store(true)
		return fmt.Errorf("review-pause: resume transaction incomplete (hold retained): %w", err)
	}
	return nil
}

// autoRecoverFailedStages resets all failed stages to pending when enabled; normal activation reapplies
// dependencies.
func (o *Orchestrator) autoRecoverFailedStages() {
	if !o.opts.Config.IsAutoRecover() {
		return
	}
	for _, s := range o.reg().Stages() {
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
	for _, s := range o.reg().Stages() {
		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		// After-hooks do not change stage status, so their recovery marker
		// takes precedence over the status-based dispatch (including done).
		if pending, ok := readHookPending(stageDir); ok && pending.Hook == hookAfter {
			o.resumeAfterHook(ctx, s)
			continue
		}

		o.continueMu.Lock()
		current := o.opts.Store.Get(s.ID)
		_, continuedHere := o.continuedThisProcess.Load(s.ID)
		o.continueMu.Unlock()
		if continuedHere {
			continue // Continue already owns this stage's restart in this process
		}
		switch current {
		// StatusAwaitingPeer (Agent Mesh Phase 5, T5.3): nothing for THIS loop to do — unlike
		// AwaitingUserInput, a stage here is correctly parked until its wait resolves. Re-deriving "is it
		// already ready" (a reply committed right before a crash) is recoverMeshWaits' job, called alongside
		// recoverAssistants/recoverFeedbackIntents below — falling through to `default` here would wrongly
		// treat it as needing a fresh planning spawn (EvStartPlanning is not even a legal transition from
		// awaiting_peer, but default unconditionally calls spawnKind regardless of whether the preceding
		// Trigger succeeded).
		case state.StatusDone, state.StatusFailed, state.StatusAwaitingApproval, state.StatusReady, state.StatusPaused, state.StatusAwaitingPeer:
			continue
		case state.StatusAwaitingAssistants:
			// Этап 7 (T7.2/T7.3): without this case the stage would fall into `default` below and be
			// mishandled as a plannable/pending-like stage (activatePrePlannedStage/EvStartPlanning — both
			// FSM-rejected from this status, but still the wrong intent). The main executor already finished;
			// nothing to (re)plan or (re)run here — just re-check the durable barrier in case the blocking
			// required task resolved right before the process went down (TestBarrier_RestoredAfterRestart).
			o.maybeCompleteAfterAssistants(ctx, s.ID)
			continue
		case state.StatusAwaitingUserInput:
			// These continuations may reach verify; both must register a lease.
			o.spawnAgentLeased(ctx, s, o.resumeInteractiveAgent)
		case state.StatusHookFailed:
			o.spawnAgentLeased(ctx, s, o.resumeHookFailedWait)
		case state.StatusRetrying:
			if !s.NeedsPlanning() {
				// Restart the execution path through ready, including its before-hook.
				// A completion marker alone cannot bypass a configured verify gate OR the required-assistants
				// barrier (Этап 7, T7.3: completeStage — not a bare Trigger(EvComplete) — checks
				// RequiredBlocker and diverts to awaiting_assistants exactly like the live completion path
				// would have; TestResumeByDoneMarkerDoesNotBypassBarrier).
				if s.Verify.IsEmpty() && stagefiles.CheckCompletion(stageDir, ".", s) == nil {
					o.completeRecoveredStage(ctx, s, state.StatusRetrying, "recovered .done")
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

// executionPhases maps an execution kind to the phase its turn runs (the phase a park resumes into).
var executionPhases = map[string]string{kindAutonomous: phaseAutonomous, kindImplementation: phaseImplementation, kindReview: phaseReview}

// parkInterruptedTurn parks a running stage whose turn ended for waits but whose process stopped before its exit
// parked it — Ctrl+C right after `afm agent wait` answered "yield", or a Pause that interrupted it then. Rerunning
// the author would ask again; a completion marker it left must not finish the stage past the wait. The park is
// what the exit would have committed (interruptedTurnEpisode), and its continuation is decided at once.
func (o *Orchestrator) parkInterruptedTurn(ctx context.Context, s flow.Stage) bool {
	rs := o.opts.Store.Snapshot()
	episode := interruptedTurnEpisode(rs, s.ID)
	if len(episode) == 0 {
		return false
	}
	phase, ok := executionPhases[o.executionKind(s)]
	if !ok {
		return false
	}
	epoch := rs.Stages[s.ID].Epoch
	o.prePeerPhase.Store(s.ID, phase)
	saveMeshPeerWaitPhase(o.stageDir(s.ID), phase)
	guard := bus.GuardCtx{Phase: phase, ExpectFrom: state.StatusRunning, ExpectEpoch: &epoch, PeerWaits: episode}
	if _, ok := o.Trigger(s.ID, bus.EvAwaitPeer, guard, "recovered: the turn had yielded on waits"); !ok {
		o.prePeerPhase.CompareAndDelete(s.ID, phase)
		return true // the stage moved on meanwhile; whoever moved it owns it
	}
	o.continueParkedStage(ctx, s.ID)
	return true
}

// resumeStageAtStatus is shared by startup and Continue: the recorded status
// describes work whose process is no longer running.
func (o *Orchestrator) resumeStageAtStatus(ctx context.Context, s flow.Stage, status state.StageStatus) {
	switch status {
	case state.StatusPlanning:
		o.resumePlanningStage(ctx, s)
	case state.StatusRunning, state.StatusRetrying:
		if status == state.StatusRunning && o.parkInterruptedTurn(ctx, s) {
			return
		}
		o.resumeExecutionStage(ctx, s, status)
	case state.StatusAwaitingPeer:
		// Continue of a paused park: the continuation is decided on the park's waits again.
		o.continueParkedStage(ctx, s.ID)
	case state.StatusRevising:
		kind := o.revisingKind(s)
		o.spawnKind(ctx, s, kind, o.withFeedbackRunner(kind))
	case state.StatusAwaitingAssistants:
		// Shared by startup (startPlanningForPending) and Continue (PausedFrom == AwaitingAssistants, restored
		// by the FSM's EvContinue table itself before this is reached) — re-check the durable barrier, it may
		// have resolved while the process was down or the stage was paused.
		o.maybeCompleteAfterAssistants(ctx, s.ID)
	default:
		// Other statuses either wait for a decision or use normal scheduling.
	}
}

// completeRecoveredStage completes a stage whose completion marker survived a restart — its agent finished the
// turn before the process stopped. A colleague's unread message still earns it the extra turn onAgentCompleted
// would have given (agentcomm_unread.go).
func (o *Orchestrator) completeRecoveredStage(ctx context.Context, s flow.Stage, from state.StageStatus, reason string) {
	phase := phaseImplementation
	if o.executionKind(s) == kindAutonomous {
		phase = phaseAutonomous
	}
	if !s.IsScript() && o.continueForUnreadMessages(ctx, s.ID, phase, nil) {
		return
	}
	o.completeStage(ctx, s.ID, from, reason)
}

// resumeExecutionStage probes the author's output once. A configured verify gate always requires a fresh
// execution pass, even when the output exists (guard: completionErr == nil && s.Verify.IsEmpty()).
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
		o.completeRecoveredStage(ctx, s, status, reason)
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
		o.Trigger(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phasePlanning}, "resume interactive")
		o.runPlanningAgent(ctx, s)
	case phaseReview:
		// Review runs after implementation completes; if we see review session open, the stage was paused inside
		// review. Fall through to implementation agent which will re-trigger review at the end.
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

// revisingKind selects which phase a crash-interrupted Revise resumes with. Session files answer this
// precisely for an interactive stage, but runnerFor only ever writes them under s.Interactive — a
// non-interactive stage (including agents:[auto], which has no planning phase at all) has none, and used to
// fall through to planning unconditionally, silently re-planning an already-implemented stage. Without a
// session file, the phase is derived from the stage itself instead: a Revise targeting the PLAN archives
// plan.md to plan.v<N>.md (state.VersionPlan, from reviseAwaitingApproval) and leaves no plan.md behind — so
// "stage plans AND has no plan.md" is the one case that means planning; everything else means execution.
func (o *Orchestrator) revisingKind(s flow.Stage) string {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	switch o.detectInterruptedPhase(stageDir) {
	case phaseImplementation:
		return kindImplementation
	case phaseReview:
		return kindReview
	case phaseAutonomous:
		return kindAutonomous
	}
	if s.NeedsPlanning() && stagefiles.CheckPlanCompletion(stageDir) != nil {
		return kindPlanning
	}
	return o.executionKind(s)
}

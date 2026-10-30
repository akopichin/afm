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

		// A Revise's bump belongs to the Revise, not to the status the stage has now (a Pause may have followed it).
		if err := o.finishReviseInvalidation(s.ID); err != nil {
			log.Printf("WARN: recover stage %q: invalidate the attempt its Revise replaced: %v", s.ID, err)
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
		case state.StatusAwaitingQuota:
			// Парк пережил рестарт: таймер ждёт остаток until, истёкший — будит стадию сразу, ручная проба — тоже.
			o.resumeQuotaWait(ctx, s)
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
			// A turn that yielded before its retry was scheduled continues on its waits, as with the exit of a live turn.
			if !s.IsScript() && o.parkInterruptedTurn(ctx, s, state.StatusRetrying) {
				continue
			}
			if !s.NeedsPlanning() {
				// Restart the execution path through ready, including its before-hook.
				// A completion marker alone cannot bypass a configured verify gate OR the required-assistants
				// barrier (Этап 7, T7.3: completeStage — not a bare Trigger(EvComplete) — checks
				// RequiredBlocker and diverts to awaiting_assistants exactly like the live completion path
				// would have; TestResumeByDoneMarkerDoesNotBypassBarrier).
				if reason, err := o.executionProbe(s, stageDir); s.Verify.IsEmpty() && err == nil {
					o.completeRecoveredStage(ctx, s, state.StatusRetrying, reason)
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

// parkInterruptedTurn parks a running or retrying stage whose turn yielded on waits but whose process stopped before
// its exit parked it (Ctrl+C, a Pause). Rerunning the author would ask again, and a completion marker it left must
// not finish the stage past the wait: the park is what the exit would have committed, continued at once if due.
func (o *Orchestrator) parkInterruptedTurn(ctx context.Context, s flow.Stage, from state.StageStatus) bool {
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
	guard := bus.GuardCtx{Phase: phase, ExpectFrom: from, ExpectEpoch: &epoch, PeerWaits: episode}
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
		if !s.IsScript() && o.parkInterruptedTurn(ctx, s, status) {
			return
		}
		o.resumeExecutionStage(ctx, s, status)
	case state.StatusAwaitingPeer:
		// Continue of a paused park: the continuation is decided on the park's waits again.
		o.continueParkedStage(ctx, s.ID)
	case state.StatusAwaitingQuota:
		// Continue of a quota pause records the manual-probe intent: the shared wake path starts the probe.
		o.resumeQuotaWait(ctx, s)
	case state.StatusRevising:
		if err := o.finishReviseInvalidation(s.ID); err != nil {
			log.Printf("WARN: recover revising stage %q: invalidate its previous attempt: %v", s.ID, err)
			return
		}
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

// finishReviseInvalidation finishes the bump of a Revise from awaiting_assistants that a crash cut off after its
// transition: without it the previous attempt's helpers and waits stay eligible. Keyed by the Revise's seq, so a
// bump that already committed is not repeated; other Revise origins invalidate nothing.
func (o *Orchestrator) finishReviseInvalidation(stageID string) error {
	seq, ok, err := o.latestReviseFromAwaitingAssistants(stageID)
	if err != nil || !ok {
		return err
	}
	opID := reviseBumpOpID(stageID, seq)
	_, committed, err := o.opts.Store.CommittedOperation(opID, opKindBumpRootGeneration)
	if err != nil {
		return err
	}
	if committed {
		o.cancelSupersededTasks(stageID) // the crash may also have come before the tasks were cancelled
		return nil
	}
	return o.bumpRootGenerationKeyed(stageID, opID)
}

// latestReviseFromAwaitingAssistants returns the seq of stageID's latest Revise when it was made from
// awaiting_assistants.
func (o *Orchestrator) latestReviseFromAwaitingAssistants(stageID string) (seq uint64, ok bool, err error) {
	history, err := o.opts.Store.History()
	if err != nil {
		return 0, false, err
	}
	for i := len(history) - 1; i >= 0; i-- {
		if t := history[i]; t.StageID == stageID && t.Event == string(bus.EvRevise) {
			return t.Seq, t.From == state.StatusAwaitingAssistants, nil
		}
	}
	return 0, false, nil
}

// completeRecoveredStage completes a stage whose completion marker survived a restart — its agent finished the
// turn before the process stopped. A wait it left open parks it first, as the exit of the live turn would; a
// colleague's unread message still earns it the extra turn onAgentCompleted would have given (agentcomm_unread.go).
func (o *Orchestrator) completeRecoveredStage(ctx context.Context, s flow.Stage, from state.StageStatus, reason string) {
	if !s.IsScript() && o.parkInterruptedTurn(ctx, s, from) {
		return
	}
	autonomous := o.executionKind(s) == kindAutonomous
	phase := phaseImplementation
	if autonomous {
		phase = phaseAutonomous
	}
	if !s.IsScript() && o.continueForUnreadMessages(ctx, s.ID, phase, nil) {
		return
	}
	if autonomous && !s.IsScript() {
		o.noteSummaryRecovered(s.ID, filepath.Join(o.opts.RunDir, s.ID))
	}
	o.completeStage(ctx, s.ID, from, reason)
}

// executionProbe is the one check of what a finished author leaves on disk, used by every restart path that completes
// a stage from its files: the summary of an autonomous stage (its .done is no proof), the .done marker otherwise.
func (o *Orchestrator) executionProbe(s flow.Stage, stageDir string) (reason string, err error) {
	if o.executionKind(s) == kindAutonomous && !s.IsScript() {
		return "recovered execution_summary.md", stagefiles.CheckAutonomousCompletion(stageDir)
	}
	return "recovered .done", stagefiles.CheckCompletion(stageDir, o.projectDir(), s)
}

// resumeExecutionStage probes the author's output once. A configured verify gate always requires a fresh
// execution pass, even when the output exists (guard: completionErr == nil && s.Verify.IsEmpty()).
// Retrying without output may instead be an interrupted planning attempt.
func (o *Orchestrator) resumeExecutionStage(ctx context.Context, s flow.Stage, status state.StageStatus) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	kind := o.executionKind(s)
	reason, completionErr := o.executionProbe(s, stageDir)
	if completionErr == nil && s.Verify.IsEmpty() {
		o.completeRecoveredStage(ctx, s, status, reason)
		return
	}

	if status == state.StatusRunning || kind == kindAutonomous || completionErr == nil {
		switch {
		case s.IsScript():
			o.concurrency.SpawnAgent(ctx, s, o.withBeforeHook(o.runScriptStage))
		case status == state.StatusRunning && o.resumeWokenVerify(ctx, s):
			// the saved verify step is repeated without the author
		default:
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

// resumeWokenVerify finishes the wake of a verify park that a restart interrupted between EvQuotaReset and the
// spawn: the saved verify step is repeated without the author, exactly as wakeFromQuota would have started it.
// false — no such wake is pending, the stage recovers as an ordinary running turn.
func (o *Orchestrator) resumeWokenVerify(ctx context.Context, s flow.Stage) bool {
	st := o.opts.Store.Snapshot().Stages[s.ID]
	q := st.Quota
	if q == nil || !q.VerifyResume || q.Target != state.QuotaTargetVerify || !o.verifyStepStillParked(s, q) {
		return false
	}
	kind, ok := kindOfPhase(o.verifyWakePhase(s, q))
	if !ok {
		return false
	}
	o.spawnKind(withVerifyResume(ctx, *q, st.Epoch), s, kind, o.turnRunner(kind, q.Feedback))
	return true
}

// verifyStepStillParked — шаг парка ещё не вынес вердикт: его manifest-запись так и осталась awaiting_quota. Намерение
// VerifyResume живёт в журнале и не знает, чем закончился повторённый шаг (отклонение не пишет переход: коррекция
// автора идёт в том же статусе) — по settled-шагу recovery обычный. Нечитаемый manifest не отменяет повтор:
// RunVerification закроется ошибкой verify storage.
func (o *Orchestrator) verifyStepStillParked(s flow.Stage, q *state.QuotaWait) bool {
	m, err := loadVerifyManifest(o.stageDir(s.ID), q.VerifyID)
	if err != nil || q.VerifyStep < 1 || q.VerifyStep > len(m.Steps) {
		return true
	}
	return m.Steps[q.VerifyStep-1].Outcome == verifyOutcomeQuotaWait
}

// resumeInteractiveAgent re-runs the agent of an unanswered question: the planning one when its session is the latest,
// otherwise the stage's execution turn. A review session means the stage was paused inside review, which the
// implementation agent re-triggers at its end; an autonomous stage has no session to tell it apart, so its kind decides.
func (o *Orchestrator) resumeInteractiveAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	phase := o.detectInterruptedPhase(stageDir)
	if phase != phasePlanning {
		phase = executionPhases[o.executionKind(s)]
	}

	epoch, ok := o.grantTurn(s.ID, bus.EvUserAnswered, bus.GuardCtx{Phase: phase}, "resume interactive")
	if !ok {
		return // the answer resumed it first
	}
	kind, _ := kindOfPhase(phase)
	o.plainRunner(kind)(withTurnEpoch(ctx, epoch), s)
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

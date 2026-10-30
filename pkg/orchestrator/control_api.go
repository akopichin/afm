package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// ErrFlowPaused is returned by any public forward-driving control action while a review-pause transaction
// is active.
var ErrFlowPaused = errors.New("flow_paused")

// rejectIfReviewPaused rejects forward control actions before any stage access while a review transaction
// is active.
func (o *Orchestrator) rejectIfReviewPaused() error {
	if o.reviewTxnActive() {
		return ErrFlowPaused
	}
	return nil
}

// FailStage marks a stage as failed with a reason.
func (o *Orchestrator) FailStage(stageID, reason string) {
	o.Trigger(stageID, bus.EvFail, bus.GuardCtx{}, reason)
	o.failBlockedStages()
}

// CancelDialog fails a stage that's awaiting user input because the user cancelled the dialog from the
// dashboard.
func (o *Orchestrator) CancelDialog(stageID string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	o.FailStage(stageID, "cancelled by user")
	return nil
}

// NotifyAnswer is called by the HTTP handler when the user submits an answer.
func (o *Orchestrator) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	return o.resumeAfterAnswer(stageID, phase, qID, answer, true)
}

// resumeAfterAnswer drives a stage out of awaiting_user_input once answer.json is already on disk. If the agent
// will not read the answer itself, we publish to the critical bus so onUserAnswered starts a continuation.
func (o *Orchestrator) resumeAfterAnswer(stageID, phase, qID, answer string, publishUI bool) error {
	if o.answerToLiveAgent(stageID, phase, qID, answer, publishUI) {
		return nil
	}
	return o.critical.Publish(context.Background(), bus.Event{
		Type:    bus.EventUserAnswered,
		StageID: stageID,
		Data:    map[string]any{keyID: qID, keyPhase: phase, keyAnswer: answer},
	})
}

// answerToLiveAgent resumes a stage whose agent will read the answer itself (liveAgents). A turn that has not started
// its agent or has already decided its exit reads nothing: the answer must start the continuation. Decided under
// answerMu, the lock of every dialog decision of a turn.
func (o *Orchestrator) answerToLiveAgent(stageID, phase, qID, answer string, publishUI bool) bool {
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	owner, live := o.currentLiveAgent(stageID)
	if !live {
		return false
	}
	if o.beforeLiveAnswerHook != nil {
		o.beforeLiveAnswerHook(stageID)
	}
	guardPhase := phase
	if saved, ok := o.preAskPhase.Load(stageID); ok {
		guardPhase = saved.(string)
	}
	_, seq, applied := o.triggerWithSeq(stageID, bus.EvUserAnswered, bus.GuardCtx{Phase: guardPhase, OwnerEpoch: &owner.epoch}, "")
	if !applied {
		// An answer can arrive before the poller surfaces its question. That live
		// owner still reads it; a stale marker or lost CAS must enqueue a restart.
		st := o.opts.Store.Stage(stageID)
		if st.OwnerEpoch != owner.epoch || (st.Status != state.StatusRunning && st.Status != state.StatusPlanning) {
			o.liveAgents.CompareAndDelete(stageID, owner)
			return false
		}
	} else {
		o.preAskPhase.Delete(stageID)
	}

	if publishUI {
		o.ui.Publish(bus.Event{Type: bus.EventUserAnswered, StageID: stageID, Data: map[string]any{
			keyID: qID, keyPhase: phase, keyAnswer: answer,
		}, Seq: seq})
	}
	return true
}

// currentLiveAgent is called under answerMu at both delivery and queued restart.
// Registration may have preceded a Pause/Continue; its presence alone is no proof.
func (o *Orchestrator) currentLiveAgent(stageID string) (*attemptOwner, bool) {
	marker, ok := o.liveAgents.Load(stageID)
	if !ok {
		return nil, false
	}
	owner, ok := marker.(*attemptOwner)
	if !ok || !o.epochIsCurrent(stageID, owner) {
		o.liveAgents.CompareAndDelete(stageID, marker)
		return nil, false
	}
	return owner, true
}

// autoApproveIfConfigured honors auto_approve regardless of dashboard or --require-approval; returns
// whether approval occurred.
func (o *Orchestrator) autoApproveIfConfigured(ctx context.Context, stage flow.Stage) bool {
	if !stage.AutoApprove {
		return false
	}
	log.Printf("auto_approve: auto-approving plan for stage %q", stage.ID)
	o.approveStage(ctx, stage.ID)
	return true
}

// approveStage долговечно переводит стадию из awaiting_approval и запускает побочные эффекты.
// Идемпотентна: если стадия уже не в awaiting_approval, только до-запускает побочные эффекты.
func (o *Orchestrator) approveStage(ctx context.Context, stageID string) {
	if o.currentStatus(stageID) == state.StatusAwaitingApproval {
		stage := o.graph.Stage(stageID)
		if stage != nil && !stage.HasAgent(flow.AgentImplementation) {
			// EvComplete из awaiting_approval может проиграть CAS конкурентному Revise на HTTP-горутине — тогда
			// стадия не завершилась, и after-hook запускать нельзя.
			if _, ok := o.Trigger(stageID, bus.EvComplete, bus.GuardCtx{}, "planning-only stage"); ok {
				o.maybeRunAfterHook(ctx, stageID)
			}
		} else {
			o.Trigger(stageID, bus.EvApprove, bus.GuardCtx{}, "")
		}
	}
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
}

// runContext returns the long-lived run-scoped context for spawning agents from HTTP-initiated actions.
func (o *Orchestrator) runContext(fallback context.Context) context.Context {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.runCtx != nil {
		return o.runCtx
	}
	return context.WithoutCancel(fallback)
}

// Approve approves a stage plan.
func (o *Orchestrator) Approve(ctx context.Context, stageID string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	o.approveStage(o.runContext(ctx), stageID)
	return nil
}

// Revise durably saves feedback and EvRevise before returning; running agents restart after interruption.
// Returns applied and transition seq.
func (o *Orchestrator) Revise(reqCtx context.Context, stageID, feedback string) (applied bool, seq uint64, err error) {
	if err := o.rejectIfReviewPaused(); err != nil {
		return false, 0, err
	}
	current := o.currentStatus(stageID)
	if current != state.StatusAwaitingApproval && current != state.StatusRunning {
		return false, 0, nil
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)

	if current == state.StatusRunning {
		_, seq, ok := o.triggerWithSeq(stageID, bus.EvRevise, bus.GuardCtx{}, feedback)
		if !ok {
			return false, 0, nil
		}
		if err := state.SaveFeedback(stageDir, feedback); err != nil {
			return false, 0, fmt.Errorf("save feedback for %s: %w", stageID, err)
		}
		if ch, ok := o.interruptChans.Load(stageID); ok {
			select {
			case ch.(chan struct{}) <- struct{}{}:
			default: // канал уже сигнализирован (двойной клик) — не блокируемся
			}
		}
		return true, seq, nil
	}

	_, seq, ok := o.triggerWithSeq(stageID, bus.EvRevise, bus.GuardCtx{}, feedback)
	if !ok {
		return false, 0, nil
	}
	if _, err := state.VersionPlan(stageDir); err != nil {
		return false, 0, fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return false, 0, fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	if stage := o.graph.Stage(stageID); stage != nil {
		// Спавним под run ctx, а не reqCtx: HTTP-хэндлер отменит reqCtx сразу
		// после возврата ответа, и агент был бы убит немедленно (см. runContext).
		o.concurrency.SpawnAgent(o.runContext(reqCtx), *stage, o.runPlanningWithFeedback)
	}
	return true, seq, nil
}

// Button resolves the named button's prompt from the flow and delivers it to the live agent via Revise.
// Unknown name or a stage with no such button is a no-op (returns nil).
func (o *Orchestrator) Button(ctx context.Context, stageID, name string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}
	prompt := stage.Buttons.Prompt(name)
	if prompt == "" {
		return nil
	}
	_, _, err := o.Revise(ctx, stageID, prompt)
	return err
}

// Pause commits paused before signaling live agents or retry backoff; interrupted agents remain paused.
func (o *Orchestrator) Pause(_ context.Context, stageID string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	switch o.currentStatus(stageID) {
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
	default:
		return nil
	}
	if stage := o.graph.Stage(stageID); stage != nil && stage.IsScript() && o.currentStatus(stageID) == state.StatusRunning {
		return nil // mid-script pause не поддержан — RunScript не принимает InterruptCh
	}

	if _, ok := o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
		return nil
	}
	// Инвалидируем before-hook: изменившийся generation запрещает запуск mainFn после Pause; повторный старт
	// делает Continue.
	o.bumpPauseGen(stageID)
	if ch, ok := o.interruptChans.Load(stageID); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
		default: // уже сигнализирован — не блокируемся
		}
	}
	return nil
}

// Continue activates stages paused from pending; other statuses resume through the same recovery path as a
// restarted run.
func (o *Orchestrator) Continue(reqCtx context.Context, stageID string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	o.continueMu.Lock()
	if o.currentStatus(stageID) != state.StatusPaused {
		o.continueMu.Unlock()
		return nil
	}
	stage := o.graph.Stage(stageID)
	if stage == nil {
		o.continueMu.Unlock()
		return nil
	}
	pausedFrom := o.opts.Store.PausedFrom(stageID)
	o.continuedThisProcess.Store(stageID, struct{}{})
	to, epoch, ok := o.triggerWithSeq(stageID, bus.EvContinue, bus.GuardCtx{PausedFrom: pausedFrom}, "")
	if !ok {
		o.continuedThisProcess.Delete(stageID)
		o.continueMu.Unlock()
		return nil
	}
	o.continueMu.Unlock()

	ctx := o.runContext(reqCtx) // не reqCtx — иначе HTTP-хендлер убьёт агента при возврате ответа
	if pausedFrom == state.StatusPending {
		o.tryActivatePrePlanned(ctx)
		o.startPlanningForUnblocked(ctx)
		return nil
	}

	o.resumeStageAtStatus(ctx, *stage, to, epoch)
	return nil
}

// Retry retries a failed stage by transitioning it to pending and restarting
// (синхронно и долговечно).
func (o *Orchestrator) Retry(ctx context.Context, stageID string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	o.retryStage(o.runContext(ctx), stageID)
	return nil
}

// RetryHook resumes a stage currently blocked on a failed before/after hook
// by re-running that hook's 3x/1-2-3s retry cycle.
func (o *Orchestrator) RetryHook(stageID string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	if !o.resolveHook(stageID, hookDecisionRetry) {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}
	return nil
}

// SkipHook resumes a stage currently blocked on a failed before/after hook
// by skipping it entirely.
func (o *Orchestrator) SkipHook(stageID string) error {
	if err := o.rejectIfReviewPaused(); err != nil {
		return err
	}
	if !o.resolveHook(stageID, hookDecisionSkip) {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}
	return nil
}

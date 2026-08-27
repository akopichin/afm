package orchestrator

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// FailStage marks a stage as failed with a reason.
func (o *Orchestrator) FailStage(stageID, reason string) {
	o.Trigger(stageID, bus.EvFail, bus.GuardCtx{}, reason)
	o.failBlockedStages()
}

// CancelDialog fails a stage that's awaiting user input because the user
// cancelled the dialog from the dashboard. Thin wrapper so server.SecondaryActions
// can require a real method instead of the HTTP layer building a closure around
// FailStage with a hardcoded reason string.
func (o *Orchestrator) CancelDialog(stageID string) error {
	o.FailStage(stageID, "cancelled by user")
	return nil
}

// NotifyAnswer is called by the HTTP handler when the user submits an answer.
// It is a thin wrapper over resumeAfterAnswer with the dialog-feed UI event
// enabled (publishUI=true) — a human answer should show up in the event feed.
func (o *Orchestrator) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	return o.resumeAfterAnswer(stageID, phase, qID, answer, true)
}

// resumeAfterAnswer drives a stage out of awaiting_user_input once answer.json
// is already on disk. If the agent goroutine is still running (its bash loop is
// polling for answer.json), we only transition the status — the loop detects
// the file and continues without a restart. If the goroutine has exited, we
// publish to the critical bus so onUserAnswered restarts it.
//
// Shared by two callers so auto-answer is symmetric with a human answer:
//   - NotifyAnswer (human via HTTP) — publishUI=true adds the dialog-feed event.
//   - the non-interactive auto-answer path (pollQuestions / autoAnswerMalformed)
//     — publishUI=false, since it already published EventAutoAnswered for the feed.
//
// The non-interactive case is what fixes the permanent hang: a non-interactive
// or agents:[auto] stage whose agent asked a question and exited (before writing
// its completion artifact) is parked in awaiting_user_input by retry.go /
// onAgentCompleted, and the ONLY driver out of that status is EvUserAnswered.
// Writing answer.json alone never moved the FSM nor restarted the exited agent,
// so the stage hung forever. In the normal case (agent still polling, stage
// still running) this is a no-op: the IsActive branch fires EvUserAnswered,
// which the FSM rejects from `running`, and no restart happens.
func (o *Orchestrator) resumeAfterAnswer(stageID, phase, qID, answer string, publishUI bool) error {
	if o.concurrency.IsActive(stageID) {
		guardPhase := o.popPreAskPhase(stageID, phase)
		_, seq, _ := o.triggerWithSeq(stageID, bus.EvUserAnswered, bus.GuardCtx{Phase: guardPhase}, "")
		if publishUI {
			o.ui.Publish(bus.Event{Type: bus.EventUserAnswered, StageID: stageID, Data: map[string]any{
				keyID: qID, keyPhase: phase, keyAnswer: answer,
			}, Seq: seq})
		}
		return nil
	}
	return o.critical.Publish(context.Background(), bus.Event{
		Type:    bus.EventUserAnswered,
		StageID: stageID,
		Data:    map[string]any{keyID: qID, keyPhase: phase, keyAnswer: answer},
	})
}

// autoApproveIfConfigured immediately approves a stage right after its plan
// becomes ready (EvPlanReady) if flow.yaml sets auto_approve: true on it —
// independent of whether a dashboard is attached and independent of
// --require-approval, both of which only govern the *default* (no
// auto_approve) headless behavior elsewhere in this package. Returns whether
// it auto-approved (callers use this to skip their own headless branch).
func (o *Orchestrator) autoApproveIfConfigured(ctx context.Context, stage flow.Stage) bool {
	if !stage.AutoApprove {
		return false
	}
	log.Printf("auto_approve: auto-approving plan for stage %q", stage.ID)
	o.approveStage(ctx, stage.ID)
	return true
}

// approveStage долговечно переводит стадию из awaiting_approval и запускает
// побочные эффекты. Вызывается СИНХРОННО (из HTTP-обработчика и из headless
// auto-approve), поэтому переход фиксируется в Store до возврата — краш после
// approve не теряет интент (recovery резюмит ready/done). Идемпотентна: если
// стадия уже не в awaiting_approval, только до-запускает побочные эффекты.
func (o *Orchestrator) approveStage(ctx context.Context, stageID string) {
	if o.currentStatus(stageID) == state.StatusAwaitingApproval {
		stage := o.graph.Stage(stageID)
		if stage != nil && !stage.HasAgent(flow.AgentImplementation) {
			o.Trigger(stageID, bus.EvComplete, bus.GuardCtx{}, "planning-only stage")
			// Планировочная стадия без implementation-агента доходит до done
			// прямо здесь, минуя onAgentCompleted/completeStage — но
			// script_after разрешён на любой стадии (flow.go), так что этот
			// путь тоже обязан его запустить, иначе хук молча не выполнится
			// для планировочных стадий.
			o.maybeRunAfterHook(ctx, stageID)
		} else {
			o.Trigger(stageID, bus.EvApprove, bus.GuardCtx{}, "")
		}
	}
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)
}

// runContext returns the long-lived run-scoped context for spawning agents from
// HTTP-initiated actions. Agents MUST NOT inherit the HTTP request context: net/http
// cancels it when the handler returns, which would kill the just-spawned agent.
// Falls back to context.WithoutCancel(fallback) if Run hasn't set runCtx yet
// (tiny window before Run starts) so the agent still isn't bound to the request.
func (o *Orchestrator) runContext(fallback context.Context) context.Context {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.runCtx != nil {
		return o.runCtx
	}
	return context.WithoutCancel(fallback)
}

// Approve approves a stage plan (синхронно и долговечно). ctx приходит от
// вызывающей стороны (HTTP request context у dashboard-инициированного approve
// или Run ctx у headless auto-approve) — подставляем run ctx перед спавном
// агента, см. runContext.
func (o *Orchestrator) Approve(ctx context.Context, stageID string) error {
	o.approveStage(o.runContext(ctx), stageID)
	return nil
}

// Revise sends feedback to re-plan a stage, ИЛИ (agent_suggest, running)
// запрашивает graceful-прерывание текущего вызова агента фразой в контексте
// (синхронно и долговечно): переход в revising фиксируется в Store до
// возврата — краш после Revise не теряет интент (recovery резюмит revising
// через тот же путь, что и planning).
//
// running-ветка ничего не спаунит сама — перезапуск с фидбеком делает
// onUserInterrupted изнутри уже идущего runWithRetry, когда SIGINT реально
// завершит текущий subprocess (см. pkg/executor: Config.InterruptCh).
func (o *Orchestrator) Revise(reqCtx context.Context, stageID, feedback string) error {
	current := o.currentStatus(stageID)
	if current != state.StatusAwaitingApproval && current != state.StatusRunning {
		return nil
	}

	stageDir := filepath.Join(o.opts.RunDir, stageID)

	if current == state.StatusRunning {
		if _, ok := o.Trigger(stageID, bus.EvRevise, bus.GuardCtx{}, feedback); !ok {
			return nil
		}
		if err := state.SaveFeedback(stageDir, feedback); err != nil {
			return fmt.Errorf("save feedback for %s: %w", stageID, err)
		}
		if ch, ok := o.interruptChans.Load(stageID); ok {
			select {
			case ch.(chan struct{}) <- struct{}{}:
			default: // канал уже сигнализирован (двойной клик) — не блокируемся
			}
		}
		return nil
	}

	if _, ok := o.Trigger(stageID, bus.EvRevise, bus.GuardCtx{}, feedback); !ok {
		return nil
	}
	if _, err := state.VersionPlan(stageDir); err != nil {
		return fmt.Errorf("version plan for %s: %w", stageID, err)
	}
	if err := state.SaveFeedback(stageDir, feedback); err != nil {
		return fmt.Errorf("save feedback for %s: %w", stageID, err)
	}

	if stage := o.graph.Stage(stageID); stage != nil {
		// Спавним под run ctx, а не reqCtx: HTTP-хэндлер отменит reqCtx сразу
		// после возврата ответа, и агент был бы убит немедленно (см. runContext).
		o.concurrency.SpawnAgent(o.runContext(reqCtx), *stage, o.runPlanningWithFeedback)
	}
	return nil
}

// Button resolves the named button's prompt from the flow and delivers it to
// the live agent via Revise. Unknown name or a stage with no such button is a
// no-op (returns nil). The status gate (running/awaiting_approval) lives in
// Revise itself, so Button doesn't re-check it.
func (o *Orchestrator) Button(ctx context.Context, stageID, name string) error {
	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}
	prompt := stage.Buttons.Prompt(name)
	if prompt == "" {
		return nil
	}
	return o.Revise(ctx, stageID, prompt)
}

// Pause synchronously transitions a stage to paused and, if it has a live
// agent or is waiting out a retry backoff, signals the same interruptChans
// channel Revise() already uses for a running stage. The only difference
// from Revise is what runWithRetry does when it wakes up: Revise restarts
// with feedback, Pause doesn't restart anything — the durable transition to
// paused already happened here, synchronously, before the signal was sent.
func (o *Orchestrator) Pause(_ context.Context, stageID string) error {
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
	if ch, ok := o.interruptChans.Load(stageID); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
		default: // уже сигнализирован — не блокируемся
		}
	}
	return nil
}

// Continue resumes a paused stage: for PausedFrom==pending (auto_run:false
// gated the stage before it ever started, or a script stage's only pause
// point) it's exactly a normal first activation; otherwise it's exactly what
// afm-restart recovery already does for a stage recorded as
// running/planning/revising/retrying (resumeStageAtStatus, Task 6) — a
// manually paused stage and a crashed-and-restarted one are, from the
// scheduler's point of view, the same situation: "the process implied by
// this status isn't running right now."
func (o *Orchestrator) Continue(reqCtx context.Context, stageID string) error {
	if o.currentStatus(stageID) != state.StatusPaused {
		return nil
	}
	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}
	pausedFrom := o.opts.Store.PausedFrom(stageID)
	to, ok := o.Trigger(stageID, bus.EvContinue, bus.GuardCtx{PausedFrom: pausedFrom}, "")
	if !ok {
		return nil
	}

	ctx := o.runContext(reqCtx) // не reqCtx — иначе HTTP-хендлер убьёт агента при возврате ответа
	if pausedFrom == state.StatusPending {
		o.tryActivatePrePlanned(ctx)
		o.startPlanningForUnblocked(ctx)
		return nil
	}

	o.resumeStageAtStatus(ctx, *stage, to)
	return nil
}

// Retry retries a failed stage by transitioning it to pending and restarting
// (синхронно и долговечно).
func (o *Orchestrator) Retry(ctx context.Context, stageID string) error {
	o.retryStage(o.runContext(ctx), stageID)
	return nil
}

// RetryHook resumes a stage currently blocked on a failed before/after hook
// by re-running that hook's 3x/1-2-3s retry cycle.
func (o *Orchestrator) RetryHook(stageID string) error {
	if !o.resolveHook(stageID, hookDecisionRetry) {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}
	return nil
}

// SkipHook resumes a stage currently blocked on a failed before/after hook
// by skipping it entirely.
func (o *Orchestrator) SkipHook(stageID string) error {
	if !o.resolveHook(stageID, hookDecisionSkip) {
		return fmt.Errorf("stage %q has no hook awaiting a decision", stageID)
	}
	return nil
}

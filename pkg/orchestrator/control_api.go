package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// FailStage marks a stage as failed with a reason.
func (o *Orchestrator) FailStage(stageID, reason string) {
	o.Trigger(stageID, EvFail, GuardCtx{}, reason)
	o.failBlockedStages()
}

// NotifyAnswer is called by the HTTP handler when the user submits an answer.
// If the agent goroutine is still running (its bash loop is awaiting
// answer.json), we only transition the status — the bash loop will detect the
// file and continue without a restart. If the goroutine has exited, we publish
// to the critical bus so onUserAnswered can restart it.
func (o *Orchestrator) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	if o.isAgentActive(stageID) {
		guardPhase := o.popPreAskPhase(stageID, phase)
		_, seq, _ := o.triggerWithSeq(stageID, EvUserAnswered, GuardCtx{Phase: guardPhase}, "")
		o.ui.Publish(Event{Type: EventUserAnswered, StageID: stageID, Data: map[string]any{
			keyID: qID, keyPhase: phase, keyAnswer: answer,
		}, Seq: seq})
		return nil
	}
	return o.critical.Publish(context.Background(), Event{
		Type:    EventUserAnswered,
		StageID: stageID,
		Data:    map[string]any{keyID: qID, "phase": phase, keyAnswer: answer},
	})
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
			o.Trigger(stageID, EvComplete, GuardCtx{}, "planning-only stage")
		} else {
			o.Trigger(stageID, EvApprove, GuardCtx{}, "")
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
		if _, ok := o.Trigger(stageID, EvRevise, GuardCtx{}, feedback); !ok {
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

	if _, ok := o.Trigger(stageID, EvRevise, GuardCtx{}, feedback); !ok {
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
		o.spawnAgent(o.runContext(reqCtx), *stage, o.runPlanningWithFeedback)
	}
	return nil
}

// Retry retries a failed stage by transitioning it to pending and restarting
// (синхронно и долговечно).
func (o *Orchestrator) Retry(ctx context.Context, stageID string) error {
	o.retryStage(o.runContext(ctx), stageID)
	return nil
}

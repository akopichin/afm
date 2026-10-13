package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// verifyOutcomeStillOwned — неатомарная проверка Pause/Revise перед обработкой verify; финальный fail
// защищает CAS EvVerifyFail.
func (o *Orchestrator) verifyOutcomeStillOwned(stageID string, onUserInterrupted func()) bool {
	switch o.currentStatus(stageID) {
	case state.StatusPaused:
		return false
	case state.StatusRevising:
		onUserInterrupted()
		return false
	default:
		return true
	}
}

// commitVerifyFailure — H1: атомарная фиксация verify-driven fail.
func (o *Orchestrator) commitVerifyFailure(stageID, reason string, onUserInterrupted func()) bool {
	if !o.verifyOutcomeStillOwned(stageID, onUserInterrupted) {
		return false
	}
	if o.verifyOutcomeGuardHook != nil {
		o.verifyOutcomeGuardHook(stageID)
	}
	if _, ok := o.Trigger(stageID, bus.EvVerifyFail, bus.GuardCtx{}, reason); ok {
		return true
	}
	// CAS проиграл гонку: конкурентный Pause/Revise успел закоммититься МЕЖДУ fast-path чтением выше и этим
	// Trigger — сама FSM атомарно отбросила переход, стадия больше не наша.
	if o.currentStatus(stageID) == state.StatusRevising {
		onUserInterrupted()
	}
	return false
}

// completeAgent публикует терминальный success стадии — durable-нотис EventAgentCompleted ПЛЮС
// critical-событие, из которого Run-цикл двигает FSM в completed.
func (o *Orchestrator) completeAgent(ctx context.Context, stageID, phase string) {
	stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventAgentCompleted), phase)
	o.publishCritical(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: stageID, Data: phase})
}

// isRetryableError checks if the error is a rate limit or server error (retryable with backoff).
func isRetryableError(err error) bool {
	return Classify(err) == ClassRetryable
}

// verifyMaxFailures — бюджет отклонений verify для стадии: сколько коррекций автора разрешить, прежде чем
// стадия провалится.
func (o *Orchestrator) verifyMaxFailures(s flow.Stage) int {
	n := o.opts.Config.Verify.ResolvedMaxFailures()
	if s.Verify.MaxFailures != nil {
		n = *s.Verify.MaxFailures
	}
	return n
}

// verifyReviewPatience — бюджет stalemate для стадии: сколько ПОДРЯД идущих verify-отклонений, НЕ
// изменивших рабочее дерево, допустить до приёма стадии. Зеркалит verifyMaxFailures.
func (o *Orchestrator) verifyReviewPatience(s flow.Stage) int {
	n := o.opts.Config.Verify.ResolvedReviewPatience()
	if s.Verify.ReviewPatience != nil {
		n = *s.Verify.ReviewPatience
	}
	return n
}

// verifyFreshCorrection — давать ли автору после verify-reject чистую сессию. Per-stage override >
// config > дефолт ON. Зеркалит verifyMaxFailures.
func (o *Orchestrator) verifyFreshCorrection(s flow.Stage) bool {
	if s.Verify.FreshCorrection != nil {
		return *s.Verify.FreshCorrection
	}
	return o.opts.Config.Verify.ResolvedFreshCorrection()
}

// dropSessionForFreshCorrection, when enabled, drops the session file so the NEXT attempt starts a fresh
// session instead of resuming one that defends its prior approach (anchoring) — the reconstruction (actions
// + Q&A digest + verify/feedback.md + incompleteReason) covers the lost context. No-op for non-interactive
// stages (no session file to drop) and when freshCorrection is disabled.
func dropSessionForFreshCorrection(freshCorrection bool, stageDir, phase string) {
	if !freshCorrection {
		return
	}
	_ = os.Remove(stagefiles.SessionFile(stageDir, phase))
}

// buildRetryContext reads the last N lines from the agent's raw stream-json
// log and formats them as a continuation context for the retry prompt.
func buildRetryContext(stageDir, phase string) string {
	jsonlName := flow.PhaseJSONL(flow.Phase(phase))

	lines := executor.RenderActions(filepath.Join(stageDir, jsonlName))
	if len(lines) == 0 {
		return ""
	}
	const maxLines = 200
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Previously completed actions (resuming after interruption)\n\n")
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			buf.WriteString(l)
			buf.WriteString("\n")
		}
	}
	buf.WriteString("\nContinue from where you left off. Do NOT redo work that is already done.\n")
	return buf.String()
}

// RetryBackoff — фиксированная пауза между попытками после retryable-ошибки. Прежний exponential
// [5s,10s,30s] сдавался после 4 попыток — z.ai overload длится дольше.
var RetryBackoff = 5 * time.Second

// MaxRetries — число повторных попыток после первого запуска. См. RetryBackoff: фиксируется в
// Orchestrator.maxRetries при New.
var MaxRetries = 15

// runWithRetry wraps an agent function with automatic retry on rate limit errors. After exhausting all
// retries: publishes EventRetryExhausted.
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
	// SpawnAgent checks pause after acquiring its slot; interruptChans handles pauses after the agent starts.
	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(s.ID, interruptCh)
	// G3: CompareAndDelete, а не безусловный Delete — тот же приём, что E3 применил к stageLeases.
	// CompareAndDelete удаляет запись, только если она всё ещё РОВНО ТА, что сохранил этот вызов.
	defer o.interruptChans.CompareAndDelete(s.ID, interruptCh)

	// resumeCtx однократно включает контекст прежних действий на attempt 0 после Continue; LoadAndDelete
	// потребляет маркер.
	resumeCtx := false
	if _, ok := o.resumeContextOnce.LoadAndDelete(s.ID); ok {
		resumeCtx = true
	}

	incompleteReason := ""
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// maxRetries/retryBackoff — снапшоты с инстанса (см. Orchestrator-комментарий):
	// эта горутина может пережить возврат Run(), поэтому globals не читаем.
	maxRetries := o.maxRetries
	retryBackoff := o.retryBackoff
	// verifyFailures — счётчик verify-отклонений этой горутины; maxVerifyFailures — их бюджет. In-memory,
	// сбрасывается на resume — как и прежнее однократное поведение.
	verifyFailures := 0
	maxVerifyFailures := o.verifyMaxFailures(s)
	freshCorrection := o.verifyFreshCorrection(s)
	// B1 stalemate: reviewPatience = 0 → выключено. prevFingerprint/unchangedRejections — in-memory состояние
	// ЭТОЙ горутины: сколько verify-отклонений ПОДРЯД оставили рабочее дерево неизменным.
	reviewPatience := o.verifyReviewPatience(s)
	prevFingerprint := ""
	unchangedRejections := 0
	fingerprintDir := o.fingerprintDir()
	for attempt := 0; attempt <= maxRetries; attempt++ {
		retryCtx := ""
		if attempt > 0 || resumeCtx {
			retryCtx = buildRetryContext(stageDir, phase)
			if incompleteReason != "" {
				retryCtx += "\n\n## Completion check failed\n\n" + incompleteReason +
					"\n\nFix the underlying problem and make the check pass before finishing.\n"
			}
			// Сессии не будет — агент не увидит прежние ответы пользователя в разговоре; дайджест их
			// реконструирует. Resumed-агенту НЕ даём: он получил бы дубль того, что и так видит.
			if !willResume(s, stageDir, phase) {
				retryCtx += dialogDigestBlock(stageDir, phase)
			}
		}
		resumeCtx = false // только самая первая попытка резюмируемого запуска получает его

		if o.currentStatus(s.ID) == state.StatusPaused {
			return // a pause landed in the shouldRun->register gap; do not start the subprocess
		}

		err := agentFn(retryCtx)
		if err == nil {
			// afm bug: интерактивный агент может завершиться, не дождавшись ответа пользователя — на диске остаётся
			// question.json без answer.json.
			if (s.Interactive || phase == phaseAutonomous) && o.hasOpenQuestion(s.ID, phase) {
				if cur := o.currentStatus(s.ID); cur != state.StatusAwaitingUserInput {
					// Первое обнаружение этого открытого вопроса — стадия ещё не в awaiting_user_input, вопрос точно
					// живой (только что написан этим же вызовом агента). Держим.
					o.Trigger(s.ID, bus.EvAskUser, bus.GuardCtx{Phase: phase}, "")
					return
				}
				// Статус уже awaiting_user_input — независимый поллер вопросов успел перевести стадию туда, ПОКА агент
				// ещё выполнялся, обогнав его собственный выход.
				if completionCheck == nil || completionCheck() == nil {
					o.completeAgent(ctx, s.ID, phase)
				}
				return
			}
			var checkErr error
			if completionCheck != nil {
				checkErr = completionCheck()
			}
			// F1: completionCheck может занять сколько угодно времени ПОСЛЕ того, как автор уже вернулся, — за это
			// время Pause/Revise мог долговечно перевести стадию в paused/revising.
			switch o.currentStatus(s.ID) {
			case state.StatusPaused:
				return
			case state.StatusRevising:
				onUserInterrupted()
				return
			}
			if checkErr == nil {
				o.completeAgent(ctx, s.ID, phase)
				return
			}
			// AI-verify: верификатор слушает тот же interruptChans, что и автор — Pause/Revise во время verify
			// всплывает сюда либо этим сентинелом, либо уже долговечным paused/revising-статусом.
			if errors.Is(checkErr, executor.ErrUserInterrupted) {
				onUserInterrupted()
				return
			}
			// Incomplete work — две НЕЗАВИСИМЫЕ политики коррекции без backoff. verifyFailures++ — только для
			// verify-ветки, чтобы транспортные ретраи и план-коррекции его не расходовали.
			if stagefiles.IsIncompleteWorkError(checkErr) {
				var vre *VerifyRejectedError
				isVerifyReject := errors.As(checkErr, &vre)
				// B1 stalemate: если автор перестал менять рабочее дерево в ответ на повторные verify-отклонения —
				// принять стадию как завершённую вместо очередной коррекции.
				if isVerifyReject && reviewPatience > 0 {
					var done bool
					done, prevFingerprint, unchangedRejections = o.handleStalemate(
						ctx, s, phase, vre, reviewPatience, fingerprintDir,
						prevFingerprint, unchangedRejections, onUserInterrupted)
					if done {
						return
					}
				}
				// На последней попытке continue оставил бы стадию активной без следующей итерации; проверяем attempt <
				// maxRetries.
				retryIncomplete := attempt < maxRetries &&
					((isVerifyReject && verifyFailures < maxVerifyFailures) ||
						(!isVerifyReject && attempt == 0))
				if retryIncomplete {
					// G2: повторная проверка ПРЯМО ПЕРЕД incompleteReason/continue — см. verifyOutcomeStillOwned.
					if o.verifyOutcomeGuardHook != nil {
						o.verifyOutcomeGuardHook(s.ID)
					}
					if !o.verifyOutcomeStillOwned(s.ID, onUserInterrupted) {
						return
					}
					if isVerifyReject {
						verifyFailures++
						dropSessionForFreshCorrection(freshCorrection, stageDir, phase)
					}
					incompleteReason = checkErr.Error()
					// Раньше публиковалось как EventStageStatusChanged с Data- сообщением — фронт рисовал это пустым
					// бейджем "→ ", и на reload сообщение терялось.
					msg := "incomplete work, retrying: " + checkErr.Error()
					o.publishNotice(bus.Event{Type: bus.EventRetryScheduled, StageID: s.ID, Data: msg})
					continue
				}
				// verify-бюджет исчерпан ИЛИ план-незавершёнка после attempt 0 —
				// падаем в общий fail-путь ниже.
			}
			// Missing artifact or second incomplete attempt — fail. См. commitVerifyFailure.
			if isVerifyDrivenError(checkErr) {
				if !o.commitVerifyFailure(s.ID, checkErr.Error(), onUserInterrupted) {
					return
				}
			} else {
				o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, checkErr.Error())
			}
			o.failBlockedStages()
			return
		}

		if errors.Is(err, executor.ErrUserInterrupted) {
			if o.currentStatus(s.ID) == state.StatusPaused {
				return // Pause() already recorded the durable transition — nothing to restart
			}
			onUserInterrupted()
			return
		}

		if !isRetryableError(err) {
			// Drop the session file so a later retry starts a fresh Claude session instead of resuming a
			// conversation that was never created. Mirrors the retryable branch.
			_ = os.Remove(stagefiles.SessionFile(stageDir, phase))
			o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, err.Error())
			o.failBlockedStages()
			return
		}

		// Retryable errors are typically caused by an overgrown session context. Drop the session file so the
		// next attempt starts a fresh Claude session.
		_ = os.Remove(stagefiles.SessionFile(stageDir, phase))

		if attempt < maxRetries {
			// Сохраняем сообщение retry в reason, чтобы reload восстановил тот же payload; seq дедуплицирует live и
			// историю.
			msg := fmt.Sprintf("attempt %d/%d in %v", attempt+1, maxRetries, retryBackoff)
			_, seq, _ := o.triggerWithSeq(s.ID, bus.EvScheduleRetry, bus.GuardCtx{Phase: phase}, msg)
			o.ui.Publish(bus.Event{
				Type:    bus.EventRetryScheduled,
				StageID: s.ID,
				Data:    msg,
				Seq:     seq,
			})
			select {
			case <-time.After(retryBackoff):
			case <-interruptCh:
				return // Pause() already transitioned to paused — nothing to resume here
			case <-ctx.Done():
				o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "cancelled during retry")
				o.failBlockedStages()
				return
			}
			switch phase {
			case phasePlanning:
				o.Trigger(s.ID, bus.EvResumeAfterRetry, bus.GuardCtx{Phase: phasePlanning}, "")
			default:
				o.Trigger(s.ID, bus.EvResumeAfterRetry, bus.GuardCtx{Phase: phaseImplementation}, "")
			}
		} else {
			_, seq, _ := o.triggerWithSeq(s.ID, bus.EvFail, bus.GuardCtx{}, "retries exhausted")
			o.failBlockedStages()
			o.publishCritical(ctx, bus.Event{Type: bus.EventRetryExhausted, StageID: s.ID, Seq: seq})
		}
	}
}

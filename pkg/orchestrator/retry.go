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

// isRetryableError checks if the error is a rate limit or server error (retryable with backoff).
func isRetryableError(err error) bool {
	return Classify(err) == ClassRetryable
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

// RetryBackoff — фиксированная пауза между попытками после retryable-ошибки
// (529/502/503/504, rate limit). Прежний exponential [5s,10s,30s] сдавался после
// 4 попыток — z.ai overload длится дольше. Фиксированный 5s + MaxRetries (как в
// ralphex) переживают окно overload.
//
// Дефолт: читается ОДИН РАЗ в New и фиксируется в Orchestrator.retryBackoff
// (immutable), чтобы агентские горутины не гонялись за мутацией package var в
// тестах. Тесты могут переопределять значение, но обязаны делать это ДО New.
var RetryBackoff = 5 * time.Second

// MaxRetries — число повторных попыток после первого запуска (всего MaxRetries+1).
// Сверху ограничено idle_timeout stage (30м default): каждая попытка ≈ agent-runtime,
// так что реально успевает меньше — idle_timeout добьёт лишнее.
//
// См. RetryBackoff: фиксируется в Orchestrator.maxRetries при New.
var MaxRetries = 15

// runWithRetry wraps an agent function with automatic retry on rate limit errors.
// On rate limit: sets status to retrying, waits with backoff, then retries.
// After exhausting all retries: publishes EventRetryExhausted.
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
	// The queued-behind-semaphore race (stage paused while runWithRetry's
	// caller was still waiting for a concurrency slot) is now closed
	// centrally by concurrency.Manager.SpawnAgent's shouldRun check, which
	// runs immediately before this function's caller — no need to repeat it
	// here. interruptChans below only needs to catch a pause that happens
	// AFTER this point (an already-running agent).
	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(s.ID, interruptCh)
	defer o.interruptChans.Delete(s.ID)

	incompleteReason := ""
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// maxRetries/retryBackoff — снапшоты с инстанса (см. Orchestrator-комментарий):
	// эта горутина может пережить возврат Run(), поэтому globals не читаем.
	maxRetries := o.maxRetries
	retryBackoff := o.retryBackoff
	for attempt := 0; attempt <= maxRetries; attempt++ {
		retryCtx := ""
		if attempt > 0 {
			retryCtx = buildRetryContext(stageDir, phase)
			if incompleteReason != "" {
				retryCtx += "\n\n## Completion check failed\n\n" + incompleteReason +
					"\n\nFix the underlying problem and make the check pass before finishing.\n"
			}
		}

		err := agentFn(retryCtx)
		if err == nil {
			// afm bug: интерактивный агент может завершиться (выйти из claude),
			// не дождавшись ответа пользователя — на диске остаётся
			// question.json без answer.json. Это не ошибка работы и не повод
			// фейлить стадию: артефакта ещё нет просто потому, что пользователь
			// не ответил. Удерживаем stage в awaiting_user_input — когда ответ
			// появится, onUserAnswered перезапустит агента и он допишет план.
			// Без этой ветки стадия падала с "missing artifact or incomplete"
			// ровно на таком выходе агента в ожидании.
			if (s.Interactive || phase == phaseAutonomous) && o.hasOpenQuestion(s.ID, phase) {
				if cur := o.currentStatus(s.ID); cur != state.StatusAwaitingUserInput {
					// Первое обнаружение этого открытого вопроса — стадия
					// ещё не в awaiting_user_input, вопрос точно живой
					// (только что написан этим же вызовом агента). Держим.
					o.Trigger(s.ID, bus.EvAskUser, bus.GuardCtx{Phase: phase}, "")
					return
				}
				// Статус уже awaiting_user_input — независимый поллер вопросов
				// (см. pollQuestions) успел перевести стадию туда, ПОКА агент
				// ещё выполнялся, обогнав его собственный выход. Найдено
				// разбором реального прод-лога — постоянно висящий, брошенный
				// вопрос (артефакт другого бага, реюз id — см.
				// TestPollQuestions_ReusedIDAfterAnswerAsksAgain) держал
				// стадию в awaiting_user_input НАВСЕГДА, хотя
				// execution_summary.md уже был записан агентом и стадия
				// объективно завершена. Раз FSM уже здесь БЕЗ участия этого
				// return'а — completion решает, действительно ли работа
				// сделана; вопрос, оставшийся на диске, в этом случае не
				// более чем повод для FSM транзишна, который уже случился.
				if completionCheck == nil || completionCheck() == nil {
					stagefiles.AppendNotice(o.opts.RunDir, s.ID, string(bus.EventAgentCompleted), phase)
					_ = o.critical.Publish(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: s.ID, Data: phase})
				}
				return
			}
			var checkErr error
			if completionCheck != nil {
				checkErr = completionCheck()
			}
			if checkErr == nil {
				stagefiles.AppendNotice(o.opts.RunDir, s.ID, string(bus.EventAgentCompleted), phase)
				_ = o.critical.Publish(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: s.ID, Data: phase})
				return
			}
			// Incomplete work — retry once without backoff
			if stagefiles.IsIncompleteWorkError(checkErr) && attempt == 0 {
				incompleteReason = checkErr.Error()
				o.ui.Publish(bus.Event{
					Type:    bus.EventStageStatusChanged,
					StageID: s.ID,
					Data:    "incomplete work, retrying: " + checkErr.Error(),
				})
				continue
			}
			// Missing artifact or second incomplete attempt — fail
			o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "missing artifact or incomplete")
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
			// Drop the session file so a later retry starts a fresh Claude session
			// instead of resuming a conversation that was never created (e.g. the
			// process died before claude created it). Mirrors the retryable branch.
			_ = os.Remove(stagefiles.SessionFile(stageDir, phase))
			o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, err.Error())
			o.failBlockedStages()
			return
		}

		// Retryable errors (e.g. 529) are typically caused by an overgrown
		// session context. Drop the session file so the next attempt starts a
		// fresh Claude session. Answers already written to answer.json files
		// remain on disk and are re-read immediately by the agent's bash loop.
		_ = os.Remove(stagefiles.SessionFile(stageDir, phase))

		if attempt < maxRetries {
			_, seq, _ := o.triggerWithSeq(s.ID, bus.EvScheduleRetry, bus.GuardCtx{Phase: phase}, "")
			o.ui.Publish(bus.Event{
				Type:    bus.EventRetryScheduled,
				StageID: s.ID,
				Data:    fmt.Sprintf("attempt %d/%d in %v", attempt+1, maxRetries, retryBackoff),
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
			_ = o.critical.Publish(ctx, bus.Event{Type: bus.EventRetryExhausted, StageID: s.ID, Seq: seq})
		}
	}
}

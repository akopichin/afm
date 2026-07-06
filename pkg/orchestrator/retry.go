package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// isRetryableError checks if the error is a rate limit or server error (retryable with backoff).
func isRetryableError(err error) bool {
	return Classify(err) == ClassRetryable
}

// buildRetryContext reads the last N lines from the agent log file
// and formats them as a continuation context for the retry prompt.
func buildRetryContext(stageDir, phase string) string {
	var logName string
	switch phase {
	case phasePlanning:
		logName = "planning.log"
	case phaseReview:
		logName = "review.log"
	default:
		logName = "implementation.log"
	}

	data, err := os.ReadFile(filepath.Join(stageDir, logName))
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
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

// RetryBackoff defines wait durations between retry attempts.
var RetryBackoff = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second}

// runWithRetry wraps an agent function with automatic retry on rate limit errors.
// On rate limit: sets status to retrying, waits with backoff, then retries.
// After exhausting all retries: publishes EventRetryExhausted.
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error) {
	backoff := append([]time.Duration{}, RetryBackoff...)
	incompleteReason := ""
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	for attempt := 0; attempt <= len(backoff); attempt++ {
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
			if s.Interactive && o.hasOpenQuestion(s.ID, phase) {
				if cur := o.currentStatus(s.ID); cur != state.StatusAwaitingUserInput {
					o.Trigger(s.ID, EvAskUser, GuardCtx{Phase: phase}, "")
				}
				return
			}
			if completionCheck == nil {
				_ = o.critical.Publish(context.Background(), Event{Type: EventAgentCompleted, StageID: s.ID, Data: phase})
				return
			}
			checkErr := completionCheck()
			if checkErr == nil {
				_ = o.critical.Publish(context.Background(), Event{Type: EventAgentCompleted, StageID: s.ID, Data: phase})
				return
			}
			// Incomplete work — retry once without backoff
			if isIncompleteWorkError(checkErr) && attempt == 0 {
				incompleteReason = checkErr.Error()
				o.ui.Publish(Event{
					Type:    EventStageStatusChanged,
					StageID: s.ID,
					Data:    "incomplete work, retrying: " + checkErr.Error(),
				})
				continue
			}
			// Missing artifact or second incomplete attempt — fail
			o.Trigger(s.ID, EvFail, GuardCtx{}, "missing artifact or incomplete")
			o.failBlockedStages()
			return
		}

		if !isRetryableError(err) {
			// Drop the session file so a later retry starts a fresh Claude session
			// instead of resuming a conversation that was never created (e.g. the
			// process died before claude created it). Mirrors the retryable branch.
			_ = os.Remove(sessionFile(stageDir, phase))
			o.Trigger(s.ID, EvFail, GuardCtx{}, err.Error())
			o.failBlockedStages()
			return
		}

		// Retryable errors (e.g. 529) are typically caused by an overgrown
		// session context. Drop the session file so the next attempt starts a
		// fresh Claude session. Answers already written to answer.json files
		// remain on disk and are re-read immediately by the agent's bash loop.
		_ = os.Remove(sessionFile(stageDir, phase))

		if attempt < len(backoff) {
			o.Trigger(s.ID, EvScheduleRetry, GuardCtx{Phase: phase}, "")
			o.ui.Publish(Event{
				Type:    EventRetryScheduled,
				StageID: s.ID,
				Data:    fmt.Sprintf("attempt %d/%d in %v", attempt+1, len(backoff), backoff[attempt]),
			})
			select {
			case <-time.After(backoff[attempt]):
			case <-ctx.Done():
				o.Trigger(s.ID, EvFail, GuardCtx{}, "cancelled during retry")
				o.failBlockedStages()
				return
			}
			switch phase {
			case phasePlanning:
				o.Trigger(s.ID, EvResumeAfterRetry, GuardCtx{Phase: phasePlanning}, "")
			default:
				o.Trigger(s.ID, EvResumeAfterRetry, GuardCtx{Phase: phaseImplementation}, "")
			}
		} else {
			o.Trigger(s.ID, EvFail, GuardCtx{}, "retries exhausted")
			o.failBlockedStages()
			_ = o.critical.Publish(context.Background(), Event{Type: EventRetryExhausted, StageID: s.ID})
		}
	}
}

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

// commitVerifyFailure checks ownership again after verification, then fences the durable failure.
func (o *Orchestrator) commitVerifyFailure(stageID, phase, reason string, owner *attemptOwner, onUserInterrupted func()) bool {
	if o.yieldIfSuperseded(stageID, phase, owner, onUserInterrupted) {
		return false
	}
	if o.verifyOutcomeGuardHook != nil {
		o.verifyOutcomeGuardHook(stageID)
	}
	if _, _, ok := o.ownerTrigger(owner, phase, stageID, bus.EvVerifyFail, bus.GuardCtx{}, reason); ok {
		return true
	}
	o.yieldIfSuperseded(stageID, phase, owner, onUserInterrupted)
	return false
}

// completeAgent queues a completion with its owner token. Durable outcome notices wait until the
// event consumer successfully commits the fenced transition; a queued stale completion says nothing.
func (o *Orchestrator) completeAgent(ctx context.Context, stageID, phase string, epoch *uint64, notices ...bus.Event) {
	if epoch != nil {
		value := *epoch
		epoch = &value
	}
	o.publishCritical(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: stageID, Data: phase,
		OwnerEpoch: epoch, CompletionNotices: notices})
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

// clearTurnSession fences both verify correction and transport-error cleanup.
func (o *Orchestrator) clearTurnSession(stageID, phase string, owner *attemptOwner, enabled bool) bool {
	if !enabled {
		return true
	}
	if phase != phaseAutonomous {
		owner = nil
	}
	err := o.withOwnerFiles(stageID, owner, "session_clear", func() error {
		dropSessionForFreshCorrection(true, filepath.Join(o.opts.RunDir, stageID), phase)
		return nil
	})
	return err == nil
}

// buildRetryContext reads the last N lines from the agent's raw stream-json
// log and formats them as a continuation context for the retry prompt.
func buildRetryContext(stageDir, phase string) string {
	return buildRetryContextFrom(stageDir, flow.PhaseJSONL(flow.Phase(phase)))
}

// buildRetryContextFrom — то же для конкретного jsonl-файла (feedback-вызов пишет в свой лог).
func buildRetryContextFrom(stageDir, jsonlName string) string {
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

// agentAnswers отмечает агента хода тем, кому уходит ответ (answerToLiveAgent), пока ход не принял решение.
// Отметка несёт владельца: при max_parallel>1 после Pause→Continue два хода одной стадии перекрываются, и отметку
// нового автора может снять только он сам.
func (o *Orchestrator) agentAnswers(stageID string, owner *attemptOwner) {
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	// Pause/Continue do not use answerMu. The gate prevents an old registration
	// from overwriting a newer marker after the launch check.
	_ = o.opts.Store.WithStageOwner(stageID, owner.epoch, func() error {
		o.liveAgents.Store(stageID, owner)
		return nil
	})
}

// stopAnswering снимает СВОЮ отметку без решения о диалоге: ответ с этого момента запускает своё продолжение.
func (o *Orchestrator) stopAnswering(stageID string, owner *attemptOwner) {
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	o.liveAgents.CompareAndDelete(stageID, owner)
}

type dialogExit int

const (
	dialogNone      dialogExit = iota // открытого вопроса нет: ход продолжается
	dialogLeft                        // стадия ждёт пользователя; ответ запустит продолжение
	dialogTakenOver                   // стадию забрал Pause/Continue/Revise раньше, чем ход успел её оставить
)

// leaveWithUser — единственное решение хода о диалоге, под answerMu как и каждый ответ: открытый вопрос читается и
// стадия отдаётся пользователю в одной критической секции, поэтому ответ либо виден здесь, либо запускает своё
// продолжение.
func (o *Orchestrator) leaveWithUser(stageID, phase string, owner *attemptOwner, dialog bool) dialogExit {
	if o.dialogHook != nil {
		o.dialogHook(stageID, false)
	}
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	o.liveAgents.CompareAndDelete(stageID, owner)
	if !dialog {
		return dialogNone
	}
	return o.leaveForOpenQuestionLocked(stageID, phase, owner)
}

// leaveForOpenQuestionLocked отдаёт стадию пользователю, если у фазы есть открытый вопрос. Вызывается под answerMu:
// и ход, и решение summary-recovery принимают его в той же секции, что и поллер вопросов с доставкой ответа.
func (o *Orchestrator) leaveForOpenQuestionLocked(stageID, phase string, owner *attemptOwner) dialogExit {
	if !o.hasOpenQuestion(stageID, phase) {
		return dialogNone
	}
	if o.currentStatus(stageID) == state.StatusAwaitingUserInput {
		return dialogLeft
	}
	if _, _, ok := o.ownerTrigger(owner, phase, stageID, bus.EvAskUser, bus.GuardCtx{Phase: phase}, ""); !ok {
		return dialogTakenOver
	}
	return dialogLeft
}

func (o *Orchestrator) turnLeftForAnswer(stageID string) {
	if o.dialogHook != nil {
		o.dialogHook(stageID, true)
	}
}

// runWithRetry wraps an agent function with automatic retry on rate limit errors. After exhausting all
// retries: publishes EventRetryExhausted.
func (o *Orchestrator) runWithRetry(ctx context.Context, s flow.Stage, phase string, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
	o.runWithRetryOpts(ctx, s, phase, retryOptions{}, agentFn, completionCheck, onUserInterrupted)
}

// retryOptions — необязательные настройки runWithRetryOpts.
type retryOptions struct {
	// actionsJSONL — raw-лог, из которого берётся контекст выполненных действий; пусто = лог фазы по умолчанию.
	// Feedback-вызов пишет в собственный файл, и контекст обязан относиться к нему.
	actionsJSONL string
	// interruptCh — канал прерывания ЭТОГО хода, если вызывающий создал его заранее и передаёт раннеру сам
	// (runnerForTurn). Пусто = канал создаёт runWithRetryOpts, а раннер берёт его из interruptChans.
	interruptCh chan struct{}
}

func (o *Orchestrator) runWithRetryOpts(ctx context.Context, s flow.Stage, phase string, opts retryOptions, agentFn func(retryContext string) error, completionCheck func() error, onUserInterrupted func()) {
	actionsJSONL := opts.actionsJSONL
	if actionsJSONL == "" {
		actionsJSONL = flow.PhaseJSONL(flow.Phase(phase))
	}
	// SpawnAgent checks pause after acquiring its slot; interruptChans handles pauses after the agent starts.
	interruptCh := opts.interruptCh
	if interruptCh == nil {
		interruptCh = make(chan struct{}, 1)
	}
	o.interruptChans.Store(s.ID, interruptCh)
	// owner фиксирует права ЭТОЙ горутины: канал и epoch стадии на входе. Summary-переходы сверяют epoch под
	// mutex Store, поэтому устаревший автор (Pause→Continue, Revise) не резервирует, не стартует и не валит стадию.
	owner := turnOwner(ctx)
	if owner == nil {
		owner = &attemptOwner{interruptCh: interruptCh, epoch: o.opts.Store.Stage(s.ID).OwnerEpoch}
	}
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
	// recovery — summary-продолжение в полёте. После restart/Continue/ответа на вопрос восстанавливаем уже
	// зарезервированный номер и инструкцию без нового списания.
	var recovery *summaryRecovery
	if phase == phaseAutonomous {
		recovery = o.restoreSummaryRecovery(s.ID, stageDir)
	}
	summaryRecovered := false // хотя бы одно продолжение было выдано автору — для notice «recovered»
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
	// failAttempt проваливает стадию как исход этой попытки. У автономной стадии fail идёт только от текущего
	// владельца: устаревший автор (Pause→Continue) не валит стадию, которой уже владеет другой.
	failAttempt := func(reason string) {
		if phase == phaseAutonomous {
			o.failAsOwner(s.ID, owner, reason, onUserInterrupted)
			return
		}
		o.ownerTrigger(owner, phase, s.ID, bus.EvFail, bus.GuardCtx{}, reason)
		o.failBlockedStages()
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		retryCtx := ""
		if attempt > 0 || resumeCtx || recovery != nil {
			retryCtx = buildRetryContextFrom(stageDir, actionsJSONL)
			if incompleteReason != "" {
				retryCtx += "\n\n## Completion check failed\n\n" + incompleteReason +
					"\n\nFix the underlying problem and make the check pass before finishing.\n"
			}
			if recovery != nil {
				retryCtx += summaryRecoveryBlock(stageDir, recovery)
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
		if o.beforeLaunchHook != nil {
			o.beforeLaunchHook(s.ID)
		}
		// Every author launch, including verify correction, belongs to this turn.
		// runnerForTurn also carries its own interrupt channel to the subprocess boundary.
		if o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) {
			return
		}

		if o.beforeLiveMarkerHook != nil {
			o.beforeLiveMarkerHook(s.ID)
		}
		o.agentAnswers(s.ID, owner)
		err := agentFn(retryCtx)
		if o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) {
			o.stopAnswering(s.ID, owner)
			return
		}
		if err != nil {
			o.stopAnswering(s.ID, owner)
		}
		if err == nil {
			if recovery != nil {
				summaryRecovered = true // инструкция доставлена; дальше решает probe summary
				recovery = nil
			}
			// afm bug: интерактивный агент может завершиться, не дождавшись ответа пользователя — на диске остаётся
			// question.json без answer.json. Вопрос, который поллер уже показал, пока агент работал, агент бросил: если
			// работа сделана, стадия завершается.
			dialog := s.Interactive || phase == phaseAutonomous
			if dialog && o.currentStatus(s.ID) == state.StatusAwaitingUserInput && o.hasOpenQuestion(s.ID, phase) {
				if o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) {
					o.stopAnswering(s.ID, owner)
					return
				}
				if completionCheck == nil || completionCheck() == nil {
					o.stopAnswering(s.ID, owner)
					if o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) {
						return
					}
					o.completeAgent(ctx, s.ID, phase, ownerFence(phase, owner))
					return
				}
			}
			switch o.leaveWithUser(s.ID, phase, owner, dialog) {
			case dialogLeft:
				o.turnLeftForAnswer(s.ID)
				return
			case dialogTakenOver:
				o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted)
				return
			}
			if o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) {
				return
			}
			var checkErr error
			if completionCheck != nil {
				checkErr = completionCheck()
			}
			if o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) {
				return
			}
			if checkErr == nil {
				var notices []bus.Event
				if summaryRecovered {
					notices = append(notices, bus.Event{Type: bus.EventRetryScheduled, StageID: s.ID,
						Data: "execution_summary.md recovered; continuing with completion checks"})
				}
				o.completeAgent(ctx, s.ID, phase, ownerFence(phase, owner), notices...)
				return
			}
			if errors.Is(checkErr, executor.ErrUserInterrupted) {
				if phase != phaseAutonomous {
					onUserInterrupted()
				}
				return
			}
			// Summary отсутствует/пуст — отдельный бюджет из двух продолжений того же автора. Решение стоит до
			// общей incomplete-ветки и не трогает attempt: transport/verify сохраняют прежний потолок.
			var summaryErr *SummaryError
			if errors.As(checkErr, &summaryErr) {
				next, ok := o.handleMissingSummary(s.ID, summaryErr, owner, onUserInterrupted)
				if !ok {
					return
				}
				recovery = next
				attempt-- // цикл сделает attempt++ — итоговый номер попытки не меняется
				continue
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
						prevFingerprint, unchangedRejections, owner, onUserInterrupted)
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
					// Verification may finish after this turn was replaced; recheck before correction.
					if o.verifyOutcomeGuardHook != nil {
						o.verifyOutcomeGuardHook(s.ID)
					}
					if o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) {
						return
					}
					if isVerifyReject && !o.clearTurnSession(s.ID, phase, owner, freshCorrection) {
						o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted)
						return
					}
					if isVerifyReject {
						verifyFailures++
					}
					incompleteReason = checkErr.Error()
					// Раньше публиковалось как EventStageStatusChanged с Data- сообщением — фронт рисовал это пустым
					// бейджем "→ ", и на reload сообщение терялось.
					// Residual R3: this per-step notice can trail a later supersession;
					// unlike completion it has no durable FSM transition to attach to.
					msg := "incomplete work, retrying: " + checkErr.Error()
					o.publishNotice(bus.Event{Type: bus.EventRetryScheduled, StageID: s.ID, Data: msg})
					continue
				}
				// verify-бюджет исчерпан ИЛИ план-незавершёнка после attempt 0 —
				// падаем в общий fail-путь ниже.
			}
			// Missing artifact or second incomplete attempt — fail. См. commitVerifyFailure.
			if !isVerifyDrivenError(checkErr) {
				failAttempt(checkErr.Error()) // у автономной стадии — ошибка чтения summary и прочие исходы
				return
			}
			if !o.commitVerifyFailure(s.ID, phase, checkErr.Error(), owner, onUserInterrupted) {
				return
			}
			o.failBlockedStages()
			return
		}

		if errors.Is(err, executor.ErrUserInterrupted) {
			if phase != phaseAutonomous {
				onUserInterrupted()
			}
			return
		}

		if !isRetryableError(err) {
			// Drop the session file so a later retry starts a fresh Claude session instead of resuming a
			// conversation that was never created. Mirrors the retryable branch.
			if !o.clearTurnSession(s.ID, phase, owner, true) {
				o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted)
				return
			}
			failAttempt(err.Error())
			return
		}

		// Retryable errors are typically caused by an overgrown session context. Drop the session file so the
		// next attempt starts a fresh Claude session.
		if !o.clearTurnSession(s.ID, phase, owner, true) {
			o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted)
			return
		}

		if attempt < maxRetries {
			// Сохраняем сообщение retry в reason, чтобы reload восстановил тот же payload; seq дедуплицирует live и
			// историю.
			msg := fmt.Sprintf("attempt %d/%d in %v", attempt+1, maxRetries, retryBackoff)
			_, seq, scheduled := o.ownerTrigger(owner, phase, s.ID, bus.EvScheduleRetry,
				bus.GuardCtx{Phase: phase}, msg)
			if !scheduled && phase == phaseAutonomous {
				o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted) // стадией уже владеет другой автор
				return
			}
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
				failAttempt("cancelled during retry")
				return
			}
			if o.beforeRetryResumeHook != nil {
				o.beforeRetryResumeHook(s.ID)
			}
			resumePhase := phaseImplementation
			if phase == phasePlanning {
				resumePhase = phasePlanning
			}
			if _, _, ok := o.ownerTrigger(owner, phase, s.ID, bus.EvResumeAfterRetry,
				bus.GuardCtx{Phase: resumePhase}, ""); !ok && phase == phaseAutonomous {
				o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted)
				return
			}
		} else {
			_, seq, failed := o.ownerTrigger(owner, phase, s.ID, bus.EvFail,
				bus.GuardCtx{}, "retries exhausted")
			if !failed && phase == phaseAutonomous {
				o.yieldIfSuperseded(s.ID, phase, owner, onUserInterrupted)
				return
			}
			o.failBlockedStages()
			o.publishCritical(ctx, bus.Event{Type: bus.EventRetryExhausted, StageID: s.ID, Seq: seq})
		}
	}
}

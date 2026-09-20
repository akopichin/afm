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

// verifyOutcomeStillOwned — дешёвый, НЕ атомарный fast-path ПЕРЕД
// verify-driven исходом (needs_changes incomplete-retry на attempt 0 ИЛИ
// финальный verify-driven fail от исхода completionCheck) — конкурентный
// Pause()/Revise() мог долговечно перевести стадию в paused/revising уже
// ПОСЛЕ единственной F1-проверки статуса (тот switch срабатывает один раз,
// сразу после completionCheck(), см. выше), но ДО того, как этот код
// успевает зафиксировать свой собственный исход — completionCheck
// (gateWithVerify->RunVerification) может занять сколько угодно времени, и
// раз он уже вернулся, свежий Pause/Revise может проскочить именно в этом
// узком окне.
//
// ВАЖНО (H1, 6-е код-ревью): это ЧТЕНИЕ, а не CAS — между этим return и
// последующим коммитом (обычный `continue` цикла ИЛИ Trigger(EvVerifyFail) в
// commitVerifyFailure ниже) остаётся точно такой же TOCTOU-зазор, только
// уже сдвинутый на несколько строк, а не устранённый. Как fast-path
// (избежать лишней работы, когда уже точно видно, что стадию увели) это
// нормально и намеренно оставлено — но КОРРЕКТНОСТЬ финального
// verify-driven fail обеспечивает исключительно атомарный CAS
// Trigger(EvVerifyFail) в commitVerifyFailure (его ограниченный From-набор,
// см. bus/fsm.go), а не это чтение. Возвращает false, если стадия по
// состоянию НА МОМЕНТ ЭТОГО ЧТЕНИЯ уже не принадлежит текущему исходу
// (paused/revising) — вызывающий код обязан ничего не коммитить и просто
// вернуться (для revising — через onUserInterrupted, симметрично уже
// существующей F1-ветке выше: тот же самый Pause/Revise уже владеет
// стадией, повторный respawn делает именно onUserInterrupted).
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

// commitVerifyFailure — H1 (6-е код-ревью): атомарная фиксация
// verify-driven fail (needs_changes-исчерпание/exec-ошибка верификатора).
// Раньше (G2, 5-е ревью) единственной защитой от устаревшего исхода было
// повторное ЧТЕНИЕ статуса (verifyOutcomeStillOwned) перед голым
// Trigger(EvFail) — но EvFail's rule.From == nil разрешает ЛЮБОЙ
// нетерминальный статус, так что если конкурентный Pause()/Revise()
// коммитился ПОСЛЕ этого чтения и ДО самого Trigger (тот же TOCTOU-зазор,
// просто сдвинутый), устаревший verify-исход всё равно молча затирал
// paused/revising. Здесь вместо EvFail используется EvVerifyFail — его
// ограниченный From-набор (см. bus/fsm.go) заставляет саму FSM атомарно
// (внутри store CAS) отбросить переход, если стадия уже не в одном из
// "активных" статусов, а не полагаться на чтение снаружи.
//
// verifyOutcomeStillOwned вызывается здесь ПЕРЕД Trigger как дешёвый
// fast-path (см. его комментарий) — просто чтобы не публиковать
// verify-диагностику, когда уже заведомо видно, что стадию увели; сам факт
// "гонка произошла именно в узком зазоре между этим чтением и Trigger"
// проверяется ТОЛЬКО через ok, возвращённый Trigger.
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
	// CAS проиграл гонку: конкурентный Pause()/Revise() успел закоммититься
	// МЕЖДУ fast-path чтением выше и этим Trigger — сама FSM (ограниченный
	// From EvVerifyFail) атомарно отбросила переход, стадия больше не наша.
	// Читаем статус ЗДЕСЬ только чтобы выбрать корректный cleanup-колбэк
	// (revising требует respawn с фидбеком через onUserInterrupted, paused —
	// ничего) — факт "не наша" уже установлен через ok=false, а не через это
	// чтение.
	if o.currentStatus(stageID) == state.StatusRevising {
		onUserInterrupted()
	}
	return false
}

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
	// G3 (5-е код-ревью): CompareAndDelete, а не безусловный Delete — тот же
	// приём, что E3 применил к stageLeases (см. spawnAgentLeased). На
	// Revise-прерывании раннер-замена (generation B) может Store'ить свой
	// interruptCh под тем же stageID ДО того, как ЭТОТ (generation A) вызов
	// runWithRetry размотает свой defer (см. onUserInterrupted → respawn —
	// новое поколение спавнится раньше, чем возвращается прерванный agentFn).
	// Безусловный Delete стёр бы канал B, а не свой собственный —
	// runnerForVerify для generation B тогда не подключил бы InterruptCh
	// вовсе (~runner_factory.go:162), и Pause()/Revise() не смогли бы
	// остановить этот верификатор. CompareAndDelete удаляет запись, только
	// если она всё ещё РОВНО ТА, что сохранил этот вызов.
	defer o.interruptChans.CompareAndDelete(s.ID, interruptCh)

	// resumeCtx — однократный флаг, взведённый armResumeContext снаружи
	// (Continue после паузы non-interactive стадии, Task 7 "review notes"):
	// заставляет собрать retry-контекст ("Previously completed actions") уже
	// на attempt 0, а не только начиная с attempt > 0, — иначе резюмируемый
	// агент перезапускается с чистого листа, не видя уже сделанную работу.
	// LoadAndDelete снимает флаг сразу, чтобы следующий вызов той же стадии
	// (без повторного arm) снова получил обычное attempt-0 поведение.
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
	for attempt := 0; attempt <= maxRetries; attempt++ {
		retryCtx := ""
		if attempt > 0 || resumeCtx {
			retryCtx = buildRetryContext(stageDir, phase)
			if incompleteReason != "" {
				retryCtx += "\n\n## Completion check failed\n\n" + incompleteReason +
					"\n\nFix the underlying problem and make the check pass before finishing.\n"
			}
		}
		resumeCtx = false // только самая первая попытка резюмируемого запуска получает его

		if o.currentStatus(s.ID) == state.StatusPaused {
			return // a pause landed in the shouldRun->register gap; do not start the subprocess
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
					o.publishCritical(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: s.ID, Data: phase})
				}
				return
			}
			var checkErr error
			if completionCheck != nil {
				checkErr = completionCheck()
			}
			// F1 (4-е код-ревью): completionCheck (gateWithVerify->RunVerification)
			// может занять сколько угодно времени ПОСЛЕ того, как автор уже
			// вернулся, — за это время Pause()/Revise() мог долговечно перевести
			// стадию в paused/revising. Раньше эта реконсиляция стояла ТОЛЬКО в
			// ветке checkErr==nil (успешный verify/completion) — needs_changes
			// уходил в коррекцию СТАРОГО поколения (см. ветку incomplete-retry
			// ниже), а exec/inconclusive/timeout/storage-ошибка — прямиком в
			// EvFail, который FSM разрешает даже из revising (From: nil у EvFail в
			// bus/fsm.go, см. ruleAllowsFrom — "любой нетерминальный статус"),
			// молча затирая уже случившийся Revise/Pause человека. Проверяем ЗДЕСЬ,
			// один раз, ДО того как ветвиться на pass/needs_changes/exec-ошибку —
			// так реконсиляция покрывает ЛЮБОЙ исход completionCheck одинаково.
			//
			// paused — восстанавливать нечего (Pause() уже сделал долговечный
			// переход). revising — перезапускаем АВТОРА с фидбеком тем же
			// respawn-callback'ом, каким обрабатывается ErrUserInterrupted из
			// самого agentFn ниже (verify никогда не является kind'ом для
			// резюма) — симметрично E4 (третье код-ревью), но теперь для ВСЕХ
			// исходов, а не только pass.
			switch o.currentStatus(s.ID) {
			case state.StatusPaused:
				return
			case state.StatusRevising:
				onUserInterrupted()
				return
			}
			if checkErr == nil {
				stagefiles.AppendNotice(o.opts.RunDir, s.ID, string(bus.EventAgentCompleted), phase)
				o.publishCritical(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: s.ID, Data: phase})
				return
			}
			// AI-verify (V5a): верификатор слушает тот же interruptChans, что и
			// автор (runnerForVerify) — Pause()/Revise() во время verify всплывает
			// сюда либо этим сентинелом (мягкий SIGINT verify-субпроцесса или
			// прерванное ожидание слота верификатора, см. runVerifyAgentStep),
			// либо (см. switch выше) уже долговечным paused/revising-статусом.
			if errors.Is(checkErr, executor.ErrUserInterrupted) {
				onUserInterrupted()
				return
			}
			// Incomplete work — retry once without backoff
			if stagefiles.IsIncompleteWorkError(checkErr) && attempt == 0 {
				// G2 (5-е код-ревью): повторная проверка ПРЯМО ПЕРЕД
				// incompleteReason/continue — см. verifyOutcomeStillOwned.
				//
				// H1 (6-е код-ревью) рассмотрел этот call site отдельно: в
				// отличие от финального verify-driven fail (см.
				// commitVerifyFailure), здесь НЕТ FSM-перехода, который можно
				// было бы затереть — `continue` ниже лишь возвращает цикл к
				// его же top-of-loop проверке (currentStatus == Paused) и
				// повторному вызову agentFn. Остаточный TOCTOU-зазор между
				// этим чтением и `continue` не может ничего разрушить: любой
				// Pause()/Revise(), закоммитившийся именно в этом зазоре, уже
				// оставил СИГНАЛ на interruptCh (тот же канал, что
				// зарегистрирован для ВСЕЙ этой горутины, см. Store у входа в
				// runWithRetry) — следующий agentFn() у этой же стадии
				// проходит через runnerFor(...).RunAgent -> executor.run,
				// который делает неблокирующую проверку InterruptCh ПРЯМО
				// ПЕРЕД cmd.Start() (см. pkg/executor: "D4 код-ревью") и,
				// если сигнал уже есть, возвращает ErrUserInterrupted, ДАЖЕ
				// НЕ ЗАПУСКАЯ subprocess. Этот err уже обрабатывается веткой
				// errors.Is(err, executor.ErrUserInterrupted) выше в этом же
				// цикле — paused и revising различаются ТАМ ЖЕ, тем же
				// способом, что и everywhere else. Поэтому здесь достаточно
				// дешёвого fast-path чтения (экономит incompleteReason/
				// EventRetryScheduled на заведомо чужой стадии) — атомарная
				// защита от клобберинга не нужна, потому что клобберить
				// нечего.
				if o.verifyOutcomeGuardHook != nil {
					o.verifyOutcomeGuardHook(s.ID)
				}
				if !o.verifyOutcomeStillOwned(s.ID, onUserInterrupted) {
					return
				}
				incompleteReason = checkErr.Error()
				// Раньше публиковалось как EventStageStatusChanged с Data-
				// сообщением (а не статусом) — фронт (extractStatusString)
				// рисовал это пустым бейджем "→ ", и на reload сообщение
				// терялось (не FSM-переход → нет в events.jsonl). По смыслу
				// это "сейчас будет повторная попытка": шлём EventRetryScheduled
				// (рендерится как "retry: <msg>") и дублируем в notices.jsonl,
				// чтобы пережить reload.
				msg := "incomplete work, retrying: " + checkErr.Error()
				o.ui.Publish(bus.Event{Type: bus.EventRetryScheduled, StageID: s.ID, Data: msg})
				stagefiles.AppendNotice(o.opts.RunDir, s.ID, string(bus.EventRetryScheduled), msg)
				continue
			}
			// Missing artifact or second incomplete attempt — fail. Причина —
			// текст самой ошибки completionCheck (IncompleteWorkError/
			// MissingArtifactError/VerifyRejectedError/VerifyExecError.Error()),
			// а не общая заглушка: только так диагностика AI-verify (номер
			// шага, id отчёта — см. VerifyRejectedError.Error()/
			// VerifyExecError.Error()) остаётся видна прямо в FSM-транзишне,
			// а не только в файлах на диске (verify/<id>/report.md).
			//
			// H1 (6-е код-ревью): verify-driven исход (needs_changes-
			// исчерпание/exec-ошибка верификатора) фиксируется через
			// commitVerifyFailure (EvVerifyFail с ограниченным From,
			// атомарный CAS) — иначе поздний verify-исход мог бы затереть
			// уже случившийся конкурентный Pause()/Revise() в TOCTOU-зазоре
			// между чтением статуса и коммитом перехода. См. commitVerifyFailure.
			//
			// H2 (7-е код-ревью): commitVerifyFailure раньше вызывался
			// БЕЗУСЛОВНО для ЛЮБОЙ ошибки, дошедшей до этой точки — но сюда
			// попадают и планировочные ошибки (missing/empty plan.md,
			// вторая неполная секция плана — CheckPlanCompletionFor,
			// agents.go's runPlanningAgent/runPlanningWithFeedback), которые
			// НИКОГДА не проходят verify (planning вообще не оборачивается
			// gateWithVerify, см. verify.go). EvVerifyFail's From сознательно
			// не включает StatusPlanning (см. bus/fsm.go) — CAS проигрывал,
			// commitVerifyFailure возвращал false, и planning-стадия
			// зависала в "planning" НАВСЕГДА без единого Trigger(EvFail).
			// Та же ловушка ждала любую другую не-verify ошибку (missing
			// artifact/missing sections/generic fatal) исполнительской
			// стадии — просто EvVerifyFail's From для неё совпадает с
			// обычными активными статусами, поэтому регрессия там незаметна
			// (но CAS-защита там и не нужна — см. isVerifyDrivenError).
			//
			// Фикс: атомарный EvVerifyFail применяется ТОЛЬКО к настоящим
			// verify-исходам (isVerifyDrivenError — *VerifyRejectedError/
			// *VerifyExecError); для всего остального — обычный
			// Trigger(EvFail) (From: nil), ровно как до появления AI-verify.
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
			// Сообщение кладём в reason самой transition, чтобы на reload
			// transitionToFeedEvents реконструировал retry_scheduled с тем же
			// payload'ом (Data: t.Reason), а не с пустой строкой — раньше reason
			// был "", и после перезагрузки строка показывала "retry:" без
			// attempt/backoff. Live и историческое событие дедуплицируются по
			// seq, дубля не будет.
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

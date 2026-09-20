package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// verifyExecutionLabel — execution-purpose метка AI-verify для логов/usage
// (accounting.Observation.Phase, executor.Config.Phase). НЕ входит в
// flow.Phases() — verify не рантайм-фаза стадии (planning/implementation/
// review/autonomous_execution), а отдельный, ортогональный проход ПОСЛЕ
// того как стадия уже заявила о завершении по file-probe.
const verifyExecutionLabel = "verify"

// verifyShellOutputLimit — тот же лимит хвоста вывода, что был у удалённого
// legacy stagefiles.RunVerify (constant вынесена в stagefiles.VerifyOutputLimit
// до его удаления и переиспользуется здесь), сохраняет прежний текст ошибки
// shell-шага один в один.
const verifyShellOutputLimit = stagefiles.VerifyOutputLimit

// verifyOutcomePass/verifyOutcomeError — значения ManifestStep.Outcome для
// успешно пройденного шага и для шага, который не удалось оценить
// (инфраструктурная/storage-ошибка) — единая точка определения вместо
// разбросанных строковых литералов "pass"/"error" (goconst).
const (
	verifyOutcomePass  = "pass"
	verifyOutcomeError = "error"
)

// Ключи payload'а observer-событий EventVerifyStarted/EventVerifyResult
// (V5a.3) — единая точка вместо разбросанных строковых литералов.
const (
	keyVerificationID = "verification_id"
	keyStep           = "step"
	keyVerifyKind     = "kind"
	keyVerifyCommand  = "command"
	keyVerdict        = "verdict"
	keyExecErrorKind  = "exec_error"
	keyVerifyReason   = "reason"
	keyReportPath     = "report_path"
)

// execErrorKindExecFailure/execErrorKindInterrupted — грубая классификация
// "почему у шага нет вердикта" для payload'а EventVerifyResult: interrupted —
// шаг прерван извне (Pause/Revise/отмена рана), exec_failure — любая другая
// причина не получить вердикт (транспорт/протокол/таймаут/сбой сохранения/
// ненулевой exit shell-команды уже нормализован в needs_changes и сюда не
// попадает).
const (
	execErrorKindInterrupted = "interrupted"
	execErrorKindExecFailure = "exec_failure"
)

// emitVerifyStarted публикует наблюдательное событие "шаг verify начал
// выполняться" — ТОЧНО тот же паттерн, что EventAutoAnswered (см. AGENTS.md
// "Auto-answering questions"): live через o.ui.Publish И durable через
// stagefiles.AppendNotice в notices.jsonl, чтобы клиент, подключившийся или
// перезагрузивший страницу ПОСЛЕ события, всё равно увидел его в ленте
// (server's reconstructNotices реплеит notices.jsonl). НЕ FSM-событие: не
// входит в events.jsonl и не входит в flow.Phases().
func (o *Orchestrator) emitVerifyStarted(stageID, verID string, idx int, kind, command string) {
	data := map[string]any{
		keyVerificationID: verID,
		keyStep:           idx,
		keyVerifyKind:     kind,
		keyVerifyCommand:  command,
	}
	o.ui.Publish(bus.Event{Type: bus.EventVerifyStarted, StageID: stageID, Data: data})
	stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventVerifyStarted), data)
}

// emitVerifyResult публикует наблюдательное событие "шаг verify завершился" —
// см. emitVerifyStarted. Ровно один из (verdict, execErrorKind) непустой:
// verdict — когда модель/shell-код реально произвели вердикт (pass/
// needs_changes/inconclusive); execErrorKind — когда шаг вообще не удалось
// оценить. reportPath — "" если для этого исхода report.md не пишется
// (проходящий shell-шаг, любая exec-ошибка).
func (o *Orchestrator) emitVerifyResult(stageID, verID string, idx int, kind, command string, verdict verify.Verdict, execErrorKind, reason, reportPath string) {
	data := map[string]any{
		keyVerificationID: verID,
		keyStep:           idx,
		keyVerifyKind:     kind,
		keyVerifyCommand:  command,
		keyVerifyReason:   reason,
	}
	if verdict != "" {
		data[keyVerdict] = string(verdict)
	}
	if execErrorKind != "" {
		data[keyExecErrorKind] = execErrorKind
	}
	if reportPath != "" {
		data[keyReportPath] = reportPath
	}
	o.ui.Publish(bus.Event{Type: bus.EventVerifyResult, StageID: stageID, Data: data})
	stagefiles.AppendNotice(o.opts.RunDir, stageID, string(bus.EventVerifyResult), data)
}

// verifyReportPath возвращает абсолютный путь report.md для данного прохода —
// единая точка вместо дублирования filepath.Join в каждом call site'е.
func verifyReportPath(stageDir, verID string) string {
	return filepath.Join(stagefiles.VerifyDir(stageDir), verID, "report.md")
}

// verifyAgentRunner — сигнатура функции, реально исполняющей один agent-шаг
// AI-verify. Инъектируемый seam (тот же приём, что o.spawnJSONFix и
// memorypipeline.AgentRunner): продакшн — execVerifyAgent (runner_factory.go),
// тесты подменяют o.runVerifyAgent напрямую, без реального subprocess.
type verifyAgentRunner func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error)

// RunVerification выполняет ОДИН последовательный fail-fast проход по
// s.Verify.Steps для стадии, уже прошедшей file-probe (CheckCompletion/
// CheckAutonomousCompletion). Первый непройденный шаг ОСТАНАВЛИВАЕТ проход —
// остальные шаги остаются "not-run" в манифесте, никакого внутреннего
// цикла коррекции/повторного запуска автора здесь нет (одна попытка на
// вызов; повтор — забота вызывающего кода через существующую incomplete-
// retry политику, см. VerifyRejectedError).
//
// Возвращает:
//   - nil                  — все шаги пройдены; активный feedback очищен;
//   - *VerifyRejectedError — шаг вернул needs-changes-эквивалент (durable
//     feedback уже записан на диск);
//   - *VerifyExecError     — шаг не удалось оценить (транспорт/протокол/
//     таймаут/inconclusive/сбой сохранения/ненулевой exit верификатора);
//   - executor.ErrUserInterrupted — проход прерван извне (см. вызывающий код).
func (o *Orchestrator) RunVerification(ctx context.Context, s flow.Stage, phase string) error {
	if s.Verify.IsEmpty() {
		return nil
	}

	// lease — общий с раннером-автором слот командного семафора (V4b, см.
	// Orchestrator.stageLeases): если стадия заспавнена обычным путём
	// (spawnKind), lease сейчас удерживает слот Stage.Command. Каждый
	// agent-шаг verify временно переводит его на свою команду
	// (runVerifyAgentStep → Lease.SwapTo) и возвращает автору здесь, в defer —
	// ПОСЛЕ ЛЮБОГО исхода (pass/reject/exec error/паника). Отсутствие lease
	// (ok=false — путь резюма стадии в обход spawnKind, см. комментарий поля)
	// — безопасная деградация: просто не участвуем в переносе слота.
	if lease, ok := o.stageLeases.Load(s.ID); ok {
		l := lease.(*concurrency.Lease)
		defer func() {
			if err := l.SwapTo(ctx, s.Command); err != nil {
				log.Printf("WARN: verify: return lease to author command for stage %s: %v", s.ID, err)
			}
		}()
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	verID := stagefiles.NewVerificationID()

	manifestSteps := make([]stagefiles.ManifestStep, len(s.Verify.Steps))
	for i, st := range s.Verify.Steps {
		manifestSteps[i] = stagefiles.ManifestStep{
			Index:   i + 1,
			Kind:    verifyStepManifestKind(st),
			Command: verifyStepManifestCommand(st),
			Outcome: "not-run",
		}
	}
	persistManifest := func() error {
		return stagefiles.WriteManifest(stageDir, verID, stagefiles.Manifest{
			RunID:          o.opts.RunID,
			StageID:        s.ID,
			Phase:          phase,
			VerificationID: verID,
			CreatedAt:      time.Now(),
			Steps:          append([]stagefiles.ManifestStep(nil), manifestSteps...),
		})
	}

	plan, _ := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	authorSummary, _ := os.ReadFile(filepath.Join(stageDir, "execution_summary.md"))
	artCtx, artErr := stagefiles.CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	if artErr != nil {
		log.Printf("WARN: collect artifacts for %s verify: %v", s.ID, artErr)
	}
	prevReport := previousVerifyReport(stageDir)

	var passedSteps strings.Builder

	for i, st := range s.Verify.Steps {
		idx := i + 1
		kind := verifyStepManifestKind(st)
		command := verifyStepManifestCommand(st)
		o.emitVerifyStarted(s.ID, verID, idx, kind, command)

		if st.Kind == flow.VerifyAgent {
			result, alias, err := o.runVerifyAgentStep(ctx, s, st, idx, verID, prompts.VerifyInputs{
				Template:       o.opts.Prompts.Verify,
				Stage:          s,
				GlobalPrompt:   o.opts.GlobalPrompt,
				StagePrompt:    s.Prompt,
				Plan:           string(plan),
				Artifacts:      artCtx,
				AuthorSummary:  string(authorSummary),
				PassedSteps:    passedSteps.String(),
				PreviousReport: prevReport,
				VerifyPrompt:   st.Prompt,
			})
			if err != nil {
				manifestSteps[i].Outcome = verifyManifestErrorOutcome(err)
				_ = persistManifest()
				execKind := execErrorKindExecFailure
				if errors.Is(err, executor.ErrUserInterrupted) {
					execKind = execErrorKindInterrupted
				}
				o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execKind, err.Error(), "")
				return err
			}

			switch result.Verdict {
			case verify.VerdictPass:
				if perr := stagefiles.SaveAcceptedResult(stageDir, verID, idx, result); perr != nil {
					manifestSteps[i].Outcome = verifyOutcomeError
					_ = persistManifest()
					reason := "verify storage failure: " + perr.Error()
					o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
					return &VerifyExecError{Reason: reason, Step: idx}
				}
				if perr := stagefiles.WriteReport(stageDir, verID, result, alias, idx); perr != nil {
					manifestSteps[i].Outcome = verifyOutcomeError
					_ = persistManifest()
					reason := "verify storage failure: " + perr.Error()
					o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
					return &VerifyExecError{Reason: reason, Step: idx}
				}
				manifestSteps[i].Outcome = string(result.Verdict)
				fmt.Fprintf(&passedSteps, "Step %d (agent %s): "+verifyOutcomePass+" — %s\n", idx, alias, result.Summary)
				o.emitVerifyResult(s.ID, verID, idx, kind, command, verify.VerdictPass, "", result.Summary, verifyReportPath(stageDir, verID))

			case verify.VerdictNeedsChanges:
				manifestSteps[i].Outcome = string(result.Verdict)
				reason := "verify needs changes (step " + fmt.Sprint(idx) + "): " + result.Summary
				return o.persistVerifyRejection(stageDir, verID, idx, kind, alias, reason, result, manifestSteps, s.ID, phase)

			case verify.VerdictInconclusive:
				manifestSteps[i].Outcome = string(result.Verdict)
				_ = persistManifest()
				o.emitVerifyResult(s.ID, verID, idx, kind, command, verify.VerdictInconclusive, "", result.Summary, "")
				return &VerifyExecError{Reason: "verify inconclusive: " + result.Summary, Step: idx}
			default:
				// Недостижимо при исправном executor.RunVerifyAgent
				// (DecodeModelResult допускает только три вердикта) —
				// защитная ветка на случай нестандартного фейкового
				// раннера в тестах: неизвестный вердикт НИКОГДА не pass.
				manifestSteps[i].Outcome = verifyOutcomeError
				_ = persistManifest()
				reason := fmt.Sprintf("verify produced unknown verdict %q", result.Verdict)
				o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
				return &VerifyExecError{Reason: reason, Step: idx}
			}
			continue
		}

		// Shell-шаг: отменяемый процесс (honor ctx), CWD — legacy "." (тот же
		// literal, что CheckCompletion передавал в RunVerify до V4a.1 —
		// см. CWD note брифа: не "чинить" контракт здесь).
		out, runErr := runVerifyShellCommand(ctx, ".", st.Run)
		stepDir := stagefiles.StepDir(stageDir, verID, idx)
		if mkErr := os.MkdirAll(stepDir, 0755); mkErr != nil {
			manifestSteps[i].Outcome = verifyOutcomeError
			_ = persistManifest()
			reason := "verify storage failure: " + mkErr.Error()
			o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
			return &VerifyExecError{Reason: reason, Step: idx}
		}
		if werr := os.WriteFile(filepath.Join(stepDir, "command.log"), []byte(out), 0644); werr != nil {
			manifestSteps[i].Outcome = verifyOutcomeError
			_ = persistManifest()
			reason := "verify storage failure: " + werr.Error()
			o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
			return &VerifyExecError{Reason: reason, Step: idx}
		}
		if runErr != nil {
			// Ненулевой exit == needs-changes-эквивалент: нормализуем в тот
			// же verify.ModelResult, что и agent-шаг, и проводим через ТОТ
			// ЖЕ commit order — единый путь persist+return для обеих форм
			// verify-отказа. Текст legacy-ошибки (для IncompleteWorkError.Reason)
			// сохраняем байт-в-байт как раньше делал stagefiles.RunVerify.
			tail := stagefiles.TailOutput(out, verifyShellOutputLimit)
			legacyReason := fmt.Sprintf("verify command failed (%v):\n%s", runErr, tail)
			result := shellRejectionResult(st.Run, tail)
			manifestSteps[i].Outcome = string(result.Verdict)
			return o.persistVerifyRejection(stageDir, verID, idx, kind, st.Run, legacyReason, result, manifestSteps, s.ID, phase)
		}
		manifestSteps[i].Outcome = verifyOutcomePass
		fmt.Fprintf(&passedSteps, "Step %d (shell): `%s` — passed\n", idx, st.Run)
		o.emitVerifyResult(s.ID, verID, idx, kind, command, verify.VerdictPass, "", "passed", "")
	}

	if err := persistManifest(); err != nil {
		return &VerifyExecError{Reason: "verify storage failure: " + err.Error()}
	}
	if err := stagefiles.ClearActiveFeedback(stageDir); err != nil {
		return &VerifyExecError{Reason: "verify storage failure: " + err.Error()}
	}
	return nil
}

// runVerifyAgentStep запускает один agent-шаг через инъектируемый
// o.runVerifyAgent и переводит verify.RunOutcome в либо провалидированный
// verify.ModelResult (готовый к дальнейшей обработке в RunVerification),
// либо СРАЗУ типизированную ошибку (VerifyExecError/executor.ErrUserInterrupted) —
// вызывающий код в этом случае просто возвращает err как есть. alias —
// команда агента (для отчёта/manifest).
func (o *Orchestrator) runVerifyAgentStep(ctx context.Context, s flow.Stage, st flow.VerifyStep, idx int, verID string, in prompts.VerifyInputs) (verify.ModelResult, string, error) {
	// Перед запуском agent-шага переводим lease на его команду (см.
	// RunVerification: тот же lease будет возвращён автору в defer, каким бы
	// ни оказался исход). Тот же командный слот, той же семантики SwapTo:
	// та же команда, что у автора, — no-op; другая — release-before-acquire.
	//
	// Ожидание слота (если команда верификатора занята max_parallel'ом)
	// обёрнуто o.pauseAwareVerifyCtx (V5a): ctx здесь — run-scoped ctx,
	// который Pause() НЕ отменяет (см. control_api.go's Pause — он только
	// долговечно переводит FSM и сигналит o.interruptChans, ctx не трогает).
	// Без этого пауза во время ожидания busy-семафора верификатора не
	// прерывала бы ожидание — стадия оставалась бы занятой этим ожиданием
	// до тех пор, пока слот не освободится сам по себе.
	if lease, ok := o.stageLeases.Load(s.ID); ok {
		waitCtx, stop := o.pauseAwareVerifyCtx(ctx, s.ID)
		swapErr := lease.(*concurrency.Lease).SwapTo(waitCtx, st.Command)
		interruptedDuringWait := stop()
		if swapErr != nil {
			if ctx.Err() == nil {
				// Родительский (run-scoped) ctx жив — ждать перестали именно
				// из-за сигнала паузы/ревизии ЭТОЙ стадии (interruptChans), а
				// не из-за отмены всего рана. Тот же сентинел, что и
				// прерывание уже запущенного verify-субпроцесса ниже —
				// runWithRetry (retry.go) обрабатывает оба источника
				// прерывания одинаково.
				return verify.ModelResult{}, st.Command, executor.ErrUserInterrupted
			}
			return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify lease swap failed: " + swapErr.Error(), Step: idx}
		}
		if interruptedDuringWait {
			// C5 код-ревью: SwapTo вернул nil (noop-семафор/одинаковая
			// команда — их fast path не блокируется и потому сам по себе не
			// видит отмену), но за время ожидания сигнал паузы/ревизии
			// РЕАЛЬНО пришёл и был поглощён именно этим ожиданием — не даём
			// верификатору стартовать на стадии, которая уже считается
			// прерванной, даже если формально lease "успешно" переведён.
			return verify.ModelResult{}, st.Command, executor.ErrUserInterrupted
		}
	}

	// C5 код-ревью: последняя точка ПЕРЕД стартом verify-субпроцесса — даже
	// если lease вообще не участвовал (safe no-op деградация выше) или своп
	// прошёл гладко без единого сигнала во время ожидания, отдельно
	// перепроверяем ctx/статус стадии прямо перед запуском. Закрывает окно
	// между "lease переведён" и "субпроцесс стартовал", в котором Pause()
	// мог durable перевести стадию в paused уже ПОСЛЕ того, как мы перестали
	// ждать lease.
	if aborted, abortErr := o.verifyLaunchAborted(ctx, s.ID); aborted {
		return verify.ModelResult{}, st.Command, abortErr
	}

	stepDir := stagefiles.StepDir(filepath.Join(o.opts.RunDir, s.ID), verID, idx)
	// Найдено этой задачей (V5a): в отличие от shell-шага чуть выше по файлу
	// (который явно создаёт stepDir перед записью command.log), agent-шаг
	// никогда не создавал stepDir сам — progress.NewLogger просто
	// os.OpenFile'ит agentLog без MkdirAll родителя. В продакшне ЛЮБОЙ
	// реальный agent-шаг verify падал бы с "open log file: ... no such file
	// or directory" ещё до первого запуска verify-субпроцесса; это было
	// незаметно во всех прежних тестах, потому что все они подменяют
	// o.runVerifyAgent фейком, минуя реальную файловую систему.
	if mkErr := os.MkdirAll(stepDir, 0755); mkErr != nil {
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify storage failure: " + mkErr.Error(), Step: idx}
	}
	agentLog := filepath.Join(stepDir, "agent.log")
	rawResult := filepath.Join(stepDir, "raw-result.json")

	prompt := prompts.BuildVerify(in)
	outcome, err := o.runVerifyAgent(ctx, s, st.Command, prompt, agentLog, rawResult)
	if err != nil {
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify execution failed: " + err.Error(), Step: idx}
	}

	if outcome.Interrupted {
		return verify.ModelResult{}, st.Command, executor.ErrUserInterrupted
	}
	if !outcome.ProcessOK {
		reason := "verify execution failed"
		if outcome.TimedOut {
			reason = "verify timed out"
		}
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: reason, Step: idx}
	}
	if outcome.ProtocolErr != nil {
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify protocol error: " + outcome.ProtocolErr.Error(), Step: idx}
	}
	if outcome.Result == nil {
		// Недостижимо при исправном executor.RunVerifyAgent (ProcessOK &&
		// ProtocolErr==nil ⇒ Result!=nil) — защитная ветка на случай
		// нестандартного фейкового раннера в тестах.
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify produced no result", Step: idx}
	}
	return *outcome.Result, st.Command, nil
}

// pauseAwareVerifyCtx возвращает ctx, отменяемый ЛИБО отменой родителя, ЛИБО
// сигналом на o.interruptChans[stageID] (Pause()/Revise(), см. control_api.go)
// — что наступит раньше, — и функцию stop, которую вызывающий код ОБЯЗАН
// вызвать сразу после того, как перестал ждать (успешно или нет), ДО того как
// канал снова понадобится реальному verify-субпроцессу (см. runnerForVerify).
// stop синхронно дожидается выхода горутины-наблюдателя (без этого могла бы
// возникнуть гонка, в которой наблюдатель, ещё не заметивший отмену, украл бы
// ПОЗДНЕЙШИЙ сигнал паузы, адресованный уже запущенному verify-процессу) и
// возвращает true, если ctx был отменён ИМЕННО сигналом на ic (а не тем, что
// вызывающий код сам прекратил ждать) — см. C5: SwapTo для noop-семафора/
// одинаковой команды не проверяет ctx во время самого ожидания (там нечего
// ждать), так что только этот флаг говорит вызывающему коду "сигнал реально
// пришёл, даже хотя SwapTo этого не заметил". Нет зарегистрированного канала
// для стадии (например, вызов вне runWithRetry) — безопасная деградация: ctx
// прокидывается как есть, stop всегда возвращает false.
func (o *Orchestrator) pauseAwareVerifyCtx(parent context.Context, stageID string) (ctx context.Context, stop func() bool) {
	ch, ok := o.interruptChans.Load(stageID)
	if !ok {
		return parent, func() bool { return false }
	}
	ic := ch.(chan struct{})
	cctx, cancel := context.WithCancel(parent)
	interrupted := make(chan bool, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Приоритетная неблокирующая проверка ПЕРЕД основным select: если
		// сигнал паузы уже лежит в канале (буферизован) к моменту, когда эта
		// горутина реально получает CPU, он ДОЛЖЕН быть замечен — иначе
		// возможен ложноотрицательный результат: если вызывающий код к этому
		// моменту уже вызвал stop() (который сам делает cancel()), основной
		// select ниже увидел бы ОБА case'а одновременно готовыми (<-ic И
		// <-cctx.Done()) и мог бы псевдослучайно выбрать cctx.Done(),
		// потеряв реальный, уже пришедший сигнал паузы.
		select {
		case <-ic:
			interrupted <- true
			cancel()
			return
		default:
		}
		select {
		case <-ic:
			interrupted <- true
			cancel()
		case <-cctx.Done():
			interrupted <- false
		}
	}()
	return cctx, func() bool {
		cancel()
		<-done
		return <-interrupted
	}
}

// verifyLaunchAborted — последняя проверка ПЕРЕД стартом verify-субпроцесса
// (C5 код-ревью): true, если родительский ctx уже отменён (полная отмена
// рана либо истёкший таймаут шага) ИЛИ (когда доступен Store) стадия уже
// durable ушла в paused/revising. o.opts.Store может быть nil в модульных
// тестах, строящих Orchestrator напрямую без полноценного Store (см.
// newVerifyTestOrchestrator) — деградация: проверяем только ctx.
func (o *Orchestrator) verifyLaunchAborted(ctx context.Context, stageID string) (bool, error) {
	if ctx.Err() != nil {
		return true, executor.ErrUserInterrupted
	}
	if o.opts.Store == nil {
		return false, nil
	}
	switch o.currentStatus(stageID) {
	case state.StatusPaused, state.StatusRevising:
		return true, executor.ErrUserInterrupted
	default:
		return false, nil
	}
}

// persistVerifyRejection выполняет commit order для needs_changes-эквивалента
// (V2a §6.3), общий для agent-шага и synthetic-результата shell-шага:
//  1. вердикт уже готов и провалидирован (result);
//  2. SaveAcceptedResult + WriteReport + WriteManifest;
//  3. WriteActiveFeedback атомарно;
//  4. ТОЛЬКО после успеха всех предыдущих — emitVerifyResult (V5a.3) и
//     типизированный VerifyRejectedError.
//
// Сбой ЛЮБОЙ персистентности на этом пути — VerifyExecError (fail closed):
// стадия не должна быть ни молча принята, ни отклонена с недописанными на
// диск файлами (частично записанный проход хуже отсутствующего). kind —
// "agent"|"shell", для payload'а observer-события EventVerifyResult; alias —
// команда агента либо shell-строка (используется и как WriteReport'овский
// alias, и как payload'овский command).
func (o *Orchestrator) persistVerifyRejection(stageDir, verID string, idx int, kind, alias, reason string, result verify.ModelResult, manifestSteps []stagefiles.ManifestStep, stageID, phase string) error {
	if err := stagefiles.SaveAcceptedResult(stageDir, verID, idx, result); err != nil {
		return &VerifyExecError{Reason: "verify storage failure: " + err.Error(), Step: idx}
	}
	if err := stagefiles.WriteReport(stageDir, verID, result, alias, idx); err != nil {
		return &VerifyExecError{Reason: "verify storage failure: " + err.Error(), Step: idx}
	}
	if err := stagefiles.WriteManifest(stageDir, verID, stagefiles.Manifest{
		RunID:          o.opts.RunID,
		StageID:        stageID,
		Phase:          phase,
		VerificationID: verID,
		CreatedAt:      time.Now(),
		Steps:          append([]stagefiles.ManifestStep(nil), manifestSteps...),
	}); err != nil {
		return &VerifyExecError{Reason: "verify storage failure: " + err.Error(), Step: idx}
	}
	if err := stagefiles.WriteActiveFeedback(stageDir, verID, result, alias, idx); err != nil {
		return &VerifyExecError{Reason: "verify storage failure: " + err.Error(), Step: idx}
	}
	o.emitVerifyResult(stageID, verID, idx, kind, alias, result.Verdict, "", result.Summary, verifyReportPath(stageDir, verID))
	return &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: reason},
		ReportID:            verID,
		Step:                idx,
	}
}

// verifyManifestErrorOutcome сообщает, каким текстом пометить ManifestStep
// при ошибке agent-шага: "interrupted" для прерывания извне, иначе "error".
func verifyManifestErrorOutcome(err error) string {
	if errors.Is(err, executor.ErrUserInterrupted) {
		return execErrorKindInterrupted
	}
	return verifyOutcomeError
}

// shellRejectionResult нормализует провал shell-команды в тот же
// verify.ModelResult, каким отвечает agent-шаг с вердиктом needs_changes —
// единый формат result.json/report.md для обеих форм verify независимо от
// того, кто их произвёл.
func shellRejectionResult(command, tail string) verify.ModelResult {
	return verify.ModelResult{
		SchemaVersion: verify.SupportedSchemaVersion,
		Verdict:       verify.VerdictNeedsChanges,
		Summary:       "shell verify command failed: " + command,
		Findings: []verify.Finding{{
			Blocking:    true,
			Title:       "shell verify command failed",
			Requirement: command,
			Evidence:    tail,
			MinimalFix:  "fix the underlying issue so the command exits 0",
		}},
	}
}

// runVerifyShellCommand запускает shell-verify команду отменяемо (honor
// ctx) — в отличие от legacy stagefiles.RunVerify (exec.Command без ctx),
// движок обязан уметь прервать зависшую команду по отмене контекста рана.
// НЕ переиспользует runScriptWithRetry (hooks.go) — у того своя, независимая
// retry-политика, которая не должна применяться здесь (ОДИН проход, без
// внутреннего цикла коррекции).
func runVerifyShellCommand(ctx context.Context, dir, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// previousVerifyReport читает report.md прохода, чей feedback ещё активен
// (verify/feedback.md), чтобы agent-шаг мог оценить закрытие прежних
// blockers, а не искать их заново. Пусто, если активного feedback нет или
// report.md прочитать не удалось (лучше пустой контекст, чем упавший engine
// из-за отсутствующего вспомогательного файла).
func previousVerifyReport(stageDir string) string {
	_, verID, ok := stagefiles.LoadActiveFeedback(stageDir)
	if !ok || verID == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(stagefiles.VerifyDir(stageDir), verID, "report.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// verifyFeedbackHeading — заголовок блока, которым активный machine-feedback
// AI-verify (verify/feedback.md) вклеивается в RetryContext автора (V4b.5).
const verifyFeedbackHeading = "## Замечания автоматической проверки"

// verifyFeedbackBlock читает АКТИВНЫЙ machine-feedback от AI-verify
// (verify/feedback.md, см. stagefiles.LoadActiveFeedback/WriteActiveFeedback)
// и оборачивает его в блок для RetryContext автора — "" если активного
// feedback нет (обычное состояние стадии без отклонённого прохода). Текст
// уже содержит вердикт, путь к полному report.md и блокирующие finding'и
// (см. stagefiles.renderActiveFeedback), здесь только читаем и оформляем
// заголовком — НИКОГДА не пишем через state.SaveFeedback/SaveFeedbackOnce:
// человеческий feedback.md (agent_suggest/Revise) и машинный
// verify/feedback.md — намеренно РАЗНЫЕ файлы, и оба должны остаться в
// RetryContext рядом, не затирая друг друга. Вызывается заново на КАЖДОЙ
// попытке (внутри closure agentFn, а не один раз до o.runWithRetry) — иначе
// коррекционная попытка (attempt 1), написанная в ТОТ ЖЕ вызов runWithRetry,
// не увидела бы feedback, который сам verify только что записал на attempt 0.
func (o *Orchestrator) verifyFeedbackBlock(stageDir string) string {
	text, _, ok := stagefiles.LoadActiveFeedback(stageDir)
	if !ok {
		return ""
	}
	return "\n\n" + verifyFeedbackHeading + "\n\n" + text
}

// gateWithVerify оборачивает существующий completionCheck (чистая file-проба
// — CheckCompletion/CheckAutonomousCompletion) AI-verify проходом (V4b):
// baseCheck выполняется ПЕРВЫМ, и если он не прошёл — его ошибка возвращается
// как есть, verify вообще не запускается (GET/status/refresh не должны
// триггерить AI — вызывающий completionCheck() код регулярно дёргает эту
// функцию, в т.ч. из веток, не связанных с реальным завершением агента, см.
// retry.go). Только когда проба прошла И у стадии есть непустой verify-
// манифест И фаза — одна из исполнительских (implementation/review/
// autonomous_execution), запускается RunVerification, чей typed error/nil
// становится итоговым результатом completionCheck.
//
// Планировочные раннеры (runPlanningAgent/runPlanningWithFeedback) НИКОГДА
// не оборачиваются этим хелпером — см. agents.go. Проверка phase здесь —
// belt-and-suspenders: V1 уже запрещает verify на planning-only стадии на
// этапе flow.ParseFile (см. flow.go's validate).
func (o *Orchestrator) gateWithVerify(ctx context.Context, s flow.Stage, phase string, baseCheck func() error) func() error {
	return func() error {
		if err := baseCheck(); err != nil {
			return err
		}
		if s.Verify.IsEmpty() {
			return nil
		}
		switch phase {
		case phaseImplementation, phaseReview, phaseAutonomous:
			return o.RunVerification(ctx, s, phase)
		default:
			return nil
		}
	}
}

// verifyKindAgent/verifyKindShell — строковые значения ManifestStep.Kind и
// payload'ового ключа "kind" в EventVerifyStarted/EventVerifyResult (V5a.3) —
// единая точка вместо разбросанных строковых литералов "agent"/"shell" (goconst).
const (
	verifyKindAgent = "agent"
	verifyKindShell = "shell"
)

func verifyStepManifestKind(st flow.VerifyStep) string {
	if st.Kind == flow.VerifyAgent {
		return verifyKindAgent
	}
	return verifyKindShell
}

func verifyStepManifestCommand(st flow.VerifyStep) string {
	if st.Kind == flow.VerifyAgent {
		return st.Command
	}
	return st.Run
}

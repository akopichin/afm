package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// verifyExecutionLabel — execution-purpose метка AI-verify для логов/usage.
const verifyExecutionLabel = "verify"

// verifyShellOutputLimit — тот же лимит хвоста вывода, что был у удалённого legacy stagefiles.RunVerify,
// сохраняет прежний текст ошибки shell-шага один в один.
const verifyShellOutputLimit = stagefiles.VerifyOutputLimit

// verifyOutcomePass/verifyOutcomeError обозначают успех шага или невозможность его оценить.
const (
	verifyOutcomePass  = "pass"
	verifyOutcomeError = "error"
)

// verifyTimedOutReason — общий текст ошибки step-таймаута и idle-таймаута verify.
const verifyTimedOutReason = "verify timed out"

// verifyExecMaxRetries/verifyExecBackoff — бюджет и пауза ретраев транзиентного инфра-сбоя verify-процесса
// (v1, без конфига — YAGNI). verifyExecBackoff — var лишь затем, чтобы тесты могли сжать паузу.
const verifyExecMaxRetries = 2

var verifyExecBackoff = 8 * time.Second

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
	keyAttempt        = "attempt"
)

// execErrorKindInterrupted обозначает Pause/Revise/cancel; exec_failure — прочие сбои проверки. Ненулевой
// shell exit даёт needs_changes.
const (
	execErrorKindInterrupted = "interrupted"
	execErrorKindExecFailure = "exec_failure"
)

// emitVerifyStarted публикует событие live и в notices.jsonl; оно не входит в FSM-лог или flow.Phases.
func (o *Orchestrator) emitVerifyStarted(stageID, verID string, idx int, kind, command string) {
	data := map[string]any{
		keyVerificationID: verID,
		keyStep:           idx,
		keyVerifyKind:     kind,
		keyVerifyCommand:  command,
	}
	o.publishNotice(bus.Event{Type: bus.EventVerifyStarted, StageID: stageID, Data: data})
}

// emitVerifyResult публикует наблюдательное событие "шаг verify завершился" — см. emitVerifyStarted.
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
	o.publishNotice(bus.Event{Type: bus.EventVerifyResult, StageID: stageID, Data: data})
}

// emitVerifyRetry публикует событие «транзиентный сбой verify-шага будет повторён после backoff» —
// live + durable, как emitVerifyStarted/emitVerifyResult. attempt — 1-based номер предстоящей повторной попытки.
func (o *Orchestrator) emitVerifyRetry(stageID, verID string, idx int, kind, command string, attempt int, reason string) {
	o.publishNotice(bus.Event{Type: bus.EventVerifyRetry, StageID: stageID, Data: map[string]any{
		keyVerificationID: verID, keyStep: idx, keyVerifyKind: kind,
		keyVerifyCommand: command, keyAttempt: attempt, keyVerifyReason: reason,
	}})
}

// verifyBackoffInterruptible спит d; возвращает false, если Pause/Revise/cancel оборвали ожидание.
func (o *Orchestrator) verifyBackoffInterruptible(ctx context.Context, stageID string, d time.Duration) bool {
	waitCtx, stop := o.pauseAwareVerifyCtx(ctx, stageID)
	defer stop()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-waitCtx.Done():
		return false
	}
}

// enrichExecReason дочитывает хвост agent.stderr.log шага и подмешивает его в baseReason, чтобы причина
// exec_failure несла реальный вывод процесса. Пустой/отсутствующий файл → baseReason без изменений.
func enrichExecReason(stepDir, baseReason string) string {
	data, err := os.ReadFile(filepath.Join(stepDir, "agent.stderr.log"))
	if err != nil {
		return baseReason
	}
	tail := stagefiles.TailOutput(string(data), verifyShellOutputLimit)
	if tail == "" {
		return baseReason
	}
	return baseReason + "\n--- stderr ---\n" + tail
}

// verifyReportPath возвращает абсолютный путь report.md для данного прохода —
// единая точка вместо дублирования filepath.Join в каждом call site'е.
func verifyReportPath(stageDir, verID string) string {
	return filepath.Join(stagefiles.VerifyDir(stageDir), verID, "report.md")
}

// verifyAgentRunner — сигнатура функции, реально исполняющей один agent-шаг AI-verify.
type verifyAgentRunner func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error)

// verifyShellRunner — сигнатура функции, реально исполняющей один shell-шаг AI-verify.
type verifyShellRunner func(ctx context.Context, dir, command string) (string, error)

// RunVerification выполняет ОДИН последовательный fail-fast проход по s.Verify.Steps для стадии, уже
// прошедшей file-probe.
func (o *Orchestrator) RunVerification(ctx context.Context, s flow.Stage, phase string) (err error) {
	if s.Verify.IsEmpty() {
		return nil
	}

	// lease — общий с раннером-автором слот командного семафора: если стадия заспавнена обычным путём, lease
	// сейчас удерживает слот Stage.Command.
	if lease, ok := o.stageLeases.Load(s.ID); ok {
		l := lease.(*concurrency.Lease)
		defer func() {
			// G1: своп ОБРАТНО на команду автора нужен ТОЛЬКО на исходе needs_changes — единственном, после
			// которого runWithRetry перезапустит АВТОРА в ТОМ ЖЕ вызове, на ЭТОМ ЖЕ lease.
			var rejected *VerifyRejectedError
			if !errors.As(err, &rejected) {
				l.Release()
				return
			}
			swapCtx, stop := o.pauseAwareVerifyCtx(ctx, s.ID)
			swapErr := l.SwapTo(swapCtx, s.Command)
			stop()
			if swapErr != nil {
				log.Printf("WARN: verify: reacquire author lease for retry, stage %s: %v", s.ID, swapErr)
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

		// Step timeout и idle-timeout действуют независимо; срабатывает первый истёкший.
		stepCtx := ctx
		if st.Timeout > 0 {
			var cancelStep context.CancelFunc
			stepCtx, cancelStep = context.WithTimeout(ctx, st.Timeout)
			defer cancelStep() //nolint:revive // накопление на малое число шагов одного прохода безопасно; см. комментарий выше
		}

		if st.Kind == flow.VerifyAgent {
			in := prompts.VerifyInputs{
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
			}
			// Транзиентный инфра-сбой verify-процесса ретраим с backoff; все прочие ошибки
			// (timeout/interrupt/protocol/storage/детерминированный exec-сбой) — fail-fast.
			var result verify.ModelResult
			var alias string
			var err error
			for execAttempt := 0; ; execAttempt++ {
				result, alias, err = o.runVerifyAgentStep(stepCtx, s, st, idx, verID, in)
				if err == nil {
					break
				}
				var ve *VerifyExecError
				transient := errors.As(err, &ve) && ve.Transient
				if !transient || execAttempt >= verifyExecMaxRetries || ctx.Err() != nil {
					break
				}
				o.emitVerifyRetry(s.ID, verID, idx, kind, command, execAttempt+1, err.Error())
				if !o.verifyBackoffInterruptible(stepCtx, s.ID, verifyExecBackoff) {
					// Backoff погашен досрочно. Истёкший дедлайн шага (st.Timeout) — это ТАЙМАУТ verify-шага
					// (терминальный exec_failure), а НЕ пользовательское прерывание: иначе runWithRetry по
					// ErrUserInterrupted перезапустил бы автора, и при детерминированном сбое стадия зациклилась бы.
					if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
						err = &VerifyExecError{Reason: verifyTimedOutReason, Step: idx}
					} else {
						err = executor.ErrUserInterrupted // Pause/Revise/отмена рана
					}
					break
				}
			}
			if err != nil {
				manifestSteps[i].Outcome = verifyManifestErrorOutcome(err)
				_ = persistManifest()
				execKind := execErrorKindExecFailure
				if errors.Is(err, executor.ErrUserInterrupted) {
					execKind = execErrorKindInterrupted
				}
				if execKind == execErrorKindExecFailure {
					// Хвост agent.stderr.log подмешиваем ОДИН раз, уже после исчерпания ретраев;
					// interrupted не обогащаем и identity ошибки не трогаем.
					var ve *VerifyExecError
					if errors.As(err, &ve) {
						stepDir := stagefiles.StepDir(filepath.Join(o.opts.RunDir, s.ID), verID, idx)
						ve.Reason = enrichExecReason(stepDir, ve.Reason)
					}
				}
				o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execKind, err.Error(), "")
				return err
			}

			// Перепроверяем stepCtx до любого вердикта: executor может вернуть успешный результат одновременно с
			// истечением дедлайна.
			if raceErr := verifyStepDeadlineRace(stepCtx, idx); raceErr != nil {
				manifestSteps[i].Outcome = verifyOutcomeError
				_ = persistManifest()
				o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, raceErr.Error(), "")
				return raceErr
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
				// Недостижимо при исправном executor.RunVerifyAgent — защитная ветка на случай нестандартного
				// фейкового раннера в тестах: неизвестный вердикт НИКОГДА не pass.
				manifestSteps[i].Outcome = verifyOutcomeError
				_ = persistManifest()
				reason := fmt.Sprintf("verify produced unknown verdict %q", result.Verdict)
				o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
				return &VerifyExecError{Reason: reason, Step: idx}
			}
			continue
		}

		// E2: синхронная проверка ДО того, как вообще создан pauseAwareVerifyCtx и стартован shell-процесс.
		// verifyShellInterruptPending закрывает именно уже-случившийся к МОМЕНТУ ВЫЗОВА случай.
		if o.verifyShellInterruptPending(s.ID) {
			manifestSteps[i].Outcome = execErrorKindInterrupted
			_ = persistManifest()
			o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindInterrupted, "verify interrupted", "")
			return executor.ErrUserInterrupted
		}

		// Shell-шаг: отменяемый процесс — CWD legacy ".".
		shellCtx, stop := o.pauseAwareVerifyCtx(stepCtx, s.ID)
		out, runErr := o.runVerifyShell(shellCtx, ".", st.Run)
		interruptedByPause := stop()

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
		if interruptedByPause {
			// Ненулевой exit после Pause/Revise означает прерывание, а не needs_changes; отказ автора не сохраняем.
			manifestSteps[i].Outcome = execErrorKindInterrupted
			_ = persistManifest()
			o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindInterrupted, "verify interrupted", "")
			return executor.ErrUserInterrupted
		}
		if runErr != nil && errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			// DeadlineExceeded у stepCtx означает истечение st.Timeout; отмена рана и пауза обрабатываются
			// отдельно.
			manifestSteps[i].Outcome = verifyOutcomeError
			_ = persistManifest()
			reason := verifyTimedOutReason
			o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
			return &VerifyExecError{Reason: reason, Step: idx}
		}
		if runErr != nil && ctx.Err() != nil {
			// Проверяем родительский ctx: stop сам отменяет shellCtx, поэтому его Err не доказывает прерывание
			// рана.
			manifestSteps[i].Outcome = verifyOutcomeError
			_ = persistManifest()
			reason := "verify execution failed: " + ctx.Err().Error()
			o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
			return &VerifyExecError{Reason: reason, Step: idx}
		}
		if runErr != nil {
			// Ненулевой shell exit нормализуем в needs_changes и сохраняем тем же путём, что отказ AI; legacy
			// reason сохраняется.
			tail := stagefiles.TailOutput(out, verifyShellOutputLimit)
			legacyReason := fmt.Sprintf("verify command failed (%v):\n%s", runErr, tail)
			result := shellRejectionResult(st.Run, tail)
			manifestSteps[i].Outcome = string(result.Verdict)
			return o.persistVerifyRejection(stageDir, verID, idx, kind, st.Run, legacyReason, result, manifestSteps, s.ID, phase)
		}
		if raceErr := verifyStepDeadlineRace(stepCtx, idx); raceErr != nil {
			// Перепроверяем дедлайн после успеха: select может выбрать done, когда stepCtx уже истёк.
			manifestSteps[i].Outcome = verifyOutcomeError
			_ = persistManifest()
			o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, raceErr.Error(), "")
			return raceErr
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

// runVerifyAgentStep возвращает валидный ModelResult или typed error; alias идентифицирует агента в
// report/manifest.
func (o *Orchestrator) runVerifyAgentStep(ctx context.Context, s flow.Stage, st flow.VerifyStep, idx int, verID string, in prompts.VerifyInputs) (verify.ModelResult, string, error) {
	// Перед запуском agent-шага переводим lease на его команду. Тот же командный слот, той же семантики
	// SwapTo: та же команда, что у автора, — no-op; другая — release-before-acquire.
	if lease, ok := o.stageLeases.Load(s.ID); ok {
		waitCtx, stop := o.pauseAwareVerifyCtx(ctx, s.ID)
		swapErr := lease.(*concurrency.Lease).SwapTo(waitCtx, st.Command)
		interruptedDuringWait := stop()
		if swapErr != nil {
			if ctx.Err() == nil {
				// Родительский ctx жив — ждать перестали именно из-за сигнала паузы/ревизии ЭТОЙ стадии, а не из-за
				// отмены всего рана.
				return verify.ModelResult{}, st.Command, executor.ErrUserInterrupted
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				// Timeout шага может истечь при ожидании слота; сохраняем именно эту причину.
				return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: verifyTimedOutReason, Step: idx}
			}
			return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify lease swap failed: " + swapErr.Error(), Step: idx}
		}
		if interruptedDuringWait {
			// После SwapTo учитываем сигнал Pause/Revise даже при успешном fast path; прерванную стадию проверять
			// нельзя.
			return verify.ModelResult{}, st.Command, executor.ErrUserInterrupted
		}
	}

	// Перед verify-subprocess повторно проверяем ctx и статус: Pause мог случиться после успешного swap
	// lease.
	if aborted, abortErr := o.verifyLaunchAborted(ctx, s.ID, idx); aborted {
		return verify.ModelResult{}, st.Command, abortErr
	}

	stepDir := stagefiles.StepDir(filepath.Join(o.opts.RunDir, s.ID), verID, idx)
	// Найдено этой задачей: в отличие от shell-шага чуть выше по файлу, agent-шаг никогда не создавал stepDir
	// сам — progress.NewLogger просто os.OpenFile'ит agentLog без MkdirAll родителя.
	if mkErr := os.MkdirAll(stepDir, 0755); mkErr != nil {
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify storage failure: " + mkErr.Error(), Step: idx}
	}
	agentLog := filepath.Join(stepDir, "agent.log")
	rawResult := filepath.Join(stepDir, "raw-result.json")

	prompt := prompts.BuildVerify(in)
	outcome, err := o.runVerifyAgent(ctx, s, st.Command, prompt, agentLog, rawResult)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: verifyTimedOutReason, Step: idx}
		}
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify execution failed: " + err.Error(), Step: idx}
	}

	if outcome.Interrupted {
		return verify.ModelResult{}, st.Command, executor.ErrUserInterrupted
	}
	if !outcome.ProcessOK {
		reason := "verify execution failed"
		transient := false
		switch {
		case outcome.TimedOut:
			// executor'ский idle-timeout (независимая гарантия, см. §3.4
			// брифа "кто раньше сработает") — та же текстовая метка.
			reason = verifyTimedOutReason
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			// Step-таймаут определяем по ctx: executor может вернуть ProcessOK=false без TimedOut/Interrupted.
			reason = verifyTimedOutReason
		default:
			// Прочая инфраструктурная ошибка запуска (не idle-timeout, не наш step-timeout) — reason остаётся
			// дефолтным. ЕДИНСТВЕННЫЙ транзиентный случай: процесс стартовал и упал с ненулевым кодом.
			transient = true
		}
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: reason, Step: idx, Transient: transient}
	}
	if outcome.ProtocolErr != nil {
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify protocol error: " + outcome.ProtocolErr.Error(), Step: idx}
	}
	if outcome.Result == nil {
		// Недостижимо при исправном executor.RunVerifyAgent (ProcessOK && ProtocolErr==nil ⇒ Result!=nil) —
		// защитная ветка на случай нестандартного фейкового раннера в тестах.
		return verify.ModelResult{}, st.Command, &VerifyExecError{Reason: "verify produced no result", Step: idx}
	}
	return *outcome.Result, st.Command, nil
}

// pauseAwareVerifyCtx отменяется родителем или interruptCh. Вызови stop до передачи канала subprocess: он
// ждёт observer и сообщает о сигнале.
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
		// Сначала проверяем буфер interruptCh: иначе одновременный stop/cancel может скрыть уже пришедшую паузу.
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

// verifyLaunchAborted — последняя проверка ПЕРЕД стартом verify-субпроцесса: true, если родительский ctx
// уже отменён ИЛИ стадия уже durable ушла в paused/revising.
func (o *Orchestrator) verifyLaunchAborted(ctx context.Context, stageID string, idx int) (bool, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return true, &VerifyExecError{Reason: verifyTimedOutReason, Step: idx}
		}
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

// verifyShellInterruptPending — E2: синхронная, неблокирующая проверка ИМЕННО канала прерывания ПЕРЕД тем,
// как вообще создавать pauseAwareVerifyCtx и стартовать shell-verify процесс.
func (o *Orchestrator) verifyShellInterruptPending(stageID string) bool {
	if ch, ok := o.interruptChans.Load(stageID); ok {
		select {
		case <-ch.(chan struct{}):
			return true
		default:
		}
	}
	if o.opts.Store == nil {
		return false
	}
	switch o.currentStatus(stageID) {
	case state.StatusPaused, state.StatusRevising:
		return true
	default:
		return false
	}
}

// persistVerifyRejection сохраняет result → report → manifest → feedback до emit и typed rejection; ошибка
// записи даёт VerifyExecError.
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

// verifyStepDeadlineRace перепроверяет собственный дедлайн шага после успешного результата.
func verifyStepDeadlineRace(stepCtx context.Context, idx int) error {
	if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
		return &VerifyExecError{Reason: verifyTimedOutReason, Step: idx}
	}
	return nil
}

// verifyManifestErrorOutcome сообщает, каким текстом пометить ManifestStep
// при ошибке agent-шага: "interrupted" для прерывания извне, иначе "error".
func verifyManifestErrorOutcome(err error) string {
	if errors.Is(err, executor.ErrUserInterrupted) {
		return execErrorKindInterrupted
	}
	return verifyOutcomeError
}

// shellRejectionResult нормализует shell-отказ в needs_changes для общего формата result.json/report.md.
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

// sanitizedShellVerifyEnv удаляет чужие транспортные секреты тем же фильтром, что executor.VerifyMode.
func sanitizedShellVerifyEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if executor.IsCrossAgentTransportSecret(kv) || executor.IsBrowserTransportEnv(kv) {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}

// runVerifyShellCommand запускает shell-verify команду отменяемо — в отличие от legacy
// stagefiles.RunVerify, движок обязан уметь прервать зависшую команду по отмене контекста рана.
func runVerifyShellCommand(ctx context.Context, dir, command string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	// Shell-verify не наследует транспортные секреты агентов и хуков; собственного recipe-секрета у shell
	// нет.
	cmd.Env = sanitizedShellVerifyEnv()
	setProcessGroup(cmd)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	// Перед запуском процесса повторно проверяем ctx и interrupt, чтобы не спавнить уже отменённый вызов.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return out.String(), err
	case <-ctx.Done():
		killProcessGroup(cmd, syscall.SIGKILL)
		<-done // дождаться закрытия io-пайпов после килла всей группы
		return out.String(), ctx.Err()
	}
}

// previousVerifyReport читает report.md прохода, чей feedback ещё активен, чтобы agent-шаг мог оценить
// закрытие прежних blockers, а не искать их заново.
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

// verifyFeedbackBlock читает АКТИВНЫЙ machine-feedback от AI-verify и оборачивает его в блок для
// RetryContext автора — "" если активного feedback нет.
func (o *Orchestrator) verifyFeedbackBlock(stageDir string) string {
	text, _, ok := stagefiles.LoadActiveFeedback(stageDir)
	if !ok {
		return ""
	}
	return "\n\n" + verifyFeedbackHeading + "\n\n" + text
}

// gateWithVerify запускает verify только после успешной file-пробы исполнительской фазы; planning и
// status-чтения его не вызывают.
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

// verifyKindAgent/verifyKindShell — значения kind для manifest и verify-событий.
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

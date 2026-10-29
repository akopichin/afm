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
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/prompts"
)

// verificationPass остаётся по указателю: passedSteps нельзя копировать после первой записи.
type verificationPass struct {
	o             *Orchestrator
	stage         flow.Stage
	stageDir      string
	verID         string
	phase         string
	manifestSteps []stagefiles.ManifestStep
	passedSteps   strings.Builder
}

func newVerificationPass(o *Orchestrator, s flow.Stage, stageDir, verID, phase string) *verificationPass {
	manifestSteps := make([]stagefiles.ManifestStep, len(s.Verify.Steps))
	for i, st := range s.Verify.Steps {
		manifestSteps[i] = stagefiles.ManifestStep{
			Index:   i + 1,
			Kind:    verifyStepManifestKind(st),
			Command: verifyStepManifestCommand(st),
			Outcome: "not-run",
		}
	}
	return &verificationPass{o: o, stage: s, stageDir: stageDir, verID: verID, phase: phase, manifestSteps: manifestSteps}
}

func (p *verificationPass) persist() error {
	return stagefiles.WriteManifest(p.stageDir, p.verID, stagefiles.Manifest{
		RunID:          p.o.opts.RunID,
		StageID:        p.stage.ID,
		Phase:          p.phase,
		VerificationID: p.verID,
		CreatedAt:      time.Now(),
		Steps:          append([]stagefiles.ManifestStep(nil), p.manifestSteps...),
	})
}

func (p *verificationPass) failStep(i, idx int, kind, command, reason string) error {
	p.manifestSteps[i].Outcome = verifyOutcomeError
	_ = p.persist()
	p.o.emitVerifyResult(p.stage.ID, p.verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
	return &VerifyExecError{Reason: reason, Step: idx}
}

func (p *verificationPass) runAgentVerifyStep(ctx context.Context, st flow.VerifyStep, i int, in prompts.VerifyInputs) error {
	o, s := p.o, p.stage
	stageDir, verID, phase := p.stageDir, p.verID, p.phase
	idx := i + 1
	kind, command := verifyStepManifestKind(st), verifyStepManifestCommand(st)
	// Step timeout и idle-timeout действуют независимо; срабатывает первый истёкший.
	stepCtx := ctx
	if st.Timeout > 0 {
		var cancelStep context.CancelFunc
		stepCtx, cancelStep = context.WithTimeout(ctx, st.Timeout)
		defer cancelStep()
	}

	in.PassedSteps = p.passedSteps.String()
	in.VerifyPrompt = st.Prompt

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
			// Истёкший дедлайн в backoff — терминальный timeout, иначе ErrUserInterrupted
			// перезапустил бы автора и мог зациклить стадию.
			if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
				err = &VerifyExecError{Reason: verifyTimedOutReason, Step: idx}
			} else {
				err = executor.ErrUserInterrupted // Pause/Revise/отмена рана
			}
			break
		}
	}
	if err != nil {
		var limit *executor.UsageLimitError
		if errors.As(err, &limit) {
			// Лимит провайдера — не ошибка проверки: шаг и счётчики сохраняются, стадия паркуется до сброса.
			p.manifestSteps[i].Outcome = verifyOutcomeQuotaWait
			if perr := p.persist(); perr != nil {
				reason := "verify storage failure: " + perr.Error()
				o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, reason, "")
				return &VerifyExecError{Reason: reason, Step: idx}
			}
			return &verifyLimitError{Limit: limit, VerifyID: verID, Step: idx}
		}
		p.manifestSteps[i].Outcome = verifyManifestErrorOutcome(err)
		_ = p.persist()
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
		p.manifestSteps[i].Outcome = verifyOutcomeError
		_ = p.persist()
		o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, raceErr.Error(), "")
		return raceErr
	}

	switch result.Verdict {
	case verify.VerdictPass:
		if perr := stagefiles.SaveAcceptedResult(stageDir, verID, idx, result); perr != nil {
			return p.failStep(i, idx, kind, command, "verify storage failure: "+perr.Error())
		}
		if perr := stagefiles.WriteReport(stageDir, verID, result, alias, idx); perr != nil {
			return p.failStep(i, idx, kind, command, "verify storage failure: "+perr.Error())
		}
		p.manifestSteps[i].Outcome = string(result.Verdict)
		p.passedSteps.WriteString(passedAgentStepLine(idx, alias, result.Summary))
		o.emitVerifyResult(s.ID, verID, idx, kind, command, verify.VerdictPass, "", result.Summary, verifyReportPath(stageDir, verID))

	case verify.VerdictNeedsChanges:
		p.manifestSteps[i].Outcome = string(result.Verdict)
		reason := "verify needs changes (step " + fmt.Sprint(idx) + "): " + result.Summary
		return o.persistVerifyRejection(stageDir, verID, idx, kind, alias, reason, result, p.manifestSteps, s.ID, phase)

	case verify.VerdictInconclusive:
		p.manifestSteps[i].Outcome = string(result.Verdict)
		_ = p.persist()
		o.emitVerifyResult(s.ID, verID, idx, kind, command, verify.VerdictInconclusive, "", result.Summary, "")
		return &VerifyExecError{Reason: "verify inconclusive: " + result.Summary, Step: idx}
	default:
		// Недостижимо при исправном executor.RunVerifyAgent — защитная ветка на случай нестандартного
		// фейкового раннера в тестах: неизвестный вердикт НИКОГДА не pass.
		return p.failStep(i, idx, kind, command, fmt.Sprintf("verify produced unknown verdict %q", result.Verdict))
	}
	return nil
}

func (p *verificationPass) runShellVerifyStep(ctx context.Context, st flow.VerifyStep, i int) error {
	o, s := p.o, p.stage
	stageDir, verID, phase := p.stageDir, p.verID, p.phase
	idx := i + 1
	kind, command := verifyStepManifestKind(st), verifyStepManifestCommand(st)
	// Step timeout и idle-timeout действуют независимо; срабатывает первый истёкший.
	stepCtx := ctx
	if st.Timeout > 0 {
		var cancelStep context.CancelFunc
		stepCtx, cancelStep = context.WithTimeout(ctx, st.Timeout)
		defer cancelStep()
	}

	// Уже пришедшая пауза должна остановить запуск до создания observer и shell-процесса.
	if o.verifyShellInterruptPending(s.ID) {
		p.manifestSteps[i].Outcome = execErrorKindInterrupted
		_ = p.persist()
		o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindInterrupted, "verify interrupted", "")
		return executor.ErrUserInterrupted
	}

	// Shell-шаг: отменяемый процесс в корне проекта агентов, иначе `go test ./...` проверял бы не тот код,
	// который меняли агенты.
	shellCtx, stop := o.pauseAwareVerifyCtx(stepCtx, s.ID)
	out, runErr := o.runVerifyShell(shellCtx, o.projectDir(), st.Run)
	interruptedByPause := stop()

	stepDir := stagefiles.StepDir(stageDir, verID, idx)
	if mkErr := os.MkdirAll(stepDir, 0755); mkErr != nil {
		return p.failStep(i, idx, kind, command, "verify storage failure: "+mkErr.Error())
	}
	if werr := os.WriteFile(filepath.Join(stepDir, "command.log"), []byte(out), 0644); werr != nil {
		return p.failStep(i, idx, kind, command, "verify storage failure: "+werr.Error())
	}
	if interruptedByPause {
		// Ненулевой exit после Pause/Revise означает прерывание, а не needs_changes; отказ автора не сохраняем.
		p.manifestSteps[i].Outcome = execErrorKindInterrupted
		_ = p.persist()
		o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindInterrupted, "verify interrupted", "")
		return executor.ErrUserInterrupted
	}
	if runErr != nil && errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
		// DeadlineExceeded у stepCtx означает истечение st.Timeout; отмена рана и пауза обрабатываются
		// отдельно.
		return p.failStep(i, idx, kind, command, verifyTimedOutReason)
	}
	if runErr != nil && ctx.Err() != nil {
		// Проверяем родительский ctx: stop сам отменяет shellCtx, поэтому его Err не доказывает прерывание
		// рана.
		return p.failStep(i, idx, kind, command, "verify execution failed: "+ctx.Err().Error())
	}
	if runErr != nil {
		// Ненулевой shell exit нормализуем в needs_changes и сохраняем тем же путём, что отказ AI; legacy
		// reason сохраняется.
		tail := stagefiles.TailOutput(out, verifyShellOutputLimit)
		legacyReason := fmt.Sprintf("verify command failed (%v):\n%s", runErr, tail)
		result := shellRejectionResult(st.Run, tail)
		p.manifestSteps[i].Outcome = string(result.Verdict)
		return o.persistVerifyRejection(stageDir, verID, idx, kind, st.Run, legacyReason, result, p.manifestSteps, s.ID, phase)
	}
	if raceErr := verifyStepDeadlineRace(stepCtx, idx); raceErr != nil {
		// Перепроверяем дедлайн после успеха: select может выбрать done, когда stepCtx уже истёк.
		p.manifestSteps[i].Outcome = verifyOutcomeError
		_ = p.persist()
		o.emitVerifyResult(s.ID, verID, idx, kind, command, "", execErrorKindExecFailure, raceErr.Error(), "")
		return raceErr
	}
	p.manifestSteps[i].Outcome = verifyOutcomePass
	p.passedSteps.WriteString(passedShellStepLine(idx, st.Run))
	o.emitVerifyResult(s.ID, verID, idx, kind, command, verify.VerdictPass, "", "passed", "")
	return nil
}

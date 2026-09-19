package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/prompts"
)

// verifyExecutionLabel — execution-purpose метка AI-verify для логов/usage
// (accounting.Observation.Phase, executor.Config.Phase). НЕ входит в
// flow.Phases() — verify не рантайм-фаза стадии (planning/implementation/
// review/autonomous_execution), а отдельный, ортогональный проход ПОСЛЕ
// того как стадия уже заявила о завершении по file-probe.
const verifyExecutionLabel = "verify"

// verifyShellOutputLimit — тот же лимит хвоста вывода, что у legacy
// stagefiles.RunVerify (stagefiles.VerifyOutputLimit), сохраняет прежний
// текст ошибки shell-шага один в один.
const verifyShellOutputLimit = stagefiles.VerifyOutputLimit

// verifyOutcomePass/verifyOutcomeError — значения ManifestStep.Outcome для
// успешно пройденного шага и для шага, который не удалось оценить
// (инфраструктурная/storage-ошибка) — единая точка определения вместо
// разбросанных строковых литералов "pass"/"error" (goconst).
const (
	verifyOutcomePass  = "pass"
	verifyOutcomeError = "error"
)

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
				return err
			}

			switch result.Verdict {
			case verify.VerdictPass:
				if perr := stagefiles.SaveAcceptedResult(stageDir, verID, idx, result); perr != nil {
					manifestSteps[i].Outcome = verifyOutcomeError
					_ = persistManifest()
					return &VerifyExecError{Reason: "verify storage failure: " + perr.Error(), Step: idx}
				}
				if perr := stagefiles.WriteReport(stageDir, verID, result, alias, idx); perr != nil {
					manifestSteps[i].Outcome = verifyOutcomeError
					_ = persistManifest()
					return &VerifyExecError{Reason: "verify storage failure: " + perr.Error(), Step: idx}
				}
				manifestSteps[i].Outcome = string(result.Verdict)
				fmt.Fprintf(&passedSteps, "Step %d (agent %s): "+verifyOutcomePass+" — %s\n", idx, alias, result.Summary)

			case verify.VerdictNeedsChanges:
				manifestSteps[i].Outcome = string(result.Verdict)
				reason := "verify needs changes (step " + fmt.Sprint(idx) + "): " + result.Summary
				return o.persistVerifyRejection(stageDir, verID, idx, alias, reason, result, manifestSteps, s.ID, phase)

			case verify.VerdictInconclusive:
				manifestSteps[i].Outcome = string(result.Verdict)
				_ = persistManifest()
				return &VerifyExecError{Reason: "verify inconclusive: " + result.Summary, Step: idx}
			default:
				// Недостижимо при исправном executor.RunVerifyAgent
				// (DecodeModelResult допускает только три вердикта) —
				// защитная ветка на случай нестандартного фейкового
				// раннера в тестах: неизвестный вердикт НИКОГДА не pass.
				manifestSteps[i].Outcome = verifyOutcomeError
				_ = persistManifest()
				return &VerifyExecError{Reason: fmt.Sprintf("verify produced unknown verdict %q", result.Verdict), Step: idx}
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
			return &VerifyExecError{Reason: "verify storage failure: " + mkErr.Error(), Step: idx}
		}
		if werr := os.WriteFile(filepath.Join(stepDir, "command.log"), []byte(out), 0644); werr != nil {
			manifestSteps[i].Outcome = verifyOutcomeError
			_ = persistManifest()
			return &VerifyExecError{Reason: "verify storage failure: " + werr.Error(), Step: idx}
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
			return o.persistVerifyRejection(stageDir, verID, idx, st.Run, legacyReason, result, manifestSteps, s.ID, phase)
		}
		manifestSteps[i].Outcome = verifyOutcomePass
		fmt.Fprintf(&passedSteps, "Step %d (shell): `%s` — passed\n", idx, st.Run)
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
	stepDir := stagefiles.StepDir(filepath.Join(o.opts.RunDir, s.ID), verID, idx)
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

// persistVerifyRejection выполняет commit order для needs_changes-эквивалента
// (V2a §6.3), общий для agent-шага и synthetic-результата shell-шага:
//  1. вердикт уже готов и провалидирован (result);
//  2. SaveAcceptedResult + WriteReport + WriteManifest;
//  3. WriteActiveFeedback атомарно;
//  4. ТОЛЬКО после успеха всех предыдущих — типизированный VerifyRejectedError.
//
// Сбой ЛЮБОЙ персистентности на этом пути — VerifyExecError (fail closed):
// стадия не должна быть ни молча принята, ни отклонена с недописанными на
// диск файлами (частично записанный проход хуже отсутствующего).
func (o *Orchestrator) persistVerifyRejection(stageDir, verID string, idx int, alias, reason string, result verify.ModelResult, manifestSteps []stagefiles.ManifestStep, stageID, phase string) error {
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
	return &VerifyRejectedError{
		IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: reason},
		ReportID:            verID,
		Step:                idx,
	}
}

// verifyManifestErrorOutcome сообщает, каким текстом пометить ManifestStep
// при ошибке agent-шага: "interrupted" для прерывания извне, иначе "error".
func verifyManifestErrorOutcome(err error) string {
	if err == executor.ErrUserInterrupted {
		return "interrupted"
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

func verifyStepManifestKind(st flow.VerifyStep) string {
	if st.Kind == flow.VerifyAgent {
		return "agent"
	}
	return "shell"
}

func verifyStepManifestCommand(st flow.VerifyStep) string {
	if st.Kind == flow.VerifyAgent {
		return st.Command
	}
	return st.Run
}

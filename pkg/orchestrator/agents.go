package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

const planningContract = `## Output Contract (mandatory)
The plan MUST contain sections: "## Tasks", "## Assumptions", "## Acceptance Criteria".`

// verifyStageNote форматирует подсказку про verify-команду, добавляемую к промпту реализации: перечисляет
// shell-шаги, которые агент должен прогнать сам перед созданием .done.
func verifyStageNote(v flow.VerifySpec) string {
	var cmds []string
	for _, st := range v.Steps {
		if st.Kind == flow.VerifyShell {
			cmds = append(cmds, st.Run)
		}
	}
	if len(cmds) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\nVerify command (runs automatically after you finish; it MUST exit 0, "+
		"so run it yourself before creating .done):\n%s", strings.Join(cmds, "\n"))
}

// preNoteBlock читает prenote.md стадии и возвращает блок для добавления в контекст агента, либо "" если
// заметки нет.
func (o *Orchestrator) preNoteBlock(stageDir string) string {
	note := state.LoadPreNote(stageDir)
	if note == "" {
		return ""
	}
	return "\n\n## User note (added before this stage started)\n\n" + note
}

// feedbackNoteBlock читает feedback.md стадии и возвращает блок для добавления в контекст агента, либо ""
// если файла нет или он пуст.
func (o *Orchestrator) feedbackNoteBlock(stageDir string) string {
	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if len(feedbackData) == 0 {
		return ""
	}
	return "\n\n## User note (added while this stage was running)\n\n" + string(feedbackData)
}

// phaseScript identifies a script-stage's own log namespace (script.log),
// distinct from the before/after hook logs.
const phaseScript = "script"

// runScriptStage executes a script-only stage: no plan.md, no approval, no LLM agent — just Stage.Script
// via execScript, retried with the same fixed 3x/1-2-3s policy as hooks.
func (o *Orchestrator) runScriptStage(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
		return
	}
	logFile := filepath.Join(stageDir, phaseScript+".log")

	o.emitStageEvent(lifecyclehooks.EventStageScriptStarted, s.ID, "")
	err := runScriptWithRetry(ctx, func() error {
		return o.execScript(ctx, s, phaseScript, s.Script, s.ScriptTimeout, logFile)
	})
	if err != nil {
		o.emitStageEvent(lifecyclehooks.EventStageScriptFailed, s.ID, err.Error())
		_, failSeq, _ := o.triggerWithSeq(s.ID, bus.EvFail, bus.GuardCtx{}, err.Error())
		// script_failed — a durable dashboard-feed notice. Same seq in both live+notice for the SAME occurrence
		// keeps the intended dedup working.
		const keyError = "error" // matches the existing "error" key used by publishHookNotice (hooks.go)
		tail := executor.ReadStderrTail(logFile, stderrTailMaxLines, stderrTailMaxBytes)
		data := map[string]string{keyError: err.Error(), dataKeyStderrTail: tail, dataKeySeq: strconv.FormatUint(failSeq, 10)}
		o.publishNotice(bus.Event{Type: bus.EventScriptFailed, StageID: s.ID, Data: data})
		o.failBlockedStages()
		return
	}
	o.emitStageEvent(lifecyclehooks.EventStageScriptFinished, s.ID, "")

	stagefiles.AppendNotice(o.opts.RunDir, s.ID, string(bus.EventAgentCompleted), phaseScript)
	o.publishCritical(ctx, bus.Event{Type: bus.EventAgentCompleted, StageID: s.ID, Data: phaseScript})
}

// sectionAssumptions зеркалит одну из секций stagefiles.RequiredPlanSections.
const sectionAssumptions = "Assumptions"

// collectDependencyPlansNoticing — тонкая обёртка над stagefiles.CollectDependencyPlans: колбэк дублирует
// предупреждение о недостающей зависимости и live, и durable.
func (o *Orchestrator) collectDependencyPlansNoticing(s flow.Stage) string {
	return stagefiles.CollectDependencyPlans(o.opts.RunDir, s, o.reg().Stages(), func(depID, msg string) {
		o.publishNotice(bus.Event{Type: bus.EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
	})
}

// collectArtifactsNoticing — тонкая обёртка над stagefiles.CollectArtifacts: ошибка сбора не фатальна,
// только WARN в лог afm.
func (o *Orchestrator) collectArtifactsNoticing(s flow.Stage, label string) string {
	artCtx, artErr := stagefiles.CollectArtifacts(".", o.opts.RunDir, s, o.reg().Stages())
	if artErr != nil {
		log.Printf("WARN: collect artifacts for %s %s: %v", s.ID, label, artErr)
	}
	return artCtx
}

func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindPlanning)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
		return
	}

	// Планирование = стандартный трек. Без снятия флага stage_autonomous в /api/status оставался бы true, а
	// дашборд прятал бы plan-панель (нет approve-кнопки на awaiting_approval).
	clearStaleAutonomousFlag(stageDir)

	// Defensive: may be a no-op if the caller already transitioned
	// the stage to "planning" (e.g. startPlanningForUnblocked).
	o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{Stage: s}, "")

	// preNote читается ДО retry-цикла — это часть исходного задания на первом старте стадии, а не поправка
	// "по месту" на каждой попытке.
	preNote := o.preNoteBlock(stageDir)

	o.runPlanningAttempt(ctx, s, stageDir, flow.PhaseLogFile(flow.PhasePlanning), "planning",
		func(retryContext string, in *prompts.Inputs) error {
			in.RetryContext = retryContext + preNote
			return nil
		})
}

func (o *Orchestrator) rePromptMissingSections(ctx context.Context, s flow.Stage, prevPlan string, missing []string, outFile string) error {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	prompt := fmt.Sprintf(
		"Your previous plan was missing required sections: %s.\nAdd ONLY the missing sections to the existing plan below. Do not rewrite the rest.\n\n<previous_plan>\n%s\n</previous_plan>",
		strings.Join(missing, ", "),
		prompts.EscapeTagsForReprompt(prevPlan),
	)
	logFile := filepath.Join(stageDir, "planning-reprompt.log")
	r := o.runnerFor(s, phasePlanning)
	if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
		return err
	}
	planMD, _ := os.ReadFile(outFile)
	issues := prompts.ValidatePlan(string(planMD), stagefiles.RequiredPlanSections)
	if !issues.IsClean() {
		if stagefiles.AdoptWrittenPlan(logFile, outFile) {
			return nil
		}
		return &MissingSectionsError{Missing: issues.MissingSections}
	}
	return nil
}

// runPlanningAttempt строит контекст, запускает и проверяет план; fillInputs обновляет feedback до сбора
// контекста на каждой попытке.
func (o *Orchestrator) runPlanningAttempt(ctx context.Context, s flow.Stage, stageDir, logFileName, artifactsWarnLabel string, fillInputs func(retryContext string, in *prompts.Inputs) error) {
	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		in := prompts.Inputs{
			Template:         o.opts.Prompts.Planning,
			Stage:            s,
			PhaseAgent:       prompts.AgentPlanning,
			StageDir:         stageDir,
			Interactive:      s.Interactive,
			OutputContractMD: planningContract,
			RetryContext:     retryContext,
			GlobalPrompt:     o.opts.GlobalPrompt,
			MemoryBlock:      o.memoryBlockForStage(s),
			BrowserRuntime:   o.browserRuntimeFor(s),
		}
		if err := fillInputs(retryContext, &in); err != nil {
			return err
		}

		in.DependencyPlans = o.collectDependencyPlansNoticing(s)
		in.Artifacts = o.collectArtifactsNoticing(s, artifactsWarnLabel)

		prompt := prompts.Build(in)
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, logFileName)

		r := o.runnerFor(s, phasePlanning)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}

		planMD, _ := os.ReadFile(outFile)
		issues := prompts.ValidatePlan(string(planMD), stagefiles.RequiredPlanSections)
		if !issues.IsClean() {
			if stagefiles.AdoptWrittenPlan(logFile, outFile) {
				return nil
			}
			if s.Interactive {
				return nil
			}
			if err := o.rePromptMissingSections(ctx, s, string(planMD), issues.MissingSections, outFile); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return stagefiles.CheckPlanCompletionFor(stageDir, s.Interactive)
	}, func() { o.spawnKind(ctx, s, kindPlanning, o.runPlanningWithFeedback) })
}

func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindPlanning)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.Trigger(s.ID, bus.EvStartPlanning, bus.GuardCtx{Stage: s}, "")

	o.runPlanningAttempt(ctx, s, stageDir, "planning-revision.log", "revise",
		func(_ string, in *prompts.Inputs) error {
			// feedback.md и LatestPlanVersion читаются ЗАНОВО на КАЖДОЙ попытке — так было в исходном
			// runPlanningWithFeedback, момент чтения сохранён дословно.
			feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
			_, prevPlan, err := state.LatestPlanVersion(stageDir)
			if err != nil {
				return fmt.Errorf("read previous plan: %w", err)
			}
			in.PreviousPlan = prevPlan
			in.Feedback = string(feedbackData)
			return nil
		})
}

func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindImplementation)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	preNote := o.preNoteBlock(stageDir)

	o.runImplementationAttempt(ctx, s, stageDir, preNote, flow.PhaseLogFile(flow.PhaseImplementation), "impl")
}

// runEmbeddedReview — review-шаг, встроенный в implementation-попытку. runnerFor вызывается здесь, внутри
// вызывающей per-attempt closure — не хоистится наружу.
func (o *Orchestrator) runEmbeddedReview(ctx context.Context, s flow.Stage, stageDir, depPlans, artCtx, verifyNote string) error {
	// Инлайн-ревью тоже должен видеть замечания AI-verify предыдущего прохода (владелец коррекции остаётся
	// implementation — ревью здесь не гейтится отдельно, это просто дополнительный контекст).
	// T8/D6: review гейтится на .done (CheckCompletion), но до этой правки его промпт ни разу не упоминал ни
	// .done, ни stage-директорию — гейт и инструкция теперь говорят об одном.
	contract := prompts.CompletionContract(flow.PhaseReview, o.stageDir(s.ID))
	reviewPrompt := prompts.Build(prompts.Inputs{
		Template:        o.opts.Prompts.Review,
		Stage:           s,
		PhaseAgent:      prompts.AgentReview,
		DependencyPlans: depPlans,
		Artifacts:       artCtx,
		StageDir:        stageDir,
		Interactive:     s.Interactive,
		GlobalPrompt:    o.opts.GlobalPrompt,
		MemoryBlock:     o.memoryBlockForStage(s),
		RetryContext:    verifyNote + contract,
		BrowserRuntime:  o.browserRuntimeFor(s),
	})
	reviewLog := filepath.Join(stageDir, flow.PhaseLogFile(flow.PhaseReview))
	rr := o.runnerFor(s, phaseReview)
	return rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog)
}

// runImplementationAttempt выполняет fresh/feedback-попытку и встроенный review; note читается до
// retry-цикла.
func (o *Orchestrator) runImplementationAttempt(ctx context.Context, s flow.Stage, stageDir, note, logFileName, artifactsWarnLabel string) {
	o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depPlans := o.collectDependencyPlansNoticing(s)
		artCtx := o.collectArtifactsNoticing(s, artifactsWarnLabel)

		// Format output artifact requirements
		if len(s.Artifacts) > 0 {
			var buf strings.Builder
			buf.WriteString("\n\nRequired output artifacts (MUST exist at these paths when stage finishes):\n\n")
			for _, art := range s.Artifacts {
				dst := art.Path
				if strings.HasPrefix(art.Path, "./") {
					dst = filepath.Join(stageDir, art.Path[2:])
				}
				desc := ""
				if art.Description != "" {
					desc = " — " + art.Description
				}
				fmt.Fprintf(&buf, "- %s%s → %s\n", art.Name, desc, dst)
			}
			artCtx += buf.String()
		}

		verifyNote := o.verifyFeedbackBlock(stageDir)
		verifyCmdNote := verifyStageNote(s.Verify)
		// T8/D5: обязательный блок завершения строит код по фактической фазе, не шаблон — доклеивается
		// последним, чтобы prompts_dir не мог его подменить/стереть.
		contract := prompts.CompletionContract(flow.PhaseImplementation, o.stageDir(s.ID))
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation,
			Stage:           s,
			PhaseAgent:      prompts.AgentImplementation,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			Plan:            string(planData),
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + note + verifyNote + verifyCmdNote + contract,
			GlobalPrompt:    o.opts.GlobalPrompt,
			MemoryBlock:     o.memoryBlockForStage(s),
			BrowserRuntime:  o.browserRuntimeFor(s),
		})
		logFile := filepath.Join(stageDir, logFileName)

		r := o.runnerFor(s, phaseImplementation)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			if err := o.runEmbeddedReview(ctx, s, stageDir, depPlans, artCtx, verifyNote); err != nil {
				return err
			}
		}

		return nil
	}, o.gateWithVerify(ctx, s, phaseImplementation, func() error {
		return stagefiles.CheckCompletion(stageDir, ".", s)
	}), func() { o.spawnKind(ctx, s, kindImplementation, o.runImplementationWithFeedback) })
}

func (o *Orchestrator) runReviewAgent(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindReview)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
		return
	}
	preNote := o.preNoteBlock(stageDir)

	depPlans := o.collectDependencyPlansNoticing(s)
	artCtx := o.collectArtifactsNoticing(s, "review")

	o.runReviewAttempt(ctx, s, stageDir, preNote, flow.PhaseLogFile(flow.PhaseReview), depPlans, artCtx)
}

// runReviewAttempt — общее тело review-попытки: строит промпт и запускает существующий runWithRetry. Общий
// и для fresh (runReviewAgent), и для feedback (runReviewWithFeedback) раннеров.
func (o *Orchestrator) runReviewAttempt(ctx context.Context, s flow.Stage, stageDir, note, logFileName, depPlans, artCtx string) {
	o.runWithRetry(ctx, s, phaseReview, func(retryContext string) error {
		verifyNote := o.verifyFeedbackBlock(stageDir)
		// T8/D6: standalone review (resume/revise/pause-recovery паths) гейтится на .done так же, как
		// embedded review — промпт теперь тоже об этом говорит.
		contract := prompts.CompletionContract(flow.PhaseReview, o.stageDir(s.ID))
		reviewPrompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Review,
			Stage:           s,
			PhaseAgent:      prompts.AgentReview,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + note + verifyNote + contract,
			GlobalPrompt:    o.opts.GlobalPrompt,
			MemoryBlock:     o.memoryBlockForStage(s),
			BrowserRuntime:  o.browserRuntimeFor(s),
		})
		reviewLog := filepath.Join(stageDir, logFileName)
		rr := o.runnerFor(s, phaseReview)
		return rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog)
	}, o.gateWithVerify(ctx, s, phaseReview, func() error {
		return stagefiles.CheckCompletion(stageDir, ".", s)
	}), func() { o.spawnKind(ctx, s, kindReview, o.runReviewWithFeedback) })
}

// runAutonomousAgent запускает автономный трек без plan.md/approval; completion требует
// execution_summary.md.
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindAutonomous)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, bus.EvFail, bus.GuardCtx{}, "mkdir failed")
		return
	}
	_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
	preNote := o.preNoteBlock(stageDir)

	o.runAutonomousAttempt(ctx, s, stageDir, preNote, flow.PhaseLogFile(flow.PhaseAutonomous), "autonomous")
}

// runAutonomousAttempt — общее тело autonomous-попытки: собирает контекст, строит промпт и запускает
// существующий runWithRetry.
func (o *Orchestrator) runAutonomousAttempt(ctx context.Context, s flow.Stage, stageDir, note, logFileName, artifactsWarnLabel string) {
	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
		artCtx := o.collectArtifactsNoticing(s, artifactsWarnLabel)
		depCtx := o.collectDependencyPlansNoticing(s)

		verifyNote := o.verifyFeedbackBlock(stageDir)
		// T8/D5: обязательный блок завершения строит код по фактической фазе (execution_summary.md для
		// autonomous), не шаблон — доклеивается последним.
		contract := prompts.CompletionContract(flow.PhaseAutonomous, o.stageDir(s.ID))
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation, // fallback, если Autonomous пустой
			Autonomous:      o.opts.Prompts.Autonomous,
			Stage:           s,
			PhaseAgent:      prompts.AgentAutonomous,
			Interactive:     true, // dialog protocol — autonomous-скилл может спрашивать пользователя
			Artifacts:       artCtx,
			DependencyPlans: depCtx,
			StageDir:        stageDir,
			GlobalPrompt:    o.opts.GlobalPrompt,
			MemoryBlock:     o.memoryBlockForStage(s),
			RetryContext:    retryContext + note + verifyNote + contract,
			BrowserRuntime:  o.browserRuntimeFor(s),
		})
		logFile := filepath.Join(stageDir, logFileName)
		r := o.runnerFor(s, phaseAutonomous)
		return r.RunAgent(ctx, phaseAutonomous, s.Name, prompt, logFile)
	}, o.gateWithVerify(ctx, s, phaseAutonomous, func() error {
		return stagefiles.CheckAutonomousCompletion(stageDir)
	}), func() { o.spawnKind(ctx, s, kindAutonomous, o.runAutonomousWithFeedback) })
}

// runImplementationWithFeedback перезапускает implementation-фазу с фидбеком пользователя — как
// runImplementationAgent, но с добавленной в контекст фразой из feedback.md.
func (o *Orchestrator) runImplementationWithFeedback(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindImplementation)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// Возвращаемся в Running. Без этого onAgentCompleted и EvComplete не увидели бы подходящий статус, и
	// стадия зависла бы в Revising даже после успешного завершения агента.
	o.Trigger(s.ID, bus.EvStartRun, bus.GuardCtx{}, "")
	feedbackNote := o.feedbackNoteBlock(stageDir)

	o.runImplementationAttempt(ctx, s, stageDir, feedbackNote, "implementation-feedback.log", "impl (feedback restart)")
}

// runReviewWithFeedback — как runReviewAgent, с фразой пользователя в контексте.
func (o *Orchestrator) runReviewWithFeedback(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindReview)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// См. runImplementationWithFeedback: возвращаемся в Running из Revising.
	o.Trigger(s.ID, bus.EvStartRun, bus.GuardCtx{}, "")
	feedbackNote := o.feedbackNoteBlock(stageDir)

	depPlans := o.collectDependencyPlansNoticing(s)
	artCtx := o.collectArtifactsNoticing(s, "review (feedback restart)")

	o.runReviewAttempt(ctx, s, stageDir, feedbackNote, "review-feedback.log", depPlans, artCtx)
}

// runAutonomousWithFeedback — как runAutonomousAgent, с фразой пользователя в контексте.
func (o *Orchestrator) runAutonomousWithFeedback(ctx context.Context, s flow.Stage) {
	o.setRunnerKind(s.ID, kindAutonomous)
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// См. runImplementationWithFeedback: возвращаемся в Running из Revising.
	o.Trigger(s.ID, bus.EvStartRun, bus.GuardCtx{}, "")
	feedbackNote := o.feedbackNoteBlock(stageDir)

	o.runAutonomousAttempt(ctx, s, stageDir, feedbackNote, "autonomous-feedback.log", "autonomous (feedback restart)")
}

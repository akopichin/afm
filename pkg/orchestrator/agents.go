package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

const planningContract = `## Output Contract (mandatory)
The plan MUST contain sections: "## Tasks", "## Assumptions", "## Acceptance Criteria".`

const sectionAssumptions = "Assumptions"

var requiredPlanSections = []string{"Tasks", sectionAssumptions, "Acceptance Criteria"}

func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}

	// Планирование = стандартный (не автономный) трек. Если от предыдущей попытки
	// (или от неудавшегося autonomous-запуска до retry) остался autonomous.flag —
	// он теперь устарел: стадия пройдёт planning и получит настоящий plan.md с
	// approve/revise. Без снятия флага stage_autonomous в /api/status оставался бы
	// true, а дашборд прятал бы plan-панель (нет approve-кнопки на awaiting_approval).
	clearStaleAutonomousFlag(stageDir)

	// Defensive: may be a no-op if the caller already transitioned
	// the stage to "planning" (e.g. startPlanningForUnblocked).
	o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "")

	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s planning: %v", s.ID, artErr)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:         o.opts.Prompts.Planning,
			Stage:            s,
			PhaseAgent:       prompts.AgentPlanning,
			DependencyPlans:  depPlans,
			Artifacts:        artCtx,
			StageDir:         stageDir,
			Interactive:      s.Interactive,
			OutputContractMD: planningContract,
			RetryContext:     retryContext,
			GlobalPrompt:     o.opts.GlobalPrompt,
		})
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning.log")

		r := o.runnerFor(s, phasePlanning)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}

		planMD, _ := os.ReadFile(outFile)
		issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
		if !issues.IsClean() {
			if adoptWrittenPlan(logFile, outFile) {
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
		return checkPlanCompletionFor(stageDir, s.Interactive)
	}, func() { o.spawnAgent(ctx, s, o.runPlanningWithFeedback) })
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
	issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
	if !issues.IsClean() {
		if adoptWrittenPlan(logFile, outFile) {
			return nil
		}
		return &MissingSectionsError{Missing: issues.MissingSections}
	}
	return nil
}

func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.Trigger(s.ID, EvStartPlanning, GuardCtx{Stage: s}, "")

	o.runWithRetry(ctx, s, phasePlanning, func(retryContext string) error {
		feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
		_, prevPlan, err := state.LatestPlanVersion(stageDir)
		if err != nil {
			return fmt.Errorf("read previous plan: %w", err)
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s revise: %v", s.ID, artErr)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:         o.opts.Prompts.Planning,
			Stage:            s,
			PhaseAgent:       prompts.AgentPlanning,
			DependencyPlans:  depPlans,
			Artifacts:        artCtx,
			PreviousPlan:     prevPlan,
			Feedback:         string(feedbackData),
			StageDir:         stageDir,
			Interactive:      s.Interactive,
			OutputContractMD: planningContract,
			RetryContext:     retryContext,
			GlobalPrompt:     o.opts.GlobalPrompt,
		})
		outFile := filepath.Join(stageDir, "plan.md")
		logFile := filepath.Join(stageDir, "planning-revision.log")

		r := o.runnerFor(s, phasePlanning)
		if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
			return err
		}
		planMD, _ := os.ReadFile(outFile)
		issues := prompts.ValidatePlan(string(planMD), requiredPlanSections)
		if !issues.IsClean() {
			if adoptWrittenPlan(logFile, outFile) {
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
		return checkPlanCompletionFor(stageDir, s.Interactive)
	}, func() { o.spawnAgent(ctx, s, o.runPlanningWithFeedback) })
}

func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s impl: %v", s.ID, artErr)
		}

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

		stageDirNote := fmt.Sprintf("\n\nStage directory for .done file: %s", stageDir)
		if s.Verify != "" {
			stageDirNote += fmt.Sprintf("\n\nVerify command (runs automatically after you finish; it MUST exit 0, "+
				"so run it yourself before creating .done):\n%s", s.Verify)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation,
			Stage:           s,
			PhaseAgent:      prompts.AgentImplementation,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			Plan:            string(planData),
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + stageDirNote,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		logFile := filepath.Join(stageDir, "implementation.log")

		r := o.runnerFor(s, phaseImplementation)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			reviewPrompt := prompts.Build(prompts.Inputs{
				Template:        o.opts.Prompts.Review,
				Stage:           s,
				PhaseAgent:      prompts.AgentReview,
				DependencyPlans: depPlans,
				Artifacts:       artCtx,
				StageDir:        stageDir,
				Interactive:     s.Interactive,
				GlobalPrompt:    o.opts.GlobalPrompt,
			})
			reviewLog := filepath.Join(stageDir, "review.log")
			rr := o.runnerFor(s, phaseReview)
			if err := rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog); err != nil {
				return err
			}
		}

		return nil
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	}, func() { o.spawnAgent(ctx, s, o.runImplementationWithFeedback) })
}

func (o *Orchestrator) runReviewAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}

	depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
		appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
		o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
	})
	artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	if artErr != nil {
		log.Printf("WARN: collect artifacts for %s review: %v", s.ID, artErr)
	}

	o.runWithRetry(ctx, s, phaseReview, func(retryContext string) error {
		reviewPrompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Review,
			Stage:           s,
			PhaseAgent:      prompts.AgentReview,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		reviewLog := filepath.Join(stageDir, "review.log")
		rr := o.runnerFor(s, phaseReview)
		return rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog)
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	}, func() { o.spawnAgent(ctx, s, o.runReviewWithFeedback) })
}

// runAutonomousAgent выполняет стадию в автономном треке — без plan.md и approval.
// Агент использует прикреплённые скиллы и обязан написать execution_summary.md
// по завершении (проверяется completion-check'ом checkAutonomousCompletion).
//
// Трек отличается от runImplementationAgent: нет чтения plan.md, нет .done,
// фаза — "autonomous_execution", используется Autonomous-шаблон промпта.
//
// MkdirAll + запись autonomous.flag в начале — тот же защитный паттерн, что
// уже есть в runPlanningAgent/runReviewAgent (см. их начало): стадия могла
// быть каскадно помечена failed через failBlockedStages/blocked_by_dep, так и
// не получив stageDir на диске (директория создаётся только при реальной
// активации). Без этого ручной retry такой стадии падал с
// "open log file: ... no such file or directory" — фикс в единой точке
// покрывает retry, resume-после-рестарта (recovery.go) и любой будущий caller.
func (o *Orchestrator) runAutonomousAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
		return
	}
	_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)

	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s autonomous: %v", s.ID, artErr)
		}
		depCtx := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})

		summaryNote := fmt.Sprintf("\n\nStage directory: %s\nWrite execution_summary.md here when done.", stageDir)
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
			RetryContext:    retryContext + summaryNote,
		})
		logFile := filepath.Join(stageDir, "autonomous.log")
		r := o.runnerFor(s, phaseAutonomous)
		return r.RunAgent(ctx, phaseAutonomous, s.Name, prompt, logFile)
	}, func() error {
		return checkAutonomousCompletion(stageDir)
	}, func() { o.spawnAgent(ctx, s, o.runAutonomousWithFeedback) })
}

// runImplementationWithFeedback перезапускает implementation-фазу с фидбеком
// пользователя (agent_suggest) — как runImplementationAgent, но с добавленной
// в контекст фразой из feedback.md. Идёт через тот же runnerFor: если стадия
// Interactive, автоматически получает --resume <session-id> (существующий
// sessionExists/loadOrCreateSession в runnerFor не меняется).
func (o *Orchestrator) runImplementationWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// Возвращаемся в Running (могли прийти из Revising — Revise() перевёл
	// сюда стадию, чтобы доставить прерывание). Без этого onAgentCompleted и
	// EvComplete не увидели бы подходящий статус, и стадия зависла бы в
	// Revising даже после успешного завершения агента.
	o.Trigger(s.ID, EvStartRun, GuardCtx{}, "")
	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	feedbackNote := ""
	if len(feedbackData) > 0 {
		feedbackNote = "\n\n## User note (added while this stage was running)\n\n" + string(feedbackData)
	}

	o.runWithRetry(ctx, s, phaseImplementation, func(retryContext string) error {
		planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
		if err != nil {
			return err
		}

		depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s impl (feedback restart): %v", s.ID, artErr)
		}

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

		stageDirNote := fmt.Sprintf("\n\nStage directory for .done file: %s", stageDir)
		if s.Verify != "" {
			stageDirNote += fmt.Sprintf("\n\nVerify command (runs automatically after you finish; it MUST exit 0, "+
				"so run it yourself before creating .done):\n%s", s.Verify)
		}
		prompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Implementation,
			Stage:           s,
			PhaseAgent:      prompts.AgentImplementation,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			Plan:            string(planData),
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + stageDirNote + feedbackNote,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		logFile := filepath.Join(stageDir, "implementation-feedback.log")

		r := o.runnerFor(s, phaseImplementation)
		if err := r.RunAgent(ctx, string(s.ImplAgent()), s.Name, prompt, logFile); err != nil {
			return err
		}

		if s.HasAgent(flow.AgentReview) {
			reviewPrompt := prompts.Build(prompts.Inputs{
				Template:        o.opts.Prompts.Review,
				Stage:           s,
				PhaseAgent:      prompts.AgentReview,
				DependencyPlans: depPlans,
				Artifacts:       artCtx,
				StageDir:        stageDir,
				Interactive:     s.Interactive,
				GlobalPrompt:    o.opts.GlobalPrompt,
			})
			reviewLog := filepath.Join(stageDir, "review.log")
			rr := o.runnerFor(s, phaseReview)
			if err := rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	}, func() { o.spawnAgent(ctx, s, o.runImplementationWithFeedback) })
}

// runReviewWithFeedback — как runReviewAgent, с фразой пользователя в контексте.
func (o *Orchestrator) runReviewWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// См. runImplementationWithFeedback: возвращаемся в Running из Revising.
	o.Trigger(s.ID, EvStartRun, GuardCtx{}, "")
	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	feedbackNote := ""
	if len(feedbackData) > 0 {
		feedbackNote = "\n\n## User note (added while this stage was running)\n\n" + string(feedbackData)
	}

	depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
		appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
		o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
	})
	artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	if artErr != nil {
		log.Printf("WARN: collect artifacts for %s review (feedback restart): %v", s.ID, artErr)
	}

	o.runWithRetry(ctx, s, phaseReview, func(retryContext string) error {
		reviewPrompt := prompts.Build(prompts.Inputs{
			Template:        o.opts.Prompts.Review,
			Stage:           s,
			PhaseAgent:      prompts.AgentReview,
			DependencyPlans: depPlans,
			Artifacts:       artCtx,
			StageDir:        stageDir,
			Interactive:     s.Interactive,
			RetryContext:    retryContext + feedbackNote,
			GlobalPrompt:    o.opts.GlobalPrompt,
		})
		reviewLog := filepath.Join(stageDir, "review-feedback.log")
		rr := o.runnerFor(s, phaseReview)
		return rr.RunAgent(ctx, phaseReview, s.Name, reviewPrompt, reviewLog)
	}, func() error {
		return checkCompletion(stageDir, ".", s)
	}, func() { o.spawnAgent(ctx, s, o.runReviewWithFeedback) })
}

// runAutonomousWithFeedback — как runAutonomousAgent, с фразой пользователя в
// контексте. Зеркалит тело runAutonomousAgent один в один (лог, аргументы
// RunAgent, текст summaryNote) и добавляет только feedbackNote поверх
// RetryContext; MkdirAll/autonomous.flag не повторяются — стадия уже была
// активирована исходным runAutonomousAgent до прерывания.
func (o *Orchestrator) runAutonomousWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	// См. runImplementationWithFeedback: возвращаемся в Running из Revising.
	o.Trigger(s.ID, EvStartRun, GuardCtx{}, "")
	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	feedbackNote := ""
	if len(feedbackData) > 0 {
		feedbackNote = "\n\n## User note (added while this stage was running)\n\n" + string(feedbackData)
	}

	o.runWithRetry(ctx, s, phaseAutonomous, func(retryContext string) error {
		artCtx, artErr := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
		if artErr != nil {
			log.Printf("WARN: collect artifacts for %s autonomous (feedback restart): %v", s.ID, artErr)
		}
		depCtx := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
			appendNotice(o.opts.RunDir, s.ID, string(EventContextWarning), fmt.Sprintf("%s: %s", depID, msg))
			o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
		})

		summaryNote := fmt.Sprintf("\n\nStage directory: %s\nWrite execution_summary.md here when done.", stageDir)
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
			RetryContext:    retryContext + summaryNote + feedbackNote,
		})
		logFile := filepath.Join(stageDir, "autonomous-feedback.log")
		r := o.runnerFor(s, phaseAutonomous)
		return r.RunAgent(ctx, phaseAutonomous, s.Name, prompt, logFile)
	}, func() error {
		return checkAutonomousCompletion(stageDir)
	}, func() { o.spawnAgent(ctx, s, o.runAutonomousWithFeedback) })
}

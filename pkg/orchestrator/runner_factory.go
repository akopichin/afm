package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// usageHintFor returns a best-effort accounting.UsageHint for the given agent
// command, taken from the docker.agents recipe declared for it (if any) —
// empty when there's no such recipe (including the plain "claude" default).
// This is only a non-authoritative hint fed to accounting.NewCollector: a
// model/channel actually discovered in the agent's own stream-json output
// always wins over it (see accounting.Collector.Finish).
func (o *Orchestrator) usageHintFor(cmd string) accounting.UsageHint {
	recipe, ok := o.opts.Config.Docker.Agents[cmd]
	if !ok {
		return accounting.UsageHint{}
	}
	return accounting.UsageHint{Channel: recipe.Type, Model: recipe.Model}
}

// runnerFor returns the appropriate Runner for a stage's phase.
// For interactive stages it generates a session id and returns an executor
// configured with --session-id / --resume and AFM_STAGE_DIR env.
func (o *Orchestrator) runnerFor(s flow.Stage, phase string) executor.Runner {
	if !s.Interactive {
		// Инъектированный runner (тесты) используется только для дефолтной команды.
		if o.opts.Runner != nil && s.Command == "" {
			return o.runner
		}
		// Per-stage runner. Для дефолтной команды берём клиент из конфига (+ его
		// ExtraArgs) — иначе разделяемый o.runner привязывал OnAction к пустому
		// stageID, и события agent_action уходили без бейджа стадии (косяк №1).
		cmd := s.Command
		var extraArgs []string
		if cmd == "" {
			cmd = o.opts.Config.Client.Command
			extraArgs = o.opts.Config.Client.ExtraArgs
		}
		cfg := executor.Config{
			Command:        cmd,
			ExtraArgs:      extraArgs,
			IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
			TruncateOutput: o.opts.Config.Executor.TruncateOutput,
			OnAction:       uiActionPublisher(o.ui, s.ID),
			WrapperDir:     executor.WrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
			Dir:            o.opts.RootDir,
			Debug:          o.opts.Debug,
			RunDir:         o.opts.RunDir,
			StageID:        s.ID,
			// Every non-interactive stage gets AFM_STAGE_DIR too, so an agent or
			// skill that uses the file-based dialog protocol always has somewhere
			// to write question.json — the poller auto-answers it (dialog_poller.go).
			StageDir:  filepath.Join(o.opts.RunDir, s.ID),
			Phase:     phase,
			UsageHint: o.usageHintFor(cmd),
			OnUsage:   o.recordUsage(s.ID, phase, ""),
		}
		if ch, ok := o.interruptChans.Load(s.ID); ok {
			cfg.InterruptCh = ch.(chan struct{})
		}
		return executor.New(cfg)
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	resume := stagefiles.SessionExists(stageDir, phase)
	sessionID, err := stagefiles.LoadOrCreateSession(stageDir, phase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: interactive stage %q: session failed: %v; using non-interactive runner\n", s.ID, err)
		return o.runnerForFallback(s, phase)
	}

	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	// Interactive stages always need the claude stream-json flags (incl. --verbose,
	// afm bug #1.1). ResolveArgs prepends defaults and dedups user overrides.
	extraArgs := executor.ResolveArgs(o.opts.Config.Client.ExtraArgs)
	cfg := executor.Config{
		Command:        cmd,
		ExtraArgs:      extraArgs,
		IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
		TruncateOutput: o.opts.Config.Executor.TruncateOutput,
		OnAction:       uiActionPublisher(o.ui, s.ID),
		SessionID:      sessionID,
		Resume:         resume,
		StageDir:       stageDir,
		WrapperDir:     executor.WrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:            o.opts.RootDir,
		Debug:          o.opts.Debug,
		RunDir:         o.opts.RunDir,
		StageID:        s.ID,
		Phase:          phase,
		UsageHint:      o.usageHintFor(cmd),
		OnUsage:        o.recordUsage(s.ID, phase, ""),
	}
	if ch, ok := o.interruptChans.Load(s.ID); ok {
		cfg.InterruptCh = ch.(chan struct{})
	}
	return executor.New(cfg)
}

func (o *Orchestrator) runnerForFallback(s flow.Stage, phase string) executor.Runner {
	if s.Command == "" {
		return o.runner
	}
	return executor.New(executor.Config{
		Command:        s.Command,
		IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
		TruncateOutput: o.opts.Config.Executor.TruncateOutput,
		OnAction:       uiActionPublisher(o.ui, s.ID),
		WrapperDir:     executor.WrapperDirFor(s.Command, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Debug:          o.opts.Debug,
		RunDir:         o.opts.RunDir,
		StageID:        s.ID,
		Phase:          phase,
		UsageHint:      o.usageHintFor(s.Command),
		OnUsage:        o.recordUsage(s.ID, phase, ""),
	})
}

// runnerForVerify собирает *executor.Executor для ОДНОГО шага AI-verify.
// В отличие от runnerFor: свежая сессия (SessionID/Resume не заданы —
// верификатор не резюмирует диалог автора), StageDir не задан (верификатор
// не участник file-based dialog протокола, читает только то, что дано в
// промпте) и Phase — отдельная execution-purpose метка verifyExecutionLabel,
// а не родительская фаза стадии (planning/implementation/review), чтобы
// usage/логи verify не путались с фазой автора. cmd — команда КОНКРЕТНОГО
// verify-шага (VerifyStep.Command), может отличаться от Stage.Command.
func (o *Orchestrator) runnerForVerify(s flow.Stage, cmd string) *executor.Executor {
	return executor.New(executor.Config{
		Command:        cmd,
		IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
		TruncateOutput: o.opts.Config.Executor.TruncateOutput,
		OnAction:       uiActionPublisher(o.ui, s.ID),
		WrapperDir:     executor.WrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:            o.opts.RootDir,
		Debug:          o.opts.Debug,
		RunDir:         o.opts.RunDir,
		StageID:        s.ID,
		Phase:          verifyExecutionLabel,
		UsageHint:      o.usageHintFor(cmd),
		OnUsage:        o.recordUsage(s.ID, verifyExecutionLabel, ""),
	})
}

// execVerifyAgent — продакшн-реализация seam'а o.runVerifyAgent (см.
// Orchestrator.runVerifyAgent): строит свежий раннер под конкретную команду
// verify-шага и делегирует его RunVerifyAgent.
func (o *Orchestrator) execVerifyAgent(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
	return o.runnerForVerify(s, cmd).RunVerifyAgent(ctx, cmd, s.Name, prompt, logFile, resultFile)
}

func uiActionPublisher(ui *bus.UIBus, stageID string) func(string, string) {
	return func(tool, detail string) {
		ui.Publish(bus.Event{Type: bus.EventAgentAction, StageID: stageID, Data: map[string]string{
			"tool":   tool,
			"detail": detail,
		}})
	}
}

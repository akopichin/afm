package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
)

// wrapperDirFor возвращает wrapper-dir для команды cmd: для generated-команд
// (autoShim) — opts.WrapperDir, чтобы сгенерированный скрипт резолвился на PATH;
// для остальных (включая claude) — пусто (используется реальный бинарник).
func wrapperDirFor(cmd string, wrapperDir string, generated map[string]bool) string {
	if generated[cmd] {
		return wrapperDir
	}
	return ""
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
			WrapperDir:     wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
			Dir:            o.opts.RootDir,
		}
		// Autonomous-фаза диалоговая: агенту нужен AFM_STAGE_DIR, чтобы писать
		// question.json и писать execution_summary.md в каталог стадии.
		if phase == phaseAutonomous {
			cfg.StageDir = filepath.Join(o.opts.RunDir, s.ID)
		}
		return executor.New(cfg)
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	resume := sessionExists(stageDir, phase)
	sessionID, err := loadOrCreateSession(stageDir, phase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: interactive stage %q: session failed: %v; using non-interactive runner\n", s.ID, err)
		return o.runnerForFallback(s)
	}

	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	// Interactive stages always need the claude stream-json flags (incl. --verbose,
	// afm bug #1.1). ResolveArgs prepends defaults and dedups user overrides.
	extraArgs := executor.ResolveArgs(o.opts.Config.Client.ExtraArgs)
	return executor.New(executor.Config{
		Command:        cmd,
		ExtraArgs:      extraArgs,
		IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
		TruncateOutput: o.opts.Config.Executor.TruncateOutput,
		OnAction:       uiActionPublisher(o.ui, s.ID),
		SessionID:      sessionID,
		Resume:         resume,
		StageDir:       stageDir,
		WrapperDir:     wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:            o.opts.RootDir,
	})
}

func (o *Orchestrator) runnerForFallback(s flow.Stage) executor.Runner {
	if s.Command == "" {
		return o.runner
	}
	return executor.New(executor.Config{
		Command:        s.Command,
		IdleTimeout:    o.opts.Config.Executor.IdleTimeout,
		TruncateOutput: o.opts.Config.Executor.TruncateOutput,
		OnAction:       uiActionPublisher(o.ui, s.ID),
		WrapperDir:     wrapperDirFor(s.Command, o.opts.WrapperDir, o.opts.GeneratedAgents),
	})
}

func uiActionPublisher(ui *UIBus, stageID string) func(string, string) {
	return func(tool, detail string) {
		ui.Publish(Event{Type: EventAgentAction, StageID: stageID, Data: map[string]string{
			"tool":   tool,
			"detail": detail,
		}})
	}
}

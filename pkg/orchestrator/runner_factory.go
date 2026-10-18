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
	"github.com/akopichin/afm/pkg/prompts"
)

// usageHintFor returns a best-effort accounting.UsageHint for the given agent command, taken from the
// docker.agents recipe declared for it — empty when there's no such recipe.
func (o *Orchestrator) usageHintFor(cmd string) accounting.UsageHint {
	recipe, ok := o.opts.Config.Docker.Agents[cmd]
	if !ok {
		return accounting.UsageHint{}
	}
	return accounting.UsageHint{Channel: recipe.Type, Model: recipe.Model}
}

// browserForStage is the single stage-aware policy for the managed browser capability. Otherwise it builds
// the per-stage executor.BrowserMCP.
func (o *Orchestrator) browserForStage(s flow.Stage, cmd string) *executor.BrowserMCP {
	d := o.opts.BrowserDescriptor
	if d == nil || s.BrowserDisabled() || !o.opts.Config.BrowserSupportedFor(cmd) {
		return nil
	}
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	return &executor.BrowserMCP{
		ServerName:   browserServerName,
		LauncherPath: browserLauncherPath(),
		WSEndpoint:   d.StageEndpoint(s.ID),
		Token:        d.Token,
		AppURL:       d.AppURL,
		AppPort:      o.opts.Config.Browser.GetAppPort(),
		Roots:        dedupRoots(o.opts.RootDir, stageDir),
		Codex:        o.opts.Config.IsCodexCommand(cmd),
	}
}

// browserServerName is the MCP server name the agent sees.
const browserServerName = "afm_chrome"

// stageCommand resolves the effective agent command for a stage (its own command
// or the configured default client).
func (o *Orchestrator) stageCommand(s flow.Stage) string {
	if s.Command != "" {
		return s.Command
	}
	return o.opts.Config.Client.Command
}

// browserRuntimeFor returns the prompt-side browser runtime block for a stage, gated by the SAME policy as
// the MCP injection so the prompt and the actual tool availability stay in lockstep.
func (o *Orchestrator) browserRuntimeFor(s flow.Stage) *prompts.BrowserRuntime {
	b := o.browserForStage(s, o.stageCommand(s))
	if b == nil {
		return nil
	}
	docker := o.opts.BrowserDescriptor != nil && o.opts.BrowserDescriptor.Docker
	return &prompts.BrowserRuntime{AppURL: b.AppURL, AppPort: b.AppPort, ServerName: b.ServerName, Docker: docker}
}

// browserLauncherPath resolves the absolute afm path used as the MCP command. Falls back to "afm" only if
// resolution fails.
func browserLauncherPath() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "afm"
}

// dedupRoots returns the effective filesystem roots (effective RootDir + stage
// dir), skipping empties and duplicates, preserving order.
func dedupRoots(root, stageDir string) []string {
	// An empty project root means the agent inherits afm's CWD as its root, so the
	// MCP's filesystemRoot must point there too.
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range []string{root, stageDir} {
		if r == "" {
			continue
		}
		// Resolve to ABSOLUTE here.
		if abs, err := filepath.Abs(r); err == nil {
			r = abs
		}
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// willResume reports whether the NEXT agent start for s/phase CONTINUES the previous session
// (--resume) instead of starting a fresh one. Single source of truth for both the runner (which
// sets executor.Config.Resume) and the retry loop (which reconstructs the context a fresh session
// would otherwise lack).
func willResume(s flow.Stage, stageDir, phase string) bool {
	return s.Interactive && stagefiles.SessionExists(stageDir, phase)
}

// runnerFor returns the appropriate Runner for a stage's phase.
func (o *Orchestrator) runnerFor(s flow.Stage, phase string) executor.Runner {
	if !s.Interactive {
		// Инъектированный runner (тесты) используется только для дефолтной команды.
		if o.opts.Runner != nil && s.Command == "" {
			return o.runner
		}
		return o.nonInteractiveRunner(s, phase)
	}

	// Абсолютный путь (T6, D4): AFM_STAGE_DIR пересекает границу процесса (CWD агента = root_dir/worktree,
	// может не совпадать с CWD afm) — относительный путь резолвился бы не туда.
	stageDir := o.stageDir(s.ID)
	resume := willResume(s, stageDir, phase)
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
		BrowserMCP:     o.browserForStage(s, cmd),
		MeshClient:     o.meshClientFor(meshStageParticipantID(s.ID)),
		OnDelivered:    o.agentCommOnDeliveredForStage(s),
		Reasoning:      o.opts.Config.Client.Reasoning,
		RunID:          o.opts.RunID,
		OnObservation:  o.observationSink(s.ID),
	}
	if ch, ok := o.interruptChans.Load(s.ID); ok {
		cfg.InterruptCh = ch.(chan struct{})
	}
	return executor.New(cfg)
}

// nonInteractiveRunner builds the per-stage non-interactive executor with the FULL stage wiring — Dir,
// StageDir, and the browser policy.
func (o *Orchestrator) nonInteractiveRunner(s flow.Stage, phase string) executor.Runner {
	// Для дефолтной команды берём клиент из конфига — иначе разделяемый runner привязывал бы OnAction к
	// пустому stageID, и события agent_action уходили бы без бейджа стадии.
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
		// Every non-interactive stage gets AFM_STAGE_DIR too, so an agent or skill that uses the file-based
		// dialog protocol always has somewhere to write question.json — the poller auto-answers it. Absolute
		// (T6, D4): see the comment on the interactive branch above — same cross-process-boundary reasoning.
		StageDir:      o.stageDir(s.ID),
		Phase:         phase,
		UsageHint:     o.usageHintFor(cmd),
		OnUsage:       o.recordUsage(s.ID, phase, ""),
		BrowserMCP:    o.browserForStage(s, cmd),
		MeshClient:    o.meshClientFor(meshStageParticipantID(s.ID)),
		OnDelivered:   o.agentCommOnDeliveredForStage(s),
		Reasoning:     o.opts.Config.Client.Reasoning,
		RunID:         o.opts.RunID,
		OnObservation: o.observationSink(s.ID),
	}
	if ch, ok := o.interruptChans.Load(s.ID); ok {
		cfg.InterruptCh = ch.(chan struct{})
	}
	return executor.New(cfg)
}

func (o *Orchestrator) runnerForFallback(s flow.Stage, phase string) executor.Runner {
	// Test seam: an injected runner stands in only for the default command.
	if o.opts.Runner != nil && s.Command == "" {
		return o.runner
	}
	return o.nonInteractiveRunner(s, phase)
}

// runnerForVerify собирает *executor.Executor для ОДНОГО шага AI-verify. cmd — команда КОНКРЕТНОГО
// verify-шага (VerifyStep.Command), может отличаться от Stage.Command.
func (o *Orchestrator) runnerForVerify(s flow.Stage, cmd string) *executor.Executor {
	cfg := executor.Config{
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
	}
	if ch, ok := o.interruptChans.Load(s.ID); ok {
		cfg.InterruptCh = ch.(chan struct{})
	}
	return executor.New(cfg)
}

// execVerifyAgent — продакшн-реализация seam'а o.runVerifyAgent (см. Orchestrator.runVerifyAgent): строит
// свежий раннер под конкретную команду verify-шага и делегирует его RunVerifyAgent.
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

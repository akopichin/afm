package orchestrator

import (
	"context"
	"log"

	"github.com/akopichin/afm/pkg/executor"
)

// resolveMemoryCommand выбирает команду агента: явную из spec или дефолтный
// клиент из конфига (с его ExtraArgs).
func (o *Orchestrator) resolveMemoryCommand(specCommand string) (string, []string) {
	if specCommand != "" {
		return specCommand, nil
	}
	return o.opts.Config.Client.Command, o.opts.Config.Client.ExtraArgs
}

// execMemoryAgent — реальная реализация seam runMemoryAgent: свежий
// изолированный агент (без --resume, без StageDir), CWD=root_dir, читает/пишет
// файлы по абсолютным путям из промпта. Зеркалит runJSONFixAgent, но
// синхронный: конвейер уже крутится в отдельной (SpawnDetached) горутине и
// вызывает шаги последовательно.
func (o *Orchestrator) execMemoryAgent(ctx context.Context, spec memoryAgentSpec) error {
	cmd, extra := o.resolveMemoryCommand(spec.command)
	cfg := executor.Config{
		Command:     cmd,
		ExtraArgs:   executor.ResolveArgs(extra),
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		WrapperDir:  wrapperDirFor(cmd, o.opts.WrapperDir, o.opts.GeneratedAgents),
		Dir:         o.opts.RootDir,
		Debug:       o.opts.Debug,
		RunDir:      o.opts.RunDir,
	}
	ex := executor.New(cfg)
	prompt := buildMemoryPrompt(o.opts.Prompts, spec)
	if err := ex.RunAgent(ctx, "memory-"+spec.kind, spec.stageName, prompt, spec.logFile); err != nil {
		log.Printf("WARN: memory %s agent (%s): %v", spec.kind, spec.stageName, err)
		return err
	}
	return nil
}

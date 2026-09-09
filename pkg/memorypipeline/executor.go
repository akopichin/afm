package memorypipeline

import (
	"context"

	"github.com/akopichin/afm/pkg/executor"
)

// NewExecRunner returns the production AgentRunner: a fresh isolated agent
// (без --resume, без StageDir — AFM_STAGE_DIR стрипается executor'ом),
// CWD=cfg.RootDir, читает/пишет файлы по абсолютным путям из промпта.
// Зеркалит runJSONFixAgent, но синхронный: конвейер уже крутится в отдельной
// (SpawnDetached) горутине и вызывает шаги последовательно.
//
// spec.Command пусто → используется дефолтная команда клиента (cfg.Command/
// cfg.ExtraArgs) — сохраняет прежнюю семантику resolveMemoryCommand.
func NewExecRunner(cfg AgentConfig, prompts Prompts) AgentRunner {
	return func(ctx context.Context, spec AgentSpec) error {
		cmd, extra := spec.Command, []string(nil)
		if cmd == "" {
			cmd, extra = cfg.Command, cfg.ExtraArgs
		}
		ex := executor.New(executor.Config{
			Command:     cmd,
			ExtraArgs:   executor.ResolveArgs(extra),
			IdleTimeout: cfg.IdleTimeout,
			WrapperDir:  cfg.WrapperDir,
			Dir:         cfg.RootDir,
			RunDir:      cfg.RunDir,
			Debug:       cfg.Debug,
			// fresh context: SessionID/Resume/StageDir intentionally unset
			// (AFM_STAGE_DIR is stripped by the executor even if inherited).
		})
		return ex.RunAgent(ctx, "memory-"+spec.Kind, spec.StageName, BuildPrompt(prompts, spec), spec.LogFile)
	}
}

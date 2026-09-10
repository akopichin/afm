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
// Всегда использует дефолтную команду клиента (cfg.Command/cfg.ExtraArgs) —
// каждый шаг конвейера памяти запускается одним и тем же настроенным
// агентом, per-spec переопределения команды нет.
func NewExecRunner(cfg AgentConfig, prompts Prompts) AgentRunner {
	return func(ctx context.Context, spec AgentSpec) error {
		ex := executor.New(executor.Config{
			Command:     cfg.Command,
			ExtraArgs:   executor.ResolveArgs(cfg.ExtraArgs),
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

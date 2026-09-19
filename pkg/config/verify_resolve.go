package config

import (
	"fmt"

	"github.com/akopichin/afm/pkg/flow"
)

// ResolveAgentCommand резолвит алиас команды агента в фактическую команду —
// то же правило, что сейчас дублируется в pkg/docker/launcher.go и
// pkg/orchestrator/runner_factory.go: "" → ClaudeCommand; иначе — либо
// recipe-ключ в cfg.Docker.Agents, либо произвольный хостовый бинарник.
// supported=true, если команда резолвится через известный afm'у путь
// (дефолтный claude или явный recipe); false — для произвольного хостового
// бинарника без recipe. Это НЕ ошибка сама по себе (stage.command вообще
// допускает любое имя из PATH) — просто "afm не знает про эту команду
// заранее". Более строгая проверка "поддерживается ли алиас именно как
// verify-адаптер" — отдельная функция SupportedVerifyCommand ниже.
//
// TODO(verify): переиспользовать здесь дублирующуюся логику из
// docker/launcher.go и orchestrator/runner_factory.go вместо двух
// независимых копий этого правила.
func ResolveAgentCommand(cmd string, cfg Config) (resolved string, supported bool) {
	if cmd == "" {
		return ClaudeCommand, true
	}
	if _, ok := cfg.Docker.Agents[cmd]; ok {
		return cmd, true
	}
	if cmd == ClaudeCommand {
		return cmd, true
	}
	return cmd, false
}

// SupportedVerifyCommand сообщает, поддерживается ли алиас как verify-адаптер
// в V1 (сам агентский verify-раннер появится в V4 — здесь только проверка
// "можно ли будет его запустить"). Консервативный список V1: голое имя
// "claude" или "codex" (сам бинарник/обёртка codex-as-claude), либо recipe с
// type: codex в docker.agents. Отдельно от ResolveAgentCommand: команда может
// быть валидным агентом стадии (например recipe type: openai), но не иметь
// verify-адаптера.
func SupportedVerifyCommand(cmd string, cfg Config) bool {
	resolved, _ := ResolveAgentCommand(cmd, cfg)
	if resolved == ClaudeCommand || resolved == RecipeTypeCodex {
		return true
	}
	recipe, ok := cfg.Docker.Agents[resolved]
	return ok && recipe.Type == RecipeTypeCodex
}

// ValidateVerifySpecs проверяет ДО старта рана, что каждый агентский
// verify-шаг (Kind==VerifyAgent) резолвится в поддерживаемый verify-адаптер.
// Живёт в pkg/config, а не в flow.Flow.validate(): та ничего не знает о
// конфиге агентов (recipes, дефолтная команда) и не должна импортировать
// pkg/config, поэтому вызывается явно на старте (cmd/afm/run.go), как только
// и флоу, и конфиг уже загружены — раньше эмиссии flow_started/запуска
// какой-либо стадии. Shell-шаги (Kind==VerifyShell) не резолвятся вовсе.
func ValidateVerifySpecs(f *flow.Flow, cfg Config) error {
	for _, s := range f.Stages {
		for i, step := range s.Verify.Steps {
			if step.Kind != flow.VerifyAgent {
				continue
			}
			if !SupportedVerifyCommand(step.Command, cfg) {
				return fmt.Errorf("stage %q: verify[%d]: command %q is not a supported verify adapter", s.ID, i+1, step.Command)
			}
		}
	}
	return nil
}

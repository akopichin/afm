package config

import (
	"fmt"

	"github.com/akopichin/afm/pkg/flow"
)

// bareCodexCommand — имя бинарника/обёртки codex-as-claude, когда verify.command
// указывает на него БЕЗ recipe в docker.agents. Отдельная константа от
// RecipeTypeCodex ("codex"), хотя их значения сейчас совпадают: это два разных
// понятия (имя команды vs тип recipe), которые случайно пишутся одинаково —
// сравнение не должно молча полагаться на это совпадение (см. находку
// код-ревью в SupportedVerifyCommand ниже).
const bareCodexCommand = "codex"

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
// ВНИМАНИЕ при будущем обобщении (TODO ниже): ветка cmd=="" → ClaudeCommand
// здесь НЕ зеркалит настоящее правило дословно — в runner_factory.go пустая
// command резолвится в o.opts.Config.Client.Command, который лишь ПО
// УМОЛЧАНИЮ равен ClaudeCommand (см. config.Default()), но может быть
// переопределён в config.yaml. Сегодня это безобидно: Flow.validate()
// гарантирует непустой step.Command для verify-агентского шага (см.
// VerifySpec.validate), так что ResolveAgentCommand("", cfg) для verify
// никогда не вызывается — эта ветка мёртвый код в текущем месте
// использования. Не копируйте это упрощение при переиспользовании ниже —
// возьмите Client.Command, а не голую константу.
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
// в V1. Единственный гарантированный verify-адаптер в V1 — codex-семейство:
// голое имя "codex" (сам бинарник/обёртка codex-as-claude) либо recipe с
// type: codex в docker.agents. "claude" здесь НЕ поддерживается: у него нет
// настоящего read-only-режима — Config.VerifyMode эмитит только
// CODEX_VERIFY=1 (executor.go), который claude игнорирует, так что
// claude-верификатор запускался бы с --dangerously-skip-permissions и мог бы
// редактировать тот самый артефакт, который должен проверять (I11/I12).
// Отдельно от ResolveAgentCommand: команда может быть валидным агентом стадии
// (например recipe type: openai, или сам claude как обычный агент), но не
// иметь verify-адаптера.
//
// Recipe для резолвнутого имени — решающее слово, проверяется ПЕРВЫМ: если
// docker.agents переопределяет ключ "codex" recipe'ом другого типа (напр.
// openai), это НЕ codex-адаптер, что бы ни говорило голое имя. Эвристика по
// голому имени (bareCodexCommand) применяется только когда для резолвнутого
// имени НЕТ recipe вовсе.
func SupportedVerifyCommand(cmd string, cfg Config) bool {
	resolved, _ := ResolveAgentCommand(cmd, cfg)
	if recipe, ok := cfg.Docker.Agents[resolved]; ok {
		return recipe.Type == RecipeTypeCodex
	}
	return resolved == bareCodexCommand
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
				return fmt.Errorf("stage %q: verify[%d]: command %q is not a supported verify adapter (v1 supports codex only)", s.ID, i+1, step.Command)
			}
		}
	}
	return nil
}

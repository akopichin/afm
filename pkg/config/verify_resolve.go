package config

import (
	"fmt"

	"github.com/akopichin/afm/pkg/flow"
)

// codexAdapterCommand — имя РЕАЛЬНОГО read-only адаптера (scripts/codex-as-claude.sh,
// устанавливается в образ как /usr/local/bin/codex-as-claude), когда
// verify.command указывает на него БЕЗ recipe в docker.agents. НЕ "codex" —
// голый бинарник codex сам по себе не понимает CODEX_VERIFY и не добавляет
// -s read-only (это делает только обёртка codex-as-claude, см. C1 код-ревью):
// на хосте (без Docker) runnerForVerify исполняет ИМЕННО это резолвнутое имя
// напрямую через PATH, так что "поддерживаемый verify-адаптер" обязан
// указывать на реальный read-only shim, а не на сырой codex CLI. Отдельная
// константа от RecipeTypeCodex ("codex", тип recipe в docker.agents) — это
// два разных понятия (конкретное имя команды vs тип recipe).
const codexAdapterCommand = "codex-as-claude"

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
// голое имя "codex-as-claude" (реальный read-only shim, scripts/codex-as-claude.sh)
// либо recipe с type: codex в docker.agents (docker autoShim генерирует
// враппер, который сам вызывает codex-as-claude и честно слушает
// CODEX_VERIFY). Голое имя "codex" (сырой бинарник) НЕ поддерживается — на
// хосте (без Docker) он исполняется напрямую через PATH и не понимает ни
// CODEX_VERIFY, ни stream-json протокол verify (C1 код-ревью): read-only
// гарантия молча не соблюдалась бы. "claude" здесь тоже НЕ поддерживается: у
// него нет настоящего read-only-режима — Config.VerifyMode эмитит только
// CODEX_VERIFY=1 (executor.go), который claude игнорирует, так что
// claude-верификатор запускался бы с --dangerously-skip-permissions и мог бы
// редактировать тот самый артефакт, который должен проверять (I11/I12).
// Отдельно от ResolveAgentCommand: команда может быть валидным агентом стадии
// (например recipe type: openai, или сам claude как обычный агент), но не
// иметь verify-адаптера.
//
// Recipe для резолвнутого имени — решающее слово, проверяется ПЕРВЫМ: если
// docker.agents переопределяет ключ "codex-as-claude" recipe'ом другого типа
// (напр. openai), это НЕ codex-адаптер, что бы ни говорило голое имя.
// Эвристика по голому имени (codexAdapterCommand) применяется только когда
// для резолвнутого имени НЕТ recipe вовсе.
func SupportedVerifyCommand(cmd string, cfg Config) bool {
	resolved, _ := ResolveAgentCommand(cmd, cfg)
	if recipe, ok := cfg.Docker.Agents[resolved]; ok {
		return recipe.Type == RecipeTypeCodex
	}
	return resolved == codexAdapterCommand
}

// ValidateVerifySpecs проверяет ДО старта рана, что каждый агентский
// verify-шаг (Kind==VerifyAgent) резолвится в поддерживаемый verify-адаптер.
// Живёт в pkg/config, а не в flow.Flow.validate(): та ничего не знает о
// конфиге агентов (recipes, дефолтная команда) и не должна импортировать
// pkg/config, поэтому вызывается явно на старте (cmd/afm/run.go), как только
// и флоу, и конфиг уже загружены — раньше эмиссии flow_started/запуска
// какой-либо стадии. Shell-шаги (Kind==VerifyShell) не резолвятся вовсе.
//
// codexRecipesShimmed (D1 второй раунд код-ревью): true, только когда этот
// ран РЕАЛЬНО сгенерирует read-only враппер для type:codex recipe (Docker-режим
// С включённым autoShim — см. вызывающий код cmd/afm/run.go). Без этого
// сигнала SupportedVerifyCommand сам по себе принимал бы ЛЮБОЙ recipe с
// Type==RecipeTypeCodex, даже когда на хосте (или с выключенным autoShim)
// afm просто исполнил бы голый бинарник командой из PATH — тот не понимает ни
// CODEX_VERIFY, ни read-only режим, и read-only гарантия молча не
// соблюдалась бы. Когда codexRecipesShimmed==false, единственный
// поддерживаемый verify-адаптер — буквальное имя реального шима
// codex-as-claude (голое имя без recipe уже отфильтровано
// SupportedVerifyCommand выше); recipe-алиас type:codex в этом режиме —
// ошибка преflight'а, а не тихий запуск небезопасного bare-codex.
func ValidateVerifySpecs(f *flow.Flow, cfg Config, codexRecipesShimmed bool) error {
	for _, s := range f.Stages {
		for i, step := range s.Verify.Steps {
			if step.Kind != flow.VerifyAgent {
				continue
			}
			if !SupportedVerifyCommand(step.Command, cfg) {
				return fmt.Errorf("stage %q: verify[%d]: command %q is not a supported verify adapter (v1 supports codex-as-claude only)", s.ID, i+1, step.Command)
			}
			resolved, _ := ResolveAgentCommand(step.Command, cfg)
			if recipe, ok := cfg.Docker.Agents[resolved]; ok && recipe.Type == RecipeTypeCodex && !codexRecipesShimmed {
				return fmt.Errorf("stage %q: verify[%d]: command %q: codex recipe requires docker autoShim to enforce read-only; use codex-as-claude on host", s.ID, i+1, step.Command)
			}
		}
	}
	return nil
}

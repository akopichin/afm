# Корневой `prompt` в flow.yaml (общий для всех стейджей)

## Current State

afm собирает системный промпт каждого агента-стейджа в `pkg/prompts/builder.go` (`Build`). Блок `<system_rules>` строится из `in.Template` — per-agent шаблонов (`Prompts.Planning/Implementation/Review` из `orchestrator.Options`). Каждый стейдж дополнительно может иметь свой `Stage.Prompt` (`pkg/flow/flow.go:79`), который рендерится в блок `<prompt>`.

Структура `Flow` (`pkg/flow/flow.go:123`) содержит только `Name`, `Description`, `MaxParallel`, `Stages` — **корневого/общего промпта для всего flow нет**. При загрузке flow в `cmd/afm/run.go:166` в `orchestrator.Options` передаётся только `f.Stages` (а не весь `f`), поэтому даже при наличии поля оно бы не дошло до сборки промпта.

`prompts.Build` вызывается в **5 местах** `pkg/orchestrator/orchestrator.go`: planning (926), повтор planning/revise (1019), implementation (1096), **inline-review после implementation** (1115, `if s.HasAgent(AgentReview)`) и отдельный `runReviewAgent` (1151). Важно: review-фаза имеет **две** точки сборки промпта — inline-путь (1115) легко пропустить. Механизма «общего промпта для всех стейджей» сейчас нет.

## Description

Добавить в `flow.yaml` корневое поле `prompt` (структура `Flow`) с общим текстом — правилами проекта / общей информацией, — который afm **безусловно** включает в системный промпт **каждого стейджа и каждой фазы** (planning / implementation / review). Это аддитивный слой поверх существующих per-stage `prompt` и per-agent шаблонов; он не заменяет и не перекрывает их. Если поле пустое/отсутствует — поведение afm не меняется (обратная совместимость).

Пример:
```yaml
name: preprompt
prompt: |
  Общие правила проекта: пиши коммиты на русском; не меняй версию go в go.mod;
  предпочитай простые решения.
stages:
  - id: propose
    ...
```

## Tasks

1. `pkg/flow/flow.go` — добавить поле `Prompt string \`yaml:"prompt"\`` в структуру `Flow` (после `Description`). Логики композиции/merge flow для верхнеуровневых полей **нет** — `flow.ParseFile` единственный загрузчик (`cmd/afm/run.go:57`), «inline-refs» относятся к артефактам/входам, а не к flow; поэтому `Flow.Prompt` проходит напрямую без отдельной проводки.
2. `pkg/orchestrator/orchestrator.go` — добавить доставку корневого промпта до вызовов `Build`: поле `GlobalPrompt string` в `Options` (рекомендуется; альтернатива — поле в `Prompts`).
3. `cmd/afm/run.go` — при построении `orchestrator.Options` (~строки 166-171) передать `f.Prompt` в новое поле.
4. `pkg/prompts/builder.go` — добавить поле `GlobalPrompt string` в `Inputs`; в `Build` рендерить блок `<global_prompt>…</global_prompt>` (с `escapeTags`, как для `Stage.Prompt`) **безусловно**, для всех агентов. Зафиксировать место блока (предлагается — сразу после `</system_rules>`, до `<context>`/`<stage>`).
5. `pkg/orchestrator/orchestrator.go` — во всех **5** точках вызова `prompts.Build` (926, 1019, 1096, 1115, 1151) передать `GlobalPrompt: o.opts.GlobalPrompt`. Особое внимание — inline-review (1115) и `runReviewAgent` (1151): обе review-точки должны получить промпт.
6. Тесты: юнит-тест на `prompts.Build` с непустым `GlobalPrompt` (блок присутствует, экранирован) и с пустым (блока нет, поведение прежнее); тест парсинга `flow.ParseFile` с корневым `prompt`.

## Assumptions

- Корневой `prompt` применяется ко **всем фазам** (planning/implementation/review) **всех** стейджей — безусловно (следует из формулировки пользователя «применяется ко всем стейджам безусловно»). «Безусловно» здесь означает применение ко всем фазам и стейджам без возможности отключения на уровне стейджа — а не рендер пустого блока при пустом поле; при пустом `prompt` блок `<global_prompt>` не добавляется вовсе (см. следующий пункт и Acceptance Criteria).
- Состав **аддитивный**: корневой промпт не перекрывает `Stage.Prompt` и per-agent шаблон — они суммируются.
- Пустое/отсутствующее поле = статус-кво (обратная совместимость, никакой новой разметки в промпте).
- Блок рендерится как `<global_prompt>…</global_prompt>` с тем же экранированием (`escapeTags`), что и `<prompt>`; имя тега и размещение — на усмотрение реализации (предложено выше).
- Новых внешних зависимостей нет; изменения только в Go-коде afm.

## Scope

**In scope:**
- Новое поле `Flow.Prompt` (`prompt` в flow.yaml).
- Доставка значения через `orchestrator.Options` во все вызовы `prompts.Build`.
- Безусловный рендер общего промпта в `prompts.Build`.
- Юнит-тесты (builder + парсинг flow).

**Out of scope:**
- Доставка глобального пользовательского `~/.claude/CLAUDE.md` (отдельная задача; здесь — только поле в flow.yaml).
- Условное применение (per-stage отключение, переопределение) — промпт безусловный.
- Изменения в UI дашборда, proxy, executor.

## Acceptance Criteria

- [ ] В `flow.yaml` работает корневой `prompt:` (многострочный через `|`), парсится в `Flow.Prompt`.
- [ ] Текст корневого `prompt` попадает в системный промпт **каждого** стейджа и **каждой** фазы (planning/implementation/review) — проверяется тестом `prompts.Build` и инспекцией собранного промпта стейджа (`.afm/runs/.../<stage>/`).
- [ ] При отсутствии/пустом `prompt` собранный промпт идентичен текущему (обратная совместимость).
- [ ] Символы `<`/`>` в корневом промпте экранируются (не ломают XML-подобную разметку промпта).
- [ ] Линт (`golangci-lint`/`go vet`) и существующие тесты `pkg/flow`, `pkg/prompts`, `pkg/orchestrator` проходят.

## Stack

- **Frameworks:** Go (стандартная библиотека + существующие пакеты afm: `pkg/flow`, `pkg/prompts`, `pkg/orchestrator`, `cmd/afm`).
- **Libraries:** `gopkg.in/yaml.v3` (уже используется для парсинга flow).
- **Infrastructure:** — (без изменений).

## External Dependencies

Новых внешних зависимостей нет — задача расширяет уже используемый путь парсинга `flow.yaml`:

| Component | Usage file | Status |
|-----------|-----------|--------|
| yaml.v3 | `.goga/usages/cooks/yaml-v3.md` | existing |

## Risks and Constraints

- **Композиция flow (inline/refs):** убедиться, что корневой `prompt` корректно проходит через merge/inline-логику flow, если она затрагивает верхнеуровневые поля (не только `Stages`).
- **5 точек вызова `Build`:** легко пропустить одну — planning (926), повтор planning (1019), implementation (1096), inline-review (1115), `runReviewAgent` (1151). Чаще всего пропускают inline-review (1115), т.к. он вложен в блок implementation. Все 5 должны получать `GlobalPrompt`.
- **Совместимость:** поле опционально; пустое значение не должно менять промпт.
- **Не путать с `~/.claude/CLAUDE.md`:** задача — только про поле в flow.yaml, а не про глобальный пользовательский промпт.

## Scope Estimate

Одна небольшая задача, декомпозиция не требуется. Объём: 1 новое поле в `Flow` + 1 поле в `prompts.Inputs` + 1 поле в `Options` + проводка в `run.go` + правка 5 точек `Build` + 2 юнит-теста.

## Existing Architecture

Затронутые пакеты: `pkg/flow` (`Flow`, `ParseFile`), `pkg/prompts` (`Inputs`, `Build`), `pkg/orchestrator` (`Options`, 5 вызовов `Build`), `cmd/afm` (`run.go`). Интеграционных точек с другими подсистемами (proxy, executor, dashboard) нет.

## Notes

- Имя поля `prompt` (вариант A) выбрано пользователем: зеркалит per-stage `prompt` — верхний уровень = общий, на стадии = частный.
- `feature.yaml` переименован в `preprompt`; рабочая ветка — `claude-copy-global-prompt`. Несмотря на названия, задача — именно про корневой `prompt` в flow.yaml (уточнено пользователем в диалоге), а не про копирование `~/.claude/CLAUDE.md`.
- Верификация propose-стейджа: код подтверждает **5** точек `prompts.Build` (не 4, как в исходном черновике) — у review две точки (inline 1115 + standalone 1151). Merge/composition flow для верхнего уровня отсутствует (`ParseFile` — единственный загрузчик, `cmd/afm/run.go:57`); `Options` (`orchestrator.go:63`) и `Stages: f.Stages` (`run.go:168`) соответствуют описанию Current State.

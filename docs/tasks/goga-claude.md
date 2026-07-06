# Аудит миграции afm на архитектуру Goga

## Current State

Первичная миграция проекта `afm` (модуль `github.com/akopichin/afm`, Go 1.26) на архитектуру
Goga уже выполнена (коммит `dad0e7c feat(goga): архитектура cell/CODEMANIFEST для проекта afm`,
далее `e849295 feat: accept goga final`):

- `goga schema` возвращает **16 клеточек**: `cmd/afm`, `tools/setstatuslinter`, `assets`, `pkg/config`,
  `pkg/docker`, `pkg/executor`, `pkg/flow`, `pkg/mcp`, `pkg/orchestrator`, `pkg/progress`, `pkg/prompts`,
  `pkg/proxy`, `pkg/server`, `pkg/state`, `pkg/web`, `pkg/web/dashboard`. 15 из них 1:1 соответствуют
  директориям с `.go`-файлами проекта; `pkg/web/dashboard` — исключение: клеточка на статические ассеты
  (`app.js`, `index.html`, `style.css`, `favicon.svg`) без единого `.go`-файла, её код-привязка —
  директива `go:embed` в соседней клеточке `pkg/web/embed.go`.
- `goga lint` проходит чисто: `cells: 16 errors: 0` — структурных нарушений DSL (казинг ключей,
  обязательные секции, синтаксис Imports/Usages) нет.
- `.goga/config.yml` задаёт `language: golang`, `codemanifest.usages` (`conventions` +
  `golang`) и глобальную аннотацию `Use conventions for code writing rules and testing.`.
- `.goga/usages/cooks/` содержит practice-файлы под все 7 прямых зависимостей `go.mod`: `cobra.md`,
  `gorilla-websocket.md`, `rapid.md`, `x-sys-windows.md`, `x-term.md`, `x-tools-analysis.md`,
  `yaml-v3.md`.
- Граф `Imports` в `goga schema` соответствует реальным внутренним импортам Go-пакетов (проверено по
  выдаче `goga schema`, не предположено).

Чего структурный `goga lint` не проверяет и что **не было предметом** предыдущей задачи миграции:

1. **Смысловое соответствие** CODEMANIFEST фактическому коду — не устарели ли сигнатуры типов/методов/
   routines и их аннотации относительно реальной реализации (дрифт контракт↔код).
2. **Полнота cell-level `.usages/`** — практики для потребителей API клеточки. По `goga schema` у части
   клеточек (например, `cmd/afm`, `tools/setstatuslinter`, `pkg/progress`, `pkg/state`, `pkg/proxy`,
   `pkg/config`, `pkg/orchestrator`, `pkg/executor`, `pkg/prompts`, `pkg/flow`) поле `usages` в выдаче
   `goga schema` пустое — не проверено, оправдано ли отсутствие (нет внутренних потребителей) или это
   пробел (потребители есть, а `.usages/` не написаны).
3. **Тестовое покрытие** контрактов — не оценивалось, насколько существующие `*_test.go` покрывают
   типы/методы/routines, заявленные в CODEMANIFEST каждой клеточки.

## Description

Провести аудит уже выполненной миграции afm на goga — не переработку архитектуры клеточек (разбиение/
объединение не рассматривается), а верификацию, что существующие CODEMANIFEST-контракты корректно и
полно описывают код, что cell-level `.usages/` присутствуют там, где у клеточки есть внутренние
потребители, и что тестовое покрытие соответствует заявленным контрактам.

Аудит разбит на 3 подзадачи по слоям зависимостей графа `Imports` (bottom-up), чтобы каждая подзадача
была независимо проверяемой и не блокировалась незавершённым аудитом соседней группы.

## Scope

**In scope:**
- Сверка каждого CODEMANIFEST с фактической реализацией: соответствуют ли типы/методы/properties/
  routines в контракте реально экспортируемым сущностям Go-пакета; актуальны ли аннотации (в т.ч.
  специфика проекта — file-based dialog protocol в `pkg/mcp`/`pkg/orchestrator`, reverse proxy в
  `pkg/proxy`, Docker-режим в `pkg/docker` — должна быть узнаваема в соответствующих аннотациях)
- Проверка cell-level `.usages/` — для клеточек с внутренними потребителями (по графу `Imports` в
  `goga schema`) наличие и актуальность practice-файлов, описывающих facade клеточки
- Оценка тестового покрытия относительно заявленных в CODEMANIFEST контрактов (какие типы/методы не
  покрыты тестами, есть ли явные пробелы)
- Итоговый отчёт по каждой из 3 групп клеточек с перечнем расхождений (если найдены) и рекомендациями
  по их устранению (устранение — последующая работа, не эта задача)

**Out of scope:**
- Изменение архитектуры клеточек (разбиение/объединение существующих 16 клеточек)
- Изменение поведения существующего Go-кода — аудит не рефакторит и не исправляет код
- Создание/обновление project-level usage-файлов в `.goga/usages/cooks/` — все 7 зависимостей уже
  покрыты, новых внешних зависимостей задача не вводит
- Физическое исправление найденных расхождений (обновление CODEMANIFEST/.usages/тестов по итогам
  аудита) — это отдельная последующая задача через `goga-change`, если расхождения будут найдены

## Acceptance Criteria

- Для каждой из 16 клеточек зафиксирован явный вердикт: контракт соответствует коду / есть расхождения
  (с перечнем конкретных несоответствий: тип/метод/сигнатура/аннотация)
- Для каждой клеточки с непустым списком потребителей в графе `Imports` (`goga schema`) зафиксировано,
  существует ли актуальный cell-level `.usages/`, и если нет — отмечено как пробел
- Для каждой клеточки указана оценка тестового покрытия относительно контракта (полное / частичное с
  перечнем непокрытых сущностей / не оценивается — если тестов на пакет нет)
- Специфика проекта из `CLAUDE.md` (file-based dialog protocol, built-in reverse proxy, Docker mode)
  явно проверена на предмет отражения в аннотациях соответствующих клеточек (`pkg/mcp`,
  `pkg/orchestrator`, `pkg/proxy`, `pkg/docker`)
- Итог оформлен как 3 отчёта (по числу подзадач-групп, см. Scope Estimate), каждый с перечнем найденных
  расхождений или явным подтверждением их отсутствия

## Stack

- **Frameworks:** нет — аудит не пишет прикладной код
- **Libraries:** нет новых — используется существующий `goga` CLI (`goga schema`, `goga lint`,
  `goga config`) и ручной построчный разбор CODEMANIFEST в сопоставлении с Go-кодом
- **Infrastructure:** нет — только файловая система (`.goga/`, `pkg/*/CODEMANIFEST`)

## External Dependencies

Новых внешних зависимостей нет — все 7 прямых зависимостей `go.mod` уже покрыты existing-файлами:

| Component                | Usage file                                | Status   |
|---------------------------|--------------------------------------------|----------|
| cobra                     | `.goga/usages/cooks/cobra.md`              | existing |
| gorilla/websocket         | `.goga/usages/cooks/gorilla-websocket.md`  | existing |
| yaml.v3                   | `.goga/usages/cooks/yaml-v3.md`            | existing |
| golang.org/x/term         | `.goga/usages/cooks/x-term.md`             | existing |
| golang.org/x/tools        | `.goga/usages/cooks/x-tools-analysis.md`   | existing |
| golang.org/x/sys/windows  | `.goga/usages/cooks/x-sys-windows.md`      | existing |
| pgregory.net/rapid        | `.goga/usages/cooks/rapid.md`              | existing |

## Risks and Constraints

- `pkg/orchestrator` — самая крупная клеточка (11 файлов: bus, completion, context, errors, fsm, graph,
  orchestrator, plan_adopt, recovery, retry, session) — риск, что дрифт контракт↔код там сложнее
  обнаружить из-за объёма; требует более внимательного построчного разбора, чем листовые клеточки
- Пустое поле `usages: []` в `goga schema` для части клеточек может быть как нормой (нет внутренних
  потребителей), так и пробелом — при аудите для каждой такой клеточки нужно явно сверяться с графом
  `Imports` других клеточек, а не считать пустоту нормой по умолчанию
- Специфика проекта (file-based dialog protocol, reverse proxy, Docker privilege drop) описана в
  `CLAUDE.md` человеческим языком — при сверке с CODEMANIFEST-аннотациями есть риск формального
  совпадения слов без фактического сохранения смысла (нужно проверять по существу, не по ключевым
  словам)

## Scope Estimate

Три подзадачи по слоям графа зависимостей `Imports` (`goga schema`), выполняемые независимо
(разногласий по порядку выполнения нет — это не bottom-up проектирование новых контрактов, а
верификация уже существующих):

1. **Группа 1 — Leaf-клетки без зависимостей (9)**: `assets`, `tools/setstatuslinter`, `pkg/flow`,
   `pkg/mcp`, `pkg/proxy`, `pkg/progress`, `pkg/config`, `pkg/state`, `pkg/web/dashboard`
2. **Группа 2 — Клетки среднего уровня, зависят только от Группы 1 (4)**: `pkg/docker` (→ pkg/flow),
   `pkg/prompts` (→ pkg/flow), `pkg/executor` (→ pkg/progress), `pkg/web` (→ pkg/web/dashboard)
3. **Группа 3 — Верхнеуровневые клетки, зависят от Групп 1–2 (и друг от друга: `pkg/orchestrator` →
   `pkg/server` → `cmd/afm`) (3)**: `pkg/orchestrator` (→ config,
   executor, flow, mcp, prompts, state), `pkg/server` (→ executor, mcp, orchestrator, state, web),
   `cmd/afm` (→ assets, config, docker, flow, orchestrator, proxy, server, state)

Каждая группа — самостоятельная подзадача аудита (manifest↔код, cell-level usages, тестовое покрытие)
со своим отчётом.

## Existing Architecture

16 клеточек уже созданы (см. Current State) и проходят `goga lint` без структурных ошибок. Эта задача
не меняет существующую архитектуру клеточек — только верифицирует её соответствие коду.

## Notes

- Предыдущая версия этого файла описывала первичную миграцию проекта на goga (создание клеточек с
  нуля) — та задача выполнена (см. `git show dad0e7c:docs/tasks/goga-claude.md` для истории); текущий
  файл переиспользует то же имя (`docs/tasks/goga-claude.md`, привязано к ветке `goga-claude`) под новую
  задачу — аудит результата этой миграции. Решение перезаписать, а не создавать отдельный файл,
  подтверждено пользователем в ходе formulировния (диалог propose-стадии)
- Разбивка на 3 подзадачи по слоям зависимостей (а не по функциональному назначению) подтверждена
  пользователем
- Примеры кода/API в задаче не нужны — аудит не вводит новый публичный API (подтверждено пользователем)

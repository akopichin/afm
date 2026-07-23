# Аудит потери контекста между стейджами + два фикса

Дата: 2026-07-23

## Цель

afm прогоняет каждую стадию flow через отдельный процесс-агент (обычно
`claude`, но может быть любой сконфигурированный агент). Между стадиями нет
общей памяти — вся преемственность идёт через файлы на диске (`plan.md`,
артефакты, логи) и промпт, который собирает `pkg/prompts/builder.go`. Этот
документ фиксирует, где в этой файловой цепочке контекст может теряться, и
описывает дизайн двух конкретных фиксов, которые делаются по итогам аудита.

## Как контекст передаётся между стейджами (механизм)

Промпт стадии (`prompts.Build`, `pkg/prompts/builder.go:43-165`) — чистая
конкатенация секций: системный шаблон + output-contract → правила
интерактивного диалога (если применимо) → `<global_prompt>` → `<context>`
(`DependencyPlans` + `Artifacts`) → `<stage>` → `<prompt>` → `<plan>` /
`<previous_plan>` → `<feedback>` → `<RetryContext>` → `<example_output>`.
Токен-бюджет нигде не проверяется — все секции вставляются целиком.

`DependencyPlans` собирает `CollectDependencyPlans`
(`pkg/orchestrator/context.go:14-53`): для каждого `stage.DependsOn` читает
`plan.md` (или `execution_summary.md` для автономного трека) из директории
зависимой стадии — **не** лог реализации, не diff, не события. Следующая
стадия видит только *намерение* предыдущей, а не то, что она реально сделала.

`RetryContext` собирает `buildRetryContext`
(`pkg/orchestrator/retry.go:22-56`): при повторной попытке той же стадии
после ошибки читает последние 200 строк human-readable `.log`-файла стадии
и вставляет их как «previously completed actions».

## Найденные места потери контекста (полный список, для истории)

Ранжировано по impact.

1. **`buildRetryContext` читает уже урезанный `.log`, а не полный `.jsonl`.**
   `pkg/executor/executor.go` пишет два файла на каждую стадию: `<phase>.jsonl`
   (сырой stream-json, построчно, без изменений) и `<phase>.log`
   (human-readable, одна строка на действие через `contentToAction`, которая
   обрезает текст/Bash-команду до `executor.Config.TruncateOutput` символов —
   недавно добавленный конфиг, `pkg/config/config.go:38-40`,
   `executor.go:24,281,378`). `buildRetryContext` (`retry.go:35`) читает
   именно `.log`. Если оператор выставил маленький `truncate_output` (фича
   существует специально, чтобы не раздувать dashboard/event-feed), у
   ретраящейся стадии обрезается контекст «что я уже сделал» — то, ради чего
   retry-context вообще существует. **Это баг: фича для уменьшения шума в
   логе/дашборде случайно ломает совершенно другую фичу (retry-continuity).**
   → Фиксится в этой итерации, см. «Фикс #1».

2. **Плановая деградация тихая.** Если `plan.md`/`execution_summary.md`
   зависимой стадии отсутствует или пуст, `CollectDependencyPlans` пишет
   `"(plan not available)\n"` (`context.go:45`) и молча продолжает — ни
   ошибки, ни события, ни следа в дашборде. Оператор узнаёт об этом только
   если явно откроет промпт следующей стадии.
   → Фиксится в этой итерации, см. «Фикс #2» (в переписке с пользователем
   назывался «#3»).

3. **`CollectDependencyPlans` в принципе не прокидывает implementation-лог,
   diff или итог работы зависимой стадии** — только план. Если реализация
   разошлась с планом (обычное дело), следующая стадия об этом не узнает,
   если расхождение не оформлено как явный `Artifact`
   (`stage.Inputs`/`stage.Artifacts`, `context.go:68-132`). **Не фиксится
   сейчас** — это осознанное архитектурное ограничение (иначе в промпт
   пришлось бы тащить произвольно большие implementation-логи без
   token-бюджета), выходит за рамки текущей итерации.

4. **Нет настоящего resume LLM-сессии.** `runWithRetry`
   (`pkg/orchestrator/retry.go:140,150`) удаляет session-файл при retryable
   *и* при non-retryable ошибках — следующая попытка всегда новый процесс
   `claude`. Вся преемственность реконструируется из файлов + retry-context.
   Для non-interactive стадий `--resume`/`--session-id` не используется
   вообще (`runner_factory.go:25-54`). **Не фиксится сейчас** — это осознанный
   дизайн (комментарий в коде объясняет: retryable-ошибки типа 529 обычно
   вызваны раздутым контекстом сессии, поэтому сброс — не баг, а лечение).

5. **`plan.v{N}.md`-версионирование зависит от вызова `VersionPlan` до
   перезаписи** (`pkg/state/state.go:253`, `control_api.go:94`). Если
   какой-то путь кода в будущем начнёт писать новый `plan.md` без
   предварительного вызова `VersionPlan`, предыдущая версия плана будет
   потеряна безвозвратно без ошибки. **Не фиксится сейчас** — сегодня все
   существующие пути (`revise`) вызывают `VersionPlan` корректно; это скорее
   инвариант, который стоит держать в уме при будущих правках, чем текущий
   баг.

## Фикс #1: retry-context читает `.jsonl`, а не `.log`

### Проблема

`buildRetryContext(stageDir, phase)` строит текст из последних 200 строк
`<phase>.log`. Эти строки формирует `progress.Logger.LogAction` из `detail`,
который уже прошёл через `contentToAction(c, e.cfg.TruncateOutput)` — то есть
обрезан, если `truncate_output` > 0 и меньше длины реального вывода. Полный,
неурезанный текст того же действия физически лежит рядом, в `<phase>.jsonl`
(построчный stream-json, пишется без обрезки, `executor.go:259,366`), но
`buildRetryContext` его не читает.

### Дизайн

Truncation для `.log`/`OnAction` (дашборд, event feed) — сознательный
компромисс UX, трогать не нужно: он существует именно чтобы не раздувать
лог и event-стрим. Проблема только в том, что `buildRetryContext` берёт
данные не из того файла. Решение — не трогать truncation вообще, а
переключить источник retry-context на `.jsonl` и рендерить действия без
лимита (`limit=0` в терминах `contentToAction`).

1. В `pkg/executor` (новый небольшой файл `retry_context.go` рядом с
   `executor.go`) — экспортированная функция:

   ```go
   // RenderActions parses a stage's raw stream-json log (<phase>.jsonl) and
   // returns one line per tool/text action, without truncation. Used to build
   // retry context — the log line detail there IS pre-truncated by
   // Config.TruncateOutput, but the retry continuation prompt must see the
   // full action, not the log's abbreviated view of it.
   func RenderActions(jsonlPath string) []string
   ```

   Реализация повторяет уже существующий паттерн чтения jsonl из
   `WrittenFiles` (`executor.go:308-337`, тот же `bufio.Scanner` с увеличенным
   буфером до 16MB — строки stream-json с полным содержимым Write легко
   превышают дефолтный лимит сканера). Для каждой распарсенной строки вызывает
   уже существующие непубличные `parseStreamEvent` и `contentToAction(c, 0)`
   (лимит 0 = без обрезки — этот кейс уже поддержан сигнатурой функции) и
   форматирует `"<tool>  <detail>"` (без временной метки — `.jsonl` не несёт
   wall-clock для отдельного события, а точная метка ретраю не нужна, важен
   сам факт и содержимое действия).

2. В `pkg/orchestrator/retry.go`, `buildRetryContext`: вместо
   `os.ReadFile(filepath.Join(stageDir, logName))` + `strings.Split` —
   `executor.RenderActions(filepath.Join(stageDir, jsonlName))`, где
   `jsonlName` — то же имя, что и раньше, но с расширением `.jsonl` вместо
   `.log` (`"planning.jsonl"`, `"review.jsonl"`, `"autonomous.jsonl"`,
   `"implementation.jsonl"`). Дальше как было: последние 200 строк, обёрнутые
   в тот же заголовок `## Previously completed actions (resuming after
   interruption)`.

3. Поведение при отсутствии/ошибке чтения `.jsonl` не меняется: пустой список
   действий → пустой `RetryContext`, как раньше при отсутствующем `.log`.

### Тестирование

Юнит-тест в `pkg/executor` на `RenderActions`: сконструировать `.jsonl` с
одной строкой stream-json, где текстовый блок длиннее любого разумного
`TruncateOutput`, убедиться, что возвращённая строка содержит текст целиком
(в отличие от того, что дал бы `contentToAction(c, 100)`).

Юнит/интеграционный тест в `pkg/orchestrator` на `buildRetryContext`:
записать `<phase>.jsonl` с длинным Bash-выводом, вызвать
`buildRetryContext`, убедиться что результат содержит вывод целиком, не
обрезанным до 100/80 символов (регрессионный тест именно на баг — раньше
такого теста не было).

## Фикс #2 (в чате — «#3»): явное предупреждение при отсутствующем плане зависимости

### Проблема

`CollectDependencyPlans` при отсутствующем/пустом `plan.md`/
`execution_summary.md` зависимой стадии молча пишет в промпт
`"(plan not available)\n"` и идёт дальше. Ни оператор в дашборде, ни лог
ничего об этом не узнают — единственный способ заметить деградацию контекста
конкретной стадии — вручную читать её промпт.

### Дизайн

Новый тип события в event-стриме дашборда (транзиентная UI-шина, не
durable event log — тот же класс, что уже используют «incomplete work,
retrying» в `retry.go:123-127` через `EventStageStatusChanged`; здесь заведён
отдельный тип, а не переиспользован статус-евент, потому что это не смена
статуса стадии, а предупреждение о деградации контекста — смешивать
семантику нежелательно).

1. `pkg/orchestrator/bus.go`: новый `EventType`:
   ```go
   EventContextWarning EventType = "context_warning"
   ```

2. `pkg/orchestrator/context.go`: `CollectDependencyPlans` получает
   дополнительный параметр `warn func(depID, msg string)`. Когда план
   зависимости отсутствует/пуст, помимо `"(plan not available)"` в тексте
   промпта, вызывает `warn(depID, "dependency stage plan is missing or
   empty — downstream stage sees no context from it")`. Если `warn == nil`,
   пропускается (используется в местах, где вызов ещё не подключён —
   но по плану подключается везде).

3. Все 5 call site в `pkg/orchestrator/agents.go`
   (`runPlanningAgent`, `runPlanningWithFeedback`, `runImplementationAgent`,
   `runReviewAgent`, `runAutonomousAgent`) передают:
   ```go
   depPlans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages, func(depID, msg string) {
       o.ui.Publish(Event{Type: EventContextWarning, StageID: s.ID, Data: fmt.Sprintf("%s: %s", depID, msg)})
   })
   ```

4. Фронтенд, `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`,
   `toFeedLine`: новый `case 'context_warning'`, аналогично `retry_exhausted`
   по стилю — `msgClass = 'feed-msg warning'`, текст = `` `context warning: ${stringify(data)}` ``.

5. Стили: `pkg/web/dashboard/skins/base/event-feed.css` (и синхронная копия
   `pkg/web/dashboard/public/skins/base/event-feed.css` — оба файла сегодня
   идентичны и правятся вместе, отдельного шага сборки, который бы их
   синхронизировал, в репозитории нет) — добавить рядом с `.feed-msg.error`:
   ```css
   .feed-msg.warning { color: var(--amber); }
   ```
   (`--amber` уже используется для похожего по серьёзности
   `.supervisor-dot.standard`).

### Тестирование

Юнит-тест в `pkg/orchestrator` на `CollectDependencyPlans`: зависимость без
`plan.md` → проверить, что `warn` вызван с ожидаемым `depID` и что
возвращённый текст промпта по-прежнему содержит `"(plan not available)"`
(текст промпта не меняется, только добавляется сигнал наружу).

Интеграционный тест (или расширение существующего) на то, что
`EventContextWarning` реально публикуется в `o.ui` при прогоне стадии с
недостающей зависимостью — по образцу существующих проверок событий в
`integration_retry_test.go`.

## Вне рамок текущей итерации

Находки #3 (implementation-лог зависимости не прокидывается), #4 (нет
настоящего resume сессии) и #5 (риск потери версии плана при будущих
правках без `VersionPlan`) зафиксированы выше как известные ограничения.
Осознанное решение: не трогать их сейчас — #3 требует продуманного
token-бюджета для промпта (отдельная фича), #4 — осознанный дизайн (см.
комментарий в коде о причине сброса сессии), #5 — не баг, а инвариант на
будущее без текущих нарушителей.

## Файлы, которые меняются

- `pkg/executor/retry_context.go` (новый) — `RenderActions`
- `pkg/executor/retry_context_test.go` (новый)
- `pkg/orchestrator/retry.go` — `buildRetryContext` источник данных
- `pkg/orchestrator/bus.go` — `EventContextWarning`
- `pkg/orchestrator/context.go` — `CollectDependencyPlans` сигнатура + вызов `warn`
- `pkg/orchestrator/agents.go` — 5 call site передают `warn` callback
- `pkg/orchestrator/context_test.go` — тест на `warn`
- `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx` — `case 'context_warning'`
- `pkg/web/dashboard/skins/base/event-feed.css` и `pkg/web/dashboard/public/skins/base/event-feed.css` — стиль `.feed-msg.warning`

## Acceptance Criteria

- `buildRetryContext` возвращает полный (неурезанный `truncate_output`) текст
  предыдущих действий стадии независимо от значения `executor.truncate_output`.
- При отсутствующем/пустом плане зависимости в event-фиде появляется
  видимое предупреждение с указанием, какая именно зависимость деградировала.
- `go build ./...`, `go vet ./...`, существующий линтер и все текущие тесты
  проходят; добавлены новые тесты на оба фикса.
- Дашборд рендерит `context_warning` отдельным, визуально отличимым от
  обычного статуса стилем (не как безымянный `default: msg = event.type`).

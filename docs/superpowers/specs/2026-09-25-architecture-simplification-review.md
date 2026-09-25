# Упрощение архитектуры AFM — 2026-09-25

Исходный checkout: `9a7f4dd3468fbdf73bed7f289f604b98c81700bb`, чистая рабочая
копия. Среда: Go 1.26.6, darwin/arm64. Разбор охватил основные пути CLI,
оркестратора, хранения состояния, HTTP-сервера и dashboard. Все пять
предложенных направлений реализованы.

Основная проблема — несколько реализаций одного решения о жизненном цикле
стадии. Scheduling, retry, recovery и Continue повторяли выбор исполнителя,
подготовку плана и проверки. CLI смешивал подготовку всех подсистем в одном
`RunE`; React-компоненты одновременно собирали интерфейс и управляли запросами.

## Выполненные изменения

| Направление | Новый владелец ответственности | Что сохраняется |
|---|---|---|
| Запуск и восстановление | [scheduling.go](../../../pkg/orchestrator/scheduling.go): `activatePrePlannedStage`, `startReadyStage`, `executionKind`; [recovery.go](../../../pkg/orchestrator/recovery.go): один startup-dispatch, `recoverPlan`, `resumeExecutionStage` | Durable переходы, CAS, run-context, lease, review hold, verify и различия before-hook между новым запуском и восстановлением |
| Сборка запуска | [run.go](../../../cmd/afm/run.go): preflight и владение ресурсами; [run_environment.go](../../../cmd/afm/run_environment.go), [run_lifecycle.go](../../../cmd/afm/run_lifecycle.go), [run_dashboard.go](../../../cmd/afm/run_dashboard.go), [run_workspace.go](../../../cmd/afm/run_workspace.go) | Приоритет конфигурации, индексы Docker-транспорта, удаление секретных переменных до запуска агентов, flush/cleanup на выходе |
| Конфигурация сервера | [server.go](../../../pkg/server/server.go): `Config.Stages map[string]StageConfig`; CLI собирает все поля за один проход | HTTP JSON-контракт, порядок стадий и кнопок; `StageView` остаётся runtime-представлением |
| Состояние React | [useSelectedStage](../../../pkg/web/dashboard/src/hooks/use-selected-stage/use-selected-stage.ts), [useReviewNoteCount](../../../pkg/web/dashboard/src/hooks/use-review-note-count/use-review-note-count.ts), [useFilePreview](../../../pkg/web/dashboard/src/components/file-browser/use-file-preview.ts) | Workspace reducer остаётся владельцем режима/attention; ручной выбор завершённой стадии сохраняется; Reload не стирает содержимое при 304/ошибке |
| Уведомления | [notices.go](../../../pkg/orchestrator/notices.go): `publishNotice` публикует один payload в UI и `notices.jsonl` | Best-effort семантика; FSM и critical completion используют прежние отдельные пути |

`Interactive` нормализуется на копии slice до `orchestrator.New`: graph и
Options больше не зависят от изменения общего массива после создания
оркестратора. При ошибке запуска HTTP-сервера закрывается и переданный workspace.
Исторические комментарии в затронутых местах заменены описанием текущих
контрактов, включая противоречивый комментарий к `onAgentCompleted`.

## Исправленные расхождения

- Retry с существующим `plan.md` раньше мог запустить implementation до
  завершения зависимости. Теперь стадия остаётся `pending`; `eager_planning`
  разрешает раннее планирование, но не раннее выполнение. Регрессия проверяет
  preplanned/planned/eager-planning × running/failed dependency и ровно один
  запуск после завершения зависимости. Все шесть случаев падали до исправления.
- Interactive-стадия без отдельного плана использует description одинаково
  при startup и при активации после зависимости. Раньше второй путь пытался
  копировать несуществующий источник плана. Проверены оба сценария реальным
  subprocess с локальным тестовым агентом.
- Reload использует поколение запроса: ответ не может устареть, затем стать
  допустимым снова только потому, что пользователь вернулся к тому же файлу.
  Проверены переходы A → B → A и FILE → DIFF → FILE с незавершённым Reload.

[Тесты подготовки запуска](../../../cmd/afm/run_preparation_test.go) дополнительно
проверяют сохранность parsed flow, индексы объединённых хуков, очистку транспорта,
fail-fast при отсутствии секрета и отсутствие чтения secrets.env для хуков без env.

## Проверки

- Dashboard: 792 теста, TypeScript typecheck и production build — успешно.
- Целевые Go-тесты CLI, server, recovery/pause/verify/hooks/notices — успешно с `-race`.
- `go test -race -p 1 ./...` — успешно для всего основного Go-модуля.
- `go build -o bin/afm ./cmd/afm` и SDK `go test -race ./...` с
  `AFM_SDK_TEST_BINARY`, указывающим на этот бинарник, — успешно, включая интеграцию.
- `./bin/golangci-lint run ./...` и `GOOS=linux ./bin/golangci-lint run ./...`
  — по 0 замечаний; `go run ./tools/setstatuslinter ./pkg/...` — успешно.
- Linux-компиляция тестов `pkg/server/workspace` — успешно.
- `git diff --check` и ссылки этого документа — без ошибок.

Runtime-проверки выполняются на macOS. Linux-проверка — статический анализ;
Docker end-to-end с настоящими AI-агентами не выполнялся.

`state.Store`, FSM, `concurrency.Manager`, `stagefiles` и `memorypipeline`
сохранили свои границы ответственности. Формат event log, snapshot и HTTP API
не изменён. Статический Go-конфиг сервера теперь принимает `Stages` вместо
пяти отдельных map.

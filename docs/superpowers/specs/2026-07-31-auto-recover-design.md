# auto_recover: автоматический retry failed-стейджей при старте рана

**Дата:** 2026-07-31
**Статус:** design (одобрен к реализации)

## Цель

Сценарий: пользователь прибил контейнер/процесс `afm run` во время выполнения. Стейдж, на котором это случилось, падает с `reason: context canceled`; все зависящие от него стейджи каскадом становятся `failed` через `blocked_by_dep`. `afm run` того же флоу резюмит существующий (ещё не `AllDone()`) run (`cmd/afm/run.go:resolveRun`), но `startPlanningForPending` намеренно не трогает терминальные `failed`-стейджи — сейчас нужно вручную прогнать `afm retry <id>` на каждый упавший стейдж по порядку.

Нужен конфиг-флаг `auto_recover` (дефолт `true`): при старте/резюме рана все стейджи в статусе `failed` автоматически переводятся в `pending` и дальше подхватываются штатным depends_on-планировщиком — без ручного retry.

Ретраятся **все** `failed`-стейджи без разбора причины (`context canceled` из-за прерывания и настоящий баг в скрипте/verify обрабатываются одинаково) — простое правило вместо эвристики по строке `reason`, которая была бы хрупкой. Лимита попыток и CLI-флага override не вводим — `afm run` запускается пользователем вручную каждый раз, отдельный троттлинг не нужен; если стейдж падает по реальной причине, он просто упадёт снова тем же образом, что видно в логе.

## Почему порядок depends_on не требует отдельного кода

Перевод `failed → pending` — это просто смена статуса, а не немедленный запуск. Дальше действует уже существующий планировщик: `depsDone()` не даст стейджу стартовать, пока его зависимости не в `done` (та же проверка, что гейтит вообще любой ещё не стартовавший `pending`-стейдж). Поэтому сброс всех `failed`-стейджей в `pending` одним проходом (в любом порядке) автоматически даёт правильную последовательность выполнения — ровно как если бы эти стейджи никогда не запускались.

## Изменения

### `pkg/config/config.go`

- Новое top-level поле:
  ```go
  type Config struct {
      ...
      AutoRecover *bool `yaml:"auto_recover"`
  }
  ```
- `Default()` явно ставит `true` (по образцу `ServerConfig.OpenBrowser`/`Port`).
- Хелпер:
  ```go
  // IsAutoRecover reports whether failed stages should be auto-retried on
  // run start. Defaults to true; only an explicit `auto_recover: false` disables it.
  func (c Config) IsAutoRecover() bool {
      return c.AutoRecover == nil || *c.AutoRecover
  }
  ```
- `mergeFile`: если `overlay.AutoRecover != nil`, переносим в `dst.AutoRecover` (по образцу `Docker.Enabled`/`Server.OpenBrowser`).

### `pkg/orchestrator/recovery.go`

Новая функция, вызывается первой строкой `startPlanningForPending` (единственная точка входа при старте `Run()`, покрывает и свежий, и резюмируемый ран):

```go
// autoRecoverFailedStages resets every stage currently in StatusFailed back to
// Pending when auto_recover is enabled (default), so a run interrupted by a
// killed process/container resumes automatically instead of requiring manual
// `afm retry` on each failed stage. Order doesn't matter: the reset stages
// re-enter the normal Pending flow below, which already gates on depsDone().
func (o *Orchestrator) autoRecoverFailedStages() {
    if !o.opts.Config.IsAutoRecover() {
        return
    }
    for _, s := range o.opts.Stages {
        if o.opts.Store.Get(s.ID) != state.StatusFailed {
            continue
        }
        if s.Interactive {
            clearInteractiveSessions(filepath.Join(o.opts.RunDir, s.ID))
        }
        if _, ok := o.Trigger(s.ID, EvManualRetry, GuardCtx{}, "auto_recover"); ok {
            log.Printf("auto_recover: stage %q failed -> pending", s.ID)
        }
    }
}
```

Вызов добавляется первой строкой `startPlanningForPending` (до основного цикла по `o.opts.Stages`):

```go
func (o *Orchestrator) startPlanningForPending(ctx context.Context) {
    o.autoRecoverFailedStages()
    for _, s := range o.opts.Stages {
        ...
```

Используется уже существующий переход `EvManualRetry: Failed → Pending` (тот же, которым пользуется ручной retry из дашборда/`scheduling.go:retryStage`) — новых правил в FSM не требуется. `clearInteractiveSessions` — уже существующий helper (`scheduling.go`), переиспользуется как есть, чтобы у интерактивных стейджей не оставалось протухшего `<phase>.session.json` (иначе retry падает с "No conversation found").

Логирование — через `log.Printf`, по образцу существующей практики в пакете (`supervisor.go`: `"supervisor: stage %s transient error..."`). У `pkg/orchestrator` нет своего пути прямого вывода в терминал CLI — `log.Printf` пишет в stderr и виден в `afm run` foreground-режиме, что и нужно для видимости пользователю. Отдельный UI-notice (`notices.jsonl`) не заводим — сам переход публикуется в UI-шину через `o.Trigger` (`EventStageStatusChanged`), дашборд увидит стейдж, ушедший из `failed`, тем же механизмом, что и любой другой переход статуса.

### `config.example.yaml` / README

- Добавить `auto_recover: true` в пример конфига рядом с другими top-level флагами, с комментарием про поведение по умолчанию.

## Тестирование

- **`pkg/config`**: `TestIsAutoRecover` — nil/true/false; `Default()` возвращает `true`; `mergeFile` переносит явный `false` из project-level конфига поверх global-default `true`.
- **`pkg/orchestrator`** (интеграционные, `recovery_test.go`):
  - Стейдж A (без deps, статус `failed`) + стейдж B (`depends_on: [A]`, статус `failed`, reason `blocked_by_dep`) → после `startPlanningForPending` с `AutoRecover` включённым (дефолт) оба становятся `pending`, затем A стартует первым; B стартует только после того как A дойдёт до `done` (не одновременно с A).
  - Тот же сетап с явным `auto_recover: false` в конфиге → A и B остаются `failed` нетронутыми (регрессия текущего поведения).
  - Интерактивный стейдж в `failed` с протухшим `planning.session.json` → после auto-recover файл сессии удалён (не мешает следующему запуску).
  - Стейдж в `done`/`awaiting_approval` не трогается auto-recover (только `failed` — terminal-but-recoverable статус).

## Вне охвата

- CLI-флаг `--no-auto-recover` — по решению не добавляем, только конфиг.
- Фильтрация по причине падения (`reason`) — ретраим все `failed`-стейджи без разбора.
- Лимит числа авто-ретраев / backoff между повторными запусками `afm run` — не нужен, `afm run` запускается пользователем вручную.

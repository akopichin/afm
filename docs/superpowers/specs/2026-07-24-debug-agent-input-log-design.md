# Дизайн: `afm --debug` — лог входа агента (промпта)

**Дата:** 2026-07-24
**Статус:** согласован, готов к плану

## Цель

Флаг `afm --debug`, при котором afm пишет **точный промпт, уходящий в агента**
(stdin), с временными метками и привязкой к стадии/фазе, в отдельные debug-логи —
чтобы можно было сопоставить вход агента с его выходом (`<phase>.jsonl`/`.log`) и
событиями (`events.jsonl`). Сейчас промпт нигде не сохраняется (собирается
`prompts.Build` в памяти и скармливается в stdin — `executor.run`,
`cmd.Stdin = strings.NewReader(prompt)`).

## Флаг и активация

- Persistent-флаг `--debug` (bool) на корневой команде (рядом с `--dir`), плюс
  переменная окружения `AFM_DEBUG` (`1`/`true`). Приоритет: **флаг > env**.
  Резолвится в `PersistentPreRunE` (`cmd/afm/main.go`) в пакетную переменную
  `debugEnabled` (по аналогии с `rootDir`).
- По умолчанию **выключено**.
- Docker: launcher прокидывает `-e AFM_DEBUG=1` в контейнер, когда режим включён,
  чтобы re-exec внутри контейнера тоже логировал (значение, не bare-форма — это не
  секрет).

## Что и в каком формате логируется

На каждом запуске агента (planning / implementation / review / autonomous /
supervisor / reprompt — всё идёт через `executor.run`) пишется одна запись:

```
=== [2026-07-24T09:00:00.123Z] stage=<id> phase=<phase> cmd=<command> session=<id> resume=<bool> ===
--- BEGIN PROMPT ---
<полный текст промпта, ушедшего в stdin>
--- END PROMPT ---

```

- Время — RFC3339 (UTC) в момент запуска.
- `phase` ∈ {planning, implementation, review, autonomous, supervisor, planning-reprompt} —
  берётся из basename `logFile` (`planning.log`→`planning`); для `RunJSONQuery`
  (супервизор, без logFile) — литерал `supervisor`.
- `stage` — `filepath.Base(cfg.StageDir)`; пусто, если `StageDir` не задан
  (напр. общий supervisor-runner) — тогда стадия видна из самого промпта.
- Секреты не пишем: только имя команды и (неявно) claude-флаги в процессе;
  env/токены в лог не попадают.

## Куда пишем (оба варианта)

1. **Единый хроно-лог:** `<runDir>/debug.log` — append по всем стадиям/фазам,
   хронологически (для сопоставления по времени со всем прогоном).
2. **По-стейджно:** `<runDir>/<stage>/<phase>.prompt.log` — append, рядом с
   `<phase>.jsonl`; ретраи/reprompt одной фазы накапливаются. Пишется только когда
   `cfg.StageDir` задан (для supervisor-runner без StageDir — только в `debug.log`).

Обе записи идентичны по содержимому.

## Точка врезки и проводка

Единственная точка — `executor.run(ctx, prompt, ...)`: через неё проходят все
агентские вызовы. План проводки:

- `executor.Config`: добавить `Debug bool` и `RunDir string`.
- `executor.run` получает новый параметр `phase string` (3 call-site:
  `RunPlanning`→phase из logFile, `RunAgent`→phase из logFile, `RunJSONQuery`→
  `"supervisor"`). В начале `run`, если `cfg.Debug`, вызвать хелпер
  `logAgentInput(phase, prompt)` до старта процесса.
- `logAgentInput` собирает запись (заголовок+промпт) и **best-effort** дописывает
  её в `<RunDir>/debug.log` и (если `StageDir != ""`) в
  `<StageDir>/<phase>.prompt.log`. Ошибки записи не валят run — пишем
  предупреждение в stderr и продолжаем (debug — вспомогательный тракт).
- `orchestrator.Options`: добавить `Debug bool` (прокинуть из `cmd/afm/run.go`).
  `runner_factory.go` (3 вызова `executor.New`) проставляет
  `Debug: o.opts.Debug`, `RunDir: o.opts.RunDir`.
- `cmd/afm/run.go`: `supervisorRunner := executor.New(...)` тоже получает
  `Debug` + `RunDir`; `orchestrator.Options{... Debug: debugEnabled}`.
- Docker (`pkg/docker/launcher.go`): добавить `-e AFM_DEBUG=1` в аргументы
  `docker run`, когда debug включён.

## Безопасность / YAGNI

- Off by default; включается только явно.
- `debug.log`/`*.prompt.log` живут под `.afm/runs/` (не коммитятся). Промпты
  бывают большими и содержат проектный контекст — предупредить в доке.
- Логируем только **вход**; выход агента уже есть в `<phase>.jsonl`/`.log`.
- Не логируем env/секреты.

## Тестирование

- Executor-юнит-тест: при `Config.Debug=true` `run` создаёт запись (заголовок +
  `BEGIN/END PROMPT` + текст промпта) в `<RunDir>/debug.log` и
  `<StageDir>/<phase>.prompt.log`; при `Debug=false` файлы не создаются. Проверить
  append (два вызова → две записи). Проверить, что при пустом `StageDir` пишется
  только `debug.log` и `run` не падает.
- Флаг/env: `--debug` и `AFM_DEBUG=1` включают режим; флаг важнее env (юнит на
  резолвер, по аналогии с `resolveRootDir`).
- Существующие тесты executor/orchestrator/docker остаются зелёными.

## Файлы

- `cmd/afm/main.go` — persistent `--debug` + резолв (`debugEnabled`, env `AFM_DEBUG`).
- `cmd/afm/run.go` — проброс `Debug` в `orchestrator.Options` и в `supervisorRunner`.
- `pkg/executor/executor.go` — поля `Debug`/`RunDir`, параметр `phase` в `run`,
  хелпер `logAgentInput`, вызов из `run`.
- `pkg/orchestrator/orchestrator.go` (`Options.Debug`) + `runner_factory.go`
  (проброс в 3 `executor.New`).
- `pkg/docker/launcher.go` — `-e AFM_DEBUG=1`.
- Доки: README (флаг `--debug`), `config.example.yaml`/`CLAUDE.md` при необходимости.
- Тесты: `pkg/executor/*_test.go`, `cmd/afm` (резолвер флага).

## Non-goals

- Не логируем выход агента (уже есть). Не добавляем ротацию/размерные лимиты
  debug-лога (YAGNI; включается вручную на время отладки). Не редактируем
  промпт/не маскируем его содержимое (это дев-инструмент; ответственность на
  включающем).

## Открытые вопросы

Нет.

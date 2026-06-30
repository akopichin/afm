# Fix: интерактивные стадии flowManager (afm bug-1 + bug-2)

- **Дата:** 2026-06-29
- **Статус:** Approved (дизайн согласован)
- **Репо:** `github.com/akopichin/afm`
- **Связанные материалы:** `tmp/afm.bug-1.md`, `tmp/afm.bug-2.md`

## Контекст

Два связанных дефекта ломают интерактивные стадии `flowmanager`. Баг #1 не даёт
им вообще запуститься на актуальном Claude Code; баг #2 роняет диалог, когда
агент пишет файл вопроса не туда. Оба наблюдались на run
`Goga feature-20260628-023623` (Claude Code 2.1.153 / 2.1.193).

Все корневые причины верифицированы по текущему коду.

## Корневые причины (верифицировано)

### Баг #1 — три подпункта

- **#1.** Дефолтные `extra_args` для `claude` не содержат `--verbose`. Claude
  Code при `--print --output-format stream-json` на ряде версий падает с
  `When using --print, --output-format=stream-json requires --verbose` (exit 1)
  ещё до создания conversation.
  - Дублировано в **двух** местах: `pkg/executor/executor.go:52-54` (дефолт для
    не-интерактивных стадий) и `pkg/orchestrator/orchestrator.go:239`
    (`requiredArgs` для интерактивных). Оба списка без `--verbose`.
  - Поведение версионно-зависимое: на 2.1.193 жёсткая ошибка не воспроизводится,
    `--verbose` описан как «Override verbose mode setting from config». Но
    `--verbose` безопасен везде и даёт executor'у полный stream-json с
    `tool_use`-событиями, на которые он рассчитывает.
- **#2.** `pkg/executor/executor.go:370` — `cmd.Stderr = io.Discard`. Реальная
  диагностика claude (включая ту самую «requires --verbose») теряется; в логе
  остаётся голый `exit status 1`.
- **#3.** `pkg/orchestrator/session.go:33-38` — `loadOrCreateSession` пишет
  UUID в `<phase>.session.json` **до** запуска claude. При падении первой
  попытки (всегда, пока не пофиксен #1) файл содержит ID для несуществующего
  conversation. Retry использует `--resume <phantom>` →
  `No conversation found with session ID: ...` → бесконечный цикл.
  - В `pkg/orchestrator/retry.go` удаление session-файла **уже есть, но только
    для retryable-ошибок** (строка 110, коммит `3cc3a0c`). Non-retryable-ветка
    (строки 100-104) файл не чистит → фантом выживает.

### Баг #2 — нарушение протокола file-based dialog

- `pkg/prompts/builder.go:44-58` — контракт указывает агенту путь через
  `$FLOWMANAGER_STAGE_DIR`, но переменная упомянута один раз вверху; к моменту
  записи `question.json` контекст агента смещается, и LLM может придумать путь
  вроде `.flowManager/stages/<stage>/...`.
- Поле `prompts.Inputs.StageDir` (`builder.go:28`) существует, но **пустое и
  нигде не используется** в `Build()` — то есть буквальный абсолютный путь
  агенту не передаётся.
- Оркестратор (`orchestrator.go:337`, `handlers.go:282-284`) и poller ищут
  вопросы и пишут ответы **только** в `runs/<run>/<stage>/`. Если агент написал
  вопрос в другое дерево каталогов — вопрос и ответ расходятся, стадия зависает
  навсегда (худший режим отказа — невидимое зависание).

## Цели / Non-goals

**Цели**
1. Интерактивные стадии запускаются на актуальном Claude Code (#1.1).
2. Диагностика claude попадает в логи (#1.2) — отладка из часовой задачи в
   30-секундную.
3. Retry интерактивной стадии всегда стартует чистую сессию (#1.3).
4. Агенту передаётся однозначный путь; нарушение контракта детектируется и
   фейлит стадию с понятной причиной, а не зависает (#2).

**Non-goals**
- Capture реального `session_id` из stream и persist только после подтверждения
  conversation (слишком большой рефактор ради #3).
- Magic fallback: перенос `question.json` из «неправильного» места в stageDir
  (сознательно отвергнуто в пользу строгого контракта + fail-fast).
- Правка навыков `goga-propose` / `goga-task-by-proposing` — они живут вне
  репо (`~/.claude/skills/`, pipx-пакет `goga`); отмечены как параллельная
  рекомендация, в этот план не входят.
- Детект версии `claude` и адаптация флагов под неё.

## Дизайн

### A1. `--verbose` + устранение дублирования

- В `pkg/executor/executor.go` ввести общую константу/хелпер:
  `DefaultClaudeArgs = ["--print","--output-format","stream-json","--verbose","--dangerously-skip-permissions"]`
  (или функция `DefaultClaudeArgs() []string`).
- `executor.New` использует её как дефолт `ExtraArgs`, когда пользователь не
  задал свои (строка 52-54).
- `runnerFor` (`orchestrator.go:239`) использует её как `requiredArgs`,
  препенд к `config.Client.ExtraArgs`.
- Дедуп: если пользовательские `config.Client.ExtraArgs` уже содержат
  `--verbose`, не добавлять второй раз (простая проверка по `==`).
- Обновить комментарий дефолта в `config.example.yaml:19` (+ `--verbose`).

### A2. stderr → файл

- В `executor.run()` принимать путь/writer для stderr (доп. параметр или поле
  `Config`; предпочитаем — параметр в `run`, чтобы не расширять `Config`).
- `RunPlanning` / `RunAgent` считают
  `stderrFile = strings.TrimSuffix(logFile, ".log") + ".stderr.log"`
  (рядом с `.log`/`.jsonl`), открывают `O_CREATE|O_APPEND|O_WRONLY`, передают
  в `run()`.
- Убираем `cmd.Stderr = io.Discard` → направляем в открытый файл.
- При ошибке открытия stderr-файла — fallback на `io.Discard`, чтобы не валить
  запуск (stderr — диагностический, некритичный).

### A3. Фантомный session_id

- В `retry.go` non-retryable-ветка (строки 100-104) перед `EvFail` добавить
  `_ = os.Remove(sessionFile(stageDir, phase))` — зеркально к retryable-ветке
  (строка 110).
- Это переиспользует уже существующий `sessionFile()` и паттерн из retryable-ветки
  — новой логики почти нет. Все интерактивные пути обёрнуты в `runWithRetry`
  (`runPlanningAgent`, `runPlanningWithFeedback`, `runImplementationAgent`,
  `runReviewAgent`, рестарты из `onUserAnswered`), поэтому этой правки достаточно
  для основного сценария.
- **Страховка (belt-and-suspenders)** в `onManualRetry` (`orchestrator.go:582+`):
  перед перезапуском интерактивной стадии удалять все её `<phase>.session.json`
  (`planning`, `implementation`, `review`) через `sessionFile()`. Покрывает edge
  cases, когда фантом остался от пути отказа вне `runWithRetry` (внешний kill и
  т.п.) — это ровно та точка, где пользователь явно просит «попробовать заново».

### B1. Усиление промпта (строгий контракт)

- Заполнить `Inputs.StageDir` при сборке промптов для интерактивных стадий:
  `runPlanningAgent`, `runImplementationAgent`, `runReviewAgent`
  (`orchestrator.go`) передают `StageDir: stageDir`. Сейчас поле пустое.
- В `pkg/prompts/builder.go` `interactive_rules` печатать **буквальный
  абсолютный путь** рядом с env-var. Текст (набросок):
  > The env var `FLOWMANAGER_STAGE_DIR` contains your stage directory:
  > `<abs path>`. Write the question file ONLY to
  > `<abs path>/<phase>.q<N>.question.json` — **nowhere else**. Do NOT invent
  > paths like `.flowManager/stages/...`.
- Путь дублируется и для записи вопроса, и для чтения answer (текущий bash-loop
  уже использует env-var — оставляем как есть, но печатаем путь явно).

### B2. Детектор-нарушения (fail-fast)

- Новая функция в `pkg/orchestrator` (например, `detectDialogViolation`):
  через существующий `executor.WrittenFiles(<phase>.jsonl)` получает все пути,
  записанные агентом; если среди них есть `*.question.json` **вне** `stageDir`
  (т.е. `filepath.Rel`/префикс-проверка показывает, что путь не внутри
  `stageDir`) — возвращает описание нарушения.
- Вызывается из `pollQuestions` **только для интерактивных стадий** (guard
  `stage.Interactive`) и **только когда в `stageDir` нет неотвеченного вопроса**
  (если он есть — нормальный поток `FindUnansweredQuestions` его обработает, и
  детектор не нужен). При обнаружении —
  `o.FailStage(stageID, "dialog protocol violation: question written to <path>, expected <stageDir>")`.
- Переиспользуем `executor.WrittenFiles` — новой I/O-логики нет. Проверка
  «вне stageDir» — сравнением абсолютизированных путей (префикс `stageDir` + os.PathSeparator).

## Сопутствующее

- `config.example.yaml:19` — обновить дефолт `extra_args` в комментарии
  (+ `--verbose`).

## Файлы

| Файл | Правка |
|------|--------|
| `pkg/executor/executor.go` | A1 (константа/дефолт + `--verbose`), A2 (stderr-параметр в `run`, убрать `io.Discard`) |
| `pkg/orchestrator/orchestrator.go` | A1 (`runnerFor` → общая константа), A3 (чистка в `onManualRetry`), B1 (`StageDir` в `Build` Inputs), B2 (`detectDialogViolation` + вызов из `pollQuestions`) |
| `pkg/orchestrator/retry.go` | A3 (`os.Remove` в non-retryable-ветке) |
| `pkg/prompts/builder.go` | B1 (печать абсолютного пути + «nowhere else» в `interactive_rules`) |
| `config.example.yaml` | A1 (обновить комментарий дефолта) |
| `pkg/session.go` | без изменений (`sessionFile()` уже есть) |

## Тесты

- `pkg/executor/executor_test.go`:
  - дефолтные `ExtraArgs` содержат `--verbose`;
  - stderr направляется в файл (через хелпер/фейковый cmd — по устоявшемуся
    паттерну файла).
- `pkg/orchestrator/integration_retry_test.go`:
  - non-retryable failure интерактивной стадии → `<phase>.session.json` удалён.
- `pkg/orchestrator/integration_interactive_test.go`:
  - агент пишет `question.json` вне `stageDir` → стадия переходит в `failed` с
    причиной «dialog protocol violation».
- `pkg/prompts/builder_test.go`:
  - интерактивный промпт содержит абсолютный путь `StageDir` и формулировку
    «nowhere else»/ONLY.

## Риски и трейд-оффы

- **A3 чистит сессию на любой non-retryable неудаче.** Если стадия упала на
  позднем этапе (conversation уже существует), retry стартует свежую сессию и
  теряет историю. Это сознательный трейд-офф в пользу надёжности: упавшая
  стадия должна реетраиться чисто, а resume-после-ответа (через `onUserAnswered`)
  использует сессию, пока стадия **не** failed — конфликта нет.
- **B2 детектор читает `.jsonl` целиком** (`WrittenFiles`). Для типичных
  интерактивных прогонов объём небольшой; вызывается только когда в `stageDir`
  вопроса нет (узкое условие). Если логи вырастут — оптимизация по byte-offset
  в отдельной задаче.
- **`--verbose` на версиях, где он не обязателен**, не ломает парсинг executor'а:
  extra-события фильтруются (`parseStreamEvent` берёт только `type=="assistant"`,
  `isErrorLine` ловит ошибки).

## Параллельная рекомендация (вне этого плана)

В навыках `goga-propose` / `goga-task-by-proposing` явно прописать, что путь
для `question.json` берётся из `$FLOWMANAGER_STAGE_DIR`, с коротким примером.
Снимает часть случаев галлюцинации на корню. Находится в другом проекте
(`~/.claude/skills/` / pipx `goga`).

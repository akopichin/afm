# Дизайн: `afm memory rebuild`

**Дата:** 2026-09-09
**Статус:** дизайн утверждён, готов к написанию плана реализации
**Автор источника:** консолидировано из `tmp/memory-rebuild-command.md`

## 1. Цель и мотивация

Позволить постфактум прогнать существующий memory pipeline по уже **успешно завершённому** run и безопасно записать per-stage memory и общий `memory.md` — **не создавая новый run** и **не меняя FSM/event log/snapshot** исторического run.

Основной сценарий: flow отработал ещё до того, как в него добавили `memory:`/`reflect:`. Пользователь дописывает эти секции в актуальный `flow.yaml` и делает backfill памяти из уже существующих логов сессий завершённого run.

Ключевая ценность в том, что для завершённого-до-включения-памяти run **нет** `reflect_dataset.yaml` (их пишет фоновый `maybeRunReflection` во время run), но **есть** логи сессий стадий. Команда сначала запускает `reflect` (capture) из этих логов, затем существующую цепочку `aggregate → prioritize → SelectHigh → update`.

## 2. Пользовательский контракт

```bash
# последний успешно завершённый run этого flow
afm memory rebuild .afm/flows/my-flow.yaml

# конкретный run
afm memory rebuild .afm/flows/my-flow.yaml --run my-flow-20260908-143012-a4f1

# безопасный просмотр без изменения памяти
afm memory rebuild .afm/flows/my-flow.yaml --dry-run

# заново сгенерировать reflect_dataset.yaml из логов
afm memory rebuild .afm/flows/my-flow.yaml --force-reflect

# управление git-коммитом (взаимоисключающие)
afm memory rebuild .afm/flows/my-flow.yaml --commit
afm memory rebuild .afm/flows/my-flow.yaml --no-commit
```

- Постоянные флаги `--dir`/`AFM_DIR` и `--debug` работают как в `afm run`.
- Пропущенный аргумент `flow.yaml` → правило `resolveFlowPath`: ровно один YAML в `.afm/flows/`, иначе явная ошибка.
- Если `--commit`/`--no-commit` не указаны — берётся `memory.commit` из flow. В `--dry-run` коммит **никогда** не выполняется.

### 2.1 Что команда делает

1. Загружает текущие global/project config и текущий flow-файл.
2. Проверяет, что память включена (`memory.path != ""`) и есть хотя бы одна реально записываемая цель (non-script stage с `reflect.mode: w|rw`; и/или project `memory.md` при `memory.mode: w|rw` и наличии пригодного dataset).
3. Находит выбранный исторический run под `<afm-root>/.afm/runs/`.
4. Берёт эксклюзивный run-lock и читает состояние **только** из `events.jsonl` через `state.LoadRunState`.
5. Проверяет, что все стадии run в `done`, а множество stage ID **точно** совпадает с текущим flow.
6. Для каждого non-script stage с `reflect.mode: w|rw`: переиспользует валидный dataset по умолчанию; либо запускает `reflect` (отсутствует/невалиден); либо всегда при `--force-reflect`.
7. Последовательно запускает `aggregate → prioritize → SelectHigh → update`: отдельно для каждого записываемого `reflect.file`, и один раз по всем выбранным datasets для project-wide `memory.md` (если глобальный mode допускает запись).
8. Показывает reused/generated datasets, изменяемые targets, итоговый diff.
9. Нормально — атомарно публикует подготовленные datasets/файлы; при `--dry-run` — оставляет целевые файлы неизменными.
10. Опционально делает локальный git-коммит **только** memory-targets; push никогда.

### 2.2 Что команда принципиально не делает

- Не вызывает `resolveRun` и не создаёт новый run.
- Не запускает scheduler, stage agents, hooks, dashboard, HTTP server.
- Не пишет FSM-события, не меняет `events.jsonl`/`state.json`/stage status/`PausedFrom`/review markers/notices feed.
- Не анализирует `script:` stages (нет агентской сессии) — сохраняет контракт `maybeRunReflection`.
- Не восстанавливает failed/partial/paused run. В v1 принимаются только runs со всеми стадиями `done`.
- Не даёт stage-фильтр: project memory должна строиться из согласованного набора datasets одного полного run.
- Не воспроизводит исторический flow-конфиг (run его не сохраняет): намеренно используется **текущий** flow YAML, **текущие** prompts, **текущая** client config; это печатается и фиксируется в manifest.

### 2.3 Семантика mode-флагов

| Конфигурация | Поведение rebuild |
|---|---|
| `memory.path` пуст | ошибка до запуска агентов |
| `memory.memory_use` | игнорируется (это только read/injection gate stage prompts) |
| `memory.mode: r` | project `memory.md` не меняется |
| `memory.mode: w\|rw` | project `memory.md` собирается из datasets выбранных write-reflect stages |
| stage без `reflect` | не создаёт dataset и per-stage файл |
| `reflect.mode: r` | dataset не создаётся, stage-файл не меняется |
| `reflect.mode: w\|rw` | stage участвует в capture/distill |
| `script:` + write-reflect | warning и skip, как в обычном run |

Несколько стадий с одним `reflect.file` merge-ятся последовательно в один staging target в порядке объявления стадий текущего flow. Один dataset нельзя включать в project aggregate дважды. Для project aggregate **нельзя** использовать произвольный `filepath.Glob(<run>/*/reflect_dataset.yaml)`: берутся только datasets стадий, прошедших eligibility gate, иначе старый/stale dataset от удалённого `reflect` незаметно попадёт в память.

## 3. Почему нельзя просто вызвать текущий `runEndOfRunMemory`

Существующий pipeline тесно привязан к `Orchestrator`:

- `memoryAgentSpec`, `buildMemoryPrompt`, `execMemoryAgent`, `distill`, `runEndOfRunMemory` — package-private;
- `orchestrator.New` требует `*state.Store`, строит FSM, buses, graph, concurrency manager;
- `runEndOfRunMemory` предполагает, что per-stage datasets уже созданы фоновым `maybeRunReflection` во время run;
- ошибки намеренно best-effort (пишется `reflect_failed`, не возвращается вызывающему);
- fixed output names (`patterns.md`, `prioritized.md`, `high.md`, `update.log`) могут содержать артефакты прошлой попытки;
- reflect получает весь stage directory и способен прочитать старые `reflect.log`/`aggregate.log` — начать анализировать сам pipeline памяти;
- update-agent переписывает настоящий target напрямую → `--dry-run` и защита от частичного сбоя невозможны.

Фиктивный Store/Orchestrator для CLI недопустим: протащит lifecycle-зависимости в offline-команду и может переписать derived snapshot при `Store.Close`. Решение — вынести memory pipeline в отдельный переиспользуемый пакет.

## 4. Целевая архитектура

```text
cmd/afm memory rebuild
        | resolve flow/run/root/config, acquire locks
        v
pkg/memorypipeline.Pipeline
        +-- SourceInventory     -> только исходные stage-session файлы
        +-- CaptureStage        -> reflect
        +-- DistillTarget       -> aggregate -> prioritize -> SelectHigh -> update
        +-- Staging/validation  -> diff -> atomic publish
        +-- Report/manifest
        |
        +--> pkg/executor       (fresh-context agent, global client command)
        +--> pkg/memory         (paths, SelectHigh, atomic writes, commit)

pkg/orchestrator -> тот же memorypipeline для live capture/end-of-run
                    (ошибки -> reflect_failed, FSM не меняется)
```

Полное извлечение (утверждено): весь pipeline переезжает в `pkg/memorypipeline`, а live orchestrator рерайтится на тот же engine. Это даёт один источник истины для live и offline путей.

Предлагаемые публичные типы (имена уточняются при реализации, смысл сохраняется):

```go
type Prompts struct { Reflect, Aggregate, Prioritize, Update string }

type AgentConfig struct {
    Command, WrapperDir, RootDir, RunDir string
    ExtraArgs   []string
    IdleTimeout time.Duration
    Debug       bool
}

type RebuildRequest struct {
    RunDir, MemoryDir, WorkDir string
    Stages       []flow.Stage
    Memory       flow.MemoryConfig
    ForceReflect bool
    DryRun       bool
}

type Report struct {
    RunID, WorkDir string
    Datasets []DatasetResult
    Targets  []TargetResult
    Committed bool
}

func New(prompts Prompts, agent AgentConfig, opts ...Option) *Pipeline
func (p *Pipeline) CaptureStage(ctx context.Context, stage flow.Stage, stageDir, datasetOut, logDir string) error
func (p *Pipeline) Rebuild(ctx context.Context, req RebuildRequest) (Report, error)
```

Один тестовый seam уровня `AgentRunner` = `func(context.Context, AgentSpec) error`, аналог нынешнего `runMemoryAgent`, но принадлежащий `memorypipeline`. Production-реализация продолжает использовать `executor.RunAgent` без session/resume и без `AFM_STAGE_DIR`.

Memory-agents используют `config.client.command` и `client.extra_args` (текущее `resolveMemoryCommand`), а не `stage.command` — это нельзя незаметно менять в backfill.

## 5. Выбор и валидация исторического run

### 5.1 Read-only resolver (`pkg/state`)

```go
func FindLatestCompletedRunDir(base, flowName string) (string, error)
```

Алгоритм:

1. Тот же anchored prefix, что `FindLatestRunDir`: после `<flowName>-` обязана идти цифра (`foo` не матчит `foo-bar`).
2. Кандидаты newest-first.
3. Для каждого читать authoritative `events.jsonl` через `LoadRunState`.
4. Первый `AllDone()` — результат.
5. Обычный incomplete run пропускать (позволяет backfill последнего completed, даже если поверх начат новый).
6. Corrupt matching log **не** скрывать fallback-ом: вернуть контекстную `ErrCorruptLog` (как `FindLatestRunForStage`).
7. Отсутствующий/нечитаемый `events.jsonl` — ошибка кандидата, не доверять `state.json`.

Explicit `--run`: принимается только безопасный basename — запретить `/`, `\`, абсолютный путь, `.`/`..` и любое значение, где `filepath.Base(v) != v`. После `Join(runsDir(), id)` проверить существование директории и anchored принадлежность имени к `flow.Name`.

### 5.2 Run lock

Публичный read-only lock helper в `pkg/state`, использующий тот же `<runDir>/.lock` и тот же `progress.Lock`, что `Store.Open`, но не открывающий/не усекающий `events.jsonl` и не пишущий snapshot:

```go
type RunLock struct { /* wraps progress.Lock */ }
func TryLockRun(runDir string) (*RunLock, error)
func (l *RunLock) Close() error
```

`Store.Open` перевести на этот helper (одна реализация locking semantics). Busy lock → `state.ErrRunLocked`; команда превращает его в понятную ошибку («выбранный run активен, дождитесь завершения или остановите `afm run`»). Rebuild **не ждёт** освобождения run lock (анализировать меняющиеся логи нельзя).

Порядок preflight: resolve directory → run lock → `LoadRunState` → `AllDone` → stage-set compatibility. Lock держится до завершения/отмены команды (команда пишет audit/dataset artifacts внутрь run directory).

### 5.3 Совместимость текущего flow с историческим run

Сравнить **точное** множество stage ID из replay с текущим `flow.Stages`:

- missing в YAML или extra в YAML — hard error с двумя отсортированными списками;
- порядок стадий из исторического `events.jsonl` надёжно не восстановить → порядок pipeline берётся из текущего YAML и записывается в manifest;
- переименование flow не поддерживается автоматически: run ID должен принадлежать `flow.Name`;
- изменение description/agents/commands само по себе не блокируется (backfill должен позволять добавить `memory`/`reflect` задним числом);
- защита от бывшей script-stage, ставшей agent-stage: если в stage-dir нет ни одного agent-session source, capture завершается ошибкой/skip как script-only.

В stdout: `using current flow definition and current prompts for historical run <id>`.

## 6. Инвентаризация исходников reflect

Не передавать reflect-agent весь stage directory. `SourceInventory(stageDir) ([]string, error)` возвращает детерминированно отсортированный список существующих regular files.

**Включать:** execution agent logs/raw streams (`planning*.log|jsonl`, `implementation*.log|jsonl`, `review*.log|jsonl`, `autonomous*.log|jsonl`); их `*.stderr.log`; `plan.md`, исторические версии plan, `execution_summary.md`; `*.dialog.jsonl`, `*.question.json`, `*.answer.json`; `prenote.md`, `feedback.md`; `before.log`/`after.log` и их stderr для agent-stage.

**Исключать:** `reflect*`, `aggregate*`, `prioritize*`, `update*` logs/jsonl/stderr; `reflect_dataset.yaml`, `patterns.md`, `prioritized.md`, `high.md`; всю `<stageDir>/memory-rebuild/` attempt-директорию; `script.log`/`script.stderr.log` как единственный source; временные файлы и symlinks (не переходить по symlink из run tree).

Фактические имена phase-файлов брать из helpers/constants `pkg/flow`, не дублировать строковые литералы. Revision/feedback variants покрыть тестовыми fixtures.

Reflect prompt меняется с «read every `*.log` under directory» на «read exactly these source paths» — закрывает саморефлексию по старым memory logs и уравнивает live reflection и rebuild. Прямой user input (`dialog`, `prenote`, `feedback`) остаётся highest-priority.

Пустой список / только hook/script output без реальной agent session: stage не отправляется агенту. Для non-script + write-reflect — hard error explicit-команды с путём; live pipeline сохраняет best-effort warning.

## 7. Dataset reuse, валидация, `--force-reflect`

### 7.1 Default

Канонический dataset — `<runDir>/<stageID>/reflect_dataset.yaml`.

- Существует и проходит строгую проверку схемы → используется, повторный reflect не оплачивается.
- Отсутствует/невалиден → генерируется новый в attempt workdir.
- `--force-reflect` всегда генерирует новый из source inventory; старый canonical не заменяется до полного успеха операции.

### 7.2 Валидация

Typed validation YAML (в `pkg/memory` или `pkg/memorypipeline`):

- ровно верхние ключи `project_level` и `session_level`;
- каждый элемент содержит `prompt`, `chosen`, `rejected`, `source`;
- текстовые поля непустые после trim;
- `source` только `user|log`;
- обе секции могут быть пустыми (валидный результат «в стадии нет сохраняемых правил»);
- размер файла ограничен разумным пределом (например, 10 MiB);
- после успешного `RunAgent` обязательно проверить существование, regular-file status и parse. Exit code 0 без свежего output ≠ успех.

Каждый agent step получает новый output path в уникальном workdir — stale fixed output прошлой попытки не читается.

## 8. Attempt workspace и безопасная публикация

```text
<runDir>/memory-rebuild/<timestamp>-<rand4>/
  manifest.json
  stages/<stage-id>/
    reflect_dataset.yaml
    reflect.log / reflect.jsonl / reflect.stderr.log
    patterns.md
    aggregate.log / aggregate.jsonl / aggregate.stderr.log
    prioritized.md
    prioritize.log / prioritize.jsonl / prioritize.stderr.log
    high.md
    target.md
    update.log / update.jsonl / update.stderr.log
  flow/
    patterns.md / prioritized.md / high.md / target.md
    aggregate.* / prioritize.* / update.*
```

Attempt directory всегда сохраняется для аудита (включая failed и dry-run). Она не внутри stage source directory, чтобы source inventory её гарантированно не прочитал.

`manifest.json` — атомарно, обновляется после каждого шага. Минимальные поля: schema version, operation id; run ID + абсолютный run path; flow path + SHA-256 текущего YAML; resolved root_dir/memory_dir; memory mode/max_rules/effective commit; current prompt source/config + SHA-256 четырёх prompt templates; started_at/finished_at/status (`running|failed|dry_run|completed|promoting`); per-stage dataset source (`reused|generated`) + source file list + hashes + dataset hash; список targets, old/new hashes, changed flag; ошибки по шагам без секретов и содержимого prompts.

### 8.1 Staging targets

Update-agent не должен видеть настоящий writable target:

1. Под общим memory lock скопировать текущий target в attempt `target.md` (отсутствующий target = empty, как сейчас).
2. Передать update-agent attempt target — он меняет только staging copy.
3. Проверить результат: заголовок `# Project rules`; только `## <Pattern>` blocks (без `## High|Medium|Low`); patterns `<= max_rules`; UTF-8/regular file/size limit; output создан именно текущей попыткой.
4. Рассчитать diff old → candidate.

Для нескольких stages с одним `reflect.file` — **один общий** staging target: следующий stage merge-ит результат предыдущего, а не старую disk-версию (критично для `--dry-run`).

### 8.2 Publish

До завершения всех LLM steps записи разрешены только внутри attempt workdir. Любой обязательный сбой/отмена: canonical datasets и memory targets неизменны; commit не выполняется; manifest = failed/cancelled; CLI возвращает non-zero.

После полного успеха:

- `--dry-run`: вывести diffs, manifest=`dry_run`, ничего не публиковать;
- normal: temp-файл рядом с каждым final path, fsync file, rename, fsync parent dir; затем manifest=`completed`;
- canonical generated datasets публиковать тем же способом в stage dirs;
- unchanged targets не переписывать и не включать в commit.

Multi-file нельзя заменить одной POSIX-атомарной операцией. Для диагностируемости crash при promotion: перед rename сохранить old bytes/hashes; сначала manifest=`promoting` со списком pending targets; после каждого rename атомарно отметить target published; следующий rebuild при незавершённом `promoting` manifest предупреждает и может безопасно пересобрать всё заново из canonical/attempt datasets. Автоматический rollback не делать.

`memory.AtomicWrite` усилить до fsync file + parent (сохранив API) и обновить его тесты; publish-path использует этот же durable writer.

## 9. Ошибки и exit semantics

Обычный live pipeline остаётся best-effort и не валит flow. Explicit `memory rebuild` — **strict**:

- config/run/schema/lock/agent/validation/publish/commit error → exit 1;
- `no high-priority patterns` → успешный no-op для target;
- отсутствие изменений → exit 0, commit не создаётся;
- SIGINT/SIGTERM отменяет общий context, останавливает subprocess через существующий process-group handling, освобождает locks, не начинает publish/commit;
- ошибки содержат `stage id`, step (`reflect|aggregate|prioritize|update|publish|commit`) и путь к attempt workdir/log;
- не печатать secrets/полный prompt/user dialog в обычный stdout (для этого `--debug` и audit files).

Pipeline возвращает typed `Report` + `errors.Join`/typed step errors, а не только логирует. Orchestrator adapter преобразует их в `reflect_failed` notices; CLI печатает summary и возвращает Cobra error.

## 10. Межпроцессные блокировки общей памяти

Одного `<runDir>/.lock` недостаточно: rebuild старого run может параллельно переписывать тот же `memory.md`, что финализирует новый `afm run`.

Общий memory-directory lock, обязательный для **всех writers** (live end-of-run и rebuild):

```text
~/.afm/locks/memory-<sha256(canonical-absolute-memory-dir)>.lock
```

Lock не кладётся внутрь `memory.path`, потому что `memory.Commit` делает `git add .` и служебный lock мог бы попасть в git. `~/.afm` монтируется в Docker, project path монтируется по тому же absolute path → host/container вычислят один ключ и один физический lock.

Нюансы: canonical key = absolute cleaned memory path + `EvalSymlinks` (две ссылки на одну директорию → один lock); acquisition context-aware (non-blocking `TryLock` + короткий ticker + `ctx.Done`, без вечного flock); CLI сообщает об ожидании и позволяет Ctrl-C (run lock уже удерживается); порядок lock-ов везде run→memory (live `afm run` держит run lock всю жизнь, memory lock — только на finalization → нет cycle); lock охватывает чтение existing target, весь staging/update и commit; stage capture (пишет только dataset внутрь уникального run) shared-memory lock не требует.

Sentinel `memorypipeline.ErrMemoryLocked` вводить только если решено делать timeout; при ожидании до context cancellation отдельный sentinel не обязателен.

## 11. Git commit

Effective commit: `--commit`→true; `--no-commit`→false; иначе `flow.Memory.Commit`; `--dry-run`→всегда false.

Перед дорогими agent calls (если commit включён): memory directory внутри git worktree; repo root определяется; staged changes под memory directory отсутствуют (иначе fail-fast с советом `--no-commit`/разобраться с index — иначе команда незаметно включит пользовательские staged memory changes в свой commit).

После успешного publish коммитить только реально изменившиеся target paths: расширить `pkg/memory.Commit` или добавить `CommitPaths` с явными pathspecs; не использовать широкий `git add .` для explicit-команды. Уже staged изменения **в том же файле** невозможно отделить от rebuild-изменений → preflight обязателен.

Сообщение: `chore(memory): rebuild from <run-id>`. Пустой diff → `committed=false`, exit 0. Commit error после publish → non-zero + явно «memory files обновлены, commit не создан». Push не делать.

## 12. Docker, root_dir, wrappers, custom prompts

Команда запускает агентов → должна поддерживать тот же execution environment, что `afm run`.

### 12.1 Общие helpers из `run.go`

Вынести небольшие переиспользуемые helpers (сейчас inline в RunE, run.go:207-225):

- `resolveAgentRoot(rootDir string, f *flow.Flow) (string, error)`;
- `resolveMemoryDir(afmRoot, agentRoot string, f *flow.Flow) (string, error)`;
- preparation Docker re-exec;
- creation/cleanup generated wrapper directory.

**Обязательная regression:** существующий `afm run` после refactor даёт те же arguments/mounts/ports/browser behavior byte-for-byte.

### 12.2 Rebuild-specific Docker behavior

- На host, если Docker включён и `AFM_IN_DOCKER != 1` — self re-exec **до** взятия locks.
- Dashboard port, browser opener, file-browser manifest для rebuild не создаются.
- Для secrets/mounts — только фактически нужный memory-agent command (global `cfg.Client.Command`); stage commands rebuild не запускает.
- `docker.UsedRecipes(nil, ...)`, `ScanCommands(nil, ...)`, `UsesCodex(nil, ...)` должны поддержать nil flow либо получить отдельные command-list helpers.
- Сохранить `CheckClaudeDockerAuth`, auto-shim validation, generated wrappers, Codex OAuth mount, least-privilege secret filtering.
- Передать исходные CLI args + абсолютный `--dir`, как в run.
- Проверить, что `runDir`, `root_dir`, `memory.path`, flow file, custom `prompts_dir` доступны в контейнере (внешний absolute `root_dir` должен быть в `docker.extra_mounts`, иначе fail-fast с путём).

### 12.3 root_dir

- Относительный `flow.root_dir` резолвится от afm-root, как в `run.go`.
- Пустой root_dir = afm-root/current CWD; для offline command передать уже абсолютный resolved CWD в executor (стабильные manifest и lock key).
- Проверить, что agent root существует и является directory до запуска LLM.
- `memory.path` резолвится относительно agent root; absolute path сохраняет поддержку.

Custom prompts грузятся через `loadPrompts(cfg.PromptsDir)`. В manifest сохраняются hashes/источник.

## 13. Path safety

Backfill добавляет новый путь записи → нельзя полагаться только на `filepath.Join`:

- `--run` — только безопасный basename под `runsDir` (см. 5.1);
- `reflect.file` — local relative path без `..`, absolute prefix, NUL (`filepath.IsLocal` + clean/rel containment);
- final stage-memory target — внутри resolved `memory.path`;
- project target — всегда `<memory.path>/memory.md`;
- operation paths — только из валидированных stage IDs (существующая валидация stage ID проверяется на `/`, `\`, `.`, `..`);
- не следовать symlink из run source inventory;
- перед publish — проверить, что parent target не стал symlink после preflight (lock защищает только AFM writers).

Эти ограничения добавить в `flow.validate`, чтобы `afm run` и rebuild имели одну семантику. Это **потенциально breaking validation** для ранее опасных конфигов — отразить в README/changelog. `reflect.file: memory.md` — **запретить** явной validation error (collision с project target).

## 14. Осознанно отложено (не в v1)

- Rebuild failed/partial runs (`--allow-partial`).
- Выбор отдельных stages и смешивание datasets нескольких runs.
- UI-кнопка rebuild / progress streaming в dashboard.
- Автоматическое хранение immutable snapshot исходного flow YAML в каждом новом run (позволит будущему rebuild отличать историческую topology/type от текущего YAML без эвристик).
- Полностью атомарная multi-file transaction (filesystem не даёт бесплатно; manifest + durable per-file rename обеспечивают диагностируемость, не математическую атомарность при hard power loss).
- Git push.

## 15. Acceptance criteria

- Команда создаёт memory из логов run, завершившегося до включения `memory`/`reflect`.
- По умолчанию выбирается последний completed run нужного flow; новый incomplete run не мешает.
- Активный или не полностью `done` run не анализируется.
- Historical FSM/event log/snapshot не меняются.
- Повторный rebuild не читает собственные старые memory logs и по умолчанию не перезапускает валидный reflect.
- Любой сбой до publish оставляет canonical datasets и memory files неизменными; attempt artifacts и причина сбоя доступны.
- `--dry-run` показывает реальные candidate diffs, не меняет память и не коммитит.
- Mode semantics, custom prompts, root_dir, wrappers, Docker совпадают с обычным run.
- Live end-of-run и offline rebuild не теряют обновления друг друга благодаря общему interprocess memory lock.
- Explicit-команда возвращает non-zero при ошибке; автоматическая reflection остаётся best-effort и не влияет на FSM.
- Git commit ограничен изменёнными memory paths, без push, без чужих staged changes.
- Все unit/integration/race/lint проверки зелёные.

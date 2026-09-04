# File browser: тулбар и переключатель вида «все / изменённые файлы»

**Дата:** 2026-09-04
**Статус:** design, готов к реализации (уточнён после review)

## Контекст

Docker-only file browser в dashboard (`pkg/server/workspace/*`,
`components/file-browser/*`, спека `2026-09-03-docker-project-file-browser-design.md`)
сейчас показывает в левой панели список root'ов, поле поиска и ленивое дерево
всех файлов выбранного root'а. Просмотреть git-diff можно только пофайлово — во
вкладке DIFF правой панели, которая сравнивает **один** выбранный файл с `HEAD`
(`workspace.FS.Diff`, `pkg/server/workspace/diff.go`). Списка «какие файлы вообще
изменились» нет — чтобы найти правки, приходится вручную обходить дерево.

Задача: добавить сверху левой панели расширяемый тулбар с переключателем вида
между тремя режимами:

- **All** — текущее поведение, полное дерево файлов;
- **Unstaged** — плоский список файлов, отличающихся от индекса (`git diff`),
  плюс untracked-файлы;
- **vs HEAD** — плоский список файлов, отличающихся от последнего коммита
  (`git diff HEAD`), плюс untracked-файлы.

Название **Uncommitted** не используем: staged-правки тоже ещё не закоммичены,
но намеренно отсутствуют в сравнении worktree с index. **Unstaged** точнее
описывает выбранную baseline; tooltip дополнительно поясняет, что untracked-файлы
тоже входят в список.

Весь интерфейс — на английском (как и остальной браузер: `FILE`/`DIFF`,
`Reload`, `Search in …`).

## Семантика режимов (git)

| Режим | Отслеживаемые изменения | Новые файлы |
|-------|-------------------------|-------------|
| Unstaged | `git diff --name-status -z --no-renames` (worktree vs index) | `git ls-files --others --exclude-standard -z` → `added` |
| vs HEAD | `git diff --name-status -z --no-renames HEAD` (worktree vs HEAD) | untracked → `added`; collision с `D` того же пути → `modified` |

- Untracked собираются в обоих режимах и обычно имеют статус `added`. Они точно
  отсутствуют в индексе, но путь всё ещё может существовать в `HEAD` после
  staged deletion; этот collision отдельно схлопывается в `modified` для
  **vs HEAD** (см. алгоритм ниже). Это важно для afm: агенты часто создают новые
  файлы.
- Разница между режимами проявляется для staged-правок. Файл, полностью
  помещённый в индекс без дальнейших изменений в worktree, отсутствует в
  **Unstaged**, но присутствует в **vs HEAD**.
- Репозиторий без первого коммита поддерживается отдельно: для **vs HEAD**
  baseline считается пустым деревом, а все реально существующие cached и
  untracked-файлы возвращаются как `added`. Это не ошибка
  `diff_unavailable`.
- Rename/copy detection принудительно выключен (`--no-renames`), чтобы результат
  не зависел от repo config и не требовал потерянного в исходном варианте
  `old_path`: rename честно представлен двумя строками — старый путь `deleted`,
  новый `added`.
- `D` показывается некликабельной строкой: файла уже нет в рабочем дереве.
  Строка другого статуса тоже может быть некликабельной, если текущий путь —
  symlink, каталог, special file или исчез во время запроса.
- Переключатель меняет только состав левой панели. Вкладка **DIFF** справа по
  существующему контракту всегда показывает `HEAD → current file`, даже если
  файл был открыт из режима **Unstaged**.

## Архитектура

Фича продолжает существующий конвейер file browser: метод `FS` → case в
`routeFiles` → типизированный клиент → UI-компонент.

```text
FileBrowserModal (viewMode: all|index|head, changesRevision)
  ├─ all        → FileTree / FileSearchResults
  └─ index/head → ChangedFilesList
        └─ getChanged(root, mode, signal)  files-client.ts
              └─ GET /api/files/changed?root&mode   files_handlers.go
                    └─ workspace.FS.Changes(ctx, root, mode)   changes.go
```

### Бэкенд: `workspace.Changes`

В `pkg/server/workspace/fs.go` добавляются типизированный mode и метод:

```go
type ChangeMode string

const (
    ChangeModeIndex ChangeMode = "index"
    ChangeModeHead  ChangeMode = "head"
)

Changes(ctx context.Context, rootID string, mode ChangeMode) (ChangeList, error)
```

Неизвестный mode даёт `ErrInvalidRootOrPath`. Добавление метода требует также
обновить все реализации/test doubles интерфейса, в частности `server.fakeFS`.

Реализация живёт в `pkg/server/workspace/changes.go` и использует `findGitDir`,
`validateRelPath`, secure `rootHandle.openat` и общий `gitTimeout`. Существующий
`runGit` напрямую использовать **нельзя**: его контракт специально для
`cat-file` трактует любой ненулевой exit code как benign «объекта нет». Для
`diff`/`ls-files` это превратило бы повреждённый repo, permission error или
невалидную команду в ложный пустой список.

Алгоритм:

1. Проверить `ctx` и mode; затем `rh, _, err := fs.resolve(rootID, ".")`.
   `resolve` здесь только выбирает root и валидирует строковый путь — он **не**
   является openat2-проверкой файла.
2. `gitDir, _, ok := findGitDir(rh, ".")` ищет `.git` в самом root. Walk-up выше
   root конструктивно невозможен. `!ok` → `ErrDiffUnavailable`. Подкаталог
   внешнего repo намеренно не поддерживается.
3. Новый строгий helper запускает Git без shell, с verified
   `--git-dir=<gitDir>`, allowlisted `--work-tree=<rootPath>`, timeout 3s на
   процесс, `--no-pager`, `GIT_OPTIONAL_LOCKS=0` и
   `-c core.fsmonitor=false`. Последние две настройки не дают read-only endpoint
   обновлять index или запускать настроенный в repo fsmonitor hook. Для `diff`
   обязательны `--no-ext-diff`, `--no-textconv` и `--no-renames`, так что repo
   config не может включить внешний diff-driver/textconv или поменять форму
   rename-результата. Stderr не возвращается клиенту.
4. Helper различает запуск/timeout, exit code и переполнение stdout:
   - для `diff`/`ls-files` допустим только exit code 0, иначе `ErrReadFailed`;
   - наличие `HEAD` проверяется через
     `rev-parse --verify --quiet HEAD^{commit}`: 0 — commit есть, 1 — unborn,
     любой другой исход — `ErrReadFailed`;
   - stdout хранится через bounded writer: после `maxChangesOutputBytes` остаток
     дренируется без накопления, а ответ помечается `Truncated`. Одного лимита
     по количеству entries недостаточно — `exec.Cmd.Output()` сначала мог бы
     забуферить произвольно большой вывод.
5. Для mode `index` выполняются последовательно:
   - `diff --no-ext-diff --no-textconv --no-renames --name-status -z --`;
   - `ls-files --others --exclude-standard -z --`.
6. Для mode `head` при существующем `HEAD` выполняются:
   - `diff --no-ext-diff --no-textconv --no-renames --name-status -z HEAD --`;
   - тот же `ls-files --others --exclude-standard -z --`.
   В unborn-repo вместо неработающего `git diff HEAD` выполняется
   `ls-files --cached --others --exclude-standard -z --`: каждый путь, который
   всё ещё существует в worktree, имеет статус `added`; cached-путь, удалённый
   из worktree, не является отличием от пустой baseline и отбрасывается.
7. Парсер учитывает реальный формат `--name-status -z`: это NUL-поля
   `status\0path\0`, а не строка `status\tpath`. Из-за `--no-renames` троек
   `R/C + old + new` нет. Маппинг: `A → added`, `D → deleted`, `M/T/U → modified`.
   Неизвестный статус или malformed complete output → `ErrReadFailed`;
   незаконченная последняя запись допустима только при сработавшем byte-cap и
   просто не включается в частичный результат.
8. Вывод Git считается недоверенным. Для каждого пути:
   - требуется валидный UTF-8;
   - `validateRelPath` отсекает absolute/`..`/NUL и `.git`/`.afm`;
   - для недоступного на диске `deleted` дальше ничего не открывается;
   - для остальных путей выполняется настоящий `rh.openat` + `fstat` с
     `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS`: `Selectable=true` только для
     текущего regular file. Symlink/directory/special/vanished остаётся видимым,
     но `Selectable=false`.
9. Entries агрегируются по полному root-relative пути. Здесь есть не только
   race-case: после staged deletion файл с тем же путём может быть заново создан
   как untracked, и `git diff HEAD` даст `D`, а `ls-files --others` — тот же путь.
   Пара `deleted + added` схлопывается в один `modified` с `Selectable`,
   вычисленным по текущему файлу; во всех остальных дублях tracked-результат
   имеет приоритет. Затем entries сортируются по **полному `Path`**,
   case-insensitive с оригинальным `Path` как tie-break.
   `sortEntries` из `list.go` нельзя переиспользовать буквально: он сортирует по
   basename и для плоского списка дал бы неверный порядок одинаковых имён из
   разных каталогов.
10. Отдельные тестируемые лимиты `maxChangesEntries` и
    `maxChangesOutputBytes` ограничивают map, JSON и stdout. Достижение любого
    лимита даёт `ChangeList.Truncated=true`, не ошибку.

Две Git-команды не образуют атомарный snapshot: worktree может измениться между
ними. Это допустимая eventual-consistency для read-only browser; дедуп защищает
форму ответа, а явный **Refresh** получает новое состояние. Ошибка одной команды
не возвращает частичный «успешный» список.

Различение исходов:

- пустой успешный вывод → пустой `ChangeList`;
- root без usable `.git` → `ErrDiffUnavailable`;
- unborn repo → валидный список относительно пустой baseline;
- timeout, git-not-found, неожиданный exit, malformed output → `ErrReadFailed`;
- byte/entry limit → частичный список с `Truncated=true`.

### Типы (`pkg/server/workspace/types.go`)

```go
type ChangeStatus string

const (
    ChangeModified ChangeStatus = "modified"
    ChangeAdded    ChangeStatus = "added"
    ChangeDeleted  ChangeStatus = "deleted"
)

type Change struct {
    Name       string       `json:"name"`
    Path       string       `json:"path"`       // root-relative, slash-separated
    Status     ChangeStatus `json:"status"`
    Selectable bool         `json:"selectable"` // true only for a current regular file
}

type ChangeList struct {
    Entries   []Change `json:"entries"`
    Truncated bool     `json:"truncated"`
}
```

Это отдельный словарь от `Diff.Status`: сейчас строки частично совпадают, но
`Diff` дополнительно имеет `clean` и описывает другой объект/момент времени.

### HTTP (`pkg/server/files_handlers.go`)

Новый case в `routeFiles`:

```go
case "changed":
    s.filesChanged(w, r)
```

`filesChanged` читает `root` и `mode`, приводит mode к `workspace.ChangeMode` и
зовёт `s.workspace.Changes`. Ошибки проходят через существующие
`writeFilesError`/`filesErrStatus`: `invalid_root_or_path` → 400,
`diff_unavailable` → 409, остальное → 500. Успешный динамический ответ получает
`Cache-Control: no-store`, чтобы кнопка Refresh не могла получить свежий на вид,
но закэшированный браузером список.

Endpoint наследует гарантии `routeFiles`: только GET (иначе существующий 404),
`X-Content-Type-Options: nosniff`, 404 при отключённом workspace и JSON-ошибку
`{"error":"<code>"}` без путей/stderr.

### Клиент (`pkg/web/dashboard/src/api/files-client.ts`)

Changed-entry делается структурно совместимым с текущими callback'ами
`openFile`/`onToggleSelect`, которые принимают `TreeEntry`; `kind: 'file'`
добавляется на клиентской границе:

```ts
export type ChangeStatus = 'modified' | 'added' | 'deleted'
export type ChangeEntry = TreeEntry & { status: ChangeStatus }
export type ChangeList = { entries: ChangeEntry[]; truncated: boolean }

export async function getChanged(
  root: string,
  mode: 'index' | 'head',
  signal?: AbortSignal,
): Promise<ChangeList>
```

`toChangeEntry` требует строковые `name`/`path` и известный `status`; malformed
entry отбрасывается, а не превращается в выдуманный `modified`. `selectable`
истинен только при точном `true`, `kind` синтезируется как `file`,
`truncated` по умолчанию `false`. Используются те же `fetchOk` и query-builder.

### UI (`components/file-browser/`)

**Тулбар** — `.file-browser-toolbar`, первый элемент внутри
`<aside className="file-browser-roots">`. Переключатель — `role="group"` с
понятным `aria-label`, три обычные кнопки с `aria-pressed`: **All / Unstaged /
vs HEAD**. Это сохраняет нативную keyboard-семантику кнопок; `role="radio"` без
roving tabindex и Arrow-key navigation не используем. У изменённых режимов есть
tooltip с точной baseline.

Рядом показывается кнопка **Refresh** (`aria-label="Refresh changed files"`),
доступная в `index/head` и disabled во время запроса. Фонового git-polling нет:
оно дорого для monorepo; актуализация явная и предсказуемая.

**Состояние панели** (`FileBrowserModal.tsx`):

- `viewMode: 'all' | 'index' | 'head'`, default `all`;
- `changesRevision`, увеличиваемый кнопкой Refresh;
- mode переживает смену root; `selection` и `activeFile` при смене mode не
  сбрасываются;
- существующий `FileTree` остаётся смонтированным, но hidden вне `all`, чтобы
  раскрытые каталоги не терялись при переключении туда-обратно.

**ChangedFilesList** — плоский список по образцу `FileSearchResults`: полный путь
с приглушённым directory-prefix, badge `M/A/D`, отдельные настоящие кнопка
открытия и checkbox, подсветка `activePath`. Checkbox и кнопка есть только у
`selectable`; остальные строки приглушены и имеют доступный текст причины
(`Deleted or unavailable in the working tree`). Цвет badge не является
единственным сигналом — буква статуса остаётся видимой.

**Загрузка** — отдельный `useEffect` с зависимостями
`[viewMode, selectedRoot, changesRevision]`. На **каждом** запуске, включая
ветки `all`/`null root`, generation увеличивается; предыдущий AbortController
отменяется. Для `index/head` старый список сразу очищается, затем вызывается
`getChanged`. Ответ/ошибка применяются только при совпадающем generation — это
закрывает в том числе A→B→A. Abort не показывается как UI error.

**Состояния списка**:

- запрос → `Loading changes…`;
- успешный пустой ответ → `No changes`;
- `FilesApiError(diff_unavailable, 409)` → `Not a git repository`;
- другая ошибка → inline error `Failed to load changes: <code>`;
- `truncated` → `Some changes are not shown`.

**Поиск** ортогонален git-статусу и виден только в `all`. Текст query
сохраняется при временном уходе в changed-mode, но search-effect получает
`viewMode` в dependencies: вне `all` он увеличивает generation, abort'ит
in-flight поиск и очищает его result/loading/error. При возврате в `all`
непустой query запускается заново. Это не позволяет скрытому search-запросу
перезаписать левую панель.

**Стили** меняются только в source-of-truth
`pkg/web/dashboard/skins/base/file-browser.css`: `.file-browser-toolbar`,
segmented control, refresh и `.change-badge` variants на существующих токенах
(`--amber/--mint/--coral/--ink*/--panel-bg/--bg-elev`).
`public/skins` вручную не редактируется — его обновляет `npm run sync-skins`
(и автоматически `npm run build`).

## Безопасность и ограничения ресурсов

- `.git` находится через secure root fd; `.git`-symlink и gitfile за пределами
  root отвергаются существующим `findGitDir`/`verifyContained`.
- Git неизбежно обходит worktree для этой фичи, поэтому получает только
  allowlisted Docker mount через `--work-tree`; shell, pager, external diff,
  textconv и fsmonitor hook не используются, optional index locks выключены.
- Имена из Git не считаются доказательством доступности: перед выдачей они
  валидируются, а `Selectable` определяется отдельным openat2/fstat. Любой
  последующий content/reference request снова проходит свою secure-open
  проверку.
- `.git`/`.afm`, absolute/escape paths и не-UTF-8 имена не попадают в JSON.
- Число entries и stdout каждого Git-процесса ограничены независимо; timeout и
  отмена HTTP request завершают child process.
- Ответ не содержит абсолютных путей или git-stderr. Endpoint остаётся за
  loopback-bind и workspace capability-gate (host-mode → 404).

## Тестирование

### Backend

`pkg/server/workspace/changes_test.go` (Linux-tagged, real temporary repo):

- матрица staged/unstaged/untracked для обоих mode;
- ignored untracked отсутствует;
- modified/added/deleted и `selectable`;
- rename представлен `deleted + added` независимо от `diff.renames` config;
- staged deletion + заново созданный untracked с тем же путём схлопывается в
  один selectable `modified`, а не в ложный некликабельный `deleted`;
- type-change/symlink/special/vanished path не открывается и не выбирается;
- unborn repo в `head` возвращает существующие cached+untracked как `added`;
- NUL parser корректен для пробелов/tab/newline в имени; malformed/unknown
  status даёт `ErrReadFailed`;
- hidden/invalid/сфабрикованный escape path фильтруется unit-тестом parser-а;
- case-insensitive full-path sort и dedup;
- byte-cap и entry-cap ставят `Truncated`, не раздувая результат;
- no repo → `ErrDiffUnavailable`; malformed repo/неожиданный Git exit →
  `ErrReadFailed`; invalid mode → `ErrInvalidRootOrPath`;
- `.git` symlink/gitfile наружу по-прежнему отвергаются.

Отдельно обновляются compile-time реализации `workspace.FS`. Linux-tagged тесты
запускаются внутри Docker/CI; локальная cross-platform проверка —
`GOOS=linux go build ./pkg/server/workspace ./pkg/server` и обычный
`go test ./...` для текущей платформы.

### HTTP

`pkg/server/files_handlers_test.go`: success JSON + `Cache-Control: no-store`,
точная передача `index/head`, invalid mode, not-repo, Git failure, non-GET и
workspace disabled. `fakeFS` получает управляемые `changes`/`changesErr`.

### Frontend

- `ChangedFilesList.test.tsx`: badges, full path, open/select, nonselectable и
  truncation/error/loading/empty states;
- `FileBrowserModal.test.tsx`: mode switch, сохранение mounted tree/query,
  остановка скрытого поиска, changed generation-guard при быстром A→B→A,
  reset старых строк, Refresh и special-case 409;
- `files-client.test.ts`: parse, invalid-entry filtering, default `truncated`,
  `FilesApiError` и forwarding AbortSignal;
- `test-support.ts`: mock route `/api/files/changed`.

Финальная frontend-проверка: `npm test`, `npm run typecheck`, `npm run build`;
build одновременно синхронизирует `skins/` в `public/skins/`.

## Вне области

- ignored/untracked режим «показать всё, включая `.gitignore`»;
- staged-only view и произвольные commit/branch comparisons;
- rename/copy detection как отдельный статус (сейчас rename = D + A);
- клиентский поиск внутри changed-list;
- автоматический polling changed-list (есть явный Refresh);
- root, являющийся подкаталогом большего repo: `.git` должен находиться в самом
  browsable root.

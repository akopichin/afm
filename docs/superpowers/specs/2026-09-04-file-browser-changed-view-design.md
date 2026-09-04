# File browser: тулбар и переключатель вида «все / изменённые файлы»

**Дата:** 2026-09-04
**Статус:** design, готов к реализации

## Контекст

Docker-only file browser в dashboard (`pkg/server/workspace/*`,
`components/file-browser/*`, спека `2026-09-03-docker-project-file-browser-design.md`)
сейчас показывает в левой панели: список root'ов, поле поиска и ленивое дерево
всех файлов выбранного root'а. Просмотреть git-diff можно только пофайлово — во
вкладке DIFF правой панели, которая сравнивает **один** выбранный файл с `HEAD`
(`workspace.FS.Diff`, `pkg/server/workspace/diff.go`). Списка «какие файлы вообще
изменились» нет — чтобы найти правки, приходится вручную обходить дерево.

Задача: добавить сверху левой панели **тулбар инструментов** (спроектированный
как расширяемый — пока в нём один инструмент) с переключателем вида панели между
тремя режимами:

- **All** — текущее поведение, полное дерево файлов;
- **Uncommitted** — плоский список файлов, отличающихся от индекса
  (`git diff`, worktree vs index), плюс новые (untracked) файлы;
- **vs HEAD** — плоский список файлов, отличающихся от последнего коммита
  (`git diff HEAD`, worktree vs HEAD), плюс те же untracked.

Весь интерфейс — на английском (как и остальной браузер: `FILE`/`DIFF`,
`Reload`, `Search in …`).

## Семантика режимов (git)

| Режим | Отслеживаемые изменения | Новые файлы |
|-------|-------------------------|-------------|
| Uncommitted | `git diff --name-status -z` (worktree vs index) | `git ls-files --others --exclude-standard -z` → `added` |
| vs HEAD | `git diff --name-status -z HEAD` (worktree vs HEAD) | те же untracked → `added` |

- **Untracked одинаковы в обоих режимах** — они не в индексе и не в HEAD, поэтому
  попадают в оба списка как `added`. Это ключевое требование: в afm агенты
  постоянно создают новые файлы, и они должны быть видны в списке изменений
  (в режиме `All` они и так видны, т.к. дерево читает файловую систему).
- Разница между режимами проявляется **только для staged-правок** (когда кто-то
  делал `git add`): файл, полностью помещённый в индекс без изменений в worktree,
  в `Uncommitted` не покажется (worktree == index), но покажется в `vs HEAD` как
  отличие от коммита. Это типовой редкий кейс — большинство flow не стейджат
  вручную.
- Deleted-файлы (`D`) показываем **некликабельной строкой-маркером** (открыть или
  сослаться нельзя — в рабочем дереве их нет).

## Архитектура

Фича повторяет устоявшийся конвейер file browser: новый метод `FS`-интерфейса →
новый case в `routeFiles` → типизированный клиент → UI-компонент. Никаких новых
механизмов; git-обход держится на тех же проверенных примитивах безопасности, что
и `Diff`.

```
FileBrowserModal (viewMode: all|index|head)
  ├─ all  → FileTree            (без изменений)
  └─ index/head → ChangedFilesList
        └─ getChanged(root, mode)  files-client.ts
              └─ GET /api/files/changed?root&mode   files_handlers.go
                    └─ workspace.FS.Changes(ctx, root, mode)   changes.go
```

### Бэкенд: `workspace.Changes`

Новый метод FS-интерфейса (`pkg/server/workspace/fs.go`):

```go
Changes(ctx context.Context, rootID, mode string) (ChangeList, error)
```

- `mode` ∈ `"index"` (Uncommitted) | `"head"` (vs HEAD). Любое другое значение →
  `ErrInvalidRootOrPath`.
- Реализация — новый файл `pkg/server/workspace/changes.go`, рядом с `diff.go`,
  переиспользует его хелперы: `findGitDir`, `runGit`, `verifyContained`,
  `repoRelPath`, а также `resolve`/`validateRelPath` из `workspace.go`/`roots.go`.

Алгоритм:

1. `rh, _, err := fs.resolve(rootID, ".")` — берём rootHandle для секьюрных
   openat-проверок путей.
2. `gitDir, _, ok := findGitDir(rh, ".")` — находит `.git` **только если сам root
   является корнем репо** (walk-up за пределы root'а конструктивно невозможен —
   `findGitDir` для `"."` не поднимается выше). `!ok` → `ErrDiffUnavailable`
   (UI покажет «не git-репозиторий»). Это типовой кейс afm: afm-root = корень репо.
3. Две git-команды через явный `--git-dir=<verified>` **и** `--work-tree=<rootPath>`
   (в отличие от `Diff`, здесь нужен work-tree — сравниваем рабочее дерево),
   обе через `runGit` (таймаут 3с, классификация infra vs benign):
   - изменённые отслеживаемые: `diff --name-status -z` (+ `HEAD` в режиме `head`);
   - новые: `ls-files --others --exclude-standard -z`.
   `runGit` уже добавляет `--git-dir`; `--work-tree=<rootPath>` и остальные
   аргументы передаются как обычные args. Разбор `-z` (NUL-разделитель;
   `--name-status` даёт пары `<status>\t<path>` для M/A/D и тройки для R/C).
4. **Каждый путь** из вывода ре-базируется к root-relative и прогоняется через
   `validateRelPath` + `resolve` (openat2-guard): всё, что вышло бы за root,
   симлинки, `.git`/`.afm` — молча отбрасывается. git физически не может
   протащить путь мимо периметра. Исключение — deleted (см. ниже): его нет на
   диске, `resolve` не пройдёт, поэтому D-строки обрабатываются отдельной веткой
   (валидируем только через `validateRelPath`, без openat).
5. Маппинг статусов git → `Change.Status`:
   - `M` → `modified`, `A` → `added`, `D` → `deleted`;
   - `R<score>` (rename) → берём **новый** путь, статус `modified`;
   - `C<score>` (copy) → новый путь, `added`;
   - untracked (из `ls-files --others`) → `added`.
6. Дедуп по пути (untracked не пересекается с diff, но rename/copy могут дать
   дубль) + сортировка по пути (регистронезависимо, как в `list.go`).
7. Кап на `maxDirEntries` записей (та же константа, что в `list.go`) →
   `ChangeList.Truncated = true` при превышении.
8. Deleted-файлы (`selectable:false`) добавляются в результат, но не проходят
   через `resolve` (их нет в worktree).

Различение исходов:
- пустой вывод обеих команд → пустой `ChangeList` (валидно, «нет изменений»);
- `findGitDir` вернул `!ok` → `ErrDiffUnavailable`;
- реальная infra-ошибка `runGit` (deadline, git не найден) → `ErrReadFailed`.

### Типы (`pkg/server/workspace/types.go`)

```go
type Change struct {
    Name       string `json:"name"`
    Path       string `json:"path"`        // root-relative, slash-separated
    Status     string `json:"status"`      // modified | added | deleted
    Selectable bool   `json:"selectable"`  // false для deleted
}

type ChangeList struct {
    Entries   []Change `json:"entries"`
    Truncated bool     `json:"truncated"`
}
```

Словарь `Status` совпадает с `Diff.Status` (`modified`/`added`), плюс `deleted`.

### HTTP (`pkg/server/files_handlers.go`)

Новый case в `routeFiles`:

```go
case "changed":
    s.filesChanged(w, r)
```

`filesChanged` читает `root` и `mode` из query, зовёт `s.workspace.Changes`, при
ошибке — существующие `writeFilesError`/`filesErrStatus` (маппинг уже покрывает
нужные коды: `invalid_root_or_path`→400, `diff_unavailable`→409, прочее→500).
Наследует общие гарантии `routeFiles`: только GET, `X-Content-Type-Options:
nosniff`, 404 когда воркспейс отключён (host-режим), тело ошибки
`{"error":"<code>"}` без утечки путей.

### Клиент (`pkg/web/dashboard/src/api/files-client.ts`)

По образцу `getSearch`:

```ts
export type ChangeStatus = 'modified' | 'added' | 'deleted'
export type ChangeEntry = { name: string; path: string; status: ChangeStatus; selectable: boolean }
export type ChangeList = { entries: ChangeEntry[]; truncated: boolean }

export async function getChanged(
  root: string,
  mode: 'index' | 'head',
  signal?: AbortSignal,
): Promise<ChangeList>
```

Тот же `fetchOk` + защитный разбор snake_case→camelCase с дефолтами на месте
отсутствующих/неверных полей, как во всех остальных `get*`.

### UI (`components/file-browser/`)

**Тулбар** — контейнер `.file-browser-toolbar` первым элементом внутри
`<aside className="file-browser-roots">` (над списком root'ов). Спроектирован как
расширяемый; пока содержит один инструмент — сегментированный переключатель из
трёх пилюль (`role="radiogroup"`, каждая пилюля `role="radio"` + `aria-checked`).
Лейблы: **All / Uncommitted / vs HEAD**.

**Состояние панели** (`FileBrowserModal.tsx`): `viewMode: 'all' | 'index' | 'head'`,
по умолчанию `'all'` (текущее поведение). Режим — состояние панели, переживает
смену root'а; `selection` при смене режима не сбрасывается.

**Рендер по режиму** (в блоке, где сейчас `FileTree`):
- `all` → существующий `FileTree` (без изменений);
- `index`/`head` → новый `ChangedFilesList` — плоский список, рендер строк по
  образцу `FileSearchResults` (иконка, путь, клик → `openFile`, чекбокс →
  `onToggleSelect`, подсветка `activePath`), плюс бейдж статуса `M/A/D`.
  Deleted-строки (`selectable:false`) — приглушённые, некликабельные, с маркером.

**Загрузка** — `useEffect` c зависимостью `[viewMode, selectedRoot]`, зовёт
`getChanged`. Тот же отлаженный паттерн, что у поиска: generation-guard +
`AbortController` (поздний ответ устаревшего режима/root'а отбрасывается по
поколению), сброс списка **сразу** при смене (чтобы клик по строке не открыл файл
из «прошлого» набора — та же причина, что в поиске). Debounce не нужен
(переключение дискретное).

**Состояния**:
- пусто → «No changes»;
- `diff_unavailable` (409) → «Not a git repository»;
- прочая ошибка → инлайн-баннер с кодом (как в остальном браузере);
- `truncated` → строка-подсказка «Some changes are not shown» (как в поиске).

**Поиск**: поле поиска (обход дерева) ортогонально git-статусу. В режимах
`index`/`head` поле поиска **скрываем**; оно возвращается в режиме `All`.

**Стили** (`skins/base/file-browser.css`): `.file-browser-toolbar`,
сегмент-контрол и `.change-badge` (варианты modified/added/deleted) — на
существующих токенах (`--amber/--mint/--coral/--ink*/--panel-bg/--bg-elev`),
поэтому автоматически работают во всех скинах × light/dark (файл уже импортируется
всеми скинами).

## Безопасность

- git запускается **только** через verified `--git-dir` (результат `findGitDir`,
  проверенный `verifyContained` — `.git`-симлинк и gitfile за пределами root'а
  отвергаются структурно) и `--work-tree=<rootPath>` (сам примонтированный root).
- Каждый путь из git проходит `validateRelPath` + openat2-`resolve` перед выдачей
  (кроме deleted, которого нет на диске — для него только `validateRelPath`).
- Ответ никогда не содержит абсолютных путей или git-stderr (тот же контракт
  `writeFilesError`, что у остальных `/api/files/*`).
- Эндпоинт наследует loopback-bind и capability-gate воркспейса (host-режим →
  404, фичи нет).

## Тестирование

- **Бэкенд** (`pkg/server/workspace/changes_test.go`, Linux-tagged — гоняется в
  Docker/CI, как остальные workspace-тесты): реальный git-репо во временной папке;
  оба режима; untracked → `added`; modified → `modified`; deleted →
  `deleted`+`selectable:false`; rename; путь-выход-за-root отсекается; не-репо →
  `ErrDiffUnavailable`; невалидный `mode` → `ErrInvalidRootOrPath`. Проверка
  сборки: `GOOS=linux go build/vet ./pkg/server/workspace/`.
- **Хендлер** (`pkg/server/files_handlers_test.go`): `TestFilesChanged_Success`,
  `TestFilesChanged_InvalidMode`, `TestFilesChanged_NotRepo`,
  `TestFilesChanged_NonGET`, `TestFilesChanged_WorkspaceDisabled`.
- **Фронт**: `ChangedFilesList.test.tsx` (рендер строк, бейджи, deleted
  некликабелен, open/select); кейсы в `FileBrowserModal.test.tsx` (переключение
  режимов, скрытие поиска в changed-режимах, пустое/ошибка/загрузка,
  generation-guard при быстром переключении); `files-client.test.ts` (`getChanged`
  разбор + ошибки).

## Вне области

- Untracked-режим «показать вообще всё включая .gitignore» — нет, используем
  `--exclude-standard`.
- Просмотр diff между произвольными коммитами/ветками — нет, только index/HEAD.
- Клиентский поиск внутри списка изменений — нет (YAGNI).
- Работа, когда root — подкаталог большего репо (`.git` выше root'а) — сознательно
  не поддерживается: список доступен только когда root сам корень репо.

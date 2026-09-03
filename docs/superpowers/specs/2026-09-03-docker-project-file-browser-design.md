# Docker file browser: файлы проекта, diff и ссылки в feedback

**Дата:** 2026-09-03  
**Статус:** design, готов к реализации

## Контекст

Dashboard сейчас показывает только артефакты afm: план, диалог, лог и event
feed. Даже в Docker mode, где проект уже примонтирован в контейнер и доступен
агенту, пользователь не может из dashboard:

- открыть дерево исходников;
- посмотреть файл с подсветкой синтаксиса;
- проверить текущий git diff файла;
- сослаться на выбранный файл в построчном комментарии к плану или вопросу.

В Docker mode уже есть два разных класса mount-ов:

1. проект (`ReExecConfig.ProjectDir`) — read-write mount по тому же абсолютному
   пути на host и в контейнере;
2. `docker.extra_mounts` — read-only mount-ы по тому же пути (для `~` путь в
   контейнере начинается с `/home/afm`).

Кроме них контейнер получает служебные/credential mount-ы: `~/.claude`,
`~/.afm`, иногда `~/.codex`, а также бинарники кастомных агентов. Они **не
являются** частью файлового workspace и никогда не должны попадать в file
browser.

Отдельная проблема — историческое назначение `extra_mounts`: README прямо
рекомендует их для токенов и конфигов вроде `~/.ai-free`. Поэтому включить в
browser все существующие строковые `extra_mounts` автоматически нельзя: после
обновления afm dashboard мог бы неожиданно начать отдавать credential-файлы.

## Цель

Только внутри Docker mode добавить в dashboard файловый менеджер, который:

1. показывает исходники project mount-а и явно разрешённых для просмотра
   `extra_mounts`;
2. лениво открывает каталоги, не сканируя весь monorepo при загрузке страницы;
3. показывает текстовые файлы, с подсветкой Go, TypeScript/TSX,
   JavaScript/JSX и Python;
4. показывает diff выбранного файла между `HEAD` и текущим working tree;
5. позволяет выбрать один или несколько файлов и вставить ссылки на них в
   построчный комментарий к плану или pending-вопросу;
6. остаётся строго read-only: через dashboard нельзя создать, изменить,
   переименовать или удалить файл.

## Вне области решения

- host mode без Docker;
- редактор файлов, сохранение патчей и `git add`/commit;
- произвольное сравнение двух ревизий/веток;
- просмотр удалённых файлов, которых уже нет в working tree;
- индексированный полнотекстовый поиск по проекту;
- blame, history и рендер бинарных форматов;
- постоянное хранение UI-выбора файлов в `events.jsonl` или `state.json`;
- автоматическая вклейка содержимого файла в feedback. Агент получает путь и
  сам решает, когда читать файл.

## Зафиксированные решения

### 1. Возможность существует только в реально запущенном контейнере

Условие включения — одновременно:

- `AFM_IN_DOCKER=1`;
- host launcher передал валидный manifest browseable roots;
- `docker.file_browser.enabled` не выставлен в `false`.

На обычном host-запуске `server.Config.FileRoots` пуст, в `/api/status`
capability выключена, кнопки нет, `/api/files/*` возвращает `404`. Мы не
поддерживаем два разных security-механизма доступа к локальному filesystem:
feature намеренно Docker-only.

### 2. Project root виден по умолчанию, extra mount требует явного `browse`

Конфиг `extra_mounts` получает backward-compatible scalar-or-object форму:

```yaml
docker:
  enabled: true
  file_browser:
    enabled: true       # optional; default true только внутри Docker mode

  extra_mounts:
    # Новый source mount: виден в file browser.
    - path: ../shared-contracts
      name: contracts   # optional UI label; default = basename(path)
      browse: true

    # Credential mount: примонтирован агенту, но dashboard его не видит.
    - path: ~/.ai-free
      browse: false

    # Старый scalar-синтаксис продолжает работать и означает browse:false.
    - ~/.legacy-agent
```

Новые Go-типы в `pkg/config`:

```go
type DockerFileBrowserConfig struct {
    Enabled *bool `yaml:"enabled"`
}

type ExtraMount struct {
    Path   string `yaml:"path"`
    Name   string `yaml:"name,omitempty"`
    Browse bool   `yaml:"browse,omitempty"`
}

type ExtraMounts []ExtraMount
```

`ExtraMounts.UnmarshalYAML` принимает и scalar, и mapping. Scalar декодируется
как `{Path: value, Browse: false}`. Пустой `path`, дубли после нормализации и
`browse:true` с пустым путём — config error до запуска Docker.

Это осознанное safe-default решение: соседние code-директории получают
`browse:true`, существующие credential mount-ы не становятся публичными для
browser после простого обновления afm.

### 3. Allowlist передаёт host launcher, сервер не восстанавливает её догадками

`pkg/docker.ReExec` уже является единственным местом, которое знает точные
container target-ы всех mount-ов и раскрывает `~` относительно разных home на
host (`$HOME`) и в контейнере (`/home/afm`). Там формируется versioned manifest:

```json
{
  "version": 1,
  "roots": [
    {
      "id": "project",
      "label": "afm",
      "container_path": "/Users/alexander/work/afm",
      "mount_read_only": false,
      "kind": "project"
    },
    {
      "id": "extra-1",
      "label": "contracts",
      "container_path": "/Users/alexander/work/shared-contracts",
      "mount_read_only": true,
      "kind": "extra"
    }
  ]
}
```

В manifest входят:

- project root всегда;
- только `extra_mounts` с `browse:true`;
- никогда — `~/.claude`, global/project credential mount-ы, временная копия
  `~/.codex`, command mounts и wrapper directory.

Manifest кодируется `base64.RawURLEncoding` и передаётся как
`AFM_DOCKER_FILE_ROOTS=<value>` отдельным argv-элементом `docker run -e`.
Пути не являются секретами; кодирование нужно только для однозначного
транспорта JSON без shell quoting. В контейнере `cmd/afm/run.go` декодирует
manifest только при `AFM_IN_DOCKER=1`, проверяет version/уникальность id/
абсолютность путей и передаёт roots в `server.Config`.

Manifest, а не повторная сборка из container config, сохраняет один source of
truth: browser видит ровно те container target-ы, которые launcher реально
добавил в `docker run`.

Прямой ручной `docker run`, минующий host self-re-exec, не получает file
browser автоматически. Для него документируется либо обычный запуск через
`AFM_USE_DOCKER=1`, либо явная передача manifest. Это лучше, чем угадывать
allowlist внутри контейнера и случайно открыть не тот путь.

### 4. Сервер работает с virtual root + relative path, не с абсолютным input

Ни один endpoint не принимает filesystem path напрямую. Клиент всегда передаёт
пару:

```text
root=project&path=pkg/server/handlers.go
```

`root` ищется в immutable allowlist, а `path` обязан быть slash-separated
relative path без `..`, NUL и абсолютного префикса. Абсолютный container path
строит только сервер после успешного secure-open.

### 5. Symlink-и видны, но в первой версии не раскрываются

Feature Docker-only, значит production filesystem — Linux. Корень каждого
workspace открывается один раз как directory fd, а каждый дочерний path — через
`unix.Openat2` (`golang.org/x/sys/unix`) с:

```text
RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS | RESOLVE_NO_SYMLINKS
```

Это закрывает и обычный `../`, и symlink race между проверкой пути и чтением.
Symlink возвращается в tree как `kind:"symlink", selectable:false`, но по нему
нельзя открыть content, directory или diff. Следование по symlink можно будет
добавить отдельно, если появится реальный use case; для MVP оно не стоит новой
границы безопасности.

Linux-реализация живёт в build-tagged файле. Не-Linux реализация не создаёт
`WorkspaceFS` с непустыми roots; это соответствует Docker-only контракту и не
создаёт менее безопасный fallback через `EvalSymlinks`.

`openat2` с `RESOLVE_*` требует Linux kernel ≥ 5.6. Docker Desktop (macOS/Windows)
использует современный VM-kernel и не затронут. На Linux-host со старым kernel
`openat2` возвращает `ENOSYS` — в этом случае feature **деградирует мягко**:
`WorkspaceFS` не поднимается, `capability.file_browser=false`, в лог пишется
явная причина, остальной afm работает как обычно. Мы никогда не откатываемся на
менее безопасный check-then-open. Проба `openat2` делается один раз при
инициализации `WorkspaceFS` (например, `openat2` самого root fd на `"."`); при
`ENOSYS` roots остаются пустыми.

### 6. `.git` и `.afm` — служебные поддеревья, не пользовательские файлы

В project tree сервер скрывает любой entry с именем `.git` или `.afm` и не
разрешает сегменты с этими именами в API path. `.git` нужен серверу для
вычисления diff, но не показывается как содержимое проекта. `.afm` содержит run
state, attachments, prompt/debug logs и потенциально project secrets — его
нельзя открывать через общий file endpoint.

Для extra root это правило применяется к дочерним `.git`/`.afm`, но если
пользователь намеренно указал сам credential-каталог как root и поставил
`browse:true`, это считается явным разрешением. Legacy scalar такого разрешения
не даёт.

Остальные dotfiles, включая `.env`, не фильтруются эвристикой: надёжно отличить
секрет от исходника по имени невозможно. Project root и `browse:true` являются
явной trust boundary пользователя.

### 7. Diff означает `HEAD → current working tree`

Для выбранного regular text file сервер ищет ближайший git repository от
директории файла вверх, не выходя за текущий allowed root. Семантика:

- tracked file: baseline blob из `HEAD` сравнивается с безопасно прочитанным
  current content — staged и unstaged изменения видны вместе;
- untracked file: synthetic unified diff от `/dev/null`, весь файл считается
  добавленным;
- clean file: успешный ответ с пустым `diff`;
- repository без `HEAD`: текущий файл считается новым;
- root без доступного `.git`: `409 diff_unavailable`, UI показывает объяснение;
- binary file: metadata `binary:true`, без попытки вывести байты;
- deleted file не показывается, потому что его нет в current tree (out of scope).

Git запускается через `exec.CommandContext` с timeout 3 секунды, без shell. Он
используется только для поиска repository/HEAD и чтения baseline blob через
`git cat-file blob HEAD:<repo-relative-path>`; external diff, textconv и чтение
current path Git-ом отсутствуют. Current content уже получен через secure fd, а
unified diff строится в Go через `github.com/aymanbagabas/go-udiff`
(`udiff.Unified("HEAD:<repo-rel>", "<repo-rel>", baselineBlob, currentContent)`).
Это maintained-порт того же diff-пакета, который использует gopls (алгоритм
Myers), с нулевыми сторонними зависимостями и лицензией BSD-3/MIT; go-difflib
(2016, «mostly for testing») отклонён в его пользу. Оба текста уже в памяти —
baseline blob из `git cat-file` и current из secure fd, — поэтому Git ничего не
diff-ит сам. Это закрывает повторное небезопасное открытие пользовательского path
после проверки, особенно для untracked файла. Режим файла и rename detection в первой версии не
показываются: diff описывает содержимое выбранного current файла.

Выход baseline и готового diff ограничен. Git stderr нормализуется в стабильный
API error без выдачи лишних внутренних путей.

### 8. File reference — текст, а не новый тип attachment

Backend возвращает для открытого/выбранного файла готовый marker:

```text
[AFM file: "/Users/alexander/work/shared-contracts/api/schema.ts"]
```

Путь кодируется как JSON string, поэтому кавычки, backslash и переводы строк в
имени не ломают marker. Используется абсолютный **container path**: он одинаково
работает при любом `flow.root_dir`, тогда как project-relative путь мог бы
резолвиться из другого agent CWD.

При выборе нескольких файлов вставляется по одному marker на строку. Никакие
bytes не копируются в stage directory. Marker едет обычным текстом через уже
существующие цепочки:

```text
PlanPanel comment
  → buildFeedback
  → POST /api/stages/{id}/revise
  → feedback.md
  → следующий prompt агента

DialogChannel question comment
  → buildFeedback
  → POST /api/stages/{id}/dialog/answer
  → answer.json/tool_result
  → текущий агент
```

Контракты `revise` и `dialog/answer`, event log и FSM не меняются. Это тот же
архитектурный принцип, который уже используется для screenshot attachment:
универсальный text transport + доступный агенту filesystem path.

## Архитектура и data flow

```text
host: afm run flow.yaml
  │
  ├─ normalize project + ExtraMounts
  ├─ docker run -v project -v all-extra ...
  └─ -e AFM_DOCKER_FILE_ROOTS=<base64-json>
       │
       ▼
container: cmd/afm/run.go
  ├─ AFM_IN_DOCKER=1 + decode manifest
  └─ server.New(Config{FileRoots: roots})
       │
       ├─ GET /api/status          capability.file_browser
       ├─ GET /api/files/roots     labels + opaque ids
       ├─ GET /api/files/tree      secure lazy directory listing
       ├─ GET /api/files/reference validate selection + build marker
       ├─ GET /api/files/content   secure bounded file read
       └─ GET /api/files/diff      HEAD → working tree
              │
              ▼
dashboard FileBrowser
  ├─ browse mode from header
  └─ picker mode from plan/question comment
       └─ insert `[AFM file: "<container-path>"]`
```

## Backend

### `pkg/config` и `pkg/docker`

`DockerConfig.ExtraMounts` меняет тип с `[]string` на `ExtraMounts`, но
сохраняет старый YAML. Launcher использует только `mount.Path`, поэтому режим
монтирования существующих конфигов не меняется.

Новые pure helpers в `pkg/docker`:

```go
type FileRootManifest struct {
    Version int                `json:"version"`
    Roots   []FileRootManifestEntry `json:"roots"`
}

func BuildFileRootManifest(projectDir string, mounts config.ExtraMounts) (FileRootManifest, error)
func EncodeFileRootManifest(m FileRootManifest) (string, error)
func DecodeFileRootManifest(raw string) (FileRootManifest, error)
```

`BuildFileRootManifest` переиспользует ту же нормализацию host/container paths,
что и построение `-v`, чтобы эти две ветки не разошлись. Browseable duplicate,
вложенный в уже browseable root, остаётся отдельным root только если у него
задано другое `name`; иначе дедуплицируется.

### `pkg/server/workspace`

Filesystem-логику лучше изолировать в новом внутреннем пакете, а не добавлять
её целиком в уже большой `handlers.go`:

```text
pkg/server/workspace/
  roots.go             manifest → immutable roots, path validation
  access_linux.go      root fd + openat2
  access_other.go      Docker-only unsupported implementation
  list.go              directory listing + pagination
  content.go           bounded text reads + language detection
  diff.go              git repo discovery + unified diff
```

Публичный слой пакета:

```go
type Root struct {
    ID            string
    Label         string
    Path          string // server-only; никогда не приходит из HTTP input
    Kind          string // project | extra
    MountReadOnly bool
}

type FS interface {
    Roots() []RootView
    List(ctx context.Context, rootID, relPath, cursor string) (Page, error)
    Reference(ctx context.Context, rootID, relPath string) (Reference, error)
    Read(ctx context.Context, rootID, relPath string) (File, error)
    Diff(ctx context.Context, rootID, relPath string) (Diff, error)
    Close() error
}
```

`server.Server` хранит `workspace.FS`, а не сырые path-ы. `Server.Shutdown`
закрывает root fd. В тестах `workspace.FS` можно подменить, не поднимая Docker.

### HTTP API

`GET /api/status` получает backward-compatible поле:

```json
{
  "capabilities": {
    "file_browser": true
  }
}
```

Отсутствующий/false capability скрывает весь UI. Новые read-only endpoint-ы:

#### `GET /api/files/roots`

```json
{
  "roots": [
    {"id":"project","label":"afm","kind":"project","mount_read_only":false},
    {"id":"extra-1","label":"contracts","kind":"extra","mount_read_only":true}
  ]
}
```

Абсолютные root path-ы намеренно не выдаются списком. Полный path появляется
только в reference конкретного успешно открытого файла.

#### `GET /api/files/tree?root=project&path=pkg/server&cursor=...`

```json
{
  "entries": [
    {"name":"handlers.go","path":"pkg/server/handlers.go","kind":"file","size":38122,"language":"go","selectable":true},
    {"name":"testdata","path":"pkg/server/testdata","kind":"directory","selectable":false},
    {"name":"linked","path":"pkg/server/linked","kind":"symlink","selectable":false}
  ],
  "next_cursor": "..."
}
```

Каталоги идут первыми, затем файлы, сортировка case-insensitive с исходным
именем как tie-breaker. Page size — 500. Cursor opaque и привязан к
`root+path`; подмена cursor для другого каталога даёт `400`. Каталог читается
заново на каждом запросе, поэтому browser показывает live working tree без
server cache.

#### `GET /api/files/reference?root=project&path=pkg/server/handlers.go`

```json
{
  "path": "pkg/server/handlers.go",
  "display_path": "afm/pkg/server/handlers.go",
  "reference": "[AFM file: \"/Users/alexander/work/afm/pkg/server/handlers.go\"]"
}
```

Checkbox вызывает этот endpoint до добавления файла в selection. Сервер ещё
раз secure-open-ит path как regular file, поэтому race после directory listing
не превращает устаревшее имя в непроверенный reference. Размер и binary-тип не
мешают сослаться на файл: ограничения content preview не должны запрещать
агенту открыть большой или нетекстовый файл собственным tool-ом.

#### `GET /api/files/content?root=project&path=pkg/server/handlers.go`

```json
{
  "path": "pkg/server/handlers.go",
  "display_path": "afm/pkg/server/handlers.go",
  "reference": "[AFM file: \"/Users/alexander/work/afm/pkg/server/handlers.go\"]",
  "language": "go",
  "size": 38122,
  "modified_at": "2026-09-03T11:22:33Z",
  "content": "package server\n..."
}
```

Максимум — 2 MiB. Большой файл получает `413 file_too_large`, binary/NUL file —
`415 binary_file`. Сервер не делает silent truncation исходника: пользователь
не должен принять неполный файл за полный. Для unchanged ответа поддерживается
`ETag`; UI посылает `If-None-Match` при Reload.

Language определяется сервером по расширению, чтобы UI не дублировал mapping:

- `.go` → `go`;
- `.ts`, `.tsx` → `typescript`;
- `.js`, `.jsx`, `.mjs`, `.cjs` → `javascript`;
- `.py`, `.pyi` → `python`;
- всё остальное текстовое → `plain`.

#### `GET /api/files/diff?root=project&path=pkg/server/handlers.go`

```json
{
  "path": "pkg/server/handlers.go",
  "baseline": "HEAD",
  "status": "modified",
  "binary": false,
  "truncated": false,
  "diff": "diff --git ...\n@@ ..."
}
```

`status`: `clean | modified | added`. Diff больше 4 MiB обрезается только на
границе строки, получает `truncated:true` и явный UI banner. В отличие от
content, для diff truncation допустим: это производное представление, а полный
файл всё ещё можно открыть отдельно.

Единая таблица ошибок:

| HTTP | Code | Смысл |
|---|---|---|
| 400 | `invalid_root_or_path` | неизвестный root, absolute/`..` path, плохой cursor |
| 404 | `not_found` | файл исчез или browser выключен |
| 409 | `diff_unavailable` | нет доступного git repository |
| 413 | `file_too_large` | content превышает 2 MiB |
| 415 | `binary_file` | файл не текстовый |
| 422 | `symlink_not_supported` | попытка открыть symlink |
| 500 | `read_failed` | внутренняя I/O ошибка без утечки host detail |

Все ответы ошибок — JSON, чтобы FileBrowser мог показать причину inline, а не
только `console.error`. Это осознанное отклонение от остального сервера, где
каждый handler отвечает plain-text через `http.Error`. Чтобы отклонение не
выглядело случайной непоследовательностью, `/api/files/*` получает один
scoped-хелпер `writeFilesError(w, httpStatus, code)` (в `handlers.go` или рядом),
который используют только file-endpoint-ы; общий серверный контракт ошибок не
трогается.

## Dashboard

### Entry points и layout

`FlowHeader` получает кнопку с folder icon и `aria-label="Open project files"`.
Она рендерится только при `capabilities.fileBrowser === true` и открывает
полноэкранный `FileBrowserModal` в browse mode.

Modal состоит из:

1. слева — список roots и lazy tree;
2. справа — header выбранного файла и tabs `FILE` / `DIFF`;
3. снизу — выбранные file chips, `Copy references` в browse mode или
   `Insert references` в picker mode.

Root label всегда виден в breadcrumb, чтобы одинаковые относительные пути из
project и extra mount не выглядели одним файлом. `Esc` закрывает modal, стрелки
и Enter работают в tree, focus trap не выпускает клавиатуру под overlay.

Выбор файлов — multi-select. Активный preview-файл и множество выбранных
файлов — разные состояния: клик открывает preview, checkbox сначала получает
validated marker из `/api/files/reference`, затем добавляет/убирает его.
Выбор живёт только в React state текущей вкладки и очищается при смене run
(`flowName + startedAt`).

### Подсветка

Используется `highlight.js/lib/core` с точечными imports только четырёх
грамматик: Go, TypeScript, JavaScript, Python. Monaco/CodeMirror для read-only
MVP избыточны. `plain` рендерится обычным `<pre><code>`.

File content никогда не проходит через Markdown renderer. Highlight.js
экранирует source до генерации spans; filename/breadcrumb/error рендерятся как
обычный React text. Diff рендерится отдельным line renderer-ом (`+`, `-`, `@@`,
headers), также без `dangerouslySetInnerHTML`.

### Вставка ссылки в comment

`FileBrowserProvider` оборачивает dashboard и предоставляет два действия:

```ts
openBrowser(): void
pickFiles(onInsert: (references: string[]) => void): void
```

`PasteableTextarea` получает новый optional prop `allowFileReferences`.
При `true` рядом с thumbnail strip появляется кнопка `Attach project file`,
которая вызывает `pickFiles`. Callback вставляет markers в текущую caret
position, используя тот же controlled `value/onChange`, что и image paste.

Prop включается только в двух местах первой версии:

- `PlanPanel` — textarea построчного комментария к плану;
- `DialogChannel` — textarea построчного комментария к pending-вопросу.

`DialogChannel` содержит ДВА `PasteableTextarea` — free-form custom answer и
per-line question comment; `allowFileReferences` ставится **только** на per-line
question comment. `PasteableTextarea` также используется в `AgentNoteModal`
(третий call site), но prop по умолчанию `false`, поэтому это безопасно.

Он не включается в Agent Note modal, pre-note и custom answer: задача этой
итерации — именно review comments к плану/вопросу. Расширить те же возможности
на остальные `PasteableTextarea` позже можно одной строкой на call site.

Сохранение comment и существующие `buildFeedback` остаются без изменений.
Если во время открытого picker stage/question сменился, provider отменяет
callback и показывает `Target comment is no longer available`, а не вставляет
reference в уже другой textarea.

## Безопасность

Добавление arbitrary-file GET API существенно повышает цену ошибки, поэтому
следующие требования являются частью feature, а не последующим hardening:

1. Когда file browser включён, Docker port публикуется только на loopback:
   `-p 127.0.0.1:<port>:<port>` вместо `-p <port>:<port>`. Иначе любой хост в
   локальной сети может открыть неаутентифицированный dashboard и читать код.
   **Важно:** текущий bind — `0.0.0.0` (все интерфейсы), то есть dashboard в
   Docker mode уже сейчас доступен из LAN; это скрытая pre-existing exposure,
   которую feature закрывает. Loopback форсируется **только при включённом
   file_browser** — обычные Docker-запуски без browser сохраняют текущее
   поведение `0.0.0.0`, чтобы не сломать существующий LAN-доступ к dashboard.
   Явный публичный bind вместе с browser когда-нибудь потребует отдельной
   auth-модели и не входит в этот design.
2. Только manifest allowlist; HTTP никогда не принимает absolute path.
3. Secure fd traversal через Linux `openat2`; никаких check-then-`os.ReadFile`.
4. Symlink traversal запрещён.
5. Credential/service mounts не попадают в manifest; legacy extra mounts
   получают `browse:false`.
6. `.git`/`.afm` скрыты из общего tree API.
7. Только GET endpoint-ы, bounded reads, Git timeout и output cap.
8. Git вызывается без shell и читает только HEAD blob; current content всегда
   приходит из secure fd.
9. API ставит `Content-Type: application/json; charset=utf-8` и
   `X-Content-Type-Options: nosniff`; source не исполняется и не рендерится как
   HTML/Markdown.
10. Root paths и raw Git stderr не попадают в общие error response. Absolute
    path выдаётся только как reference после успешного открытия конкретного
    разрешённого файла.

## Изменения по файлам

| Область | Файлы | Ответственность |
|---|---|---|
| Config | `pkg/config/config.go`, tests | scalar-or-object `ExtraMounts`, `file_browser.enabled`, validation/merge |
| Docker | `pkg/docker/launcher.go`, tests | единая нормализация mount-ов, manifest, loopback `-p` |
| Wiring | `cmd/afm/run.go` | decode manifest только в container, `server.Config.FileRoots` |
| Secure FS | `pkg/server/workspace/*` | allowlist, openat2, list/content/diff/limits |
| HTTP | `pkg/server/server.go`, `handlers.go`, tests | routes, capability, JSON contracts |
| API client | `pkg/web/dashboard/src/api/files-client.ts` | typed roots/tree/content/diff fetches |
| UI state | `components/file-browser/*` | provider, modal, tree, viewer, selection |
| Comments | `PasteableTextarea`, `PlanPanel`, `DialogChannel` | picker entry point + caret insertion |
| Highlighting | dashboard `package.json`, viewer CSS | selective highlight.js languages + diff lines |
| Docs | `README.md`, `config.example.yaml` | Docker-only behavior, `browse:true`, limits/security |

## Ошибки и конкурентные изменения

Файлы меняет живой агент, поэтому snapshot consistency между tree/content/diff
не гарантируется и не должна симулироваться:

- файл исчез между list и open → `404`, tree node удаляется после refresh;
- файл изменился между FILE и DIFF → каждый endpoint показывает свой свежий
  результат, UI выводит `modified_at` и кнопку Reload;
- root mount пропал → root остаётся в manifest, но получает unavailable state;
- выбранный reference не проверяется повторно при отправке feedback: это путь,
  а не snapshot содержимого. Агент либо прочитает новую версию, либо сообщит,
  что файл исчез.

## Тестирование

### Go

- config: legacy scalar остаётся mount-ом и `browse:false`; object parsing;
  merge/validation; порядок сохраняется;
- launcher: manifest содержит project + только `browse:true`; credential,
  command и codex mounts отсутствуют; `~` превращается именно в container home;
  Docker publish использует `127.0.0.1`;
- manifest: bad version, duplicate id, relative path, corrupt base64;
- workspace (Linux, build-tagged `//go:build linux` — не запускаются на macOS
  dev-host, только в CI/Docker): happy-path list/reference/read; `../`, absolute
  path, encoded traversal; symlink наружу/внутрь; symlink race; `.git`/`.afm`;
  missing file; large/binary; reference на large/binary всё равно разрешён;
  `openat2` `ENOSYS` → capability off, а не крэш;
- diff: tracked staged+unstaged, untracked, clean, no repo, repo without HEAD,
  nested repo, binary, timeout/output cap;
- handlers: disabled → 404/capability false; enabled wire shape; стабильные JSON
  errors без абсолютного root path.

### Frontend

- capability false скрывает header/comment buttons и не вызывает files API;
- lazy tree, pagination, roots с одинаковыми basename, loading/error/retry;
- extension → ожидаемая grammar; plain fallback; source HTML не исполняется;
- FILE/DIFF switch, clean/binary/unavailable/truncated states;
- multi-select, deselect, Copy, deterministic order markers;
- insertion exactly at caret and preservation of surrounding comment text;
- picker работает в PlanPanel и question line comment, но отсутствует в
  pre-note/AgentNote/custom answer;
- смена stage/question отменяет stale picker target;
- keyboard/focus/Escape accessibility.

### Docker smoke/E2E

Запуск с тремя mount-ами:

1. project (browseable);
2. соседний code root с `browse:true` (browseable, read-only);
3. fake credential root в legacy scalar-форме (mounted агенту, отсутствует в
   `/api/files/roots`).

Проверить в реальном browser: открыть Go/TS/JS/Python, увидеть подсветку и diff,
выбрать project + extra file, вставить оба markers в plan comment и question
comment, убедиться по `feedback.md`/`answer.json`, что агент получил точные
container path-ы.

## Отклонённые варианты

### Отдать произвольный абсолютный path из query

Минимум кода, но любой path traversal/ошибка UI открывает `~/.afm`,
`~/.claude`, `/proc` или root filesystem образа. Отклонено: opaque root +
relative path — обязательная граница.

### Автоматически показать все старые `extra_mounts`

Нарушает backward security: существующие конфиги используют эти mounts для
токенов. Отклонено в пользу `browse:true`; scalar остаётся private.

### Копировать выбранный файл в stage attachments

Создаёт stale copy, расходует место, теряет связь с live working tree и особенно
плохо работает для больших файлов. Агент уже видит исходный mount. Отклонено в
пользу ссылки на container path.

### Вклеивать содержимое файла в feedback

Раздувает `feedback.md`, `answer.json`, prompt и event history; выбранный файл
может оказаться очень большим. Агент умеет читать path собственным tool-ом.
Отклонено.

### Monaco/CodeMirror

Это хорошие editor-компоненты, но здесь нет редактирования. Для четырёх
грамматик selective highlight.js заметно меньше по bundle/сложности. Вернуться
к editor-компоненту стоит только вместе с line selection или editing.

### Вычислять diff в browser

Browser не имеет ни HEAD blob, ни git index/status; пришлось бы сначала отдать
лишние данные и продублировать git semantics на TypeScript. Отклонено: diff —
серверная производная от уже доступного в контейнере repository.

## Критерии готовности

Решение готово, когда в Docker mode пользователь может открыть folder button,
увидеть project и каждый `browse:true` extra root, безопасно прочитать файлы и
`HEAD → working tree` diff, выбрать несколько файлов и вставить их абсолютные
container references в комментарии к плану и вопросу; при этом host mode,
legacy credential mounts и существующие revise/dialog протоколы остаются без
изменений.

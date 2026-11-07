# Server and workspace

Пользовательское поведение: [dashboard](../../docs/dashboard.md),
[Docker file browser](../../docs/docker.md#project-file-browser).

## API и необязательный UI

- API-сервер не импортирует `pkg/web`: UI — необязательный `http.Handler`, который
  передаёт launcher (`cmd/afm`). Без него `/` отвечает 404, а `/api/*` работает.
  Новый код API не должен требовать UI; регресс импорта ломает режим core.
- `/api/runtime` — GET-only, `no-store`, без путей, токенов и PID: протокол, режим,
  `run_id` и `ready`. Нулевой `RuntimeInfo` читается как «dashboard, не готов».
- `ready` берётся из `engine.Run.Ready` (транспорты привязаны, `Start` прошёл), а не
  из факта открытого порта: сервер слушает раньше, чем ран готов принимать работу.

## Stage views and actions

- `StageConfig` хранит настройки, `StageView` — runtime-состояние. Button API
  принимает только label и резолвит prompt на сервере; `Buttons` сохраняет
  YAML-порядок. Pre-note разрешён только pending non-script стадии.
- Не показывай постоянный `PausedFrom` как текущую паузу: поле view заполняется
  только для paused. ShowPlan/attention должны позволять Continue для auto-стадий.
- Dialog layout зависит от реальной истории или awaiting-user-input и должен
  совпадать с клиентским `hasContent`, иначе появляется пустая панель.
- Verify-report читай относительно `os.OpenRoot(runDir)`; client-supplied ID и
  путь из notice не должны давать произвольное чтение файлов.

## Stage order in the dashboard

Display-order — стабильный топологический порядок зависимостей с исходным
`StageOrder` как tie-breaker. Не переставляй authoritative `RunState.StageOrder`
ради UI. Передавай зависимости и для динамически добавленных стадий.

## Workspace security boundary

- Разрешённые корни приходят из host-манифеста. Extra-mount без `browse: true`
  приватен; ответы API не раскрывают абсолютные root-пути или raw git stderr.
- Проверяй сырой rel-path до Clean; блокируй traversal, absolute/NUL и служебные
  `.git`/`.afm`. Доступ — через root fd и Linux `openat2` с containment и запретом
  symlinks. Без поддержки capability выключается; слабого fallback нет.
- Read/reference открывают только regular files с nonblocking-флагом. Сохраняй
  лимиты размера, проверку binary/UTF-8, ETag и таймауты git-операций.
- Git metadata и worktree-ссылки проверяются общей политикой workspace; не
  обходи secure resolver ради diff/search. Файловый API остаётся GET-only.
- Linux-tagged workspace-тесты выполняй на Linux/Docker: macOS их не покрывает.

# Docker transport

Настройка и рецепты: [docs/docker.md](../../docs/docker.md).

## Container auto-detection

`InContainer()` запрещает nested re-exec и host browser-open в любом контейнере.
`ReExecedIntoContainer()` разрешает потребление собственного afm-транспорта:
wrappers, hook secrets и file-root manifest. Маркер чужого контейнера не даёт
этих данных; не подменяй один predicate другим.

## PID 1

`docker run` идёт с `--init`: PID 1 — tini, он reap'ает осиротевших потомков
агентов. afm не ждёт процессы, которых не запускал; без `--init` убитые в конце
хода потомки копились бы зомби. tini пробрасывает сигналы и код выхода afm.

## Mounts and wrappers

- Пути mount и file-root manifest должны вычисляться одним резолвером. Legacy
  extra-mount приватен; browseable только явный `browse: true`. При file browser
  dashboard-порт публикуется на loopback.
- Не монтируй read-only одиночный файл, который приложение заменяет rename
  (например, `~/.claude.json`). HOME задаётся после gosu: неизвестный uid иначе
  получает `/`. TTY проверяется `term.IsTerminal`, не `os.ModeCharDevice`.
- Секреты и system prompt резолвятся на хосте и передаются через bare `-e NAME`.
  Секретные значения не должны оказаться в argv. Файл под смонтированным корнем
  всё равно доступен процессам контейнера — транспорт не изолирует такой файл.
- Wrapper discovery учитывает команды verify и других запускаемых агентов;
  Codex binary резолвится ДО добавления wrapper-каталога в PATH, иначе рекурсия.
- Lifecycle hook transport использует позиции в общем combined-hooks списке и
  sorted env keys. Host и контейнер должны иметь один порядок, включая overrides.

При изменении browser/hook-транспорта читай [browser](../browser/AGENTS.md) и
[lifecyclehooks](../lifecyclehooks/AGENTS.md). Релизные образы собирает
`.github/workflows/release.yml`; `make docker-build/docker-push` — локальная
single-arch сборка. Пины меняются согласованно с host-адаптерами.

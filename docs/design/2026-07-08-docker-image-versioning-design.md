# Дизайн: версионирование Docker-имиджа afm (SemVer + авто-бамп)

**Дата:** 2026-07-08
**Статус:** утверждён (brainstorming), ожидает плана реализации
**Ветка:** `docker-image-versioning`

## Цель

Сейчас `make docker-push` собирает и пушит только `akopichin/afm:latest` — невозможно
выдать людям конкретный иммутабельный тег (каждый паблиш перезаписывает `:latest`). Нужно:
каждый релиз получает уникальный SemVer-тег `:vX.Y.Z` (иммутабельный, не меняется при
следующих паблишах) + `:latest` как rolling-указатель. Плюс — версия видна внутри имиджа
(`afm --version`), чтобы можно было понять, что именно в нём.

## Решения (из brainstorming)

- **Схема версии:** SemVer (`vX.Y.Z`) через git-теги.
- **Триггер релиза:** `make release-patch` / `release-minor` / `release-major` — авто-бамп
  соответствующего уровня от последнего git-тега.
- **Скоуп:** только docker-имидж, через Makefile (НЕ goreleaser). Без публикации бинарников,
  без SCM-релиза, без `GITLAB_TOKEN`.
- **Версия в бинарнике:** `afm --version` (cobra `Version`), вшивается через `-ldflags`.
- `:latest` остаётся rolling; `make docker-push` остаётся dev-only `:latest`.

## Контекст (текущее состояние)

- **Makefile** (`docker-build`/`docker-push`/`docker-run`): `DOCKER_IMAGE := akopichin/afm`,
  `DOCKER_TAG := latest` → пушит только `:latest`.
- **`.goreleaser.yml`**: секция `dockers` уже строит `akopichin/afm:{{ .Tag }}` + `:latest`, но
  только при `goreleaser release` по git-тегу (с бинарниками и SCM-релизом). Этим путём не пользуемся.
- **`Dockerfile.runtime`**: `go build -o /afm ./cmd/afm` — без `-ldflags`, версия не вшита.
- **`cmd/afm/main.go`**: нет переменной версии, нет `--version`.
- Module-path `github.com/akopichin/afm` (GitLab) → полный goreleaser потребовал бы `GITLAB_TOKEN`.

## Архитектура

Релиз идёт через Makefile + `scripts/release.sh`. Скрипт: читает последний SemVer git-тег,
бампит уровень, собирает имидж с двумя тегами (`:vX.Y.Z` + `:latest`), пушит оба, и только
после успешного пуша создаёт локальный git-тег. Версия вшивается в бинарник через build-arg
+ ldflags.

### Data flow

```
make release-patch
  → ./scripts/release.sh patch
    → последний тег v1.2.3 → next v1.2.4
    → проверка: чистое дерево
    → docker build --build-arg AFM_VERSION=v1.2.4 \
        -t akopichin/afm:v1.2.4 -t akopichin/afm:latest -f Dockerfile.runtime .
    → docker push akopichin/afm:v1.2.4 && docker push akopichin/afm:latest
    → git tag -a v1.2.4 -m "Release v1.2.4"   (только после успешного пуша)
  → docker run akopichin/afm:v1.2.4 afm --version  →  "afm version v1.2.4"
```

## Компоненты (точные изменения)

### 1. `cmd/afm/main.go` — версия в бинарнике

- Добавить `var version = "dev"` на уровне пакета.
- В `newRootCmd`: `root.Version = version` — cobra автоматически регистрирует флаг `--version`
  (печатает `afm version <version>` и выходит). При `version == "dev"` (локальная сборка без
  ldflags) флаг всё равно работает.

### 2. `Dockerfile.runtime` — проброс версии в сборку

- Добавить `ARG AFM_VERSION=dev` (в стадии builder, перед `go build`).
- Заменить `RUN CGO_ENABLED=0 go build -o /afm ./cmd/afm`
  на `RUN CGO_ENABLED=0 go build -ldflags="-X main.version=$AFM_VERSION" -o /afm ./cmd/afm`.

### 3. `Makefile`

- `VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`.
- `build`: добавить `-ldflags "-X main.version=$(VERSION)"` (локальный бинарник тоже показывает версию).
- `docker-build`: `docker build --build-arg AFM_VERSION=$(VERSION) -f Dockerfile.runtime -t $(DOCKER_IMAGE):$(DOCKER_TAG) .`.
- Новые таргеты:
  ```make
  .PHONY: release-patch release-minor release-major
  release-patch:
  	./scripts/release.sh patch
  release-minor:
  	./scripts/release.sh minor
  release-major:
  	./scripts/release.sh major
  ```

### 4. `scripts/release.sh` (новый, `#!/bin/sh`, POSIX) — бамп + релиз

```sh
#!/bin/sh
set -e

level="$1"
case "$level" in patch|minor|major) ;; *)
    echo "usage: $0 {patch|minor|major}" >&2; exit 2 ;;
esac

# последний SemVer-тег v[0-9]* (несемверные/экспериментальные игнорируем), или v0.0.0
latest=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null | tail -1 || true)
[ -n "$latest" ] || latest=v0.0.0
latest=${latest#v}
major=$(echo "$latest" | cut -d. -f1); minor=$(echo "$latest" | cut -d. -f2); patch=$(echo "$latest" | cut -d. -f3)

case "$level" in
  major) major=$((major+1)); minor=0; patch=0 ;;
  minor) minor=$((minor+1)); patch=0 ;;
  patch) patch=$((patch+1)) ;;
esac
next="v$major.$minor.$patch"

# не релизить грязное состояние
if [ -n "$(git status --porcelain)" ]; then
    echo "error: working tree not clean — commit/stash first" >&2; exit 1
fi
# предохранитель от повторного тега (если тег уже существует)
if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
    echo "error: tag $next already exists" >&2; exit 1
fi

echo "releasing $next"
docker build --build-arg AFM_VERSION="$next" \
    -t akopichin/afm:"$next" -t akopichin/afm:latest -f Dockerfile.runtime .
docker push akopichin/afm:"$next"
docker push akopichin/afm:latest

# тег создаём только после успешного пуша
git tag -a "$next" -m "Release $next"
echo "released $next (tag created locally; push to remote: git push origin $next)"
```

Порядок **build → push → tag** критичен: при сбое пуша git-тег не создаётся → повторный
запуск пересчитает тот же `next` (тега нет) и ретрайит идемпотентно.

### 5. `.goreleaser.yml`

- Убрать секцию `dockers:` (docker теперь дело Makefile). Бинарная конфигурация (`builds`/
  `archives`/`checksum`) остаётся как бездействующая — на случай будущих релизов бинарников.

### 6. Документация

- `CLAUDE.md` (раздел «Публикация нового образа»): описать `make release-{patch,minor,major}`
  (версионированный релиз), пояснить, что `make docker-push` — dev-only `:latest`.
- `release-notes.md`: новая запись.

## Обработка ошибок

- **Грязное дерево** → `release.sh` падает до билда/тега.
- **Нет предыдущих тегов** → старт с `v0.0.0` → `v0.0.1` / `v0.1.0` / `v1.0.0`.
- **Не-SemVer теги** → игнорируются (`--match 'v[0-9]*'`).
- **Тег уже существует** → явная ошибка (предохранитель).
- **Сбой `docker build`/`push`** → `set -e` прерывает; git-тег не создаётся (он в конце) →
  повторный запуск пересчитает тот же `next` и ретрайит.
- **`docker login`** уже настроен (текущий `make docker-push` работает) — без изменений.

## Тестирование

- **`afm --version`**: Go-тест, что `newRootCmd().Version == version` и флаг `--version`
  зарегистрирован; плюс `make build && ./bin/afm --version` показывает git-describe (не «dev»).
- **`release.sh` (бамп)**: `sh -n` + `shellcheck`; вручную — во временной git-репе создать
  тег `v1.2.3`, прогнать логику бампа и проверить `v1.2.4`/`v1.3.0`/`v2.0.0`. (Бамп — простая
  shell-арифметика; отдельный Go-хелпер — overkill.)
- **E2E**: `make release-patch` (против тестового image-имени или локально) → оба тега
  запушены, `afm --version` в имидже = тегу.

## Трейд-оффы / обратная совместимость

- ✅ Каждый релиз = уникальный immutable `:vX.Y.Z` + rolling `:latest`; старые теги не перезаписываются.
- ✅ Версия видна внутри имиджа (`afm --version`).
- ✅ Без токенов/SCM-релиза (только `docker login`).
- ⚠️ Имидж остаётся `linux/amd64` только (multi-arch — отдельная задача).
- ⚠️ Git-тег создаётся **локально**; push тега в remote — опциональный ручной шаг.

## Не в скоупе

- Multi-arch (amd64+arm64).
- Публикация бинарников/архивов и GitLab-релизов (goreleaser) — не используем.
- Авто-push git-тега в remote.

## Ссылки

- afm: `Makefile`, `Dockerfile.runtime`, `.goreleaser.yml`, `cmd/afm/main.go`, `CLAUDE.md` → Docker Mode.

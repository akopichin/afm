# GitHub CI + автоматический релиз при push в main

## Контекст

Проект `afm` синхронизируется из приватного `~/work/flowManager` (GitLab) через
`sync-upstream.sh` — очищает внутренние упоминания и публикует идентичный код.
Сейчас (2026-07-22) синк сделан, закоммичен и запушен: содержимое двух репозиториев
идентично.

`afm` (GitHub, `akopichin/afm`) на данный момент:
- имеет только теги `v0.2.1`/`v0.2.2` (синкались редко, истории версий нет);
- не имеет `.github/workflows` — нет CI вообще;
- уже содержит `.goreleaser.yml` для сборки бинарников (не используется — нет CI,
  который бы его запускал);
- релиз docker-образа (`akopichin/afm` на Docker Hub) делается вручную:
  `make release-patch/minor/major` → `scripts/release.sh` бампит SemVer от
  последнего тега, собирает мультиарх (`linux/amd64,linux/arm64` через buildx),
  пушит `:vX.Y.Z` + `:latest`, создаёт и пушит git-тег.

`flowManager` (GitLab, приватный) — на `v0.5.6`, с полной историей тегов от
`v0.1.0`, использует внутренний GitLab CI (`.gitlab-ci.yml` с `include` на
внутренние конфиги — непереносим на GitHub).

## Цель

1. Пометить текущий HEAD в afm тегом `v0.5.6` (без восстановления промежуточной
   истории тегов — она не нужна, раз в afm её и не было).
2. Настроить GitHub Actions так, чтобы:
   - на **любую** ветку (push/PR) — сборка + тесты + линт;
   - на **push в main** — то же самое, плюс автоматический релиз: бамп
     patch-версии, docker-образ (мультиарх) в Docker Hub, бинарники и
     GitHub Release, обновление Homebrew tap — чтобы `brew install akopichin/afm`
     всегда ставил актуальную версию.
3. Дальнейшая разработка ведётся в afm (GitHub) как в основном репозитории.
   `flowManager`/`sync-upstream.sh` не трогаем — остаются как есть, вне
   рамок этой задачи.

## Архитектура

Два workflow-файла с чётким разделением ответственности:

```
push/PR (любая ветка) ──► ci.yml: validate (build+test+lint)
                                │
                                ▼ (только push в main, после validate)
                           ci.yml: auto-release-tag
                                │  (scripts/release.sh patch,
                                │   push тега ЧЕРЕЗ PAT)
                                ▼
push тега vX.Y.Z (авто ИЛИ ручной minor/major) ──► release.yml: release
                                                        │
                                    ┌───────────────────┼───────────────────┐
                                    ▼                    ▼                   ▼
                              docker buildx        goreleaser          goreleaser
                              (мультиарх push      (бинарники +        (brews: пуш
                               в Docker Hub)         GitHub Release)     формулы в
                                                                         homebrew-afm)
```

Ключевое архитектурное решение: **тег — единственная точка входа в реальный
релиз**. Не важно, создан ли тег автоматически (push в main) или вручную
(`make release-minor`/`release-major`) — `release.yml` реагирует на сам тег и
делает одно и то же. Это исключает дублирование логики сборки docker-образа и
бинарников в двух местах.

### Почему нужен PAT для пуша авто-тега

GitHub по умолчанию не даёт событиям, вызванным пушем через встроенный
`GITHUB_TOKEN`, запускать другие workflow (защита от бесконечных циклов
workflow→workflow). Если `auto-release-tag` запушит тег дефолтным токеном,
`release.yml` просто не запустится. Поэтому checkout в этой job делается с
`token: secrets.RELEASE_TOKEN` (fine-grained PAT) — пуш от имени PAT
рассматривается как внешний и триггерит `release.yml` штатно.

### Гонки при частых push в main

Если два push в main происходят быстро друг за другом, оба run'а
`auto-release-tag` могут прочитать один и тот же "последний тег" и попытаться
создать одинаковую следующую версию. `scripts/release.sh` уже страхует это
(guard "tag already exists" → exit 1), но чтобы не превращать это в красный
крестик на ровном месте, job использует `concurrency: group: main-release` —
запуски на main для этой job выполняются строго последовательно.

## Компоненты

### `.github/workflows/ci.yml`

Триггеры: `push` (любая ветка), `pull_request`.

**Job `validate`** (всегда):
- `actions/setup-go@v5` с `go-version: '1.26'` (точно как в `go.mod` — версия
  Go не меняется без явного предупреждения, см. правило в CLAUDE.md).
- `actions/setup-node@v4` (v22 — соответствует версии, используемой для сборки
  `pkg/web/dashboard`).
- `make build` (полная сборка, включая фронтенд — ловит поломки дашборда, а не
  только Go-кода).
- `make test` (`go test ./... -v -race`).
- `make bindeps` (тянет `golangci-lint v2.11.4`, как локально) → затем
  `golangci-lint run` **без** `--fix` (в отличие от локального `make lint`:
  auto-fix в CI молча чинит и делает job зелёным даже при реальных проблемах —
  CI должен падать явно) → `setstatuslinter ./pkg/...`.

**Job `auto-release-tag`** (`needs: validate`,
`if: github.ref == 'refs/heads/main' && github.event_name == 'push'`):
- `concurrency: group: main-release, cancel-in-progress: false`.
- `actions/checkout@v4` с `token: ${{ secrets.RELEASE_TOKEN }}` и
  `fetch-depth: 0` (нужна полная история тегов для `git describe`).
- `git config user.name`/`user.email` (бот-идентити, например
  `afm-release-bot` / `afm-release-bot@users.noreply.github.com`).
- `./scripts/release.sh patch`.

### `scripts/release.sh` (упрощается)

Убирается вся docker-логика (buildx/login/push). Остаётся:
- парсинг уровня (`patch`/`minor`/`major`) и `--dry-run`;
- вычисление следующей версии от последнего тега `v[0-9]*`;
- guard на грязное дерево, guard на существующий тег;
- `git tag -a "$next" -m "Release $next"`;
- `git push origin "$next"`.

`make release-patch/minor/major` в Makefile не меняются по интерфейсу — вызывают
тот же скрипт. Ручной `release-patch` теперь используется редко (patch и так
автоматический на каждый push в main), но остаётся рабочим для ситуаций вне
основного потока.

### `.github/workflows/release.yml`

Триггер: `push`, `tags: ['v*.*.*']`.

Job `release`:
- `actions/checkout@v4`, `fetch-depth: 0`.
- `actions/setup-go@v5` (`go-version: '1.26'`).
- `docker/setup-buildx-action@v3`.
- `docker/login-action@v3` с `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN`.
- Мультиарх docker build+push: `docker buildx build --platform
  linux/amd64,linux/arm64 --build-arg AFM_VERSION=${{ github.ref_name }}
  -f Dockerfile.runtime -t akopichin/afm:${{ github.ref_name }}
  -t akopichin/afm:latest --push .`
- `goreleaser/goreleaser-action@v6` с `args: release --clean`, `env:`
  `GITHUB_TOKEN` (дефолтный — создание GitHub Release в этом же репо) и
  `RELEASE_TOKEN` (для кросс-репо пуша Homebrew formula, см. ниже).

Ассеты дашборда (`pkg/web/dashboard/index.html`, `assets/*` и т.д.) уже
закоммичены в git — `go:embed` берёт их из чекаутнутого дерева, поэтому
Node/npm в этом workflow не нужен.

### `.goreleaser.yml` (дополняется)

Существующие `builds`/`archives`/`checksum` не меняются. Добавляется:

```yaml
brews:
  - name: afm
    directory: Formula
    homepage: "https://github.com/akopichin/afm"
    description: "AI flow manager — orchestrates multi-stage agent runs"
    repository:
      owner: akopichin
      name: homebrew-afm
      branch: main
      token: "{{ .Env.RELEASE_TOKEN }}"
    commit_author:
      name: afm-release-bot
      email: afm-release-bot@users.noreply.github.com
    test: |
      system "#{bin}/afm", "--version"
```

`license` не указывается — в репозитории пока нет файла `LICENSE`; можно добавить
позже отдельной задачей.

### Homebrew tap: `akopichin/homebrew-afm`

Новый пустой публичный репозиторий. Заполняется автоматически goreleaser при
первом релизе (создаёт `Formula/afm.rb`). После этого `brew install
akopichin/afm` (Homebrew-соглашение `user/repo` → тап `user/homebrew-repo`)
работает и подтягивает актуальную версию при каждом релизе.

## Секреты и разовая настройка (репозиторий `akopichin/afm` → Settings → Secrets and variables → Actions)

Однин PAT на оба репозитория (проще в поддержке, чем два раздельных):

| Секрет | Назначение | Как получить |
|---|---|---|
| `RELEASE_TOKEN` | fine-grained PAT, `contents:write` на `akopichin/afm` **и** `akopichin/homebrew-afm` — пуш авто-тега (обходит anti-loop защиту GITHUB_TOKEN) + пуш Homebrew formula | github.com/settings/personal-access-tokens/new |
| `DOCKERHUB_USERNAME` | логин в Docker Hub из CI | существующий аккаунт `akopichin` на hub.docker.com |
| `DOCKERHUB_TOKEN` | access token (не пароль) для `docker/login-action` | hub.docker.com → Account Settings → Security → New Access Token |

Эти секреты нужно завести **до** того, как коммит с новыми workflow попадёт в
main — иначе первый же push (сам этот коммит) запустит `auto-release-tag` и
упадёт на недостающих секретах. Добавляются пользователем вручную (через `!
gh secret set ...` в терминале), не через вставку значения в чат.

Репозиторий `akopichin/homebrew-afm` создаётся (`gh repo create
akopichin/homebrew-afm --public`) тоже до первого релиза.

## Порядок внедрения

1. Тег `v0.5.6` на текущий HEAD, push — делается отдельно, до появления
   workflow-файлов (чтобы не быть внезапно подхваченным `release.yml`, которого
   ещё не существует — на этом шаге он гарантированно не существует).
2. Создать репозиторий `akopichin/homebrew-afm`.
3. Пользователь заводит секреты `RELEASE_TOKEN`, `DOCKERHUB_USERNAME`,
   `DOCKERHUB_TOKEN`.
4. Добавить `.github/workflows/ci.yml`, `.github/workflows/release.yml`,
   упростить `scripts/release.sh`, дополнить `.goreleaser.yml`.
5. Push в main — сам этот push станет первым авто-релизом (`v0.5.7`),
   что одновременно служит интеграционным тестом всего пайплайна.

## Тестирование

- Локально: `./scripts/release.sh --dry-run patch` — печатает следующую версию
  без побочных эффектов (эта логика уже существует и не меняется).
- `.goreleaser.yml` можно проверить локально: `goreleaser release --snapshot
  --clean` (без пуша, без тега) — убеждаемся, что бинарники собираются и
  brews-конфиг синтаксически валиден.
- End-to-end: сам push шага 5 выше — если `v0.5.7` появился на Docker Hub,
  в GitHub Releases и в `akopichin/homebrew-afm`, пайплайн подтверждён рабочим.
- Проверка отката: если `auto-release-tag` или `release` job упадёт — `main`
  всё равно содержит рабочий код (validate уже прошёл), просто релиз не
  случился; следующий push повторит попытку с той же (неизменившейся)
  следующей версией.

## Вне рамок задачи

- `flowManager` и `sync-upstream.sh` не меняются.
- Не создаётся `LICENSE` файл (можно добавить отдельно).
- Не переносится история промежуточных тегов `v0.1.0`–`v0.5.5`.

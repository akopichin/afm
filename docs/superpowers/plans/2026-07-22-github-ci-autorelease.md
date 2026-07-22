# GitHub CI + автоматический релиз при push в main — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Тег `v0.5.6` на текущий HEAD в `afm`, плюс GitHub Actions пайплайн: валидация (build+test+lint) на любую ветку, и полный авто-релиз (patch-версия, мультиарх docker-образ, бинарники, GitHub Release, Homebrew formula) на каждый push в `main`.

**Architecture:** Тег — единственная точка входа в реальный релиз. `ci.yml` валидирует всегда и на push в main дополнительно бампает+пушит следующий patch-тег (через PAT, не дефолтный `GITHUB_TOKEN` — иначе тег не запустит другой workflow). `release.yml`, триггерясь по любому тегу `v*.*.*` (авто или ручной), делает всю фактическую сборку: docker multi-arch push + goreleaser (бинарники + GitHub Release + Homebrew tap).

**Tech Stack:** GitHub Actions, GoReleaser v2, Docker Buildx, Go 1.26, Homebrew tap.

## Global Constraints

- Go-версия в CI — ровно `1.26`, как в `go.mod`. Никогда не менять версию Go без явного предупреждения пользователю.
- Лог CI-линтера — БЕЗ `--fix` (в отличие от локального `make lint`): CI должен падать явно на проблемах, а не молча их чинить.
- Коммиты — на русском языке, без `Co-Authored-By`.
- Не пушить в `origin main` до последнего таска этого плана — до тех пор, пока не заведены секреты `RELEASE_TOKEN`/`DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` и не создан репозиторий `akopichin/homebrew-afm`, push в main запустит `auto-release-tag`, который упадёт на отсутствующих секретах/репозитории. Локальные коммиты между тасками — нормально, `git push origin main` — только в Task 8.
- `flowManager` и `sync-upstream.sh` не трогать — вне рамок задачи.
- Формула Homebrew — без поля `license` (в репозитории нет файла `LICENSE`).

---

### Task 1: Тег v0.5.6 на текущий HEAD

**Files:** нет (только git-тег, без изменений кода).

- [ ] **Step 1: Убедиться, что дерево чистое**

Run: `git status --short`
Expected: пустой вывод (нет незакоммиченных изменений).

- [ ] **Step 2: Создать аннотированный тег**

Run: `git tag -a v0.5.6 -m "Release v0.5.6"`

(Аннотированный — так же, как теги в upstream `flowManager`, где `git cat-file -t v0.5.6` → `tag`.)

- [ ] **Step 3: Запушить тег**

Run: `git push origin v0.5.6`
Expected: `* [new tag]         v0.5.6 -> v0.5.6`

- [ ] **Step 4: Проверить на GitHub**

Run: `git ls-remote --tags origin | grep v0.5.6`
Expected: строка с хэшем текущего HEAD и `refs/tags/v0.5.6`.

На этом шаге в репозитории ещё нет `.github/workflows` — тег не может ничего
случайно запустить.

---

### Task 2: gh CLI, аутентификация, tap-репозиторий

**Files:** нет.

**⚠️ Требует участия человека:** `gh auth login` — интерактивный OAuth-флоу,
подагент не может пройти его сам. Эти шаги выполняет пользователь.

- [ ] **Step 1: Проверить/поставить gh CLI**

Run: `which gh || brew install gh`
Expected: путь к бинарнику `gh` (Homebrew уже стоит: `Homebrew 6.0.11`).

- [ ] **Step 2: Аутентифицироваться (пользователь запускает сам)**

Попросите пользователя выполнить в терминале (не через Bash-тул агента —
интерактивный OAuth):
```
! gh auth login
```
Expected после выполнения: `gh auth status` показывает `Logged in to github.com as akopichin`.

- [ ] **Step 3: Проверить аутентификацию**

Run: `gh auth status`
Expected: `✓ Logged in to github.com account akopichin`

- [ ] **Step 4: Создать публичный tap-репозиторий**

Run: `gh repo create akopichin/homebrew-afm --public --description "Homebrew tap for afm"`
Expected: `✓ Created repository akopichin/homebrew-afm on github.com`

- [ ] **Step 5: Проверить**

Run: `gh repo view akopichin/homebrew-afm --json name,visibility`
Expected: `{"name":"homebrew-afm","visibility":"PUBLIC"}`

---

### Task 3: Упростить scripts/release.sh (убрать docker-логику)

**Files:**
- Modify: `scripts/release.sh` (полная замена содержимого)

**Interfaces:**
- Consumes: ничего нового.
- Produces: `./scripts/release.sh [--dry-run] {patch|minor|major}` — теперь
  ТОЛЬКО вычисляет следующую версию, коммитит тег `vX.Y.Z` и пушит его.
  Больше не собирает и не пушит docker-образ — это стало ответственностью
  `.github/workflows/release.yml` (Task 6), реагирующего на сам факт пуша тега.

- [ ] **Step 1: Заменить содержимое scripts/release.sh**

```sh
#!/bin/sh
# Бампит SemVer от последнего git-тега и пушит новый тег vX.Y.Z.
# Сама сборка (docker-образ, бинарники, Homebrew formula) происходит в
# GitHub Actions (.github/workflows/release.yml), которая реагирует на пуш
# этого тега — единая точка входа в релиз что для авто-патча из CI (push в
# main), что для ручного minor/major отсюда.
# --dry-run: только напечатать следующую версию (без git tag/push).
set -e

if [ "$1" = "--dry-run" ]; then dry=1; shift; else dry=0; fi
level="$1"
case "$level" in
    patch|minor|major) ;;
    *) echo "usage: $0 [--dry-run] {patch|minor|major}" >&2; exit 2 ;;
esac

# последний SemVer-тег v[0-9]* (несемверные/экспериментальные игнорируются), или v0.0.0
latest=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
[ -n "$latest" ] || latest=v0.0.0
latest=${latest#v}
major=$(echo "$latest" | cut -d. -f1)
minor=$(echo "$latest" | cut -d. -f2)
patch=$(echo "$latest" | cut -d. -f3)

case "$level" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
esac
next="v$major.$minor.$patch"

if [ "$dry" = 1 ]; then
    echo "$next"
    exit 0
fi

# не релизить грязное состояние
if [ -n "$(git status --porcelain)" ]; then
    echo "error: working tree not clean — commit/stash first" >&2
    exit 1
fi
# предохранитель от повторного тега
if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
    echo "error: tag $next already exists" >&2
    exit 1
fi

echo "tagging $next (latest was ${latest:-none})"
git tag -a "$next" -m "Release $next"
git push origin "$next"
echo "tagged and pushed $next — release.yml will build+publish it"
```

- [ ] **Step 2: Сделать исполняемым (на случай сброса прав при replace)**

Run: `chmod +x scripts/release.sh`

- [ ] **Step 3: Проверить dry-run**

Run: `./scripts/release.sh --dry-run patch`
Expected: `v0.5.7` (следующая версия после только что запушенного `v0.5.6`).

- [ ] **Step 4: Проверить usage-guard**

Run: `./scripts/release.sh badlevel; echo "exit=$?"`
Expected: `usage: ./scripts/release.sh [--dry-run] {patch|minor|major}` на stderr, `exit=2`.

- [ ] **Step 5: Закоммитить**

```bash
git add scripts/release.sh
git commit -m "$(cat <<'EOF'
refactor: убрать docker-сборку из release.sh

Docker multi-arch build+push и goreleaser переезжают в
.github/workflows/release.yml, который реагирует на сам пуш тега.
release.sh теперь только бампает версию и пушит тег — единая точка
входа в релиз что для авто-патча из CI, что для ручного minor/major.
EOF
)"
```

---

### Task 4: CI-валидация (`.github/workflows/ci.yml`) + Makefile lint-ci

**Files:**
- Modify: `Makefile:46-49` (добавить target `lint-ci` рядом с существующим `lint`)
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Produces: `make lint-ci` — тот же golangci-lint + setstatuslinter, что и
  `make lint`, но БЕЗ `--fix` (используется из CI; локальный `make lint` с
  `--fix` не меняется).

- [ ] **Step 1: Добавить target lint-ci в Makefile**

Найти существующий блок (строки 46-48):
```makefile
lint: $(GOLANGCI_BIN) $(SETSTATUSLINTER_BIN)
	$(GOENV) $(GOLANGCI_BIN) run --fix ./...
	$(SETSTATUSLINTER_BIN) ./pkg/...
```

Заменить на:
```makefile
lint: $(GOLANGCI_BIN) $(SETSTATUSLINTER_BIN)
	$(GOENV) $(GOLANGCI_BIN) run --fix ./...
	$(SETSTATUSLINTER_BIN) ./pkg/...

# lint-ci — то же самое, но без --fix: CI должен падать явно на проблемах,
# а не молча их чинить и зеленеть.
.PHONY: lint-ci
lint-ci: $(GOLANGCI_BIN) $(SETSTATUSLINTER_BIN)
	$(GOENV) $(GOLANGCI_BIN) run ./...
	$(SETSTATUSLINTER_BIN) ./pkg/...
```

- [ ] **Step 2: Проверить локально**

Run: `make lint-ci`
Expected: exit code 0 (репозиторий сейчас чист от линт-ошибок — это существующий инвариант проекта).

- [ ] **Step 3: Создать .github/workflows/ci.yml**

```yaml
name: CI

on:
  push:
  pull_request:

jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'

      - uses: actions/setup-node@v4
        with:
          node-version: '22'
          cache: 'npm'
          cache-dependency-path: pkg/web/dashboard/package-lock.json

      - name: Build
        run: make build

      - name: Test
        run: make test

      - name: Lint
        run: make lint-ci

  auto-release-tag:
    needs: validate
    if: github.ref == 'refs/heads/main' && github.event_name == 'push'
    runs-on: ubuntu-latest
    concurrency:
      group: main-release
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v4
        with:
          token: ${{ secrets.RELEASE_TOKEN }}
          fetch-depth: 0

      - name: Configure git identity
        run: |
          git config user.name "afm-release-bot"
          git config user.email "afm-release-bot@users.noreply.github.com"

      - name: Bump and push patch tag
        run: ./scripts/release.sh patch
```

- [ ] **Step 4: Проверить синтаксис YAML**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))" && echo "yaml ok"`
Expected: `yaml ok`

- [ ] **Step 5: Закоммитить**

```bash
git add Makefile .github/workflows/ci.yml
git commit -m "$(cat <<'EOF'
feat: добавить CI-валидацию и авто-тег патч-версии на push в main

ci.yml: job validate (build+test+lint-ci) на любой push/PR; job
auto-release-tag (только push в main, после validate) пушит следующий
patch-тег через RELEASE_TOKEN — GITHUB_TOKEN по умолчанию не может
триггерить другие workflow, поэтому release.yml не запустится без PAT.
Makefile: добавлен lint-ci (golangci-lint без --fix — CI должен падать
явно, а не молча чинить).
EOF
)"
```

**Не пушить `git push origin main` на этом шаге — секреты и tap-репозиторий
для `auto-release-tag`/`release.yml` ещё не готовы (см. constraint выше).**

---

### Task 5: Homebrew formula в .goreleaser.yml

**Files:**
- Modify: `.goreleaser.yml`

**Interfaces:**
- Consumes: репозиторий `akopichin/homebrew-afm` (Task 2), секрет
  `RELEASE_TOKEN` (передаётся в env `release.yml`, Task 6).
- Produces: goreleaser при релизе публикует `Formula/afm.rb` в
  `akopichin/homebrew-afm` — именно это делает рабочим `brew install
  akopichin/afm`.

- [ ] **Step 1: Дополнить .goreleaser.yml**

Текущее содержимое (`checksum:` — последний блок файла) не меняется, в конец
добавляется:

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
    install: |
      bin.install "afm"
    test: |
      system "#{bin}/afm", "--version"
```

Полный файл после правки:
```yaml
version: 2

project_name: afm

before:
  hooks:
    - go mod tidy

builds:
  - main: ./cmd/afm
    binary: afm
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64

archives:
  - format: tar.gz
    format_overrides:
      - goos: windows
        format: zip
    name_template: "{{ .ProjectName }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: "checksums.txt"

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
    install: |
      bin.install "afm"
    test: |
      system "#{bin}/afm", "--version"
```

- [ ] **Step 2: Установить goreleaser локально для проверки конфига**

Run: `brew install goreleaser`
Expected: goreleaser доступен, `goreleaser --version` печатает версию.

- [ ] **Step 3: Проверить конфиг**

Run: `RELEASE_TOKEN=dummy goreleaser check`
Expected: `1 configuration file(s) validated` без ошибок (значение `RELEASE_TOKEN`
не важно для `check` — команда не резолвит шаблон `.Env.RELEASE_TOKEN`
фактическим сетевым вызовом, только валидирует структуру YAML).

- [ ] **Step 4: Закоммитить**

```bash
git add .goreleaser.yml
git commit -m "$(cat <<'EOF'
feat: добавить Homebrew formula в .goreleaser.yml

Публикует Formula/afm.rb в akopichin/homebrew-afm при каждом релизе —
это то, что делает рабочим `brew install akopichin/afm`. Без license:
в репозитории пока нет файла LICENSE.
EOF
)"
```

---

### Task 6: Release workflow (`.github/workflows/release.yml`)

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: секреты `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`, `RELEASE_TOKEN`
  (заводятся в Task 7); `.goreleaser.yml` (Task 5); `Dockerfile.runtime`
  (не меняется).
- Produces: на каждый пуш тега `v*.*.*` — docker-образ `akopichin/afm:vX.Y.Z`
  + `akopichin/afm:latest` (мультиарх linux/amd64+arm64) на Docker Hub,
  GitHub Release с бинарниками, обновлённая Homebrew formula.

- [ ] **Step 1: Создать .github/workflows/release.yml**

```yaml
name: Release

on:
  push:
    tags:
      - 'v*.*.*'

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to Docker Hub
        uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKERHUB_USERNAME }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      - name: Build and push docker image (multi-arch)
        run: |
          docker buildx build \
            --platform linux/amd64,linux/arm64 \
            --build-arg AFM_VERSION="${{ github.ref_name }}" \
            -f Dockerfile.runtime \
            -t akopichin/afm:${{ github.ref_name }} \
            -t akopichin/afm:latest \
            --push .

      - name: Release binaries + Homebrew formula
        uses: goreleaser/goreleaser-action@v6
        with:
          distribution: goreleaser
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}
```

- [ ] **Step 2: Проверить синтаксис YAML**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))" && echo "yaml ok"`
Expected: `yaml ok`

- [ ] **Step 3: Закоммитить**

```bash
git add .github/workflows/release.yml
git commit -m "$(cat <<'EOF'
feat: добавить release-workflow на пуш тега v*.*.*

Единая точка входа в релиз: неважно, откуда пришёл тег (авто-патч из
ci.yml или ручной make release-minor/major) — release.yml собирает и
пушит мультиарх docker-образ, затем goreleaser публикует бинарники,
GitHub Release и Homebrew formula.
EOF
)"
```

**Снова: `origin main` пока не трогаем — секреты ещё не заведены (Task 7).**

---

### Task 7: Секреты репозитория (пользователь заводит сам)

**Files:** нет.

**⚠️ Требует участия человека:** значения токенов не должны попадать в чат
агента — пользователь вводит их напрямую в терминале.

- [ ] **Step 1: Создать PAT и добавить как RELEASE_TOKEN**

Попросите пользователя:
1. Открыть https://github.com/settings/personal-access-tokens/new
2. Создать fine-grained PAT с `Repository access` → выбрать `akopichin/afm`
   и `akopichin/homebrew-afm`, permission `Contents: Read and write`.
3. Скопировать токен и выполнить в терминале:
```
! gh secret set RELEASE_TOKEN --repo akopichin/afm
```
   (gh спросит значение — пользователь вставляет токен, он не проходит
   через ассистента).

- [ ] **Step 2: Проверить**

Run: `gh secret list --repo akopichin/afm`
Expected: строка `RELEASE_TOKEN` с датой обновления.

- [ ] **Step 3: Создать Docker Hub access token**

Попросите пользователя:
1. Открыть https://hub.docker.com/settings/security
2. New Access Token → Read & Write scope.
3. Выполнить:
```
! gh secret set DOCKERHUB_USERNAME --repo akopichin/afm --body akopichin
! gh secret set DOCKERHUB_TOKEN --repo akopichin/afm
```

- [ ] **Step 4: Проверить**

Run: `gh secret list --repo akopichin/afm`
Expected: строки `RELEASE_TOKEN`, `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`.

---

### Task 8: Push в main — первый реальный авто-релиз

**Files:** нет.

- [ ] **Step 1: Финальная проверка перед пушем**

Run: `git log --oneline origin/main..HEAD`
Expected: список из коммитов Task 3–6 (release.sh, ci.yml+Makefile,
.goreleaser.yml, release.yml) — всё, что накопилось локально.

- [ ] **Step 2: Push**

Run: `git push origin main`
Expected: коммиты уходят в main; это триггерит `ci.yml` → `validate` →
`auto-release-tag` → тег `v0.5.7` → `release.yml`.

- [ ] **Step 3: Проследить за validate job**

Run: `gh run watch --repo akopichin/afm`
(или открыть https://github.com/akopichin/afm/actions)
Expected: job `validate` зелёный (build+test+lint-ci прошли).

- [ ] **Step 4: Проследить за auto-release-tag и release**

Run: `gh run list --repo akopichin/afm --limit 5`
Expected: `auto-release-tag` зелёный, следом отдельный run workflow `Release`
(триггернутый пушем тега `v0.5.7`) тоже зелёный.

- [ ] **Step 5: Проверить результат — GitHub Release**

Run: `gh release view v0.5.7 --repo akopichin/afm`
Expected: релиз существует, содержит архивы для linux/darwin/windows ×
amd64/arm64 + `checksums.txt`.

- [ ] **Step 6: Проверить результат — Docker Hub**

Run: `docker manifest inspect akopichin/afm:v0.5.7 | grep architecture`
Expected: строки `"architecture": "amd64"` и `"architecture": "arm64"`.

- [ ] **Step 7: Проверить результат — Homebrew tap**

Run: `gh api repos/akopichin/homebrew-afm/contents/Formula/afm.rb --jq .name`
Expected: `afm.rb`

- [ ] **Step 8: Проверить установку через brew**

Run: `brew install akopichin/afm && afm --version`
Expected: устанавливается без ошибок, версия соответствует `v0.5.7`.

Если что-то из шагов 3–8 упало: `main` всё равно содержит рабочий код
(`validate` уже был зелёным до попытки релиза) — чинить проблему и
дождаться следующего push, который повторит попытку со следующей
(неизменившейся) версией.

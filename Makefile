LOCAL_BIN=$(CURDIR)/bin
PROJECT_NAME=afm

export GO111MODULE=on
GOENV:=GO111MODULE=on

# version вшивается в бинарник через -ldflags. git describe → тег/SHA, иначе "dev".
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

GOLANGCI_BIN=$(LOCAL_BIN)/golangci-lint
GOLANGCI_TAG=v2.11.4

.PHONY: build test lint clean install bindeps vendor

# The public repository must never contain vendored private engine sources.
vendor:
	@echo "Vendoring is disabled; use pinned module downloads." >&2
	@exit 1

$(LOCAL_BIN):
	mkdir -p $(LOCAL_BIN)

$(GOLANGCI_BIN): $(LOCAL_BIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LOCAL_BIN) $(GOLANGCI_TAG)

.PHONY: bindeps
bindeps: $(GOLANGCI_BIN)

DASHBOARD_DIR := $(CURDIR)/pkg/web/dashboard

# web собирает дашборд до go build, чтобы go:embed включил свежие assets.
.PHONY: web
web:
	@test -d $(DASHBOARD_DIR)/node_modules || (echo ">> installing dashboard dependencies" && cd $(DASHBOARD_DIR) && npm install)
	cd $(DASHBOARD_DIR) && npm run build

build: web
	$(GOENV) CGO_ENABLED=0 go build -v -ldflags "-X main.version=$(VERSION)" -o $(LOCAL_BIN)/$(PROJECT_NAME) ./cmd/afm

# TESTFLAGS передаёт флаги go test; CI использует -p 1, чтобы race-пакеты не конкурировали за два CPU и не
# давали ложных таймаутов.
test:
	$(GOENV) go test ./... -race -timeout 30m $(TESTFLAGS)
	cd contracts && GOWORK=off go test -race -timeout 15m ./...

# schema-check — compiles the hand-written JSON Schemas, checks exact field paths and parser edge cases,
# and validates repository examples.
.PHONY: schema-check
schema-check:
	python3 scripts/sync-schemas.py --check
	$(GOENV) go test ./pkg/schemacheck/
	cd contracts && GOWORK=off go test ./schema

# web-test/web-typecheck запускают Vitest и tsc; vite build этих проверок не выполняет.
.PHONY: web-test web-typecheck
web-test:
	@test -d $(DASHBOARD_DIR)/node_modules || (cd $(DASHBOARD_DIR) && npm install)
	cd $(DASHBOARD_DIR) && npm test

web-typecheck:
	@test -d $(DASHBOARD_DIR)/node_modules || (cd $(DASHBOARD_DIR) && npm install)
	cd $(DASHBOARD_DIR) && npm run typecheck

.PHONY: generate
generate:
	$(GOENV) go run ./tools/genstagestatus
	python3 scripts/sync-schemas.py

# generate-check требует актуальный TS-файл статусов после изменений state.AllStatuses.
.PHONY: generate-check
generate-check: generate
	git diff --exit-code -- pkg/web/dashboard/src/types/stage-status.generated.ts schema/

SETSTATUSLINTER_BIN=$(LOCAL_BIN)/setstatuslinter

$(SETSTATUSLINTER_BIN): $(LOCAL_BIN)
	$(GOENV) go build -o $(SETSTATUSLINTER_BIN) ./tools/setstatuslinter

# Lint host and Linux code so build-tagged files are checked on macOS too.
lint: $(GOLANGCI_BIN) $(SETSTATUSLINTER_BIN)
	$(GOENV) $(GOLANGCI_BIN) run --fix ./...
	GOOS=linux $(GOENV) $(GOLANGCI_BIN) run --fix ./...
	$(SETSTATUSLINTER_BIN) ./pkg/...

# lint-ci — то же самое, но без --fix: CI должен падать явно на проблемах,
# а не молча их чинить и зеленеть.
.PHONY: lint-ci
lint-ci: $(GOLANGCI_BIN) $(SETSTATUSLINTER_BIN) generate-check
	$(GOENV) $(GOLANGCI_BIN) run ./...
	$(SETSTATUSLINTER_BIN) ./pkg/...

clean:
	rm -rf $(LOCAL_BIN)/

# install обновляет бинарник и PATH-копии; после cp каждую копию подписываем заново для macOS.
install: web
	$(GOENV) go install -ldflags "-X main.version=$(VERSION)" ./cmd/afm
	@src=$$(go env GOPATH)/bin/$(PROJECT_NAME); \
	codesign -f -s - $$src && echo "codesigned: $$src"; \
	for f in $(HOME)/homebrew/bin/afm; do \
		if [ -e $$f ] && [ ! -L $$f ]; then \
			cp $$src $$f && \
			codesign -f -s - $$f && echo "updated+signed copy: $$f"; \
		fi; \
	done

# Ставит актуальные embedded-скиллы из исходников (без сборки dashboard).
.PHONY: install-skills
install-skills:
	$(GOENV) go run ./cmd/afm install-skills

DOCKER_IMAGE := akopichin/afm
DOCKER_TAG   := latest

.PHONY: docker-build docker-push docker-run

# Docker packaging consumes prebuilt Linux binaries from the trusted build.
docker-build:
	./scripts/build-docker-image.sh "$(VERSION)" "$(CURDIR)/binaries"

docker-push:
	./scripts/build-docker-image.sh "$(VERSION)" "$(CURDIR)/binaries" --push

docker-run:
	docker run --rm -it \
	  -v $(PWD):/project \
	  -v $(HOME)/.claude:/home/afm/.claude \
	  -v $(HOME)/.afm:/home/afm/.afm \
	  $(if $(wildcard $(HOME)/.codex),-v $(HOME)/.codex:/tmp/host-codex:ro) \
	  -e AFM_HOST_UID=$(shell id -u) -e AFM_HOST_GID=$(shell id -g) \
	  -e ANTHROPIC_API_KEY \
	  $(DOCKER_IMAGE):$(DOCKER_TAG) $(ARGS)

RELEASE_TARGETS := $(addprefix release-,patch minor major \
	patch-alpha minor-alpha major-alpha patch-beta minor-beta major-beta)

# Проверяем все цели до выполнения рецептов, чтобы опечатка ветки не выпустила HEAD.
RELEASE_BRANCH := $(filter main v1.x v2.x,$(MAKECMDGOALS))
ifneq ($(filter $(RELEASE_TARGETS),$(MAKECMDGOALS)),)
ifneq ($(words $(filter $(RELEASE_TARGETS),$(MAKECMDGOALS))),1)
$(error Specify exactly one release target)
endif
ifneq ($(filter-out $(RELEASE_TARGETS) main v1.x v2.x,$(MAKECMDGOALS)),)
$(error Release targets accept only an optional main, v1.x or v2.x branch)
endif
endif
ifneq ($(RELEASE_BRANCH),)
ifneq ($(words $(filter $(RELEASE_TARGETS),$(MAKECMDGOALS))),1)
$(error Specify exactly one release target with a branch)
endif
ifneq ($(words $(RELEASE_BRANCH)),1)
$(error Specify exactly one release branch)
endif
.PHONY: $(RELEASE_BRANCH)
$(RELEASE_BRANCH):
	@:
endif

.PHONY: $(RELEASE_TARGETS)
$(RELEASE_TARGETS):
	./scripts/release.sh $(subst -, ,$(patsubst release-%,%,$@)) $(if $(RELEASE_BRANCH),--branch $(RELEASE_BRANCH))

.PHONY: sdk-test sdk-test-integration sdk-lint

sdk-test:
	cd sdk && GOWORK=off go test ./... -v -race

# sdk-test-integration builds afm with dashboard assets before spawning it in integration tests.
sdk-test-integration: build
	cd sdk && GOWORK=off AFM_SDK_TEST_BINARY=$(LOCAL_BIN)/$(PROJECT_NAME) go test ./... -v -race -run Integration

sdk-lint: $(GOLANGCI_BIN)
	cd sdk && GOWORK=off $(GOLANGCI_BIN) run ./...

.PHONY: mesh-smoke

ENGINE_DIR ?=

# The real-provider scenario lives with engine integration tests and needs an explicit trusted workspace.
mesh-smoke: build
	@test -n "$(ENGINE_DIR)" && test -f go.work || (echo "Set ENGINE_DIR and create go.work with scripts/dev-workspace.sh" >&2; exit 1)
	cd "$(ENGINE_DIR)/integration/afm" && GOWORK="$(CURDIR)/go.work" AFM_MESH_SMOKE_AFM_BIN="$(LOCAL_BIN)/$(PROJECT_NAME)" go test -tags afmintegration -run TestAdapterSmoke_SendReadReplyYieldResume ./... -v

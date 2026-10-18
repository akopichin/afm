LOCAL_BIN=$(CURDIR)/bin
PROJECT_NAME=afm

export GO111MODULE=on
GOENV:=GO111MODULE=on

# version вшивается в бинарник через -ldflags. git describe → тег/SHA, иначе "dev".
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

GOLANGCI_BIN=$(LOCAL_BIN)/golangci-lint
GOLANGCI_TAG=v2.11.4

.PHONY: build test lint clean install bindeps vendor

# Go 1.14+ automatically uses the checked-in vendor/ tree for builds and tests.
# Regenerate it after changing go.mod or go.sum.
vendor:
	$(GOENV) go mod vendor

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
	$(GOENV) go test ./... -v -race $(TESTFLAGS)

# schema-check — compiles the hand-written JSON Schemas, checks exact field paths and parser edge cases,
# and validates repository examples.
.PHONY: schema-check
schema-check:
	$(GOENV) go test ./pkg/schemacheck/

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

# generate-check требует актуальный TS-файл статусов после изменений state.AllStatuses.
.PHONY: generate-check
generate-check: generate
	git diff --exit-code -- pkg/web/dashboard/src/types/stage-status.generated.ts

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

SKILLS_DIR=$(HOME)/.claude/skills
SKILLS_SRC=$(CURDIR)/assets/claude/skills

.PHONY: install-skills
install-skills:
	@for skill in $(SKILLS_SRC)/*/; do \
		name=$$(basename $$skill); \
		mkdir -p $(SKILLS_DIR)/$$name; \
		cp $$skill/SKILL.md $(SKILLS_DIR)/$$name/SKILL.md; \
		echo "installed skill: $$name"; \
	done

DOCKER_IMAGE := akopichin/afm
DOCKER_TAG   := latest

.PHONY: docker-build docker-push docker-run

# Dockerfile.runtime собирает дашборд сам; docker-build не требует host web-сборки.
docker-build:
	docker build --build-arg AFM_VERSION=$(VERSION) -f Dockerfile.runtime -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push: docker-build
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-run:
	docker run --rm -it \
	  -v $(PWD):/project \
	  -v $(HOME)/.claude:/home/afm/.claude \
	  -v $(HOME)/.afm:/home/afm/.afm \
	  -e AFM_HOST_UID=$(shell id -u) -e AFM_HOST_GID=$(shell id -g) \
	  -e ANTHROPIC_API_KEY \
	  $(DOCKER_IMAGE):$(DOCKER_TAG) $(ARGS)

.PHONY: release-patch release-minor release-major
release-patch:
	./scripts/release.sh patch
release-minor:
	./scripts/release.sh minor
release-major:
	./scripts/release.sh major

.PHONY: sdk-test sdk-test-integration sdk-lint

sdk-test:
	cd sdk && go test ./... -v -race

# sdk-test-integration builds afm with dashboard assets before spawning it in integration tests.
sdk-test-integration: build
	cd sdk && AFM_SDK_TEST_BINARY=$(LOCAL_BIN)/$(PROJECT_NAME) go test ./... -v -race -run Integration

sdk-lint: $(GOLANGCI_BIN)
	cd sdk && $(GOLANGCI_BIN) run ./...

.PHONY: mesh-smoke

# mesh-smoke runs Agent Mesh T9.1's gated real-adapter smoke test (pkg/orchestrator/agentcomm_adapter_smoke_test.go)
# against a real, credentialed executor adapter named by AFM_MESH_SMOKE_COMMAND (e.g. claude, codex, or a
# configured recipe wrapper, with its own credentials already in this shell's environment). Builds afm first
# (mirrors sdk-test-integration's own AFM_SDK_TEST_BINARY wiring) so the adapter's own `afm agent` CLI calls
# resolve to the binary this target just built, not a stale one elsewhere on PATH. Skips cleanly when
# AFM_MESH_SMOKE_COMMAND is unset.
mesh-smoke: build
	AFM_MESH_SMOKE_AFM_BIN=$(LOCAL_BIN)/$(PROJECT_NAME) go test -run TestAdapterSmoke_SendReadReplyYieldResume ./pkg/orchestrator/... -v

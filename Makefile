LOCAL_BIN=$(CURDIR)/bin
PROJECT_NAME=afm

export GO111MODULE=on
GOENV:=GO111MODULE=on

# version вшивается в бинарник через -ldflags. git describe → тег/SHA, иначе "dev".
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

GOLANGCI_BIN=$(LOCAL_BIN)/golangci-lint
GOLANGCI_TAG=v2.11.4

.PHONY: build test lint clean install bindeps

$(LOCAL_BIN):
	mkdir -p $(LOCAL_BIN)

$(GOLANGCI_BIN): $(LOCAL_BIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LOCAL_BIN) $(GOLANGCI_TAG)

.PHONY: bindeps
bindeps: $(GOLANGCI_BIN)

DASHBOARD_DIR := $(CURDIR)/pkg/web/dashboard

# web собирает React-дашборд (Vite → index.html + assets/* в корне pkg/web/dashboard/),
# откуда go:embed вкомпилирует его в бинарь. Обязательный шаг ПЕРЕД go-сборкой —
# иначе в бинарь попадёт устаревший/пустой дашборд. npm-зависимости ставятся
# автоматически при отсутствии node_modules.
.PHONY: web
web:
	@test -d $(DASHBOARD_DIR)/node_modules || (echo ">> installing dashboard dependencies" && cd $(DASHBOARD_DIR) && npm install)
	cd $(DASHBOARD_DIR) && npm run build

build: web
	$(GOENV) CGO_ENABLED=0 go build -v -ldflags "-X main.version=$(VERSION)" -o $(LOCAL_BIN)/$(PROJECT_NAME) ./cmd/afm

test:
	$(GOENV) go test ./... -v -race

SETSTATUSLINTER_BIN=$(LOCAL_BIN)/setstatuslinter

$(SETSTATUSLINTER_BIN): $(LOCAL_BIN)
	$(GOENV) go build -o $(SETSTATUSLINTER_BIN) ./tools/setstatuslinter

lint: $(GOLANGCI_BIN) $(SETSTATUSLINTER_BIN)
	$(GOENV) $(GOLANGCI_BIN) run --fix ./...
	$(SETSTATUSLINTER_BIN) ./pkg/...

clean:
	rm -rf $(LOCAL_BIN)/

# install обновляет ~/go/bin и освежает копии бинарника в других
# директориях PATH (например ~/homebrew/bin), чтобы не оставались устаревшие.
# codesign -s - создаёт ad-hoc подпись: macOS 26+ убивает неподписанные бинарники
# с SIGKILL (Code Signature Invalid) при запуске любого subcommand.
# ВАЖНО: cp подписанного бинарника ставит xattr com.apple.provenance, который
# рассинхронизируется с подписью → macOS 26 убивает копию (SIGKILL 137, spctl
# rejected). Поэтому копию нужно пере-подписать НА МЕСТЕ после cp.
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

# docker-build не зависит от web: Dockerfile.runtime сам собирает React-дашборд
# в node-стадии (см. stage web) — это же использует release.sh, так что релизный
# образ всегда получает свежий фронт из исходников.
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

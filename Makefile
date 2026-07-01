LOCAL_BIN=$(CURDIR)/bin
PROJECT_NAME=afm

export GO111MODULE=on
GOENV:=GO111MODULE=on

GOLANGCI_BIN=$(LOCAL_BIN)/golangci-lint
GOLANGCI_TAG=v2.11.4

.PHONY: build test lint clean install bindeps

$(LOCAL_BIN):
	mkdir -p $(LOCAL_BIN)

$(GOLANGCI_BIN): $(LOCAL_BIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(LOCAL_BIN) $(GOLANGCI_TAG)

.PHONY: bindeps
bindeps: $(GOLANGCI_BIN)

build:
	$(GOENV) CGO_ENABLED=0 go build -v -o $(LOCAL_BIN)/$(PROJECT_NAME) ./cmd/afm

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
install:
	$(GOENV) go install ./cmd/afm
	@src=$$(go env GOPATH)/bin/$(PROJECT_NAME); \
	for f in $(HOME)/homebrew/bin/afm $(HOME)/homebrew/bin/afm; do \
		if [ -e $$f ] && [ ! -L $$f ]; then \
			cp $$src $$f && echo "updated copy: $$f"; \
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

docker-build:
	docker build -f Dockerfile.runtime -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push: docker-build
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-run:
	docker run --rm -it \
	  -v $(PWD):/project \
	  -v $(HOME)/.claude:/root/.claude \
	  -v $(HOME)/.afm:/root/.afm \
	  -e ANTHROPIC_API_KEY \
	  $(DOCKER_IMAGE):$(DOCKER_TAG) $(ARGS)

LOCAL_BIN=$(CURDIR)/bin
PROJECT_NAME=flowmanager

export GO111MODULE=on
GO111MODULE=on

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
	$(GOENV) CGO_ENABLED=0 go build -v -o $(LOCAL_BIN)/$(PROJECT_NAME) ./cmd/flowmanager

test:
	$(GOENV) go test ./... -v -race

lint: $(GOLANGCI_BIN)
	$(GOENV) $(GOLANGCI_BIN) run --fix ./...

clean:
	rm -rf $(LOCAL_BIN)/

install:
	$(GOENV) go install ./cmd/flowmanager

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

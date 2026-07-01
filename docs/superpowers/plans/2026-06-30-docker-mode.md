# Docker Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Когда `docker.enabled: true` (или `AFM_USE_DOCKER=1`), команда `afm run flow.yaml` автоматически перезапускает себя внутри Docker-контейнера с примонтированным проектом, `~/.claude/`, `~/.afm/` и нестандартными агентами из flow.

**Architecture:** Self-re-exec — в начале `RunE` проверяем флаг Docker, и если он выставлен и мы не внутри контейнера, вызываем `syscall.Exec` который заменяет текущий процесс на `docker run ...`. Внутри контейнера стартует та же `afm run` с `AFM_IN_DOCKER=1`, что предотвращает рекурсию. Вся Docker-логика изолирована в новом пакете `pkg/docker`.

**Tech Stack:** Go stdlib (`syscall`, `os/exec`, `os`), `pkg/flow` (для парсинга flow YAML), Docker CLI (внешняя зависимость)

## Global Constraints

- Go 1.26 (не менять версию в go.mod)
- Модуль: `github.com/akopichin/afm`
- Паттерн `*bool` для nullable config (как в `ProxyConfig.Enabled`)
- Коммиты на русском языке
- Линт: `./bin/golangci-lint run --fix ./...` после каждого таска
- Docker образ: `akopichin/afm:latest`
- Golang builder в Dockerfile: `golang:1.26-bookworm`

---

## File Structure

| Файл | Действие | Ответственность |
|------|----------|-----------------|
| `pkg/docker/launcher.go` | Create | `CommandMount`, `ReExecConfig`, `ScanCommands()`, `ReExec()`, `execFunc` |
| `pkg/docker/launcher_test.go` | Create | Unit-тесты `ScanCommands` и построения аргументов `docker run` |
| `pkg/config/config.go` | Modify | Добавить `DockerConfig`, `IsDockerEnabled()`, `GetImage()`, обновить `mergeFile` |
| `pkg/config/config_test.go` | Modify | Тесты `IsDockerEnabled()` и `GetImage()` |
| `cmd/afm/run.go` | Modify | Вызов `docker.ReExec()` в начале `RunE` |
| `Dockerfile.runtime` | Create | Публичный образ для Docker Hub |
| `Makefile` | Modify | Таргеты `docker-build`, `docker-push`, `docker-run` |
| `.goreleaser.yml` | Modify | Секция `dockers` для публикации при релизе |
| `config.example.yaml` | Modify | Закомментированная секция `docker:` |
| `CLAUDE.md` | Modify | Секция "Docker Mode" |
| `README.md` | Modify | Секция "Запуск в Docker" |

---

## Task 1: DockerConfig в pkg/config

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `config.DockerConfig`, методы `IsDockerEnabled() bool` и `GetImage() string` на `DockerConfig`; поле `Docker DockerConfig` в `Config`

- [ ] **Шаг 1: Написать падающие тесты**

Добавить в `pkg/config/config_test.go`:

```go
func TestDockerConfig_IsDockerEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	cases := []struct {
		name        string
		cfg         config.DockerConfig
		envUseDocker string
		envInDocker  string
		want        bool
	}{
		{"enabled=true", config.DockerConfig{Enabled: &trueVal}, "", "", true},
		{"enabled=false", config.DockerConfig{Enabled: &falseVal}, "", "", false},
		{"nil+env=1", config.DockerConfig{}, "1", "", true},
		{"nil+env=true", config.DockerConfig{}, "true", "", true},
		{"nil+env=", config.DockerConfig{}, "", "", false},
		{"in_docker overrides", config.DockerConfig{Enabled: &trueVal}, "", "1", false},
		{"explicit=true wins over AFM_IN_DOCKER=1 not", config.DockerConfig{Enabled: &trueVal}, "", "1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_USE_DOCKER", tc.envUseDocker)
			t.Setenv("AFM_IN_DOCKER", tc.envInDocker)
			if got := tc.cfg.IsDockerEnabled(); got != tc.want {
				t.Errorf("IsDockerEnabled()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestDockerConfig_GetImage(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "")
		cfg := config.DockerConfig{}
		if cfg.GetImage() != "akopichin/afm:latest" {
			t.Errorf("got %q, want akopichin/afm:latest", cfg.GetImage())
		}
	})
	t.Run("config override", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "")
		cfg := config.DockerConfig{Image: "myrepo/afm:v1"}
		if cfg.GetImage() != "myrepo/afm:v1" {
			t.Errorf("got %q", cfg.GetImage())
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("AFM_DOCKER_IMAGE", "local/afm:dev")
		cfg := config.DockerConfig{Image: "myrepo/afm:v1"}
		if cfg.GetImage() != "local/afm:dev" {
			t.Errorf("got %q", cfg.GetImage())
		}
	})
}

func TestLoadFrom_DockerConfig(t *testing.T) {
	dir := t.TempDir()
	trueVal := true
	writeYAML(t, dir, "config.yaml", `
docker:
  enabled: true
  image: test/afm:dev
`)
	cfg, err := config.LoadFrom(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Docker.Enabled == nil || *cfg.Docker.Enabled != trueVal {
		t.Errorf("Docker.Enabled: got %v, want &true", cfg.Docker.Enabled)
	}
	if cfg.Docker.Image != "test/afm:dev" {
		t.Errorf("Docker.Image: got %q", cfg.Docker.Image)
	}
	_ = trueVal
}
```

- [ ] **Шаг 2: Убедиться что тесты не компилируются**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/config/... 2>&1 | head -20
```

Ожидается: ошибка компиляции — `DockerConfig` и методы не существуют.

- [ ] **Шаг 3: Реализовать DockerConfig в config.go**

Добавить после `ProxyConfig` struct в `pkg/config/config.go`:

```go
// DockerConfig configures Docker-mode self-re-exec.
type DockerConfig struct {
	Enabled *bool  `yaml:"enabled"` // nil = смотрим AFM_USE_DOCKER
	Image   string `yaml:"image"`
}

// IsDockerEnabled returns true if Docker mode should be used.
// AFM_IN_DOCKER=1 always returns false (already inside container).
func (d DockerConfig) IsDockerEnabled() bool {
	if os.Getenv("AFM_IN_DOCKER") == "1" {
		return false
	}
	if d.Enabled != nil {
		return *d.Enabled
	}
	v := os.Getenv("AFM_USE_DOCKER")
	return v == "1" || v == "true"
}

// GetImage returns the Docker image to use, preferring AFM_DOCKER_IMAGE env var.
func (d DockerConfig) GetImage() string {
	if img := os.Getenv("AFM_DOCKER_IMAGE"); img != "" {
		return img
	}
	if d.Image != "" {
		return d.Image
	}
	return "akopichin/afm:latest"
}
```

Добавить поле в `Config` struct (после `Proxy ProxyConfig`):

```go
Docker DockerConfig `yaml:"docker"`
```

Обновить `mergeFile` — добавить после блока `Proxy`:

```go
if overlay.Docker.Enabled != nil {
    dst.Docker.Enabled = overlay.Docker.Enabled
}
if overlay.Docker.Image != "" {
    dst.Docker.Image = overlay.Docker.Image
}
```

- [ ] **Шаг 4: Запустить тесты**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/config/... -v -run 'Docker|LoadFrom_Docker'
```

Ожидается: все новые тесты PASS.

- [ ] **Шаг 5: Линт**

```bash
cd /Users/alexander.kopichin/work/flowManager && ./bin/golangci-lint run --fix ./pkg/config/...
```

Ожидается: нет ошибок.

- [ ] **Шаг 6: Полные тесты config**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/config/... -v
```

Ожидается: все тесты PASS.

- [ ] **Шаг 7: Коммит**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat: DockerConfig — IsDockerEnabled/GetImage + mergeFile"
```

---

## Task 2: pkg/docker — ScanCommands

**Files:**
- Create: `pkg/docker/launcher.go`
- Create: `pkg/docker/launcher_test.go`

**Interfaces:**
- Consumes: `flow.Flow` (из `pkg/flow`), `exec.LookPath` из stdlib
- Produces:
  - `docker.CommandMount{HostPath, ContainerName string}`
  - `docker.ScanCommands(f *flow.Flow, globalCmd string) []CommandMount`

- [ ] **Шаг 1: Написать падающий тест**

Создать `pkg/docker/launcher_test.go`:

```go
package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
)

// writeTempFlow создаёт временный flow YAML и возвращает путь к нему.
func writeTempFlow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanCommands_SkipsClaude(t *testing.T) {
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "claude"},
			{ID: "s2", Command: ""},
		},
	}
	mounts := docker.ScanCommands(f, "claude")
	if len(mounts) != 0 {
		t.Errorf("expected 0 mounts, got %d: %v", len(mounts), mounts)
	}
}

func TestScanCommands_FindsBinary(t *testing.T) {
	// Создаём фиктивный бинарник во временной директории.
	dir := t.TempDir()
	binPath := filepath.Join(dir, "myagent")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "myagent"},
			{ID: "s2", Command: "claude"},
		},
	}
	mounts := docker.ScanCommands(f, "claude")
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(mounts))
	}
	if mounts[0].ContainerName != "myagent" {
		t.Errorf("ContainerName: got %q, want myagent", mounts[0].ContainerName)
	}
	if mounts[0].HostPath != binPath {
		t.Errorf("HostPath: got %q, want %q", mounts[0].HostPath, binPath)
	}
}

func TestScanCommands_DeduplicatesCommands(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "glm51")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "glm51"},
			{ID: "s2", Command: "glm51"},
			{ID: "s3", Command: "glm51"},
		},
	}
	mounts := docker.ScanCommands(f, "claude")
	if len(mounts) != 1 {
		t.Errorf("expected 1 unique mount, got %d", len(mounts))
	}
}

func TestScanCommands_GlobalCmdMounted(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "glm51")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name:   "test",
		Stages: []flow.Stage{{ID: "s1", Command: ""}},
	}
	// Если globalCmd не claude — тоже монтируем.
	mounts := docker.ScanCommands(f, "glm51")
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount for global cmd, got %d", len(mounts))
	}
	if mounts[0].HostPath != binPath {
		t.Errorf("HostPath: got %q, want %q", mounts[0].HostPath, binPath)
	}
}

func TestScanCommands_SkipsMissingBinary(t *testing.T) {
	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "nonexistent-binary-xyz-42"},
		},
	}
	mounts := docker.ScanCommands(f, "claude")
	if len(mounts) != 0 {
		t.Errorf("expected 0 mounts for missing binary, got %d", len(mounts))
	}
}
```

- [ ] **Шаг 2: Убедиться что тесты не компилируются**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/docker/... 2>&1 | head -10
```

Ожидается: пакет `docker` не существует.

- [ ] **Шаг 3: Реализовать ScanCommands**

Создать `pkg/docker/launcher.go`:

```go
package docker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/akopichin/afm/pkg/flow"
)

// CommandMount описывает нестандартный агент для монтирования в контейнер.
type CommandMount struct {
	HostPath      string
	ContainerName string
}

// ReExecConfig параметры для перезапуска afm в Docker.
type ReExecConfig struct {
	Image      string
	ProjectDir string // абсолютный путь к директории проекта
	Commands   []CommandMount
	ExtraArgs  []string // os.Args[1:]
}

// execFunc — заменяемая в тестах обёртка над syscall.Exec.
var execFunc = func(argv0 string, argv []string, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}

// ScanCommands возвращает список нестандартных (не claude) агентов из flow,
// которые нужно смонтировать в Docker-контейнер.
// Бинарники, не найденные в PATH, молча пропускаются.
func ScanCommands(f *flow.Flow, globalCmd string) []CommandMount {
	seen := make(map[string]bool)
	var mounts []CommandMount

	addCmd := func(cmd string) {
		if cmd == "" || cmd == "claude" || seen[cmd] {
			return
		}
		seen[cmd] = true
		hostPath, err := exec.LookPath(cmd)
		if err != nil {
			return
		}
		mounts = append(mounts, CommandMount{
			HostPath:      hostPath,
			ContainerName: filepath.Base(cmd),
		})
	}

	addCmd(globalCmd)
	for _, s := range f.Stages {
		addCmd(s.Command)
	}
	return mounts
}

// ReExec заменяет текущий процесс на docker run с нужными монтированиями.
// Возвращает ошибку только если docker не найден в PATH; в случае успеха
// syscall.Exec никогда не возвращает управление.
func ReExec(cfg ReExecConfig) error {
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found in PATH: %w", err)
	}

	home, _ := os.UserHomeDir()

	args := []string{"docker", "run", "--rm"}

	if isTTY() {
		args = append(args, "-it")
	}

	// Монтируем проект по тому же абсолютному пути — os.Args проходят без изменений.
	args = append(args,
		"-v", cfg.ProjectDir+":"+cfg.ProjectDir,
		"-v", home+"/.claude:/root/.claude",
		"-v", home+"/.afm:/root/.afm",
		"-w", cfg.ProjectDir,
	)

	// Нестандартные агенты монтируем read-only.
	for _, m := range cfg.Commands {
		args = append(args, "-v", m.HostPath+":/usr/local/bin/"+m.ContainerName+":ro")
	}

	// Окружение внутри контейнера.
	args = append(args, "-e", "AFM_IN_DOCKER=1")
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
		if val := os.Getenv(key); val != "" {
			args = append(args, "-e", key+"="+val)
		}
	}

	// Образ + оригинальные аргументы afm.
	args = append(args, cfg.Image)
	args = append(args, cfg.ExtraArgs...)

	return execFunc(dockerBin, args, os.Environ())
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
```

Импорт `"fmt"` уже включён в блок выше.

- [ ] **Шаг 4: Запустить тесты ScanCommands**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/docker/... -v -run TestScanCommands
```

Ожидается: все 5 тестов PASS.

- [ ] **Шаг 5: Линт**

```bash
cd /Users/alexander.kopichin/work/flowManager && ./bin/golangci-lint run --fix ./pkg/docker/...
```

Ожидается: нет ошибок.

- [ ] **Шаг 6: Коммит**

```bash
git add pkg/docker/
git commit -m "feat: pkg/docker — ScanCommands, CommandMount, ReExecConfig"
```

---

## Task 3: pkg/docker — ReExec + тесты

**Files:**
- Modify: `pkg/docker/launcher.go` (уже содержит ReExec из Task 2 — добавляем недостающий import)
- Modify: `pkg/docker/launcher_test.go`

**Interfaces:**
- Consumes: `docker.CommandMount`, `docker.ReExecConfig`
- Produces: `docker.ReExec(cfg ReExecConfig) error` (уже в launcher.go, тестируем)

- [ ] **Шаг 1: Добавить тесты ReExec в launcher_test.go**

Добавить в `pkg/docker/launcher_test.go`:

```go
func TestReExec_BuildsDockerArgs(t *testing.T) {
	// Перехватываем execFunc чтобы не запускать реальный docker.
	var capturedArgv0 string
	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgv0 = argv0
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	// Создаём фейковый docker бинарник.
	dir := t.TempDir()
	dockerBin := filepath.Join(dir, "docker")
	if err := os.WriteFile(dockerBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image:      "akopichin/afm:latest",
		ProjectDir: "/home/user/myproject",
		Commands: []docker.CommandMount{
			{HostPath: "/usr/local/bin/glm51", ContainerName: "glm51"},
		},
		ExtraArgs: []string{"run", "flow.yaml"},
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}

	if capturedArgv0 != dockerBin {
		t.Errorf("argv0: got %q, want %q", capturedArgv0, dockerBin)
	}

	// Проверяем ключевые флаги в построенных аргументах.
	argsStr := strings.Join(capturedArgs, " ")

	checks := []string{
		"docker run --rm",
		"-v /home/user/myproject:/home/user/myproject",
		":/root/.claude",  // хост-путь зависит от ОС, проверяем только контейнерную часть
		":/root/.afm",
		"-w /home/user/myproject",
		"-v /usr/local/bin/glm51:/usr/local/bin/glm51:ro",
		"-e AFM_IN_DOCKER=1",
		"akopichin/afm:latest",
		"run flow.yaml",
	}
	for _, check := range checks {
		if !strings.Contains(argsStr, check) {
			t.Errorf("args missing %q\nfull args: %s", check, argsStr)
		}
	}
}

func TestReExec_DockerNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // пустая PATH — docker не найдётся
	err := docker.ReExec(docker.ReExecConfig{
		Image:      "akopichin/afm:latest",
		ProjectDir: "/tmp/proj",
		ExtraArgs:  []string{"run", "flow.yaml"},
	})
	if err == nil {
		t.Fatal("expected error when docker not in PATH")
	}
}

func TestReExec_PassthroughEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	t.Setenv("ANTHROPIC_BASE_URL", "https://custom.api")

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	dockerBin := filepath.Join(dir, "docker")
	os.WriteFile(dockerBin, []byte("#!/bin/sh\n"), 0755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	docker.ReExec(docker.ReExecConfig{
		Image:      "akopichin/afm:latest",
		ProjectDir: "/tmp/proj",
		ExtraArgs:  []string{"run", "flow.yaml"},
	})

	argsStr := strings.Join(capturedArgs, " ")
	if !strings.Contains(argsStr, "-e ANTHROPIC_API_KEY=sk-test-key") {
		t.Errorf("ANTHROPIC_API_KEY not passed: %s", argsStr)
	}
	if !strings.Contains(argsStr, "-e ANTHROPIC_BASE_URL=https://custom.api") {
		t.Errorf("ANTHROPIC_BASE_URL not passed: %s", argsStr)
	}
}
```

Добавить импорт `"strings"` в начало файла.

- [ ] **Шаг 2: Экспортировать SetExecFunc/ResetExecFunc из launcher.go**

Тесты используют `docker.SetExecFunc` и `docker.ResetExecFunc` — добавить в `pkg/docker/launcher.go`:

```go
// SetExecFunc заменяет функцию exec (только для тестов).
func SetExecFunc(f func(string, []string, []string) error) {
	execFunc = f
}

// ResetExecFunc возвращает функцию exec к дефолтной (только для тестов).
func ResetExecFunc() {
	execFunc = func(argv0 string, argv []string, envv []string) error {
		return syscall.Exec(argv0, argv, envv)
	}
}
```

- [ ] **Шаг 3: Запустить тесты ReExec**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/docker/... -v -run TestReExec
```

Ожидается: все тесты PASS.

- [ ] **Шаг 4: Полные тесты пакета**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/docker/... -v
```

Ожидается: все тесты PASS.

- [ ] **Шаг 5: Линт**

```bash
cd /Users/alexander.kopichin/work/flowManager && ./bin/golangci-lint run --fix ./pkg/docker/...
```

- [ ] **Шаг 6: Коммит**

```bash
git add pkg/docker/
git commit -m "feat: pkg/docker — ReExec + SetExecFunc/ResetExecFunc для тестов"
```

---

## Task 4: Интеграция в cmd/afm/run.go

**Files:**
- Modify: `cmd/afm/run.go`

**Interfaces:**
- Consumes: `config.DockerConfig.IsDockerEnabled()`, `config.DockerConfig.GetImage()`, `docker.ScanCommands()`, `docker.ReExec()`, `docker.ReExecConfig`

- [ ] **Шаг 1: Добавить Docker re-exec в RunE**

В `cmd/afm/run.go` добавить импорт:
```go
"github.com/akopichin/afm/pkg/docker"
```

В `RunE`, после того как `flowPath` и `f` определены (после строки `f, err := flow.ParseFile(flowPath)`), добавить блок:

```go
// Docker self-re-exec: если включён Docker-режим и мы не внутри контейнера —
// перезапускаем себя в Docker.
if cfg.Docker.IsDockerEnabled() {
    absDir, absErr := filepath.Abs(rootDir)
    if absErr != nil {
        return fmt.Errorf("resolve project dir: %w", absErr)
    }
    cmds := docker.ScanCommands(f, cfg.Client.Command)
    return docker.ReExec(docker.ReExecConfig{
        Image:      cfg.Docker.GetImage(),
        ProjectDir: absDir,
        Commands:   cmds,
        ExtraArgs:  os.Args[1:],
    })
}
```

Проверь что импорт `"os"` уже есть в run.go (он там есть).

- [ ] **Шаг 2: Убедиться что проект компилируется**

```bash
cd /Users/alexander.kopichin/work/flowManager && go build ./cmd/afm/...
```

Ожидается: успешная компиляция, бинарник в `./bin/afm` или текущей директории.

- [ ] **Шаг 3: Проверить что --help работает**

```bash
cd /Users/alexander.kopichin/work/flowManager && go run ./cmd/afm/... --help
```

Ожидается: вывод help без ошибок.

- [ ] **Шаг 4: Проверить что AFM_IN_DOCKER=1 блокирует рекурсию**

```bash
cd /Users/alexander.kopichin/work/flowManager && AFM_USE_DOCKER=1 AFM_IN_DOCKER=1 go run ./cmd/afm/... run --help 2>&1 | head -5
```

Ожидается: обычный вывод help (не попытка запустить docker).

- [ ] **Шаг 5: Запустить все тесты**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./... -v 2>&1 | tail -30
```

Ожидается: все тесты PASS.

- [ ] **Шаг 6: Линт**

```bash
cd /Users/alexander.kopichin/work/flowManager && ./bin/golangci-lint run --fix ./...
```

- [ ] **Шаг 7: Коммит**

```bash
git add cmd/afm/run.go
git commit -m "feat: run.go — Docker self-re-exec при docker.enabled или AFM_USE_DOCKER"
```

---

## Task 5: Dockerfile.runtime + Makefile + goreleaser

**Files:**
- Create: `Dockerfile.runtime`
- Modify: `Makefile`
- Modify: `.goreleaser.yml`

- [ ] **Шаг 1: Создать Dockerfile.runtime**

Создать `/Users/alexander.kopichin/work/flowManager/Dockerfile.runtime`:

```dockerfile
# Stage 1: build afm binary
FROM golang:1.26-bookworm AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /afm ./cmd/afm

# Stage 2: runtime environment
FROM ubuntu:24.04

# Node 22 + Python 3.12 + dev tools
RUN apt-get update && apt-get install -y \
      curl \
      git \
      ca-certificates \
      gnupg \
      python3 \
      python3-pip \
      python3-venv \
      build-essential && \
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# Go 1.26 (для verify: go test и подобных команд в flow)
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:$PATH"

# claude CLI
RUN npm install -g @anthropic-ai/claude-code

# afm binary
COPY --from=builder /afm /usr/local/bin/afm

WORKDIR /project
ENTRYPOINT ["/usr/local/bin/afm"]
```

- [ ] **Шаг 2: Убедиться что образ собирается**

```bash
cd /Users/alexander.kopichin/work/flowManager && docker build -f Dockerfile.runtime -t akopichin/afm:dev . 2>&1 | tail -10
```

Ожидается: `Successfully built ...` или `=> => naming to docker.io/akopichin/afm:dev`.

- [ ] **Шаг 3: Smoke test образа**

```bash
docker run --rm akopichin/afm:dev --help 2>&1 | head -5
```

Ожидается: вывод help afm.

- [ ] **Шаг 4: Добавить таргеты в Makefile**

Добавить в конец `Makefile`:

```makefile
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
```

- [ ] **Шаг 5: Обновить .goreleaser.yml**

Добавить в `.goreleaser.yml` секцию `dockers` после `archives`:

```yaml
dockers:
  - image_templates:
      - "akopichin/afm:{{ .Tag }}"
      - "akopichin/afm:latest"
    dockerfile: Dockerfile.runtime
    build_flag_templates:
      - "--platform=linux/amd64"
```

- [ ] **Шаг 6: Проверить make docker-build**

```bash
cd /Users/alexander.kopichin/work/flowManager && make docker-build 2>&1 | tail -5
```

Ожидается: успешная сборка.

- [ ] **Шаг 7: Коммит**

```bash
git add Dockerfile.runtime Makefile .goreleaser.yml
git commit -m "feat: Dockerfile.runtime, make docker-build/push/run, goreleaser docker"
```

---

## Task 6: Документация

**Files:**
- Modify: `config.example.yaml`
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [ ] **Шаг 1: Добавить docker секцию в config.example.yaml**

Добавить в конец файла `config.example.yaml`:

```yaml

# Docker mode — запускать afm и агентов внутри Docker-контейнера.
# Включается через конфиг или переменную окружения AFM_USE_DOCKER=1.
# docker:
#   # Включить Docker mode. nil/absent → смотрим $AFM_USE_DOCKER.
#   # Default: false
#   # enabled: true
#
#   # Docker-образ для запуска.
#   # Default: "akopichin/afm:latest" (или $AFM_DOCKER_IMAGE)
#   # image: akopichin/afm:latest
```

- [ ] **Шаг 2: Добавить секцию Docker Mode в CLAUDE.md**

Добавить в конец файла `CLAUDE.md`:

```markdown

## Docker Mode

afm умеет автоматически перезапускать себя внутри Docker при включённом Docker-режиме.

### Включение

Через конфиг (`.afm/config.yaml` или `~/.afm/config.yaml`):
```yaml
docker:
  enabled: true
  image: akopichin/afm:latest   # опционально, это дефолт
```

Или через переменную окружения:
```bash
AFM_USE_DOCKER=1 afm run flow.yaml
```

### Что монтируется автоматически

| Хост | Контейнер | Назначение |
|------|-----------|------------|
| `$(pwd)` (абсолютный путь) | тот же путь | Проект + `.afm/` (runs, flows, config) |
| `~/.claude/` | `/root/.claude` | Auth, skills, память |
| `~/.afm/` | `/root/.afm` | Глобальный конфиг afm |
| Нестандартные агенты из flow | `/usr/local/bin/<cmd>` (`:ro`) | Кастомные команды |

### Environment Variables

| Переменная | Назначение |
|-----------|------------|
| `AFM_USE_DOCKER=1` | Включить Docker mode без правки конфига |
| `AFM_IN_DOCKER=1` | Выставляется внутри контейнера — предотвращает рекурсию (не трогать) |
| `AFM_DOCKER_IMAGE` | Переопределить образ (например, для локальной сборки) |
| `ANTHROPIC_API_KEY` | Автоматически пробрасывается в контейнер |
| `ANTHROPIC_BASE_URL` | Автоматически пробрасывается в контейнер |

### Публикация нового образа

```bash
make docker-push   # собирает Dockerfile.runtime и пушит в akopichin/afm:latest
```

### Отладка

```bash
# Посмотреть что именно будет запущено
AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=local/afm:dev afm run flow.yaml

# Войти в контейнер вручную
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/root/.claude \
  -v ~/.afm:/root/.afm \
  akopichin/afm:latest bash
```

### Нестандартные агенты (не claude)

Если в flow прописан `command: glm51` (или другой не-claude бинарник), afm автоматически:
1. Находит бинарник через `which glm51`
2. Монтирует его в контейнер: `-v /path/to/glm51:/usr/local/bin/glm51:ro`

Бинарники, не найденные в PATH на хосте, молча пропускаются.
```

- [ ] **Шаг 3: Добавить секцию "Запуск в Docker" в README.md**

Найти в `README.md` конец секции `## Установка` и добавить после неё:

```markdown
### Запуск в Docker (без локальной установки)

```bash
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/root/.claude \
  -v ~/.afm:/root/.afm \
  -e ANTHROPIC_API_KEY \
  akopichin/afm:latest \
  run flow.yaml
```

Или включить автоматический Docker-режим в конфиге — тогда обычная команда `afm run` сама перезапустится в контейнере:

```yaml
# .afm/config.yaml
docker:
  enabled: true
```

Образ включает: claude CLI, Node 22, Python 3.12, Go 1.26, git.
```

- [ ] **Шаг 4: Проверить что всё компилируется и тесты проходят**

```bash
cd /Users/alexander.kopichin/work/flowManager && go test ./... && echo "ALL PASS"
```

- [ ] **Шаг 5: Коммит**

```bash
git add config.example.yaml CLAUDE.md README.md
git commit -m "docs: Docker Mode — config.example, CLAUDE.md, README"
```

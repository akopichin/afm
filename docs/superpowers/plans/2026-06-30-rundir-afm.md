# `--dir` flag + rename `.flowManager` → `.afm` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename `.flowManager` → `.afm` everywhere and add `--dir` persistent flag + `FLOWMANAGER_DIR` env variable so users can specify the parent directory for `.afm`.

**Architecture:** Package-level `rootDir string` in `cmd/flowmanager/main.go` is set by `PersistentPreRunE` (flag → env → `.`). All commands read the effective `.afm` path via `fmDir()`. `pkg/state.FindLatestRunDir` receives `base` as an explicit parameter instead of hardcoding the path.

**Tech Stack:** Go stdlib, cobra (`github.com/spf13/cobra`).

## Global Constraints

- Do NOT change the Go version in `go.mod`.
- Commit messages in Russian.
- Run `go vet ./...` and `golangci-lint run` (or equivalent) before each commit — zero lint warnings.
- Directory `.flowManager` → `.afm` is a breaking change; no migration code in this scope.
- `fmDir()` zero-value behaviour: `filepath.Join("", ".afm")` = `.afm` — tests that create subcommands in isolation (without root `PersistentPreRunE`) still work because `rootDir` defaults to `""`.

---

## File Map

| File | Change |
|------|--------|
| `pkg/state/state.go` | `FindLatestRunDir(base, flowName string)` — remove hardcoded `.flowManager/runs` |
| `pkg/state/state_test.go` | Add `TestFindLatestRunDir` |
| `pkg/config/config.go` | `Load()` uses `.afm` instead of `.flowManager` |
| `cmd/flowmanager/main.go` | Add `var rootDir string`, `fmDir()`, persistent `--dir` flag, `PersistentPreRunE` |
| `cmd/flowmanager/run.go` | `resolveFlowPath`/`resolveRun` use `fmDir()`; `config.LoadFrom` replaces `config.Load()` |
| `cmd/flowmanager/check.go` | Use `fmDir()` |
| `cmd/flowmanager/approve.go` | `findLatestRunDir` uses `fmDir()` |
| `cmd/flowmanager/init.go` | Use `fmDir()` |
| `cmd/flowmanager/list.go` | Use `fmDir()` |
| `cmd/flowmanager/revise_test.go` | `makeRunState`/`loadRunState` use `.afm`; same for `approve_test.go` assertion |
| `cmd/flowmanager/approve_test.go` | Update `TestApproveWrongFlowBug` path assertion |
| `CLAUDE.md` | All `.flowManager` refs → `.afm` |
| `README.md` | All `.flowManager` refs → `.afm` |
| `example-flow.yaml` | Comment `.flowManager/flows/` → `.afm/flows/` |

---

## Task 1: pkg/state — параметризовать FindLatestRunDir

**Files:**
- Modify: `pkg/state/state.go`
- Modify: `pkg/state/state_test.go`

**Interfaces:**
- Produces: `FindLatestRunDir(base string, flowName string) (string, error)` — callers pass `base = fmDir()`

- [x] **Step 1: Написать падающий тест**

Добавить в `pkg/state/state_test.go`:

```go
func TestFindLatestRunDir(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs")

	// run-старый
	old := filepath.Join(base, "myflow-20260101-100000")
	os.MkdirAll(old, 0755)

	// run-новый (алфавитно позже)
	newer := filepath.Join(base, "myflow-20260101-120000")
	os.MkdirAll(newer, 0755)

	got, err := state.FindLatestRunDir(base, "myflow")
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Errorf("got %q, want %q", got, newer)
	}
}

func TestFindLatestRunDir_NotFound(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs")
	os.MkdirAll(base, 0755)

	_, err := state.FindLatestRunDir(base, "noflow")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
```

- [x] **Step 2: Убедиться что тест не компилируется**

```bash
cd /Users/alexander.kopichin/work/flowManager
go test ./pkg/state/... 2>&1
```

Ожидание: ошибка компиляции — `too many arguments in call to state.FindLatestRunDir`.

- [x] **Step 3: Обновить pkg/state/state.go**

Заменить функцию `FindLatestRunDir`:

```go
// FindLatestRunDir finds the most recent run directory for a given flow name
// under <base>/.
func FindLatestRunDir(base, flowName string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read runs dir: %w", err)
	}
	var latest string
	prefix := flowName + "-"
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
			latest = filepath.Join(base, e.Name())
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no run found for flow %q", flowName)
	}
	return latest, nil
}
```

- [x] **Step 4: Запустить тесты**

```bash
go test ./pkg/state/... -v
```

Ожидание: все тесты PASS (включая новые `TestFindLatestRunDir*`).

- [x] **Step 5: Коммит**

```bash
git add pkg/state/state.go pkg/state/state_test.go
git commit -m "refactor: FindLatestRunDir принимает base-директорию вместо хардкода"
```

---

## Task 2: pkg/config — переименовать .flowManager → .afm в Load()

**Files:**
- Modify: `pkg/config/config.go`

**Interfaces:**
- `Load()` теперь ищет `~/.afm/config.yaml` и `.afm/config.yaml`
- `LoadFrom(globalDir, projectDir string)` не меняется

- [x] **Step 1: Обновить Load() в pkg/config/config.go**

Заменить функцию `Load`:

```go
// Load loads configuration from the standard locations:
// ~/.afm/config.yaml (global) and .afm/config.yaml (project).
func Load() (Config, error) {
	home, _ := os.UserHomeDir()
	return LoadFrom(
		filepath.Join(home, ".afm"),
		".afm",
	)
}
```

- [x] **Step 2: Запустить тесты пакета config**

```bash
go test ./pkg/config/... -v
```

Ожидание: все PASS (тесты используют `LoadFrom` — не затронуты).

- [x] **Step 3: Коммит**

```bash
git add pkg/config/config.go
git commit -m "refactor: config.Load() использует .afm вместо .flowManager"
```

---

## Task 3: cmd/flowmanager/main.go — persistent flag + fmDir()

**Files:**
- Modify: `cmd/flowmanager/main.go`

**Interfaces:**
- Produces: `var rootDir string` (package-level)
- Produces: `func fmDir() string` — возвращает `filepath.Join(rootDir, ".afm")`
- Флаг `--dir` доступен для всех подкоманд

- [x] **Step 1: Обновить main.go**

Заменить весь файл `cmd/flowmanager/main.go`:

```go
package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var rootDir string

func fmDir() string {
	return filepath.Join(rootDir, ".afm")
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "flowmanager",
		Short: "Orchestrate multi-stage AI flows",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if rootDir == "" {
				rootDir = os.Getenv("FLOWMANAGER_DIR")
			}
			if rootDir == "" {
				rootDir = "."
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&rootDir, "dir", "", "base directory for .afm (default: current dir, env: FLOWMANAGER_DIR)")
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newReviseCmd(),
		newRetryCmd(),
		newInitCmd(),
		newListCmd(),
	)
	return root
}
```

- [x] **Step 2: Убедиться что проект компилируется**

```bash
go build ./cmd/flowmanager/... 2>&1
```

Ожидание: успешная сборка (команды ещё используют хардкод — это нормально на этом шаге).

- [x] **Step 3: Коммит**

```bash
git add cmd/flowmanager/main.go
git commit -m "feat: добавить --dir persistent flag и fmDir() helper"
```

---

## Task 4: Обновить все cmd-команды + тесты

**Files:**
- Modify: `cmd/flowmanager/run.go`
- Modify: `cmd/flowmanager/check.go`
- Modify: `cmd/flowmanager/approve.go`
- Modify: `cmd/flowmanager/init.go`
- Modify: `cmd/flowmanager/list.go`
- Modify: `cmd/flowmanager/revise_test.go`
- Modify: `cmd/flowmanager/approve_test.go`

**Interfaces:**
- Consumes: `fmDir()` из main.go (Task 3)
- Consumes: `state.FindLatestRunDir(base, flowName)` (Task 1)
- Consumes: `config.LoadFrom(globalDir, projectDir)` из pkg/config (уже существует)

- [x] **Step 1: Обновить run.go**

В `run.go` заменить:
1. `config.Load()` → `config.LoadFrom` с правильными путями
2. `resolveFlowPath` — `.flowManager` → `fmDir()`
3. `resolveRun` — `.flowManager` → `fmDir()`
4. Вызов `state.FindLatestRunDir` — добавить `base` аргумент

Обновить импорты (добавить `"os"` если нет) и функции:

```go
// В начале RunE, заменить config.Load() на:
home, _ := os.UserHomeDir()
cfg, err := config.LoadFrom(filepath.Join(home, ".afm"), fmDir())
if err != nil {
    return err
}
```

```go
func resolveFlowPath(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	entries, err := os.ReadDir(filepath.Join(fmDir(), "flows"))
	if err != nil {
		return "", errors.New("no flow file provided and " + fmDir() + "/flows/ not found")
	}
	var yamls []string
	for _, e := range entries {
		if !e.IsDir() && (filepath.Ext(e.Name()) == extYAML || filepath.Ext(e.Name()) == extYML) {
			yamls = append(yamls, filepath.Join(fmDir(), "flows", e.Name()))
		}
	}
	if len(yamls) == 0 {
		return "", errors.New("no flow YAML files found in " + fmDir() + "/flows/")
	}
	if len(yamls) == 1 {
		return yamls[0], nil
	}
	return "", fmt.Errorf("multiple flow files found; specify one: %v", yamls)
}
```

```go
func resolveRun(f *flow.Flow) (runDir string, store *state.Store, err error) {
	base := filepath.Join(fmDir(), "runs")

	stageIDs := make([]string, len(f.Stages))
	for i, s := range f.Stages {
		stageIDs[i] = s.ID
	}

	existing, lookErr := state.FindLatestRunDir(base, f.Name)
	if lookErr == nil {
		store, err = state.Open(existing, stageIDs)
		if err == nil {
			snap := store.Snapshot()
			if !snap.AllDone() {
				fmt.Printf("flowmanager: resuming run %s\n", filepath.Base(existing))
				return existing, store, nil
			}
		} else {
			fmt.Fprintf(os.Stderr, "warning: failed to open existing run %s: %v; starting new run\n", filepath.Base(existing), err)
		}
		if store != nil {
			store.Close()
		}
	}

	ts := time.Now().Format("20060102-150405")
	runDir = filepath.Join(base, f.Name+"-"+ts)
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return
	}
	store, err = state.Open(runDir, stageIDs)
	return
}
```

- [x] **Step 2: Обновить check.go**

Заменить:
```go
base := filepath.Join(".flowManager", "runs")
```
На:
```go
base := filepath.Join(fmDir(), "runs")
```

- [x] **Step 3: Обновить approve.go**

В функции `findLatestRunDir` заменить:
```go
base := filepath.Join(".flowManager", "runs")
```
На:
```go
base := filepath.Join(fmDir(), "runs")
```

- [x] **Step 4: Обновить init.go**

Заменить:
```go
outPath := ".flowManager/flows/" + name + ".yaml"
os.MkdirAll(".flowManager/flows", 0755) //nolint:errcheck
```
На:
```go
outPath := filepath.Join(fmDir(), "flows", name+".yaml")
os.MkdirAll(filepath.Join(fmDir(), "flows"), 0755) //nolint:errcheck
```

Добавить `"path/filepath"` в импорты если его нет.

- [x] **Step 5: Обновить list.go**

Заменить:
```go
dir := filepath.Join(".flowManager", "flows")
```
На:
```go
dir := filepath.Join(fmDir(), "flows")
```

- [x] **Step 6: Обновить тестовые helpers в revise_test.go**

Заменить в `makeRunState`:
```go
runDir := filepath.Join(".flowManager", "runs", runName)
```
На:
```go
runDir := filepath.Join(".afm", "runs", runName)
```

- [x] **Step 7: Обновить approve_test.go**

В `TestApproveWrongFlowBug` заменить:
```go
runDir := filepath.Join(".flowManager", "runs", "flow-a-20260101-100000")
```
На:
```go
runDir := filepath.Join(".afm", "runs", "flow-a-20260101-100000")
```

- [x] **Step 8: Запустить все тесты**

```bash
go test ./... -v 2>&1 | tail -40
```

Ожидание: все PASS, нет FAIL.

- [x] **Step 9: Проверить линтер**

```bash
go vet ./...
```

Ожидание: нет warnings.

- [x] **Step 10: Коммит**

```bash
git add cmd/flowmanager/run.go cmd/flowmanager/check.go cmd/flowmanager/approve.go \
        cmd/flowmanager/init.go cmd/flowmanager/list.go \
        cmd/flowmanager/revise_test.go cmd/flowmanager/approve_test.go
git commit -m "feat: все команды используют fmDir() и .afm"
```

---

## Task 5: Обновить документацию

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`
- Modify: `example-flow.yaml`

- [x] **Step 1: Обновить CLAUDE.md**

Заменить все вхождения `.flowManager` на `.afm` в `CLAUDE.md`.
Также добавить описание нового параметра в секцию "Environment Variables":

```markdown
| `FLOWMANAGER_DIR` | Родительская директория для `.afm` (если не задан `--dir`) | CLI / env |
```

И добавить описание `--dir` флага в раздел "Common Changes" или в начало документа.

- [x] **Step 2: Обновить README.md**

Заменить все вхождения `.flowManager` на `.afm`.
Заменить `~/.flowManager/config.yaml` на `~/.afm/config.yaml`.
Добавить после описания команды `run` секцию о `--dir`:

```markdown
## Указание рабочей директории

По умолчанию `.afm/` создаётся в текущей папке. Чтобы вынести её в другое место:

```bash
# Флаг (разовый запуск)
flowmanager --dir ~/my-flows run

# Переменная окружения (постоянно)
export FLOWMANAGER_DIR=~/my-flows
flowmanager run
```

Все команды (`run`, `check`, `approve`, `revise`, `retry`, `init`, `list`) уважают `--dir`.
```

- [x] **Step 3: Обновить example-flow.yaml**

Заменить комментарий:
```yaml
# Или положить в .flowManager/flows/ — тогда run без аргументов сам найдёт.
```
На:
```yaml
# Или положить в .afm/flows/ — тогда run без аргументов сам найдёт.
```

- [x] **Step 4: Коммит**

```bash
git add CLAUDE.md README.md example-flow.yaml
git commit -m "docs: переименовать .flowManager → .afm, документировать --dir"
```

---

## Self-Review

### Spec coverage

| Требование из спека | Задача |
|---------------------|--------|
| Переименовать `.flowManager` → `.afm` | Task 1–5 |
| `~/.flowManager` → `~/.afm` | Task 2 |
| `--dir` persistent flag на root команде | Task 3 |
| `FLOWMANAGER_DIR` env переменная | Task 3 |
| Приоритет: flag > env > `.` | Task 3 |
| Все подкоманды уважают `--dir` | Task 4 |
| `FindLatestRunDir` принимает `base` | Task 1 |
| project config берётся из `fmDir()` | Task 4 (`config.LoadFrom`) |
| Тесты: `makeRunState` использует `.afm` | Task 4 |
| Документация обновлена | Task 5 |

### Placeholder scan

Нет TBD, TODO, или неполных шагов.

### Type consistency

- `FindLatestRunDir(base, flowName string)` определена в Task 1, используется в Task 4 с сигнатурой `state.FindLatestRunDir(base, f.Name)` — совпадает.
- `fmDir() string` определена в Task 3, используется во всех Task 4 шагах — без аргументов, возвращает `string` — совпадает.
- `config.LoadFrom(globalDir, projectDir string)` уже существует в `pkg/config/config.go` — сигнатура не меняется.

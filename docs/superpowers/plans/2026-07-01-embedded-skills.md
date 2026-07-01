# Embedded AFM Skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Встроить AFM-скилы (`afm`, `afm-check`, `afm-init`, `afm-retry`, `afm-review`) в бинарник и добавить команду `afm install-skills`, заменяющую ручное копирование из `install.sh`.

**Architecture:** `assets.SkillsFS` (второй `embed.FS`) хранит файлы `claude/skills/*/SKILL.md`. Команда `install-skills` обходит этот FS через `fs.WalkDir` и записывает файлы в `~/.claude/skills/`. `install.sh` делегирует установку скилов бинарнику через интерактивный вопрос.

**Tech Stack:** Go 1.26, `embed`, `io/fs`, cobra, bash

## Global Constraints

- Модуль: `github.com/akopichin/afm`
- Go версию в `go.mod` не менять
- Все коммиты на русском языке
- После правок линт должен проходить: `./bin/golangci-lint run ./...`
- Тесты: `go test ./cmd/afm/... -count=1`

---

### Task 1: Добавить `SkillsFS` в `assets/assets.go`

**Files:**
- Modify: `assets/assets.go`

**Interfaces:**
- Produces: `assets.SkillsFS embed.FS` — экспортируемый embed.FS с путями вида `claude/skills/afm/SKILL.md`

- [ ] **Step 1: Убедиться что директория существует**

```bash
ls assets/claude/skills/
```

Ожидаемый вывод: `afm  afm-check  afm-init  afm-retry  afm-review`

- [ ] **Step 2: Добавить вторую embed-директиву в `assets/assets.go`**

Открыть файл и добавить после существующей `var FS embed.FS`:

```go
package assets

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed prompts
var FS embed.FS

//go:embed claude/skills
var SkillsFS embed.FS

// ReadPrompt returns a prompt by filename. If overrideDir is non-empty,
// reads from that directory instead of the embedded files.
func ReadPrompt(name, overrideDir string) (string, error) {
	if overrideDir != "" {
		data, err := os.ReadFile(filepath.Join(overrideDir, name))
		return string(data), err
	}
	data, err := FS.ReadFile("prompts/" + name)
	return string(data), err
}
```

- [ ] **Step 3: Проверить что билд не сломан**

```bash
go build ./...
```

Ожидаемый вывод: (пусто, без ошибок)

- [ ] **Step 4: Коммит**

```bash
git add assets/assets.go
git commit -m "feat(assets): встроить скилы afm/* в SkillsFS"
```

---

### Task 2: Команда `install-skills` — реализация и тесты (TDD)

**Files:**
- Create: `cmd/afm/install_skills.go`
- Create: `cmd/afm/install_skills_test.go`

**Interfaces:**
- Consumes: `assets.SkillsFS embed.FS` (из Task 1)
- Produces:
  - `newInstallSkillsCmd() *cobra.Command` — фабрика cobra-команды, регистрируется в `main.go`
  - `installSkills(skillsDir string, force bool) error` — внутренняя логика, тестируется напрямую
  - `resolveSkillsDir(override string) (string, error)` — разворачивает `~`/пустой путь в абсолютный

- [ ] **Step 1: Написать тест-файл**

Создать `cmd/afm/install_skills_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSkillsCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("installSkills: %v", err)
	}
	for _, name := range []string{"afm", "afm-check", "afm-init", "afm-retry", "afm-review"} {
		p := filepath.Join(dir, name, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("ожидался файл %s: %v", p, err)
		}
	}
}

func TestInstallSkillsSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("первая установка: %v", err)
	}
	target := filepath.Join(dir, "afm", "SKILL.md")
	if err := os.WriteFile(target, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("повторная установка: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "modified" {
		t.Error("файл не должен был перезаписаться без --force")
	}
}

func TestInstallSkillsForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := installSkills(dir, false); err != nil {
		t.Fatalf("первая установка: %v", err)
	}
	target := filepath.Join(dir, "afm", "SKILL.md")
	original, _ := os.ReadFile(target)
	if err := os.WriteFile(target, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installSkills(dir, true); err != nil {
		t.Fatalf("force установка: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) == "modified" {
		t.Error("файл должен был перезаписаться при --force")
	}
	if len(data) == 0 || string(data) != string(original) {
		t.Errorf("содержимое после --force не совпадает с embedded: got %q", string(data))
	}
}

func TestResolveSkillsDirOverride(t *testing.T) {
	got, err := resolveSkillsDir("/custom/path")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/path" {
		t.Errorf("ожидался /custom/path, получили %s", got)
	}
}

func TestResolveSkillsDirDefault(t *testing.T) {
	got, err := resolveSkillsDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Error("resolveSkillsDir(\"\") вернул пустую строку")
	}
	// Должен содержать .claude/skills
	if filepath.Base(got) != "skills" {
		t.Errorf("ожидался путь .../.claude/skills, получили %s", got)
	}
}
```

- [ ] **Step 2: Запустить тесты — убедиться что падают**

```bash
go test ./cmd/afm/... -run TestInstallSkills -v -count=1
```

Ожидаемый вывод: ошибка компиляции `undefined: installSkills`

- [ ] **Step 3: Создать `cmd/afm/install_skills.go`**

```go
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/assets"
)

func newInstallSkillsCmd() *cobra.Command {
	var skillsDir string
	var force bool

	cmd := &cobra.Command{
		Use:   "install-skills",
		Short: "Установить AFM Claude-скиллы в ~/.claude/skills/",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveSkillsDir(skillsDir)
			if err != nil {
				return err
			}
			return installSkills(dir, force)
		},
	}
	cmd.Flags().StringVar(&skillsDir, "skills-dir", "", "путь назначения (по умолчанию: ~/.claude/skills)")
	cmd.Flags().BoolVar(&force, "force", false, "перезаписать существующие файлы")
	return cmd
}

func resolveSkillsDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

func installSkills(dest string, force bool) error {
	fmt.Printf("Установка AFM-скиллов в %s/\n", dest)

	return fs.WalkDir(assets.SkillsFS, "claude/skills", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// p = "claude/skills/afm/SKILL.md" → rel = "afm/SKILL.md"
		rel := strings.TrimPrefix(p, "claude/skills/")
		skillName := path.Dir(rel) // "afm"

		destPath := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(destPath), err)
		}

		if !force {
			if _, statErr := os.Stat(destPath); statErr == nil {
				fmt.Printf("  - %s (пропущен, уже существует)\n", skillName)
				return nil
			}
		}

		data, err := assets.SkillsFS.ReadFile(p)
		if err != nil {
			return fmt.Errorf("читаю embedded скилл %s: %w", p, err)
		}
		if err := os.WriteFile(destPath, data, 0644); err != nil {
			return fmt.Errorf("запись %s: %w", destPath, err)
		}
		fmt.Printf("  + %s\n", skillName)
		return nil
	})
}
```

- [ ] **Step 4: Запустить тесты — убедиться что проходят**

```bash
go test ./cmd/afm/... -run "TestInstallSkills|TestResolveSkillsDir" -v -count=1
```

Ожидаемый вывод: все тесты PASS

- [ ] **Step 5: Проверить линт**

```bash
./bin/golangci-lint run ./cmd/afm/...
```

Ожидаемый вывод: (пусто, без ошибок)

- [ ] **Step 6: Коммит**

```bash
git add cmd/afm/install_skills.go cmd/afm/install_skills_test.go
git commit -m "feat(cmd): команда install-skills — устанавливает AFM-скиллы из embedded FS"
```

---

### Task 3: Зарегистрировать команду в `main.go`

**Files:**
- Modify: `cmd/afm/main.go`

**Interfaces:**
- Consumes: `newInstallSkillsCmd() *cobra.Command` (из Task 2)

- [ ] **Step 1: Добавить `newInstallSkillsCmd()` в `root.AddCommand`**

В `cmd/afm/main.go` найти блок `root.AddCommand(...)` и добавить новую команду:

```go
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newReviseCmd(),
		newRetryCmd(),
		newInitCmd(),
		newListCmd(),
		newInstallSkillsCmd(),
	)
```

- [ ] **Step 2: Проверить что команда видна в help**

```bash
go run ./cmd/afm/... --help
```

Ожидаемый вывод: в списке команд присутствует `install-skills   Установить AFM Claude-скиллы в ~/.claude/skills/`

- [ ] **Step 3: Smoke-test команды**

```bash
go run ./cmd/afm/... install-skills --skills-dir /tmp/test-skills
ls /tmp/test-skills/
```

Ожидаемый вывод: `afm  afm-check  afm-init  afm-retry  afm-review`

- [ ] **Step 4: Запустить все тесты**

```bash
go test ./... -count=1
```

Ожидаемый вывод: все тесты PASS

- [ ] **Step 5: Коммит**

```bash
git add cmd/afm/main.go
git commit -m "feat(cmd): зарегистрировать install-skills в root"
```

---

### Task 4: Обновить `install.sh`

**Files:**
- Modify: `install.sh`

- [ ] **Step 1: Открыть `install.sh` и применить изменения**

Убрать строку `SKILLS_DIR="$HOME/.claude/skills"` (больше не нужна).

Заменить весь блок `# --- Claude Skills ---` (от комментария до конца файла) на:

```bash
# --- Claude Skills ---
echo ""
read -p "Установить Claude-скиллы (/afm, /afm-check и др.) в ~/.claude/skills/? [Y/n] " answer
case "$answer" in
  [nN]*)
    echo "Пропущено. Установи позже: afm install-skills"
    ;;
  *)
    "$INSTALL_DIR/$BIN_NAME" install-skills
    ;;
esac
```

- [ ] **Step 2: Проверить синтаксис скрипта**

```bash
bash -n install.sh
```

Ожидаемый вывод: (пусто, без ошибок)

- [ ] **Step 3: Ручная проверка — dry run (ответить "n")**

```bash
# Установим бинарник чтобы был доступен
go build -o /tmp/afm-test ./cmd/afm/
INSTALL_DIR=/tmp BIN_NAME=afm-test bash install.sh
```

Ввести `n` на вопрос. Ожидаемый вывод: `Пропущено. Установи позже: afm install-skills`

- [ ] **Step 4: Коммит**

```bash
git add install.sh
git commit -m "feat(install): делегировать установку скиллов команде afm install-skills"
```

---

## Self-Review

**Spec coverage:**
- ✅ `assets.SkillsFS` встроен (Task 1)
- ✅ `afm install-skills [--skills-dir] [--force]` реализован (Task 2)
- ✅ Идемпотентность без `--force`, перезапись с `--force` (Task 2, тесты)
- ✅ Зарегистрирован в root (Task 3)
- ✅ `install.sh` спрашивает пользователя и делегирует бинарнику (Task 4)

**Placeholder scan:** нет TBD, все шаги содержат реальный код.

**Type consistency:** `installSkills(dir string, force bool) error` — одна сигнатура во всех задачах и тестах.

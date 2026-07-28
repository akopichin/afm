# Единые константы имён тем скина и путей .afm/runs/flows — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Свести к одному источнику правды имена тем скина (`goga`/`novacorps`/`coffee`), продублированные в `pkg/config` и `pkg/server`, и путь `.afm`/`runs`/`flows`, независимо построенный литералами в 3 пакетах.

**Architecture:** `pkg/config` — единственный из затронутых пакетов без внутренних (`afm/pkg/*`) зависимостей (чистый leaf, уже подтверждено в предыдущих спеках этой серии), поэтому именно там живут разделяемые константы (`ThemeGoga/Novacorps/Coffee`, `AfmDir`). `pkg/server` и `pkg/docker/secrets.go` их импортируют. `cmd/afm/main.go` получает два новых хелпера (`runsDir()`/`flowsDir()`), по образцу уже существующего `fmDir()`, используемых во всех остальных файлах пакета `main`.

**Tech Stack:** Go 1.26.4, стандартная библиотека — без новых внешних зависимостей.

## Global Constraints

- `config.ThemeGoga = "goga"`, `config.ThemeNovacorps = "novacorps"`, `config.ThemeCoffee = "coffee"` — экспортированные версии существующих неэкспортируемых `themeGoga`/`themeNovacorps`/`themeCoffee` в `pkg/config/config.go`. Значения и логика `EffectiveTheme()` не меняются, только видимость.
- `pkg/server`'s `themeGoga`/`themeNovacorps`/`themeCoffee` удаляются, использования переводятся на `config.Theme*`. `themeCustom` остаётся локальным (уникальное для сервера понятие — активный `skin_dir`, в `pkg/config` не существует). Логика `builtinSkinName()` (switch с default-веткой, БЕЗ warning на неизвестное значение) не меняется — это сознательно отличается от `config.EffectiveTheme()`'s поведения (with warning), и эти два switch'а НЕ объединяются в один, только источник трёх строк-значений становится общим.
- `config.AfmDir = ".afm"` — новая экспортируемая константа в `pkg/config/config.go`.
- `cmd/afm/main.go` получает `runsDir()`/`flowsDir()` рядом с `fmDir()`, и `fmDir()` использует `config.AfmDir` вместо литерала.
- Все 5 мест `filepath.Join(fmDir(), "runs")` (`check.go`, `approve.go`, `retry.go`, `revise.go`, `run.go`) заменяются на `runsDir()`.
- Все использования `filepath.Join(fmDir(), "flows")`/`+"/flows/"` (`init.go`, `list.go`, `run.go` — 4 вхождения) заменяются на `flowsDir()`-based конструкции, без изменения текста сообщений об ошибках.
- `pkg/docker/secrets.go`'s два литерала `".afm"` заменяются на `config.AfmDir`.
- `cmd/afm/run.go:41`'s `filepath.Join(home, ".afm")` (глобальный `~/.afm`, отдельно от `fmDir()`) заменяется на `filepath.Join(home, config.AfmDir)`.
- Публичные сигнатуры существующих функций не меняются — только внутренняя реализация и набор экспортированных констант.
- Импорты `path/filepath`, ставшие неиспользуемыми после замены единственного использования (`approve.go`, `retry.go`), удаляются; там, где `filepath.` используется ещё для чего-то (`revise.go`, `check.go`, `init.go`, `list.go`), импорт остаётся.

---

### Task 1: `pkg/config/config.go` — `ThemeGoga/Novacorps/Coffee` + `AfmDir`

**Files:**
- Modify: `pkg/config/config.go`

**Interfaces:**
- Consumes: ничего из предыдущих задач (первая задача).
- Produces: `config.ThemeGoga`, `config.ThemeNovacorps`, `config.ThemeCoffee`, `config.AfmDir` — используются в Task 2-5.

- [ ] **Step 1: Экспортировать константы тем**

Текущий блок (строки ~251-255):

```go
// Dashboard theme names returned by EffectiveTheme.
const (
	themeGoga      = "goga"
	themeNovacorps = "novacorps"
	themeCoffee    = "coffee"
)
```

Заменить на:

```go
// Dashboard theme names returned by EffectiveTheme.
const (
	ThemeGoga      = "goga"
	ThemeNovacorps = "novacorps"
	ThemeCoffee    = "coffee"
)
```

- [ ] **Step 2: Обновить `EffectiveTheme()`**

Текущее тело (строки ~261-272):

```go
func (c Config) EffectiveTheme() string {
	switch strings.ToLower(strings.TrimSpace(c.Theme)) {
	case themeGoga:
		return themeGoga
	case themeNovacorps:
		return themeNovacorps
	case themeCoffee, "":
		return themeCoffee
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown theme %q, using %s\n", c.Theme, themeCoffee)
		return themeCoffee
	}
}
```

Заменить на (только переименование, логика идентична):

```go
func (c Config) EffectiveTheme() string {
	switch strings.ToLower(strings.TrimSpace(c.Theme)) {
	case ThemeGoga:
		return ThemeGoga
	case ThemeNovacorps:
		return ThemeNovacorps
	case ThemeCoffee, "":
		return ThemeCoffee
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown theme %q, using %s\n", c.Theme, ThemeCoffee)
		return ThemeCoffee
	}
}
```

- [ ] **Step 3: Добавить `AfmDir`**

Добавить рядом с `ClaudeCommand` (например, сразу после блока с `ClaudeCommand`/`RecipeType*`, до `AgentRecipe`):

```go
// AfmDir — имя служебного каталога afm: и глобального (~/.afm), и per-project.
const AfmDir = ".afm"
```

- [ ] **Step 4: Собрать и прогнать пакет**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./pkg/config/... && go test ./pkg/config/... -v`
Expected: чистая сборка, все существующие тесты проходят без изменений (тесты используют `EffectiveTheme()` и строковые литералы, не имена констант напрямую).

- [ ] **Step 5: go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go vet ./pkg/config/...`
Expected: без замечаний.

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/config/config.go
git commit -m "feat(config): экспортируем ThemeGoga/Novacorps/Coffee, добавляем AfmDir"
```

---

### Task 2: `pkg/server` — использовать `config.Theme*`

**Files:**
- Modify: `pkg/server/server.go`
- Modify: `pkg/server/server_test.go`

**Interfaces:**
- Consumes: `config.ThemeGoga`, `config.ThemeNovacorps`, `config.ThemeCoffee` из Task 1.
- Produces: ничего нового для последующих задач.

`pkg/server` сегодня НЕ импортирует `pkg/config` (получает уже нормализованную строку через `Config.Theme string`) — этот таск добавляет новую, безопасную (без риска цикла) зависимость.

- [ ] **Step 1: Добавить импорт `config` в `server.go`**

Текущий блок импорта:

```go
import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/web"
)
```

Заменить на (добавлена строка `"github.com/akopichin/afm/pkg/config"`, алфавитный порядок перед `orchestrator`):

```go
import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/web"
)
```

- [ ] **Step 2: Убрать локальные `themeGoga`/`themeNovacorps`/`themeCoffee`**

Текущий блок (строки ~20-29):

```go
// Имена скинов. themeGoga/themeNovacorps соответствуют
// pkg/config.Config.EffectiveTheme() == "goga"/"novacorps"; themeCoffee —
// дефолтный встроенный скин, захардкоженный в index.html (пустое/неизвестное
// значение EffectiveTheme схлопывается в него); themeCustom — активный skin_dir.
const (
	themeGoga      = "goga"
	themeNovacorps = "novacorps"
	themeCoffee    = "coffee"
	themeCustom    = "custom"
)
```

Заменить на (оставлена только уникальная для сервера `themeCustom`, комментарий обновлён):

```go
// themeCustom — активный skin_dir (не значение EffectiveTheme, поэтому не в
// pkg/config; goga/novacorps/coffee берутся из config.Theme*).
const themeCustom = "custom"
```

- [ ] **Step 2: Заменить использования в `builtinSkinName()`**

Текущее тело (строки ~236-244):

```go
func (s *Server) builtinSkinName() string {
	switch s.theme {
	case themeGoga:
		return themeGoga
	case themeNovacorps:
		return themeNovacorps
	default:
		return themeCoffee
	}
}
```

Заменить на:

```go
func (s *Server) builtinSkinName() string {
	switch s.theme {
	case config.ThemeGoga:
		return config.ThemeGoga
	case config.ThemeNovacorps:
		return config.ThemeNovacorps
	default:
		return config.ThemeCoffee
	}
}
```

- [ ] **Step 3: Заменить оставшиеся 2 использования `themeCoffee`**

Строка ~203:
```go
			[]byte(`href="`+skinHrefFor(themeCoffee)+`"`), []byte(`href="`+skinHref+`"`))
```
→
```go
			[]byte(`href="`+skinHrefFor(config.ThemeCoffee)+`"`), []byte(`href="`+skinHref+`"`))
```

Строка ~205:
```go
			[]byte(`class="theme-`+themeCoffee+`"`), []byte(`class="theme-`+skinName+`"`))
```
→
```go
			[]byte(`class="theme-`+config.ThemeCoffee+`"`), []byte(`class="theme-`+skinName+`"`))
```

- [ ] **Step 4: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: сборка `pkg/server` упадёт на `server_test.go` (следующий шаг чинит это) — если основной пакет собирается чисто, а тестовый нет, это ожидаемо на этом этапе; выполни `go build ./pkg/server/...` (без `./...`, не включает тесты) чтобы подтвердить именно продакшн-код собирается чисто перед переходом к тестам.

- [ ] **Step 5: Обновить `server_test.go`**

Добавить импорт `"github.com/akopichin/afm/pkg/config"` в `server_test.go` (проверь текущий блок импорта файла, добавь по алфавиту). Заменить 7 использований (строки ~121, 147, 173, 210, 239, 302, 322 — используй поиск по `themeGoga`/`themeNovacorps` в файле, чтобы найти все):
- `themeGoga` → `config.ThemeGoga`
- `themeNovacorps` → `config.ThemeNovacorps`

- [ ] **Step 6: Прогнать пакет server + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go test ./pkg/server/... -v && go vet ./pkg/server/...`
Expected: всё зелёное.

- [ ] **Step 7: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/server/server.go pkg/server/server_test.go
git commit -m "refactor(server): используем config.ThemeGoga/Novacorps/Coffee вместо локальных копий"
```

---

### Task 3: `cmd/afm/main.go` — `AfmDir`, `runsDir()`, `flowsDir()`

**Files:**
- Modify: `cmd/afm/main.go`
- Modify: `cmd/afm/main_test.go`

**Interfaces:**
- Consumes: `config.AfmDir` из Task 1.
- Produces: `func runsDir() string`, `func flowsDir() string` — используются в Task 4.

- [ ] **Step 1: Добавить импорт `config`**

Текущий блок импорта:

```go
import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/docker"
)
```

Заменить на (добавлена строка, алфавитный порядок перед `docker`):

```go
import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
)
```

- [ ] **Step 2: `fmDir()` использует `config.AfmDir`, добавить `runsDir()`/`flowsDir()`**

Текущее:

```go
// fmDir возвращает путь к служебному каталогу .afm относительно rootDir.
// rootDir задаётся флагом --dir, иначе переменной AFM_DIR, иначе "."
// (PersistentPreRunE в корневой команде).
func fmDir() string {
	return filepath.Join(rootDir, ".afm")
}
```

Заменить на:

```go
// fmDir возвращает путь к служебному каталогу .afm относительно rootDir.
// rootDir задаётся флагом --dir, иначе переменной AFM_DIR, иначе "."
// (PersistentPreRunE в корневой команде).
func fmDir() string {
	return filepath.Join(rootDir, config.AfmDir)
}

// runsDir возвращает путь к каталогу с ранами внутри .afm.
func runsDir() string {
	return filepath.Join(fmDir(), "runs")
}

// flowsDir возвращает путь к каталогу с flow.yaml внутри .afm.
func flowsDir() string {
	return filepath.Join(fmDir(), "flows")
}
```

- [ ] **Step 3: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: чистая сборка (`runsDir()`/`flowsDir()` пока не используются нигде за пределами этого файла — это нормально, Go не ругается на неиспользуемые package-level функции).

- [ ] **Step 4: Написать тесты для `runsDir()`/`flowsDir()`**

Добавить в `cmd/afm/main_test.go`, сразу после существующего `TestFmDir`, по тому же паттерну (сохранение/восстановление `rootDir`):

```go
func TestRunsDir(t *testing.T) {
	prev := rootDir
	t.Cleanup(func() { rootDir = prev })

	rootDir = "/tmp/x"
	want := "/tmp/x/.afm/runs"
	if got := runsDir(); got != want {
		t.Errorf("runsDir() with rootDir=%q = %q, want %q", rootDir, got, want)
	}
}

func TestFlowsDir(t *testing.T) {
	prev := rootDir
	t.Cleanup(func() { rootDir = prev })

	rootDir = "/tmp/x"
	want := "/tmp/x/.afm/flows"
	if got := flowsDir(); got != want {
		t.Errorf("flowsDir() with rootDir=%q = %q, want %q", rootDir, got, want)
	}
}
```

- [ ] **Step 5: Запустить новые тесты**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./cmd/afm/... -run 'TestRunsDir|TestFlowsDir|TestFmDir' -v`
Expected: PASS — все 3 теста (включая неизменённый `TestFmDir`, который теперь идёт через `config.AfmDir`).

- [ ] **Step 6: Прогнать весь пакет + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./cmd/afm/... -v && go vet ./cmd/afm/...`
Expected: всё зелёное.

- [ ] **Step 7: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add cmd/afm/main.go cmd/afm/main_test.go
git commit -m "feat(cmd/afm): runsDir()/flowsDir() + fmDir() на config.AfmDir"
```

---

### Task 4: `cmd/afm` — использовать `runsDir()`/`flowsDir()` везде

**Files:**
- Modify: `cmd/afm/check.go`
- Modify: `cmd/afm/approve.go`
- Modify: `cmd/afm/retry.go`
- Modify: `cmd/afm/revise.go`
- Modify: `cmd/afm/init.go`
- Modify: `cmd/afm/list.go`
- Modify: `cmd/afm/run.go`

**Interfaces:**
- Consumes: `runsDir()`, `flowsDir()` из Task 3.
- Produces: ничего нового для последующих задач.

- [ ] **Step 1: `check.go`**

Строка ~49:
```go
			base := filepath.Join(fmDir(), "runs")
```
→
```go
			base := runsDir()
```
(`filepath` используется в файле ещё в 4 других местах — импорт не трогать.)

- [ ] **Step 2: `approve.go`**

Строка ~20:
```go
			runDir, stageIDs, err := state.FindLatestRunForStage(filepath.Join(fmDir(), "runs"), stageID)
```
→
```go
			runDir, stageIDs, err := state.FindLatestRunForStage(runsDir(), stageID)
```
Это единственное использование `filepath.` в файле — импорт `"path/filepath"` удаляется из блока импорта:
```go
import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/state"
)
```
→
```go
import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/state"
)
```

- [ ] **Step 3: `retry.go`**

Та же замена, что в `approve.go` (строка ~20, идентичная структура файла): заменить `filepath.Join(fmDir(), "runs")` на `runsDir()`, удалить ставший неиспользуемым импорт `"path/filepath"`.

- [ ] **Step 4: `revise.go`**

Строка ~37:
```go
			runDir, stageIDs, err := state.FindLatestRunForStage(filepath.Join(fmDir(), "runs"), stageID)
```
→
```go
			runDir, stageIDs, err := state.FindLatestRunForStage(runsDir(), stageID)
```
(`filepath` используется в файле ещё в одном месте, строка ~56 `filepath.Join(runDir, stageID)` — импорт НЕ трогать.)

- [ ] **Step 5: `init.go`**

Текущее (строка ~45):
```go
			flowsDir := filepath.Join(fmDir(), "flows")
			if err := os.MkdirAll(flowsDir, 0755); err != nil {
				return fmt.Errorf("create flows dir: %w", err)
			}
			outPath := filepath.Join(flowsDir, name+".yaml")
```

Локальная переменная `flowsDir` переименовывается в `dir` (иначе затеняет пакетную функцию `flowsDir()` из Task 3 — компилируется, но нечитаемо и не переиспользует функцию):

```go
			dir := flowsDir()
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("create flows dir: %w", err)
			}
			outPath := filepath.Join(dir, name+".yaml")
```

(`filepath` используется в файле ещё в одном месте, строка ~107 `filepath.Join(repoDir, ".gitignore")` — импорт НЕ трогать.)

- [ ] **Step 6: `list.go`**

Текущее (строка ~16):
```go
			dir := filepath.Join(fmDir(), "flows")
```
→
```go
			dir := flowsDir()
```
(`filepath` используется в файле ещё в 2 местах — импорт НЕ трогать.)

- [ ] **Step 7: `run.go` — 4 вхождения "flows" в `resolveFlowPath`**

Текущее тело:

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

Заменить на (текст сообщений об ошибках не меняется — меняется только то, как строится путь):

```go
func resolveFlowPath(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	entries, err := os.ReadDir(flowsDir())
	if err != nil {
		return "", errors.New("no flow file provided and " + flowsDir() + "/ not found")
	}
	var yamls []string
	for _, e := range entries {
		if !e.IsDir() && (filepath.Ext(e.Name()) == extYAML || filepath.Ext(e.Name()) == extYML) {
			yamls = append(yamls, filepath.Join(flowsDir(), e.Name()))
		}
	}
	if len(yamls) == 0 {
		return "", errors.New("no flow YAML files found in " + flowsDir() + "/")
	}
	if len(yamls) == 1 {
		return yamls[0], nil
	}
	return "", fmt.Errorf("multiple flow files found; specify one: %v", yamls)
}
```

- [ ] **Step 8: `run.go` — "runs" на строке ~407 (`resolveRun`)**

Текущее:
```go
func resolveRun(f *flow.Flow) (runDir string, store *state.Store, err error) {
	base := filepath.Join(fmDir(), "runs")
```
→
```go
func resolveRun(f *flow.Flow) (runDir string, store *state.Store, err error) {
	base := runsDir()
```

- [ ] **Step 9: `run.go` — глобальный `~/.afm` на строке ~41**

Текущее:
```go
			home, _ := os.UserHomeDir()
			cfg, err := config.LoadFrom(filepath.Join(home, ".afm"), fmDir())
```
→
```go
			home, _ := os.UserHomeDir()
			cfg, err := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
```
(`pkg/config` уже импортирован в `run.go`.)

- [ ] **Step 10: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: чистая сборка (подтверждает, что импорты `path/filepath` в `approve.go`/`retry.go` убраны верно, а везде, где `filepath` ещё нужен — он остался).

- [ ] **Step 11: Прогнать весь пакет + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./cmd/afm/... -v && go vet ./cmd/afm/...`
Expected: всё зелёное.

- [ ] **Step 12: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add cmd/afm/check.go cmd/afm/approve.go cmd/afm/retry.go cmd/afm/revise.go cmd/afm/init.go cmd/afm/list.go cmd/afm/run.go
git commit -m "refactor(cmd/afm): используем runsDir()/flowsDir()/config.AfmDir вместо литералов"
```

---

### Task 5: `pkg/docker/secrets.go` — использовать `config.AfmDir` + полный e2e-прогон

**Files:**
- Modify: `pkg/docker/secrets.go`

**Interfaces:**
- Consumes: `config.AfmDir` из Task 1.
- Produces: ничего для последующих задач (финальная задача плана).

`pkg/docker` уже импортирует `pkg/config` в других файлах пакета (`launcher.go`, `wrapper.go`), но `secrets.go` — отдельный файл, нужен свой импорт.

- [ ] **Step 1: Добавить импорт `config`**

Текущий блок импорта:
```go
import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)
```
Заменить на:
```go
import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/config"
)
```

- [ ] **Step 2: Заменить оба литерала**

Текущее (строки ~106-109):
```go
		files = []string{
			filepath.Join(homeDir(), ".afm", "secrets.env"),
			filepath.Join(projectDir, ".afm", "secrets.env"),
		}
```
→
```go
		files = []string{
			filepath.Join(homeDir(), config.AfmDir, "secrets.env"),
			filepath.Join(projectDir, config.AfmDir, "secrets.env"),
		}
```

- [ ] **Step 3: Собрать и прогнать пакет + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go test ./pkg/docker/... -v && go vet ./pkg/docker/...`
Expected: всё зелёное.

- [ ] **Step 4: Полный прогон проекта**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go vet ./... && go test ./... -race`
Expected: все пакеты зелёные — финальная проверка, что новая зависимость `pkg/server`→`pkg/config` (Task 2) и все переименования/замены по всему плану не сломали ничего в остальном проекте.

- [ ] **Step 5: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/docker/secrets.go
git commit -m "refactor(docker): secrets.go использует config.AfmDir вместо литерала"
```

---

### Task 6: Сборка бинарника, тестовый Docker-образ, ручная e2e-проверка в браузере

**Files:** нет изменений кода — только сборка и ручная проверка.

**Interfaces:**
- Consumes: весь код из Task 1-5.
- Produces: ничего (финальная задача плана).

Пользователь явно попросил после реализации собрать бинарник, собрать тестовый Docker-образ, прогнать реальный тестовый flow в браузере и убедиться в отсутствии регрессий — этот план не трогает поведение приложения (чистый рефакторинг констант/путей), но именно темы скина и `.afm`/`runs`/`flows` — это ровно то, что видно и проверяется через реальный запуск дашборда, поэтому ручная проверка здесь особенно уместна.

- [ ] **Step 1: Собрать бинарник**

Run: `cd /Users/alexander.kopichin/work/personal/afm && export JAVA_HOME=$HOME/Java/temurin-17.jdk/Contents/Home && make build`
Expected: сборка завершается без ошибок, бинарник `./bin/afm` создан.

- [ ] **Step 2: Собрать тестовый Docker-образ**

Run: `cd /Users/alexander.kopichin/work/personal/afm && make docker-build`
Expected: образ `akopichin/afm:latest` собирается без ошибок (см. `Makefile` — `docker-build` собирает `$(DOCKER_IMAGE):$(DOCKER_TAG)` = `akopichin/afm:latest` из `Dockerfile.runtime`).

- [ ] **Step 3: Прогнать существующий demo-flow нативным бинарником**

Использовать уже существующий в репозитории `example-flow-interactive.yaml` (корень репо) — интерактивная demo-стадия с диалогом, хороший канарейка сразу на несколько вещей: скин дашборда, путь `.afm/runs/`, файловый диалог-протокол.

Run: `cd /Users/alexander.kopichin/work/personal/afm && ./bin/afm run example-flow-interactive.yaml`
(Из вывода команды взять URL дашборда — печатается в лог при старте сервера.)

- [ ] **Step 4: Открыть дашборд в браузере, проверить нативный прогон**

Открыть напечатанный URL. Проверить:
- Дашборд открывается, применяется дефолтный скин (coffee, при пустом/незаданном `theme` в `.afm/config.yaml`).
- Стадия `discovery` доходит до диалога — открыть панель "Communication channel", ответить на вопросы (язык программирования, стиль архитектуры).
- В логе сервера нет warning "unknown theme" (тема не задана явно → coffee — валидный дефолт, не должен предупреждать).
- В файловой системе появился `.afm/runs/<run-id>/` (подтверждает `runsDir()`/`fmDir()` резолвятся туда же, куда и раньше).

Остановить прогон (Ctrl-C) после проверки.

- [ ] **Step 5: Прогнать тот же flow через Docker-образ**

Run: `cd /Users/alexander.kopichin/work/personal/afm && AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=akopichin/afm:latest ./bin/afm run example-flow-interactive.yaml`
Открыть напечатанный URL дашборда (порт пробрасывается на хост). Повторить те же проверки, что в Step 4 — дашборд открывается, скин применяется, диалог отвечается, `.afm/runs/<run-id>/` создаётся (на хосте, через смонтированный volume).

Остановить прогон (Ctrl-C) после проверки.

- [ ] **Step 6: Проверить CLI-команды, зависящие от `runsDir()`/`flowsDir()`**

Run: `cd /Users/alexander.kopichin/work/personal/afm && ./bin/afm list`
Expected: находит `.yaml` файлы в `.afm/flows/`, если там что-то есть, либо корректное сообщение "No flows found" — не падает.

Run: `cd /Users/alexander.kopichin/work/personal/afm && ./bin/afm check`
Expected: показывает статус последнего рана (из Step 3/5) без ошибки "no runs found".

- [ ] **Step 7: Зафиксировать результат**

Если всё прошло без регрессий — сообщить явно, что конкретно проверено (нативный бинарник, Docker-образ, оба открыты в браузере, диалоговая стадия отвечена, `afm list`/`afm check` работают). Если найдена регрессия — задокументировать точно (что сломалось, на каком шаге) и вернуться к соответствующей задаче 1-5 для фикса перед тем как считать план завершённым.

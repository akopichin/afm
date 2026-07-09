# open_browser default=false + печать URL — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Сменить default `server.open_browser` с `true` на `false`; при `false` печатать URL дашборда с подсказкой открыть вручную (браузер не дёргать), при `true` — открывать как раньше. Работает для Docker и локального запуска.

**Architecture:** `server.open_browser` уже существует (`ServerConfig.OpenBrowser *bool`). Меняется только default: `Default()` и `IsOpenBrowser()` (nil→`false`). В `run.go` печать URL остаётся безусловной, а `openBrowser()` зовётся только при `true`; при `false` печатается подсказка. `launchHostBrowserOpener` (Docker host-opener) уже gated на `IsOpenBrowser()` — без изменений.

**Tech Stack:** Go 1.26 (std), YAML-конфиг (`gopkg.in/yaml.v3`), тесты `testing`.

## Global Constraints

- Go-версию в `go.mod` (1.26) НЕ менять.
- После правок: `go vet ./...` и `golangci-lint run` чистые (revive/govet/errcheck/unused/gosec/staticcheck/copyloopvar/goconst/ineffassign; errcheck исключает Write/Close/MkdirAll).
- Коммиты на русском, без `Co-Authored-By`.
- Не добавлять `open_viewer`, env-override, deprecated-алиасы.
- Не трогать `launchHostBrowserOpener`, `browserCmd`, Docker re-exec-логику, `openBrowser`/`exec.Command`.
- Не связывать с подписанием бинарника (отдельная проблема codesign).

## File Structure

| Файл | Ответственность | Действие |
|---|---|---|
| `pkg/config/config.go` | `Default()` и `IsOpenBrowser()` — default open_browser | modify |
| `pkg/config/config_test.go` | тесты дефолта и `IsOpenBrowser` | modify |
| `cmd/afm/run.go` | печать подсказки при `!IsOpenBrowser()`; условие открытия | modify |
| `config.example.yaml` | закомментированная секция `server:` с `open_browser` | modify |

---

### Task 1: Config — default open_browser=false

**Files:**
- Modify: `pkg/config/config.go` (`Default()` ~стр.149-157; `IsOpenBrowser()` ~стр.32-37)
- Test: `pkg/config/config_test.go` (`TestServerConfigDefaults` ~стр.73-81)

**Interfaces:**
- Produces: `(s ServerConfig) IsOpenBrowser() bool` — nil → `false`, иначе `*OpenBrowser`. Потребляется в Task 2 (`run.go`).

- [ ] **Step 1: Write the failing tests**

В `pkg/config/config_test.go` заменить `TestServerConfigDefaults` (стр.73-81) целиком и добавить новый табличный тест сразу после него:

```go
func TestServerConfigDefaults(t *testing.T) {
	cfg := config.Default()
	if cfg.Server.GetPort() != 9876 {
		t.Errorf("default port: got %d, want 9876", cfg.Server.GetPort())
	}
	if cfg.Server.IsOpenBrowser() {
		t.Error("default open_browser should be false")
	}
}

func TestServerConfig_IsOpenBrowser(t *testing.T) {
	var s config.ServerConfig
	if s.IsOpenBrowser() {
		t.Error("nil OpenBrowser should default to false")
	}
	tb := true
	s.OpenBrowser = &tb
	if !s.IsOpenBrowser() {
		t.Error("OpenBrowser=true should return true")
	}
	fb := false
	s.OpenBrowser = &fb
	if s.IsOpenBrowser() {
		t.Error("OpenBrowser=false should return false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/config/ -run 'TestServerConfigDefaults|TestServerConfig_IsOpenBrowser' -v`
Expected: FAIL — `TestServerConfigDefaults`: "default open_browser should be false" (текущий default `true`); `TestServerConfig_IsOpenBrowser`: "nil OpenBrowser should default to false".

- [ ] **Step 3: Write minimal implementation**

В `pkg/config/config.go`:

3a. `IsOpenBrowser()` (стр.32-37) — сменить nil-ветку на `false`:

```go
// IsOpenBrowser returns OpenBrowser value (defaults to false).
func (s ServerConfig) IsOpenBrowser() bool {
	if s.OpenBrowser == nil {
		return false
	}
	return *s.OpenBrowser
}
```

3b. `Default()` (стр.149-157) — сменить `openBrowser := true` на `false`:

```go
func Default() Config {
	openBrowser := false
	port := 9876
	return Config{
		Client:   ClientConfig{Command: "claude"},
		Executor: ExecutorConfig{IdleTimeout: 30 * time.Minute, MaxParallel: 0},
		Server:   ServerConfig{Port: &port, OpenBrowser: &openBrowser},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/config/ -run 'TestServerConfigDefaults|TestServerConfig_IsOpenBrowser' -v`
Expected: PASS (оба теста).

- [ ] **Step 5: Lint + full config package tests**

Run: `go vet ./pkg/config/ && go test ./pkg/config/ -v`
Expected: vet чисто; все тесты пакета PASS (включая существующие). Никаких других тестов, утверждающих `default open_browser should be true`, не осталось — если есть, они тоже упадут и их нужно поправить по тому же образцу (проверить `grep -n "should be true" pkg/config/config_test.go`).

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): default open_browser=false"
```

---

### Task 2: run.go — печать URL + условие открытия; example config

**Files:**
- Modify: `cmd/afm/run.go:217` и `cmd/afm/run.go:221-223`
- Modify: `config.example.yaml` (добавить секцию `server:` после блока `theme:`)

**Interfaces:**
- Consumes: `cfg.Server.IsOpenBrowser()` из Task 1 (теперь `false` по умолчанию).

- [ ] **Step 1: Modify `run.go`**

В `cmd/afm/run.go` заменить блок печати/открытия (стр.217 + 221-223). Сейчас:

```go
				fmt.Printf("  dashboard: %s\n", dashURL)
				// Внутри Docker-контейнера (Linux) openBrowser зовёт xdg-open, которого
				// там нет и без display — пропуск; браузер уже открывает хост-side
				// opener (launchHostBrowserOpener), запущенный до re-exec.
				if cfg.Server.IsOpenBrowser() && os.Getenv("AFM_IN_DOCKER") != "1" {
					openBrowser(dashURL)
				}
```

Стать:

```go
				fmt.Printf("  dashboard: %s\n", dashURL)
				if cfg.Server.IsOpenBrowser() {
					// Локально — openBrowser; в Docker — хост-side opener уже запущен
					// (launchHostBrowserOpener, run.go:78), в контейнере xdg-open нет.
					if os.Getenv("AFM_IN_DOCKER") != "1" {
						openBrowser(dashURL)
					}
				} else {
					fmt.Printf("  → open this URL in your browser to follow the run\n")
				}
```

Точная замена: `old_string` — весь блок от `fmt.Printf("  dashboard: %s\n", dashURL)` до закрывающей `}` условия `if cfg.Server.IsOpenBrowser() && os.Getenv("AFM_IN_DOCKER") != "1" { openBrowser(dashURL) }` (включая комментарий над ним). `new_string` — блок выше.

- [ ] **Step 2: Modify `config.example.yaml`**

Добавить закомментированную секцию `server:` сразу после блока `theme:` (который заканчивается строкой `# theme: goga`), перед `# AI client settings`:

```yaml
# server:
#   # Открывать браузер с дашбордом автоматически при запуске.
#   # Default: false — URL печатается в лог, откройте вручную.
#   # open_browser: true
#   # port: 9876
```

- [ ] **Step 3: Build + vet + tests**

Run: `go build ./... && go vet ./... && go test ./pkg/config/ ./cmd/afm/`
Expected: build и vet чисто; тесты PASS. `cmd/afm` не имеет теста на printf-вывод — это принимается (поведение видно в логе при запуске, проверяется в smoke-шаге ниже).

- [ ] **Step 4: Smoke — поведение при default (false)**

Запустить с любым флоу (например `go run ./cmd/afm run` с существующим flow; если `.afm/flows/` пуст — создать минимальный или использовать `--port 0` невозможен, т.к. нужен port>0 для дашборда). Минимальная проверка без реального агента:

```bash
# Временно: минимальный flow без тяжёлого агента не обязателен — достаточно
# убедиться, что при default конфиге (open_browser отсутствует → false) в выводе
# появляется строка-подсказка и НЕ запускается браузер.
go run ./cmd/afm run 2>&1 | grep -E "dashboard:|open this URL"
```

Expected: в выводе есть `dashboard: http://localhost:...` и `→ open this URL in your browser to follow the run`; браузер не открывается (нет вызова `open`/`xdg-open`).

Если запуск с реальным флоу нежелателен (токены/трафик) — достаточно `go build` + `go vet` + `go test` из Step 3 и визуальной проверки кода: при `IsOpenBrowser()==false` (теперь дефолт) выполнится ветка `else` с `fmt.Printf` подсказки, `openBrowser` не зовётся.

- [ ] **Step 5: Commit**

```bash
git add cmd/afm/run.go config.example.yaml
git commit -m "feat(run): печать URL дашборда при open_browser=false (default)"
```

---

## Самопроверка плана (post-write)

**1. Spec coverage:**
- `Default()` openBrowser=false → Task 1 (Step 3b) ✓
- `IsOpenBrowser()` nil→false → Task 1 (Step 3a) ✓
- `mergeFile` без изменений → не трогаем ✓
- run.go печать подсказки + условие открытия → Task 2 (Step 1) ✓
- `launchHostBrowserOpener` без изменений (уже gated на IsOpenBrowser) → не трогаем ✓
- тесты: инвертировать `TestServerConfigDefaults` + `TestServerConfig_IsOpenBrowser` → Task 1 (Step 1) ✓
- example config секция `server:` → Task 2 (Step 2) ✓
- крайние случаи (port=0, Docker+false, local+false) → описаны в спеке; port=0 — блок не выполняется (run.go:188); Docker/local+false — ветка else печатает подсказку ✓
- YAGNI (нет open_viewer/env/алиасов, не трогаем browserCmd/Docker) → соблюдено ✓

**2. Placeholder scan:** TBD/TODO/«add appropriate» — нет. Все кодовые шаги содержат полный код; команды с ожидаемым выводом.

**3. Type consistency:** `IsOpenBrowser()` (Task 1) → `cfg.Server.IsOpenBrowser()` (Task 2) — сигнатура `(s ServerConfig) bool`, nil→false. `OpenBrowser *bool` поле не меняется. `Default()` возвращает `Config` с `Server.OpenBrowser = &openBrowser` (openBrowser=false). Имена согласованы.

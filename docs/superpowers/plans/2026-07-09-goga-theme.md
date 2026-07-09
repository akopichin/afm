# Goga-тема дашборда — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить вторую тему дашборда «goga» (визуально по сайту qarium.ru/goga), включаемую флажком `theme: goga` в `~/.afm/config.yaml`.

**Architecture:** Тема — отдельный самодостаточный `style-goga.css` (стиль с нуля, Nova Corps `style.css` не трогается). Выбор темы статичен из конфига при старте сервера; доставка в браузер — server-side replace в `index.html` (меняется `<link href>` и класс `<body>`) при отдаче `/`. Без FOUC, без нового эндпоинта. Мини-правка `app.js`: палитра графика потребления читается из CSS-токенов (с fallback на mint, чтобы Nova Corps не изменился).

**Tech Stack:** Go 1.26 (std `net/http`, `embed`), vanilla CSS/JS (без build-шага), YAML-конфиг (`gopkg.in/yaml.v3`). Тесты — стандартный `testing` + `httptest`.

## Global Constraints

- Go-версию в `go.mod` (1.26) НЕ менять (правило из CLAUDE.md).
- После правок — `go vet ./...` и `golangci-lint run` должны быть чистыми (revive/govet/errcheck/unused/gosec/staticcheck/copyloopvar/goconst/ineffassign; errcheck исключает `Write`/`Close`/`MkdirAll` — см. `.golangci.yml`).
- Коммиты на русском, без `Co-Authored-By` (правила из CLAUDE.md).
- `style.css`, `index.html`, `favicon.svg`, `markdown-it.min.js`, `pkg/web/embed.go` НЕ трогать.
- goga-CSS обязан определять тот же набор имён CSS-токенов, что `style.css` (иначе inline-стили `app.js` `var(--c-awaiting)`/`var(--text)` сломаются).
- Тема статична: никакого runtime-переключателя в UI, никакого `/api/config`.

## File Structure

| Файл | Ответственность | Действие |
|---|---|---|
| `pkg/config/config.go` | поле `Theme` в `Config`, мерж, `EffectiveTheme()` | modify |
| `pkg/config/config_test.go` | тесты `Theme`/`EffectiveTheme` | modify |
| `pkg/server/server.go` | `Theme` в `Config`/`Server`, `serveStatic`/`serveIndex`, replace, `fileServer` | modify |
| `pkg/server/server_test.go` | тесты handler `/` (goga/default) + `/style-goga.css` 200 | modify |
| `cmd/afm/run.go` | проброс `Theme: cfg.EffectiveTheme()` в `server.Config` | modify |
| `pkg/web/dashboard/style-goga.css` | полный CSS goga-темы | create |
| `pkg/web/dashboard/app.js` | `USAGE_COLORS` → чтение CSS-токенов с fallback | modify |

---

### Task 1: Config — поле `Theme`, мерж, `EffectiveTheme()`

**Files:**
- Modify: `pkg/config/config.go` (struct `Config` ~стр.137-146; `mergeFile` ~стр.172-233)
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `Config.Theme string` (yaml `theme`); метод `(c Config) EffectiveTheme() string` → `"goga"` или `"novacorps"`. Потребуется в Task 3 (`run.go`) и используется в Task 2 (`server.Config.Theme`, но это отдельное поле — проброс через run.go в Task 3).

- [ ] **Step 1: Write the failing test**

Добавить в конец `pkg/config/config_test.go`:

```go
func TestThemeMerge(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: goga\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "goga" {
		t.Errorf("theme: got %q, want %q", cfg.Theme, "goga")
	}
}

func TestThemeEmptyDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "theme: \"\"\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "" {
		t.Errorf("empty theme should stay empty: got %q", cfg.Theme)
	}
}

func TestEffectiveTheme(t *testing.T) {
	cases := []struct {
		name  string
		theme string
		want  string
	}{
		{"empty", "", "novacorps"},
		{"goga", "goga", "goga"},
		{"goga-upper", "GOGA", "goga"},
		{"goga-spaced", "  goga  ", "goga"},
		{"novacorps", "novacorps", "novacorps"},
		{"unknown", "dark", "novacorps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Theme: tc.theme}
			if got := cfg.EffectiveTheme(); got != tc.want {
				t.Errorf("EffectiveTheme(%q)=%q, want %q", tc.theme, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/ -run 'TestThemeMerge|TestThemeEmptyDoesNotOverride|TestEffectiveTheme' -v`
Expected: FAIL — `cfg.Theme undefined` / `cfg.EffectiveTheme undefined` (компиляция).

- [ ] **Step 3: Write minimal implementation**

В `pkg/config/config.go`:

3a. Добавить импорт `"strings"` в блок import (стр.3-11). После правки блок:

```go
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)
```

3b. Добавить поле `Theme` в struct `Config` (после `PromptsDir`, ~стр.145):

```go
// Config is the merged configuration for afm.
type Config struct {
	Client     ClientConfig     `yaml:"client"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Server     ServerConfig     `yaml:"server"`
	Proxy      ProxyConfig      `yaml:"proxy"`
	Docker     DockerConfig     `yaml:"docker"`
	Pricing    PricingConfig    `yaml:"pricing"`
	Accounting AccountingConfig `yaml:"accounting"`
	PromptsDir string           `yaml:"prompts_dir"`
	Theme      string           `yaml:"theme"`
}
```

3c. Добавить мерж в `mergeFile` (после блока `PromptsDir`, ~стр.196-198):

```go
	if overlay.PromptsDir != "" {
		dst.PromptsDir = overlay.PromptsDir
	}
	if overlay.Theme != "" {
		dst.Theme = overlay.Theme
	}
```

3d. Добавить метод `EffectiveTheme` (сразу после `Default()`, перед `LoadFrom`):

```go
// EffectiveTheme returns the normalized dashboard theme name.
// "goga" activates the goga theme; any other value (incl. empty/"novacorps")
// falls back to the default "novacorps". Unknown values log a warning to stderr.
func (c Config) EffectiveTheme() string {
	t := strings.ToLower(strings.TrimSpace(c.Theme))
	if t == "goga" {
		return "goga"
	}
	if c.Theme != "" && t != "novacorps" {
		fmt.Fprintf(os.Stderr, "warning: unknown theme %q, using novacorps\n", c.Theme)
	}
	return "novacorps"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/config/ -run 'TestThemeMerge|TestThemeEmptyDoesNotOverride|TestEffectiveTheme' -v`
Expected: PASS (все 3 теста + 6 subtests).

- [ ] **Step 5: Lint + full config package tests**

Run: `go vet ./pkg/config/ && go test ./pkg/config/ -v`
Expected: vet чисто, все тесты пакета PASS (включая существующие).

- [ ] **Step 6: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): поле theme + EffectiveTheme для темы дашборда"
```

---

### Task 2: Server — server-side replace доставки темы

**Files:**
- Modify: `pkg/server/server.go` (struct `Config` ~стр.31-46; struct `Server` ~стр.17-28; `New` ~стр.49-75)
- Test: `pkg/server/server_test.go`

**Interfaces:**
- Consumes: `web.FS` (`pkg/web`, уже импортирован в server.go) — `ReadFile("index.html")` и `http.FileServer(http.FS(web.FS))`.
- Produces: `Config.Theme string`; `Server` хранит предподготовленный `indexBytes` и `fileServer`; маршрут `/` обслуживается `serveStatic` (отдаёт `indexBytes` для `/` и `/index.html`, остальное — FileServer).

- [ ] **Step 1: Write the failing test**

Добавить в конец `pkg/server/server_test.go` (package `server` — white-box, `stubAccountant` уже определён в `usage_handler_test.go`):

```go
func TestServer_IndexDefaultTheme(t *testing.T) {
	srv := New(Config{Accountant: &stubAccountant{}})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="style.css"`) {
		t.Error("default тема должна ссылаться на style.css")
	}
	if !strings.Contains(body, `class="theme-novacorps"`) {
		t.Error("default тема должна ставить class theme-novacorps")
	}
	if strings.Contains(body, "style-goga") {
		t.Error("default тема не должна ссылаться на style-goga.css")
	}
}

func TestServer_IndexGogaTheme(t *testing.T) {
	srv := New(Config{Theme: "goga", Accountant: &stubAccountant{}})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="style-goga.css"`) {
		t.Error("goga тема должна ссылаться на style-goga.css")
	}
	if !strings.Contains(body, `class="theme-goga"`) {
		t.Error("goga тема должна ставить class theme-goga")
	}
	if strings.Contains(body, "theme-novacorps") {
		t.Error("goga тема не должна содержать theme-novacorps")
	}
	if strings.Contains(body, `href="style.css"`) {
		t.Error("goga тема не должна ссылаться на style.css")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run 'TestServer_IndexDefaultTheme|TestServer_IndexGogaTheme' -v`
Expected: FAIL — `Config.Theme undefined` (компиляция).

- [ ] **Step 3: Write minimal implementation**

В `pkg/server/server.go`:

3a. Добавить импорты `"bytes"` и `"os"` в блок import (стр.3-14). После правки:

```go
import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/web"
)
```

3b. Добавить поля в struct `Server` (стр.17-28). После `accountant` и перед `httpSrv`:

```go
type Server struct {
	runDir         string
	store          *state.Store
	uiBus          *orchestrator.UIBus
	approveFn      func(ctx context.Context, stageID string) error
	reviseFn       func(ctx context.Context, stageID, feedback string) error
	retryFn        func(ctx context.Context, stageID string) error
	dialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn func(stageID string) error
	accountant     Accountant
	theme          string        // "goga" или "" (default novacorps)
	indexBytes     []byte        // предподготовленный index.html (с заменами для goga)
	fileServer     http.Handler // отдаёт статику (style.css, app.js, ...)
	httpSrv        *http.Server
}
```

3c. Добавить поле `Theme` в struct `Config` (стр.31-46). После `Accountant Accountant`:

```go
type Config struct {
	Port           int
	RunDir         string
	Store          *state.Store
	UIBus          *orchestrator.UIBus
	ApproveFn      func(ctx context.Context, stageID string) error
	ReviseFn       func(ctx context.Context, stageID, feedback string) error
	RetryFn        func(ctx context.Context, stageID string) error
	DialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn func(stageID string) error
	Accountant Accountant
	Theme      string
}
```

3d. Переписать `New` (стр.48-75) — сохранить тему, подготовить `indexBytes`, создать `fileServer`, заменить маршрут `/`:

```go
// New creates a Server.
func New(cfg Config) *Server {
	s := &Server{
		runDir:         cfg.RunDir,
		store:          cfg.Store,
		uiBus:          cfg.UIBus,
		approveFn:      cfg.ApproveFn,
		reviseFn:       cfg.ReviseFn,
		retryFn:        cfg.RetryFn,
		dialogAnswerFn: cfg.DialogAnswerFn,
		dialogCancelFn: cfg.DialogCancelFn,
		accountant:     cfg.Accountant,
		theme:          cfg.Theme,
		fileServer:     http.FileServer(http.FS(web.FS)),
	}

	// Предподготовка index.html: при theme=="goga" подменяем ссылку на CSS и класс
	// body. Замены строковые — файл style-goga.css для этого не нужен (embed отдаст
	// его позже через fileServer). Ошибка чтения embed невозможна на практике, но
	// обрабатываем: при nil serveIndex делегирует на fileServer.
	indexBytes, err := web.FS.ReadFile("index.html")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read embedded index.html: %v\n", err)
	} else {
		if cfg.Theme == "goga" {
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`href="style.css"`), []byte(`href="style-goga.css"`))
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`class="theme-novacorps"`), []byte(`class="theme-goga"`))
		}
		s.indexBytes = indexBytes
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.Handle("/api/usage", UsageHandler(cfg.Accountant))
	mux.HandleFunc("/", s.serveStatic)

	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// serveStatic отдаёт index.html (с подставленной темой) для "/" и "/index.html",
// остальную статику делегирует на FileServer.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		s.serveIndex(w, r)
		return
	}
	s.fileServer.ServeHTTP(w, r)
}

// serveIndex отдаёт предподготовленный index.html. Если embed-чтение не удалось
// (indexBytes пуст), fallback на FileServer — защита от регрессии embed.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if len(s.indexBytes) == 0 {
		s.fileServer.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.indexBytes)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run 'TestServer_IndexDefaultTheme|TestServer_IndexGogaTheme' -v`
Expected: PASS.

- [ ] **Step 5: Lint + full server package tests**

Run: `go vet ./pkg/server/ && golangci-lint run ./pkg/server/... && go test ./pkg/server/ -v`
Expected: vet/lint чисто, все тесты пакета PASS (включая существующие `TestServerRouteStages`, `TestServerServesMarkdownIt`, `TestServer_UsageRouteWired` и т.д. — маршрут `/` теперь через `serveStatic`, но `/markdown-it.min.js` и `/api/...` по-прежнему отдаются).

- [ ] **Step 6: Commit**

```bash
git add pkg/server/server.go pkg/server/server_test.go
git commit -m "feat(server): server-side подстановка темы в index.html"
```

---

### Task 3: Проброс темы в `run.go`

**Files:**
- Modify: `cmd/afm/run.go:189-205` (блок `server.New(server.Config{...})`)

**Interfaces:**
- Consumes: `config.Config.EffectiveTheme()` (из Task 1); `server.Config.Theme` (из Task 2).
- Produces: тема из конфига доходит до `server.New`.

- [ ] **Step 1: Modify `run.go`**

В `cmd/afm/run.go` в вызове `server.New(server.Config{...})` (стр.189) добавить поле `Theme`. Полная структура после правки:

```go
				srv := server.New(server.Config{
					Port:       cfg.Server.GetPort(),
					RunDir:     runDir,
					Store:      store,
					Accountant: accountant,
					Theme:      cfg.EffectiveTheme(),
					UIBus:      orch.UIBus(),
					ApproveFn:  orch.Approve,
					ReviseFn:   orch.Revise,
					RetryFn:    orch.Retry,
					DialogAnswerFn: func(stageID, phase, qID, answer string, fromOptions bool) error {
						return orch.NotifyAnswer(stageID, phase, qID, answer, fromOptions)
					},
					DialogCancelFn: func(stageID string) error {
						orch.FailStage(stageID, "cancelled by user")
						return nil
					},
				})
```

- [ ] **Step 2: Build + vet**

Run: `go build ./... && go vet ./...`
Expected: сборка и vet чисто (без ошибок компиляции; `cfg.EffectiveTheme()` существует из Task 1, `server.Config.Theme` — из Task 2).

- [ ] **Step 3: Commit**

```bash
git add cmd/afm/run.go
git commit -m "feat(run): проброс theme из конфига в dashboard-сервер"
```

---

### Task 4: Создать `style-goga.css`

**Files:**
- Create: `pkg/web/dashboard/style-goga.css`
- Test: `pkg/server/server_test.go` (новый тест `/style-goga.css` 200)

**Interfaces:**
- Consumes: тот же DOM, что `index.html` + `app.js` (классы перечислены в спеке, секция 3 «Покрытие классов»). CSS-токены — те же имена, что в `style.css`.
- Produces: файл `style-goga.css`, автоматически попадает в `//go:embed dashboard/*` (`pkg/web/embed.go`) — `fileServer` начнёт его отдавать.

- [ ] **Step 1: Write the failing test**

Добавить в `pkg/server/server_test.go`:

```go
func TestServer_ServesGogaStylesheet(t *testing.T) {
	srv := New(Config{Theme: "goga", Accountant: &stubAccountant{}})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/style-goga.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /style-goga.css: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--mint") {
		t.Error("style-goga.css должен определять CSS-токен --mint")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/server/ -run TestServer_ServesGogaStylesheet -v`
Expected: FAIL — 404 (файл `style-goga.css` ещё не существует, FileServer отдаёт 404).

- [ ] **Step 3: Create `style-goga.css`**

Создать файл `pkg/web/dashboard/style-goga.css` с полным содержимым:

```css
/* ============================================================
   afm Dashboard — Goga theme
   Тёмная tech-тема по мотивам qarium.ru/goga.
   Самодостаточный CSS: те же DOM-классы и имена токенов, что у
   Nova Corps (style.css), но своя палитра/шрифт/радиусы и без
   неон-декора. Грузится вместо style.css при theme: goga.
   ============================================================ */

/* === Reduced motion gate === */
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}

/* === Reset === */
*, *::before, *::after {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

/* === Design tokens (те же имена, что в style.css) === */
:root {
  /* base surfaces */
  --bg:        #0A0E1A;
  --bg-elev:   #121830;

  /* text */
  --ink:       #FFFFFF;
  --ink-hi:    #FFFFFF;
  --ink-dim:   #A0AEC0;

  /* accents: teal primary, blue secondary (маппинг бывших mint/amber/violet) */
  --mint:      #20D4BF;   /* primary teal */
  --mint-soft: rgba(255, 255, 255, 0.08);  /* бордюры панелей */
  --grid:      rgba(255, 255, 255, 0.03);
  --amber:     #3882F6;   /* brand-blue — secondary accent */
  --violet:    #3882F6;   /* диалог → blue */
  --coral:     #FF6B5A;   /* failed/retry — red */

  /* status map */
  --c-pending:        var(--ink-dim);
  --c-planning:       var(--mint);
  --c-awaiting:       var(--amber);
  --c-revising:       var(--amber);
  --c-ready:          var(--mint);
  --c-running:        var(--amber);
  --c-done:           var(--mint);
  --c-failed:         var(--coral);
  --c-retrying:       var(--amber);
  --c-awaiting-user:  var(--violet);

  /* legacy aliases для app.js inline styles */
  --text:     var(--ink);
  --text-dim: var(--ink-dim);

  /* токен для графика потребления (читается app.js) */
  --usage-grid: rgba(255, 255, 255, 0.06);

  /* радиусы */
  --r-card: 12px;
  --r-btn:  8px;
  --r-code: 4px;
}

/* === Base === */
html, body {
  height: 100%;
  font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto,
    "Helvetica Neue", Arial, sans-serif;
  font-size: 13px;
  line-height: 1.55;
  background: var(--bg);
  color: var(--ink);
  -webkit-font-smoothing: antialiased;
}

a { color: var(--mint); text-decoration: none; }
a:hover { text-decoration: underline; }

::selection { background: rgba(32, 212, 191, 0.30); color: #fff; }

.hidden { display: none !important; }

/* === Header (glass) === */
#header {
  display: grid;
  grid-template-columns: auto 1fr auto auto auto;
  align-items: center;
  gap: 18px;
  padding: 12px 24px;
  background: rgba(10, 14, 26, 0.8);
  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  border-bottom: 1px solid var(--mint-soft);
  position: sticky;
  top: 0;
  z-index: 20;
}

#header h1 {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.08em;
  color: var(--ink-hi);
}

.flow-name {
  font-size: 13px;
  color: var(--mint);
  font-weight: 500;
}

.ws-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 12px;
  border: 1px solid currentColor;
  border-radius: var(--r-btn);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  margin-left: auto;
}
.ws-status::before {
  content: "";
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.ws-status.connected    { color: var(--mint); }
.ws-status.connected::before { box-shadow: 0 0 8px currentColor; }
.ws-status.disconnected { color: var(--coral); }

/* === Logo (статичный, без spin) === */
.logo {
  position: relative;
  width: 26px;
  height: 26px;
  flex-shrink: 0;
}
.logo > span { position: absolute; inset: 0; width: 100%; height: 100%; }
.logo svg { display: block; width: 100%; height: 100%; }

/* === Main layout === */
#main {
  display: grid;
  grid-template-columns: 240px 1fr 300px;
  gap: 20px;
  padding: 20px 24px;
  height: calc(100vh - 58px - 52px);
  overflow: hidden;
  position: relative;
}

/* Panel base */
#stages-panel,
#detail-panel,
#feed-panel {
  position: relative;
  z-index: 1;
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-card);
  padding: 16px;
  overflow-y: auto;
  min-height: 0;
}

/* Pane titles */
#stages-panel h2,
#feed-panel h2 {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.12em;
  color: var(--ink-hi);
  margin-bottom: 14px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--mint-soft);
}

/* === Footer === */
#footer {
  display: grid;
  grid-template-columns: 1fr auto auto;
  align-items: center;
  gap: 22px;
  padding: 12px 24px;
  background: var(--bg);
  border-top: 1px solid var(--mint-soft);
  font-size: 12px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--ink-dim);
}

.footer-item { display: flex; align-items: center; gap: 8px; }
.footer-label { color: var(--ink-dim); }

/* === Progress bar (teal→blue, без shimmer) === */
.progress-bar {
  width: 200px;
  height: 6px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 9999px;
  overflow: hidden;
}
.progress-fill {
  height: 100%;
  width: 0%;
  background: linear-gradient(90deg, var(--mint), var(--amber));
  border-radius: 9999px;
  transition: width 0.4s ease;
}

#progress-text,
#started-at,
#elapsed {
  color: var(--ink-hi);
  letter-spacing: 0.05em;
}

/* === Stages list === */
#stages-list {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.stage-item {
  display: grid;
  grid-template-columns: 18px 1fr auto;
  align-items: center;
  gap: 10px;
  padding: 9px 8px;
  border-radius: var(--r-btn);
  cursor: pointer;
  position: relative;
  font-size: 13px;
  color: var(--ink);
  transition: background 0.15s;
}
.stage-item:hover { background: rgba(255, 255, 255, 0.04); }

.stage-label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.stage-id { font-weight: 600; }
.stage-name {
  font-size: 11px;
  opacity: 0.65;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* status-dot */
.status-dot {
  width: 12px;
  height: 12px;
  border-radius: 50%;
  border: 2px solid var(--c-pending);
  position: relative;
  flex-shrink: 0;
  background: transparent;
}
.status-dot::after {
  content: "";
  position: absolute;
  inset: 2px;
  border-radius: 50%;
  background: currentColor;
}
.status-dot[data-status="pending"]              { color: var(--c-pending);       border-color: var(--c-pending); }
.status-dot[data-status="pending"]::after       { background: transparent; }
.status-dot[data-status="planning"]             { color: var(--c-planning);      border-color: var(--c-planning); }
.status-dot[data-status="awaiting_approval"]    { color: var(--c-awaiting);      border-color: var(--c-awaiting); }
.status-dot[data-status="revising"]             { color: var(--c-revising);      border-color: var(--c-revising); }
.status-dot[data-status="ready"]                { color: var(--c-ready);         border-color: var(--c-ready); }
.status-dot[data-status="running"]              { color: var(--c-running);       border-color: var(--c-running); }
.status-dot[data-status="done"]                 { color: var(--c-done);          border-color: var(--c-done); }
.status-dot[data-status="failed"]               { color: var(--c-failed);        border-color: var(--c-failed); }
.status-dot[data-status="retrying"]             { color: var(--c-retrying);      border-color: var(--c-retrying); }
.status-dot[data-status="awaiting_user_input"]  { color: var(--c-awaiting-user); border-color: var(--c-awaiting-user); }

/* мягкий пульс активных состояний */
.status-dot[data-status="running"],
.status-dot[data-status="planning"],
.status-dot[data-status="revising"],
.status-dot[data-status="retrying"],
.status-dot[data-status="awaiting_user_input"] {
  animation: dotPulse 1.8s ease-in-out infinite;
}
@keyframes dotPulse {
  0%, 100% { box-shadow: 0 0 0 0 currentColor; opacity: 0.85; }
  50%      { box-shadow: 0 0 0 3px transparent; opacity: 1; }
}

.dialog-badge {
  font-size: 13px;
  margin-left: 4px;
  color: var(--c-awaiting-user);
}

/* === Active stage === */
.stage-item.active {
  background: rgba(32, 212, 191, 0.10);
}
.stage-item.active::before {
  content: "";
  position: absolute;
  left: 0;
  top: 6px;
  bottom: 6px;
  width: 3px;
  border-radius: 9999px;
  background: var(--mint);
}

/* === Detail panel === */
.detail-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--ink-dim);
  font-size: 14px;
}
.detail-content.hidden { display: none; }

.detail-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 18px;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--mint-soft);
}
.detail-header h2 {
  font-size: 16px;
  font-weight: 600;
  color: var(--ink-hi);
  flex: 1;
}

.ornament { display: none; }

/* === Status badges === */
.status-badge {
  display: inline-block;
  padding: 3px 12px;
  border: 1px solid currentColor;
  border-radius: 9999px;
  background: transparent;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.status-badge[data-status="pending"]              { color: var(--c-pending); }
.status-badge[data-status="planning"]             { color: var(--c-planning); }
.status-badge[data-status="awaiting_approval"]    { color: var(--c-awaiting); }
.status-badge[data-status="revising"]             { color: var(--c-revising); }
.status-badge[data-status="ready"]                { color: var(--c-ready); }
.status-badge[data-status="running"]              { color: var(--c-running); }
.status-badge[data-status="done"]                 { color: var(--c-done); }
.status-badge[data-status="failed"]               { color: var(--c-failed); }
.status-badge[data-status="retrying"]             { color: var(--c-retrying); }
.status-badge[data-status="awaiting_user_input"]  { color: var(--c-awaiting-user); }

/* === Sections === */
.section { margin-bottom: 20px; }
.section h3 {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.12em;
  color: var(--ink-hi);
  margin-bottom: 10px;
}

.empty-hint {
  color: var(--ink-dim);
  font-size: 13px;
  font-style: italic;
}

/* === Markdown body (plan) === */
.markdown-body {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-card);
  padding: 14px 16px;
  max-height: 420px;
  overflow-y: auto;
  font-size: 13px;
  line-height: 1.6;
}
.markdown-body h1, .markdown-body h2, .markdown-body h3 {
  margin-top: 14px;
  margin-bottom: 8px;
  color: var(--ink-hi);
}
.markdown-body h1 { font-size: 16px; }
.markdown-body h2 { font-size: 14px; }
.markdown-body h3 { font-size: 13px; color: var(--mint); }
.markdown-body p { margin-bottom: 8px; }
.markdown-body ul, .markdown-body ol { margin-left: 22px; margin-bottom: 8px; }
.markdown-body li::marker { color: var(--ink-dim); }
.markdown-body code {
  background: rgba(32, 212, 191, 0.10);
  border: none;
  border-radius: var(--r-code);
  padding: 1px 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  color: var(--mint);
}
.markdown-body pre {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-card);
  padding: 12px;
  margin-bottom: 10px;
  overflow-x: auto;
}
.markdown-body pre code {
  background: transparent;
  padding: 0;
  color: var(--ink);
  font-size: 12px;
}
.markdown-body strong { color: var(--ink-hi); font-weight: 600; }

/* Checkboxes */
.cb {
  display: inline-block;
  width: 14px;
  height: 14px;
  text-align: center;
  font-size: 11px;
  line-height: 14px;
  margin-right: 4px;
  vertical-align: middle;
  border-radius: var(--r-code);
}
.cb-done { background: var(--mint); color: var(--bg); }
.cb-open { background: transparent; border: 1px solid var(--ink-dim); color: transparent; }

/* === Plan line-by-line review === */
#plan-content {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-card);
  padding: 10px 6px;
  max-height: 420px;
  overflow-y: auto;
}

.plan-line {
  display: grid;
  grid-template-columns: 36px 1fr 24px;
  gap: 4px;
  align-items: flex-start;
  padding: 2px 8px;
  border-radius: var(--r-btn);
  cursor: pointer;
  font-size: 13px;
  line-height: 1.6;
  transition: background 0.1s;
}
.plan-line:hover { background: rgba(255, 255, 255, 0.04); }
.plan-line.has-comment {
  background: rgba(56, 130, 246, 0.10);
  border-left: 3px solid var(--amber);
  margin-left: -3px;
  padding-left: 8px;
}
.plan-line.has-comment:hover { background: rgba(56, 130, 246, 0.16); }

.line-num {
  text-align: right;
  padding-right: 8px;
  color: var(--ink-dim);
  font-size: 12px;
  opacity: 0.6;
  user-select: none;
}
.plan-line:hover .line-num { color: var(--mint); opacity: 1; }

.line-content { min-width: 0; word-wrap: break-word; }
.line-content > p { margin: 0; }
.line-content h1 { color: var(--ink-hi); font-size: 16px; }
.line-content h2 { color: var(--ink-hi); font-size: 14px; }
.line-content h3 { color: var(--mint); font-size: 13px; }
.line-content code {
  background: rgba(32, 212, 191, 0.10);
  border-radius: var(--r-code);
  padding: 1px 6px;
  color: var(--mint);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
.line-content pre {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-card);
  padding: 10px;
  margin: 4px 0;
  overflow-x: auto;
}
.line-content strong { color: var(--ink-hi); font-weight: 600; }
.line-content li { margin-left: 20px; }

.line-comment-marker {
  text-align: center;
  font-size: 10px;
  color: var(--amber);
  opacity: 0;
  transition: opacity 0.15s;
  user-select: none;
}
.plan-line:hover .line-comment-marker { opacity: 0.6; }
.plan-line.has-comment .line-comment-marker { opacity: 1; }

/* === Special plan sections === */
.plan-section-wrapper {
  margin: 10px 0;
  padding: 10px 0 10px 14px;
  border-left: 3px solid var(--mint);
  background: rgba(32, 212, 191, 0.05);
  border-radius: 0 var(--r-btn) var(--r-btn) 0;
}
.plan-section-wrapper.plan-section-assumptions {
  border-left-color: var(--amber);
  background: rgba(56, 130, 246, 0.06);
}
.plan-section-wrapper.plan-section-criteria {
  border-left-color: var(--mint);
  background: rgba(32, 212, 191, 0.06);
}
.plan-section-wrapper .section-header {
  cursor: pointer;
  user-select: none;
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--ink-hi);
}
.plan-section-wrapper.plan-section-assumptions .section-header { color: var(--amber); }
.plan-section-wrapper.plan-section-criteria    .section-header { color: var(--mint); }
.plan-section-wrapper .section-header .toggle { font-size: 12px; display: inline-block; transition: transform 0.2s; }
.plan-section-wrapper.collapsed .section-header .toggle { transform: rotate(-90deg); }
.plan-section-wrapper.collapsed .plan-section-body { display: none; }

/* === Inline comment form === */
.line-comment-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  grid-column: 1 / -1;
  margin: 6px 0 8px 36px;
  padding: 12px;
  background: var(--bg-elev);
  border: 1px solid var(--amber);
  border-radius: var(--r-card);
}
.line-comment-form textarea {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-btn);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 13px;
  resize: vertical;
  min-height: 56px;
}
.line-comment-form textarea:focus { outline: none; border-color: var(--mint); }
.line-comment-form .comment-actions { display: flex; gap: 8px; }
.line-comment-display { border-color: var(--mint-soft); background: var(--bg-elev); }

.comment-hint {
  margin-top: 8px;
  font-size: 11px;
  color: var(--amber);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}

/* === Action buttons === */
.btn {
  background: transparent;
  border: 1px solid var(--mint);
  color: var(--mint);
  padding: 8px 18px;
  border-radius: var(--r-btn);
  font-family: inherit;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  cursor: pointer;
  transition: background 0.15s, box-shadow 0.15s;
}
.btn:hover:not(:disabled) { box-shadow: 0 0 10px rgba(32, 212, 191, 0.35); }
.btn:disabled { opacity: 0.35; cursor: not-allowed; }

.btn-approve {
  background: var(--mint);
  color: var(--bg);
  border-color: var(--mint);
}
.btn-approve:hover:not(:disabled) {
  background: #2ee8d2;
  border-color: #2ee8d2;
  color: var(--bg);
}
.btn-revise { border-color: var(--amber); color: var(--amber); }
.btn-retry  { border-color: var(--coral); color: var(--coral); }
.btn-send   { background: var(--mint); color: var(--bg); border-color: var(--mint); }
.btn-cancel { border-color: var(--ink-dim); color: var(--ink-dim); }

.actions-row { display: flex; gap: 10px; margin-bottom: 12px; }

/* === Log section === */
.log-content {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-card);
  padding: 14px 16px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 1.55;
  max-height: 300px;
  overflow-y: auto;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--ink-dim);
}
.log-content:empty { display: none; }
.log-text-line  { color: var(--ink); font-weight: 500; }
.log-error-line { color: var(--coral); }

/* === Dialog section === */
#dialog-section {
  position: relative;
  border: 1px solid rgba(56, 130, 246, 0.35);
  border-radius: var(--r-card);
  padding: 16px;
  background: var(--bg-elev);
}
#dialog-section h3 {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  letter-spacing: 0.1em;
  color: var(--violet);
  text-transform: uppercase;
  margin-bottom: 14px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--mint-soft);
}
#dialog-section h3 .sr-only {
  position: absolute;
  width: 1px; height: 1px; padding: 0; margin: -1px;
  overflow: hidden; clip: rect(0,0,0,0); white-space: nowrap; border: 0;
}
#dialog-section h3::before {
  content: "";
  width: 6px; height: 6px;
  border-radius: 50%;
  background: var(--violet);
  box-shadow: 0 0 8px var(--violet);
  flex-shrink: 0;
}
#dialog-section h3::after { content: "DIALOG · CHANNEL OPEN"; font-size: 12px; }

/* History */
.dialog-history {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-height: 280px;
  overflow-y: auto;
  margin-bottom: 14px;
}
.dialog-history.collapsed { display: none; }

.dialog-history .qa {
  border-left: 2px solid rgba(56, 130, 246, 0.35);
  padding-left: 12px;
}
.dialog-history .qa .q { font-size: 13px; color: var(--ink); margin-bottom: 4px; }
.dialog-history .qa .q::before { content: "Q · "; color: var(--mint); font-weight: 600; }
.dialog-history .qa .a { font-size: 12px; color: var(--ink-dim); padding-left: 16px; }
.dialog-history .qa .a::before { content: "A · "; color: var(--amber); font-weight: 600; }

.dialog-history .agent-msg {
  border-left: 2px solid rgba(56, 130, 246, 0.35);
  padding-left: 12px;
  font-size: 13px;
  color: var(--ink);
  white-space: pre-wrap;
  overflow-wrap: break-word;
}
.dialog-history .agent-msg.md { white-space: normal; }

.phase-divider {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.12em;
  color: var(--ink-dim);
  border-bottom: 1px solid var(--mint-soft);
  padding-bottom: 4px;
  margin: 8px 0;
}

/* Pending question */
.dialog-pending {
  position: relative;
  border: 1px solid var(--violet);
  border-radius: var(--r-card);
  padding: 16px;
  margin-bottom: 12px;
  background: var(--bg-elev);
}
.dialog-pending .dialog-question {
  font-size: 14px;
  line-height: 1.55;
  color: var(--ink-hi);
  margin-bottom: 14px;
}

/* Options */
.dialog-pending .dialog-options {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
  counter-reset: opt;
}
.dialog-pending .dialog-options button {
  counter-increment: opt;
  background: transparent;
  border: 1px solid rgba(56, 130, 246, 0.5);
  border-radius: var(--r-btn);
  color: #9db8f0;
  padding: 7px 14px 7px 10px;
  font-family: inherit;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  cursor: pointer;
  transition: all 0.15s;
}
.dialog-pending .dialog-options button::before {
  content: counter(opt, upper-alpha) " ";
  color: var(--mint);
  margin-right: 6px;
  font-weight: 700;
}
.dialog-pending .dialog-options button:hover {
  border-color: var(--violet);
  color: #fff;
  box-shadow: 0 0 8px rgba(56, 130, 246, 0.4);
}
.dialog-pending .dialog-options button.selected {
  background: rgba(56, 130, 246, 0.20);
  border-color: var(--violet);
  color: #fff;
}
.dialog-pending .dialog-options.dimmed button:not(.selected) { opacity: 0.4; }

/* Custom textarea */
.dialog-pending .dialog-custom {
  width: 100%;
  min-height: 60px;
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-btn);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 13px;
  resize: vertical;
}
.dialog-pending .dialog-custom::placeholder { color: var(--ink-dim); }
.dialog-pending .dialog-custom:focus { outline: none; border-color: var(--violet); }
.dialog-pending .dialog-custom:disabled { opacity: 0.4; cursor: not-allowed; }

/* Dialog actions */
.dialog-pending .dialog-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-top: 10px;
}
.dialog-pending .btn-cancel-dialog {
  background: transparent;
  border: 1px solid rgba(255, 107, 90, 0.5);
  border-radius: var(--r-btn);
  color: #ff8a7a;
  padding: 8px 18px;
  font-family: inherit;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  cursor: pointer;
}
.dialog-pending .btn-cancel-dialog:hover { box-shadow: 0 0 8px rgba(255, 107, 90, 0.4); }

.typing-indicator {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  letter-spacing: 0.1em;
  color: var(--violet);
  text-transform: uppercase;
}
.typing-indicator .blink {
  display: inline-block;
  width: 6px;
  height: 12px;
  background: var(--violet);
  animation: caret 1s steps(1) infinite;
}
@keyframes caret { 50% { opacity: 0; } }

.dialog-toggle {
  background: transparent;
  border: none;
  color: var(--ink-dim);
  font-family: inherit;
  font-size: 11px;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  cursor: pointer;
  padding: 4px 0;
  margin-top: 4px;
}
.dialog-toggle:hover { color: var(--mint); }

.dialog-pending.hidden { display: none !important; }

/* === Event feed === */
#feed-content { font-size: 13px; line-height: 1.5; }

.feed-entry {
  display: grid;
  grid-template-columns: 38px 1fr;
  gap: 8px;
  padding: 6px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  word-break: break-word;
}
.feed-entry:hover { background: rgba(255, 255, 255, 0.03); }

.feed-time {
  color: var(--ink-dim);
  font-size: 11px;
  text-align: right;
  padding-right: 2px;
}
.feed-msg { color: var(--ink); font-size: 13px; line-height: 1.5; }
.feed-msg.action { color: var(--ink-dim); }
.feed-msg.error  { color: var(--coral); }

.feed-stage-badge {
  display: inline;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--amber);
  margin-right: 6px;
}
.feed-stage-badge::after { content: " ·"; color: var(--ink-dim); margin: 0 4px 0 2px; }
.feed-stage-badge.status-pending             { color: var(--c-pending); }
.feed-stage-badge.status-planning            { color: var(--c-planning); }
.feed-stage-badge.status-awaiting_approval   { color: var(--c-awaiting); }
.feed-stage-badge.status-revising            { color: var(--c-revising); }
.feed-stage-badge.status-ready               { color: var(--c-ready); }
.feed-stage-badge.status-running             { color: var(--c-running); }
.feed-stage-badge.status-done                { color: var(--c-done); }
.feed-stage-badge.status-failed              { color: var(--c-failed); }
.feed-stage-badge.status-retrying            { color: var(--c-retrying); }
.feed-stage-badge.status-awaiting_user_input { color: var(--c-awaiting-user); }

/* === Markdown shared (.md) === */
.md p { margin: 6px 0; }
.md > :first-child { margin-top: 0; }
.md > :last-child { margin-bottom: 0; }
.md table { border-collapse: collapse; margin: 8px 0; font-size: 13px; }
.md th, .md td { border: 1px solid var(--mint-soft); padding: 5px 10px; text-align: left; }
.md th { color: var(--ink-hi); background: var(--bg-elev); text-transform: uppercase; font-size: 11px; }
.md tr:nth-child(even) td { background: rgba(255, 255, 255, 0.02); }
.md blockquote { margin: 8px 0; padding: 2px 12px; border-left: 2px solid var(--mint); color: var(--ink-dim); }
.md em { color: var(--ink-hi); font-style: italic; }
.md a { color: var(--mint); text-decoration: underline; }
.md a:hover { color: var(--ink-hi); }
.md ul, .md ol { margin: 4px 0; padding-left: 22px; }
.md ul ul, .md ol ol, .md ul ol, .md ol ul { margin: 2px 0; }
.md hr { border: none; border-top: 1px solid var(--mint-soft); margin: 12px 0; }

/* ============================================================
   Consumption panel — slide-out overlay (right edge)
   ============================================================ */
.usage-panel {
  position: fixed;
  top: 58px;
  bottom: 52px;
  right: 0;
  width: 26px;
  z-index: 50;
  display: flex;
  align-items: center;
  pointer-events: none;
}

.usage-toggle {
  pointer-events: auto;
  width: 26px;
  height: 104px;
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  border-right: none;
  border-radius: var(--r-btn) 0 0 var(--r-btn);
  color: var(--mint);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  z-index: 2;
  transition: background 0.15s, box-shadow 0.15s, color 0.15s;
}
.usage-toggle:hover {
  background: var(--bg);
  color: var(--ink-hi);
  box-shadow: -3px 0 14px rgba(32, 212, 191, 0.25);
}
.usage-toggle-arrow { width: 16px; height: 16px; transition: transform 0.34s cubic-bezier(0.22, 1, 0.36, 1); }
.usage-panel.open .usage-toggle-arrow { transform: scaleX(-1); }

.usage-toggle::after {
  content: "ПОТРЕБЛ.";
  position: absolute;
  bottom: 10px;
  left: 50%;
  transform: translateX(-50%) rotate(180deg);
  writing-mode: vertical-rl;
  font-size: 8px;
  letter-spacing: 0.18em;
  color: var(--ink-dim);
  opacity: 0.7;
}

.usage-panel-body {
  pointer-events: none;
  position: absolute;
  top: 0;
  bottom: 0;
  right: 26px;
  width: 334px;
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  border-right: none;
  border-radius: var(--r-card) 0 0 var(--r-card);
  padding: 16px 14px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 14px;
  transform: translateX(calc(100% + 26px));
  transition: transform 0.34s cubic-bezier(0.22, 1, 0.36, 1);
}
.usage-panel.open .usage-panel-body {
  transform: translateX(0);
  pointer-events: auto;
}

.usage-panel-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--mint-soft);
}
.usage-panel-head h2 {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.12em;
  color: var(--ink-hi);
}
.usage-total { font-size: 16px; color: var(--mint); }

.usage-controls { display: flex; flex-direction: column; gap: 10px; }
.usage-metric-switch {
  display: flex;
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-btn);
  overflow: hidden;
}
.usage-metric {
  flex: 1;
  background: transparent;
  border: none;
  border-right: 1px solid var(--mint-soft);
  color: var(--ink-dim);
  padding: 7px 4px;
  font-family: inherit;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}
.usage-metric:last-child { border-right: none; }
.usage-metric:hover { color: var(--ink); background: rgba(255, 255, 255, 0.04); }
.usage-metric.active { color: var(--bg); background: var(--mint); }

.usage-stage-filter { display: flex; align-items: center; gap: 8px; }
.usage-stage-label {
  font-size: 10px;
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--ink-dim);
  white-space: nowrap;
}
#usage-stage-select {
  flex: 1;
  min-width: 0;
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-btn);
  color: var(--ink);
  padding: 6px 8px;
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
}
#usage-stage-select:focus { outline: none; border-color: var(--mint); }

.usage-chart-wrap {
  position: relative;
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  border-radius: var(--r-card);
  padding: 10px;
  min-height: 180px;
}
.usage-chart { display: block; width: 100%; height: 180px; }
.usage-empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--ink-dim);
  font-size: 12px;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}

.usage-meta {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 10px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--ink-dim);
}

@media (max-width: 720px) {
  .usage-panel { width: 86vw; }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/server/ -run TestServer_ServesGogaStylesheet -v`
Expected: PASS — `style-goga.css` теперь embed'ится и отдаётся (200), тело содержит `--mint`.

- [ ] **Step 5: Lint + full server tests**

Run: `go vet ./... && golangci-lint run ./... && go test ./pkg/server/ -v`
Expected: всё чисто, все тесты PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/style-goga.css pkg/server/server_test.go
git commit -m "feat(dashboard): тема goga (style-goga.css)"
```

---

### Task 5: `app.js` — палитра графика theme-aware

**Files:**
- Modify: `pkg/web/dashboard/app.js:73-80` (блок `USAGE_COLORS`)

**Interfaces:**
- Consumes: CSS-токены `--mint`, `--amber`, `--ink-dim`, `--usage-grid` (определены и в `style.css` для `--mint`/`--amber`/`--ink-dim`, и в `style-goga.css` для всех четырёх).
- Produces: `USAGE_COLORS` берёт значения из активной темы; при отсутствии токена — fallback на текущие mint-значения (поведение Nova Corps не меняется).

- [ ] **Step 1: Modify `app.js`**

Заменить блок `USAGE_COLORS` (стр.73-80):

```js
    // Палитра графика — читается из CSS-токенов активной темы, с fallback на
    // mint-палитру Nova Corps. var() в SVG presentation attributes не работает,
    // а генерить SVG удобнее строками — поэтому читаем computed values один раз.
    // В Nova Corps --usage-grid не определён → fallback → график не меняется;
    // в goga-теме --mint/--amber/--ink-dim/--usage-grid дают teal/blue палитру.
    var _cssVars = getComputedStyle(document.documentElement);
    function _cssVar(name, fallback) {
        var v = _cssVars.getPropertyValue(name).trim();
        return v || fallback;
    }
    var USAGE_COLORS = {
        mint:   _cssVar("--mint",       "#6fd4cc"),
        amber:  _cssVar("--amber",      "#e5d442"),
        inkDim: _cssVar("--ink-dim",    "#4a8a85"),
        grid:   _cssVar("--usage-grid", "rgba(111, 212, 204, 0.10)")
    };
```

- [ ] **Step 2: Verify embedded dashboard still serves + JS syntax sanity**

Run: `go test ./pkg/server/ -run 'TestServerServesMarkdownIt|TestServer_IndexDefaultTheme|TestServer_IndexGogaTheme' -v`
Expected: PASS (статика отдаётся; index для обеих тем корректен).

Дополнительно — быстрая проверка синтаксиса JS (node должен быть доступен):
Run: `node --check pkg/web/dashboard/app.js`
Expected: нет вывода (синтаксис валиден).

- [ ] **Step 3: Commit**

```bash
git add pkg/web/dashboard/app.js
git commit -m "feat(dashboard): палитра графика потребления читается из CSS-токенов"
```

---

### Task 6: Финальная проверка — линт, сборка, smoke-тест

**Files:**
- None (проверки)

- [ ] **Step 1: Full lint + vet + build + tests**

Run: `go vet ./... && golangci-lint run ./... && go build ./... && go test ./...`
Expected: всё чисто, все тесты PASS, бинарник собирается. Go-версия в `go.mod` осталась 1.26.

- [ ] **Step 2: Smoke-тест темы goga (визуально)**

2a. Временно включить тему — добавить в `~/.afm/config.yaml` (создать если нет):
```yaml
theme: goga
```

2b. Запустить дашборд с любым потоком (например существующим в `.afm/flows/`):
```bash
afm run
```
(или `go run ./cmd/afm run`)

2c. Открыть URL из вывода (`dashboard: http://localhost:9876`) в браузере. Проверить:
- Фон тёмно-синий (`#0A0E1A`), карточки `#121830`, бордюры светлые полупрозрачные.
- Акцент teal `#20D4BF` (ссылки, primary-кнопка «Одобрить», active-стадия, чекбоксы).
- Secondary blue `#3882F6` (running/awaiting статусы, кнопка «Отправить правку»).
- Шрифт sans-serif (НЕ моноширинный), base ~13px.
- Скруглённые углы (панели 12px, кнопки 8px, бейджи pill). Без scanlines/scan-луча/shimmer/notched-углов.
- Header — glass (полупрозрачный + blur).
- Открыть панель потребления (стрелка справа) — график teal/blue (не mint).

2d. Проверить default-тему: убрать `theme: goga` (или `theme: novacorps`) из конфига, перезапустить — должна вернуться Nova Corps (моноширинный, mint, scanlines).

2e. Проверить unknown-тему: поставить `theme: dark` — в stderr выводится `warning: unknown theme "dark", using novacorps`, дашборд в Nova Corps.

2f. Откатить временный `theme:` в `~/.afm/config.yaml` к исходному состоянию (не коммитить пользовательский конфиг).

- [ ] **Step 3: Финальный коммит (если остались правки)**

Если smoke-тест выявил мелкие правки в `style-goga.css` — внести и закоммитить:
```bash
git add pkg/web/dashboard/style-goga.css
git commit -m "fix(dashboard): правки goga-темы по результатам smoke-теста"
```
Если правок нет — шаг пропустить.

---

## Самопроверка плана (post-write)

**1. Spec coverage:**
- Поле `theme` в конфиге + `EffectiveTheme()` → Task 1 ✓
- Server-side replace доставки (`serveStatic`/`serveIndex`, замены `href`/`class`) → Task 2 ✓
- Проброс `cfg.EffectiveTheme()` в `server.Config` (`run.go`) → Task 3 ✓
- Полный `style-goga.css` (goga-палитра, sans-serif, радиусы, без неона, все классы) → Task 4 ✓
- `app.js` `USAGE_COLORS` theme-aware с fallback → Task 5 ✓
- Токены те же имена (включая `--text`/`--c-awaiting` для app.js inline + новый `--usage-grid`) → Task 4 (`:root`) ✓
- Обработка ошибок (embed read nil → fallback FileServer; unknown theme → warning + novacorps) → Task 2/Task 1 ✓
- Тесты (config + server) → Task 1/Task 2/Task 4 ✓
- Линт/vet/build + smoke → Task 6 ✓
- YAGNI (нет runtime-toggle, нет `/api/config`, `style.css`/`index.html` не трогаем) → соблюдено во всём плане ✓

**2. Placeholder scan:** TBD/TODO/«add appropriate» — нет. Весь CSS дан полностью; тесты содержат реальный код; команды с ожидаемым выводом.

**3. Type consistency:** `Config.Theme string` (Task 1) → `server.Config.Theme string` (Task 2) → `cfg.EffectiveTheme() string` в `run.go` (Task 3). `EffectiveTheme()` определён в Task 1, использован в Task 3. `serveStatic`/`serveIndex`/`s.indexBytes`/`s.fileServer` определены в Task 2. `USAGE_COLORS` поля `mint/amber/inkDim/grid` сохранены (Task 5) — совместимо с потребителями в `renderUsageChart`. Имена согласованы.

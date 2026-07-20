# Dashboard Skins Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the dashboard's two hardcoded themes (novacorps/goga) into a config-driven, externally-overridable, component-based skin system with an independent dark/light mode toggle.

**Architecture:** Structural CSS is split into `pkg/web/dashboard/public/skins/base/*.css` partials (one per dashboard component). Each skin (`skins/novacorps/`, `skins/goga/`) is just `@import`s of those partials plus its own `:root[data-theme="dark"]`/`="light"` token blocks and small decorative overrides — the same pattern goga already uses today (`@import url("./style.css")`), decomposed. `theme:` in config still selects a built-in skin; a new `skin_dir:` config key (mirroring `prompts_dir`) fully replaces the active skin with an external directory. Dark/light is orthogonal to skin choice, driven by a `data-theme` attribute set by an inline bootstrap script (no FOUC) and toggled via a React hook + Footer button, persisted to `localStorage`.

**Tech Stack:** Go 1.26 (embed.FS, net/http), React 18 + TypeScript + Vite (dashboard frontend), vanilla CSS with custom properties (no build step for CSS — browser-native `@import`), vitest + @testing-library/react (frontend tests), go test + golangci-lint (backend).

## Global Constraints

- Do not change the Go version in `go.mod` (CLAUDE.md).
- All commits are in Russian, no `Co-Authored-By` trailer (CLAUDE.md).
- After Go changes: `go vet ./...` and `golangci-lint run` must be clean.
- After frontend changes: `npm run typecheck` and `npm test` (vitest) must pass in `pkg/web/dashboard/`.
- `theme:` YAML key is NOT renamed to `skin:` — deliberate decision to avoid breaking existing `.afm/config.yaml` files (see spec).
- No CSS build/bundling step is introduced — skins are delivered as plain files resolved via browser-native `@import`.
- No third "example" skin and no in-dashboard skin-picker UI — skin/mode selection stays config- and localStorage-driven only.
- Source of truth for dashboard static assets is `pkg/web/dashboard/public/`; `npm run build` (from `pkg/web/dashboard/`) copies `public/*` into the repo root of that directory (`outDir: '.'`) where `pkg/web/embed.go` embeds it. Never hand-edit the root-level copies (`pkg/web/dashboard/skins/`, `index.html`) directly — edit `public/` (and `index.dev.html` for the HTML template) and rebuild.

---

## Task 1: Config — `skin_dir` field

**Files:**
- Modify: `pkg/config/config.go:200-209` (`Config` struct), `pkg/config/config.go:282-287` (`mergeFile`)
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.SkinDir string` (yaml tag `skin_dir`), merged with the same "non-empty overlay wins" rule as `PromptsDir`/`Theme`.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/config/config_test.go` (near `TestThemeMerge`/`TestThemeEmptyDoesNotOverride`, e.g. right after line 255):

```go
func TestSkinDirMerge(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "skin_dir: /tmp/my-skin\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SkinDir != "/tmp/my-skin" {
		t.Errorf("skin_dir: got %q, want %q", cfg.SkinDir, "/tmp/my-skin")
	}
}

func TestSkinDirEmptyDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "config.yaml", "skin_dir: \"\"\n")
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SkinDir != "" {
		t.Errorf("empty skin_dir should stay empty: got %q", cfg.SkinDir)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/config/... -run TestSkinDir -v`
Expected: FAIL — `cfg.SkinDir undefined (type config.Config has no field or method SkinDir)`

- [ ] **Step 3: Add the field and merge logic**

In `pkg/config/config.go`, in the `Config` struct (around line 200-209):

```go
// Config is the merged configuration for afm.
type Config struct {
	Client     ClientConfig     `yaml:"client"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Server     ServerConfig     `yaml:"server"`
	Docker     DockerConfig     `yaml:"docker"`
	Supervisor SupervisorConfig `yaml:"supervisor"`
	PromptsDir string           `yaml:"prompts_dir"`
	Theme      string           `yaml:"theme"`
	SkinDir    string           `yaml:"skin_dir"`
}
```

In `mergeFile` (around line 282-287), right after the existing `Theme` merge block:

```go
	if overlay.PromptsDir != "" {
		dst.PromptsDir = overlay.PromptsDir
	}
	if overlay.Theme != "" {
		dst.Theme = overlay.Theme
	}
	if overlay.SkinDir != "" {
		dst.SkinDir = overlay.SkinDir
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/config/... -run TestSkinDir -v`
Expected: PASS (both `TestSkinDirMerge` and `TestSkinDirEmptyDoesNotOverride`)

- [ ] **Step 5: Run the full config package test suite**

Run: `go test ./pkg/config/...`
Expected: PASS (no regressions)

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): добавлен skin_dir для внешнего оверрайда скина дашборда"
```

---

## Task 2: CSS — base structural partials

**Files:**
- Create: `pkg/web/dashboard/public/skins/base/reset.css`
- Create: `pkg/web/dashboard/public/skins/base/header.css`
- Create: `pkg/web/dashboard/public/skins/base/layout.css`
- Create: `pkg/web/dashboard/public/skins/base/stages-list.css`
- Create: `pkg/web/dashboard/public/skins/base/plan-panel.css`
- Create: `pkg/web/dashboard/public/skins/base/log-panel.css`
- Create: `pkg/web/dashboard/public/skins/base/dialog-channel.css`
- Create: `pkg/web/dashboard/public/skins/base/event-feed.css`
- Create: `pkg/web/dashboard/public/skins/base/markdown.css`

**Interfaces:**
- Produces: a fixed set of CSS custom properties every skin's `:root[data-theme="dark"|"light"]` block MUST define for these partials to render correctly: `--bg`, `--bg-elev`, `--panel-bg`, `--ink`, `--ink-hi`, `--ink-dim`, `--mint`, `--mint-soft`, `--grid`, `--amber`, `--violet`, `--coral`, `--qa-answer`, `--dialog-option-text`, `--btn-cancel-dialog-text`, `--supervisor-accent`, `--c-pending`, `--c-planning`, `--c-awaiting`, `--c-revising`, `--c-ready`, `--c-running`, `--c-done`, `--c-failed`, `--c-retrying`, `--c-awaiting-user`, `--text`, `--text-dim`.
- Note: this task deliberately does NOT migrate the "Consumption panel" CSS block (`public/style.css:1447-1665`) — grepping `src/**/*.tsx` confirms no React component renders `usage-panel`/`usage-toggle`/`usage-chart` classes (the accounting/usage route was already removed backend-side), so it is dead CSS and is dropped rather than carried forward.
- Note: this task fixes one latent bug found while transcribing: `public/style.css:1693` had `border: 1px solid var(--teal)` where `--teal` is not defined anywhere in either theme (dead token reference — the rule currently renders with no border in both themes). Corrected to `var(--mint)` below.
- Note: two more simplifications, both visually negligible: (a) the panel/textarea/usage-chart dark-navy backgrounds (`rgba(8, 20, 26, 0.88)`, `rgba(8, 20, 26, 0.6)`, plus the two now-deleted usage-panel ones at `0.96`) collapse into one new token `--panel-bg`, since without a token they'd render as broken solid-dark boxes under a light theme; (b) two `color: #fff` literals in the dialog-options button hover/selected states become `var(--ink-hi)` (identical value today: `#e7faf7` novacorps / `#FFFFFF` goga, both effectively white).

- [ ] **Step 1: Create `reset.css`**

```css
/* pkg/web/dashboard/public/skins/base/reset.css
   Box-sizing reset, reduced-motion gate, base typography/link/selection. */

@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}

*, *::before, *::after {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

html, body {
  height: 100%;
  font-family: "JetBrains Mono", "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
  font-size: 11px;
  line-height: 1.5;
  background: var(--bg);
  color: var(--ink);
  -webkit-font-smoothing: antialiased;
}

a { color: var(--mint); text-decoration: none; }
a:hover { text-decoration: underline; }

::selection { background: rgba(229, 212, 66, 0.35); color: var(--ink-hi); }

.hidden { display: none !important; }
```

- [ ] **Step 2: Create `header.css`**

```css
/* pkg/web/dashboard/public/skins/base/header.css
   Top bar: logo mark, flow name, WS status. */

#header {
  display: grid;
  grid-template-columns: auto 1fr auto auto auto;
  align-items: center;
  gap: 18px;
  padding: 10px 20px;
  background: var(--bg);
  border-bottom: 1px solid var(--mint-soft);
}

#header h1 {
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.3em;
  text-transform: uppercase;
  color: var(--ink-hi);
}

.flow-name {
  font-size: 11px;
  color: var(--mint);
}

.ws-status {
  padding: 3px 10px;
  border: 1px solid currentColor;
  font-size: 9px;
  font-weight: 600;
  letter-spacing: 0.25em;
  text-transform: uppercase;
}
.ws-status.connected    { color: var(--mint); }
.ws-status.disconnected { color: var(--coral); }

/* === Animated logo === */
.logo {
  position: relative;
  width: 24px;
  height: 24px;
  flex-shrink: 0;
}
.logo > span {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
}
.logo svg {
  display: block;
  width: 100%;
  height: 100%;
}

/* Цвета кольца/дуги/ядра логотипа — токены, а не хардкод в JSX (см. FlowHeader.tsx),
   иначе перекраска скина не затрагивала бы логотип. */
.logo .l-ring svg { stroke: var(--mint); }
.logo .l-arc svg  { stroke: var(--amber); }
.logo .l-core svg { fill: var(--mint); }

.logo .l-ring { animation: spinSlow 14s linear infinite; }
.logo .l-arc  { animation: spinFast 4s linear infinite; }
.logo .l-core { animation: corePulse 2.4s ease-in-out infinite; transform-origin: center; }

@keyframes spinSlow { to { transform: rotate(360deg); } }
@keyframes spinFast { to { transform: rotate(-360deg); } }
@keyframes corePulse {
  0%, 100% { opacity: 0.55; transform: scale(1); }
  50%      { opacity: 1; transform: scale(1.18); }
}

/* === WS indicator с пульсом === */
.ws-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
}
.ws-status::before {
  content: "";
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.ws-status.connected::before {
  box-shadow: 0 0 8px currentColor;
  animation: wsBlink 2s ease-in-out infinite;
}
@keyframes wsBlink {
  0%, 100% { box-shadow: 0 0 4px currentColor; }
  50%      { box-shadow: 0 0 12px currentColor; }
}
```

- [ ] **Step 3: Create `layout.css`**

```css
/* pkg/web/dashboard/public/skins/base/layout.css
   #main grid/panels, footer, resizable panel layout, attention signal. */

#main {
  display: grid;
  grid-template-columns: 240px 1fr 280px;
  gap: 20px;
  padding: 18px 20px;
  height: calc(100vh - 56px - 50px);
  overflow: hidden;
  position: relative;
  background-image:
    linear-gradient(var(--grid) 1px, transparent 1px),
    linear-gradient(90deg, var(--grid) 1px, transparent 1px);
  background-size: 24px 24px;
}

/* Panel base */
#stages-panel,
#detail-panel,
#feed-panel {
  position: relative;
  z-index: 1;
  background: var(--panel-bg);
  border: 1px solid var(--mint-soft);
  padding: 14px;
  overflow-y: auto;
  min-height: 0;
}

/* Лента событий занимает всю высоту панели и скроллится сама:
   без display:flex у #feed-panel flex:1 на .event-feed-scroll игнорируется,
   контейнер не ограничивается по высоте, не переполняется — и автоскролл
   с кнопкой «↓ к последнему» остаются инертными. */
#feed-panel {
  display: flex;
  flex-direction: column;
  overflow: visible;
}

/* Notched corner accents */
#stages-panel::before,
#detail-panel::before,
#feed-panel::before,
#stages-panel::after,
#detail-panel::after,
#feed-panel::after {
  content: "";
  position: absolute;
  width: 10px;
  height: 10px;
  border: 1px solid var(--mint);
  pointer-events: none;
}
#stages-panel::before,
#detail-panel::before,
#feed-panel::before {
  top: -1px; left: -1px;
  border-right: 0;
  border-bottom: 0;
}
#stages-panel::after,
#detail-panel::after,
#feed-panel::after {
  bottom: -1px; right: -1px;
  border-left: 0;
  border-top: 0;
}

/* Pane titles */
#stages-panel h2,
#feed-panel h2 {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.22em;
  color: var(--ink-hi);
  margin-bottom: 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--mint-soft);
}

/* === Footer === */
#footer {
  display: grid;
  grid-template-columns: 1fr auto auto auto;
  align-items: center;
  gap: 22px;
  padding: 10px 20px;
  background: var(--bg);
  border-top: 1px solid var(--mint-soft);
  font-size: 10px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--ink-dim);
}

.footer-item {
  display: flex;
  align-items: center;
  gap: 8px;
}
.footer-label { color: var(--ink-dim); }

/* === Footer progress bar with shimmer === */
.progress-bar {
  width: 180px;
  height: 4px;
  background: rgba(111, 212, 204, 0.12);
  position: relative;
  overflow: hidden;
}
.progress-fill {
  position: absolute;
  inset: 0 auto 0 0;
  width: 0%;
  background: linear-gradient(90deg, rgba(229, 212, 66, 0.15), var(--amber));
  transition: width 0.4s ease;
}
.progress-fill::before {
  content: "";
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: 30%;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.55), transparent);
  animation: shimmer 2.2s linear infinite;
}
@keyframes shimmer {
  0%   { transform: translateX(-100%); }
  100% { transform: translateX(330%); }
}
.progress-fill::after {
  content: "";
  position: absolute;
  right: -1px;
  top: -3px;
  bottom: -3px;
  width: 2px;
  background: var(--amber);
  box-shadow: 0 0 10px var(--amber);
  animation: tipPulse 1.2s ease-in-out infinite;
}
@keyframes tipPulse {
  0%, 100% { box-shadow: 0 0 6px var(--amber); }
  50%      { box-shadow: 0 0 16px var(--amber); }
}

#progress-text,
#started-at,
#elapsed {
  color: var(--ink-hi);
  letter-spacing: 0.1em;
}

/* === Auto-scroll: диалог и фид (useStickToBottom) === */
.dialog-scroll,
.event-feed-scroll {
  overflow-y: auto;
  position: relative;
}

/* Диалог заполняет всю высоту panel-frame-body (и весь оверлей при maximize) —
   без фиксированного потолка. Прокрутка — внутри .dialog-scroll. */
.dialog-scroll {
  flex: 1;
  min-height: 0;
}

/* Лента занимает всё доступное место панели, но не выталкивает заголовок */
.event-feed-scroll {
  flex: 1;
  min-height: 0;
}

.jump-latest {
  position: sticky;
  bottom: 6px;
  margin-left: auto;
  display: block;
  background: rgba(111, 212, 204, 0.15);
  border: 1px solid var(--mint);
  color: var(--ink);
  padding: 3px 8px;
  font-size: 10px;
  cursor: pointer;
}

/* === Resizable dashboard layout (react-resizable-panels) ===
   #main становится flex-контейнером для горизонтального PanelGroup.
   Внутри центральной колонки — .detail-column (flex-col): detail-header сверху,
   под ним вертикальный PanelGroup (.detail-rows) из plan/dialog/log.
   Переопределяет grid выше (равная специфичность #main, более поздний порядок
   в этом же файле — побеждает); размеры панелей сохраняются в localStorage
   (afm-cols / afm-rows). */
#main {
  display: flex;
  min-height: 0;
}

.detail-column {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
  /* Внутренние отступы центральной колонки, чтобы панели не «прилипали» к краям. */
  padding: 8px;
  box-sizing: border-box;
}

/* Вертикальный PanelGroup занимает остаток высоты под detail-header.
   Важно flex:1 1 0 + min-height:0, иначе внутренние панели не скроллятся. */
.detail-rows {
  flex: 1 1 0;
  min-height: 0;
}

/* resize-хендлы: тонкая полоса, подсвечивается при hover/active. */
.resize-handle {
  flex: 0 0 4px;
  background: rgba(183, 135, 255, 0.2);
  transition: background 0.15s;
}
.resize-handle:hover,
.resize-handle[data-resize-handle-active] {
  background: var(--violet);
}
.resize-handle-v {
  cursor: col-resize;
  width: 4px;
}
.resize-handle-h {
  cursor: row-resize;
  height: 4px;
}

/* Каркас панели: заголовок сверху, тело скроллится. */
.panel-frame {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}
.panel-frame-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.panel-frame-body {
  flex: 1;
  min-height: 0;
  /* flex-колонка: внутренний scroll-контейнер (dialog/event-feed) заполняет высоту;
     для панелей без такого контейнера (plan/log) содержимое скроллится самим body. */
  display: flex;
  flex-direction: column;
  overflow: auto;
}
/* Прямой ребёнок body (внешний враппер панели — #dialog-section, #feed-panel и т.п.)
   заполняет высоту и сам становится flex-колонкой, иначе внутренний scroll-контейнер
   (.dialog-scroll/.event-feed-scroll) не получит высоту и схлопнется. */
.panel-frame-body > * {
  flex: 1 1 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* Иконочные кнопки (maximize, mode-toggle) — аскетичный контурный стиль. */
.icon-btn {
  background: transparent;
  border: 1px solid rgba(111, 212, 204, 0.4);
  color: var(--ink);
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  padding: 2px 6px;
}
.icon-btn:hover {
  border-color: var(--mint);
}

/* Полноэкранный оверлей для максимизированной панели (портал в <body>). */
.maximize-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  background: var(--bg);
  display: flex;
  flex-direction: column;
  overflow: auto;
  padding: 16px;
}

/* === Attention-сигнал ===
   Когда стадия ждёт действия пользователя (awaiting_user_input / awaiting_approval),
   подсвечиваем её в трёх местах: элемент списка стадий (сайдбар), точка в шапке и
   рамка соответствующей панели (plan/dialog). */
@keyframes attentionPulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(229, 212, 66, 0); }
  50%      { box-shadow: 0 0 12px 2px rgba(229, 212, 66, 0.65); }
}

.panel-frame.attention {
  border: 1px solid var(--amber);
  animation: attentionPulse 1.6s ease-in-out infinite;
}

.stages-list [data-attention='true'] {
  animation: attentionPulse 1.6s ease-in-out infinite;
}

.attention-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--amber);
  animation: attentionPulse 1.2s ease-in-out infinite;
}
```

- [ ] **Step 4: Create `stages-list.css`**

```css
/* pkg/web/dashboard/public/skins/base/stages-list.css
   Sidebar stage list, status dots, active-stage indicator. */

#stages-list {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.stage-item {
  display: grid;
  grid-template-columns: 18px 1fr auto;
  align-items: center;
  gap: 10px;
  padding: 8px 4px;
  border-bottom: 1px solid rgba(111, 212, 204, 0.06);
  cursor: pointer;
  position: relative;
  font-size: 11px;
  letter-spacing: 0.05em;
  color: var(--ink);
  text-transform: uppercase;
  transition: background 0.15s;
}

.stage-item:hover {
  background: rgba(111, 212, 204, 0.06);
}

/* Средняя ячейка сетки .stage-item: id крупно + name мелко под ним.
   .stage-id наследует font-size/text-transform/letter-spacing от .stage-item. */
.stage-label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.stage-id {
  font-weight: 600;
}

.stage-name {
  font-size: 9px;
  text-transform: none;
  letter-spacing: 0;
  opacity: 0.6;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* status-dot стилизуется как кольцо */
.status-dot {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  border: 1px solid var(--c-pending);
  position: relative;
  flex-shrink: 0;
  background: transparent;
}
.status-dot::after {
  content: "";
  position: absolute;
  inset: 3px;
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

/* "running" rings — pulse */
.status-dot[data-status="running"],
.status-dot[data-status="planning"],
.status-dot[data-status="revising"],
.status-dot[data-status="retrying"] {
  animation: runRing 1.6s ease-in-out infinite;
}
.status-dot[data-status="awaiting_user_input"] {
  animation: askPulse 1.8s ease-in-out infinite;
}

@keyframes runRing {
  0%, 100% { box-shadow: 0 0 0 0 currentColor; }
  50%      { box-shadow: 0 0 0 4px transparent; }
}
@keyframes askPulse {
  0%, 100% { box-shadow: 0 0 0 0 currentColor; }
  50%      { box-shadow: 0 0 0 5px transparent; }
}

/* Dialog badge на стадии с awaiting_user_input */
.dialog-badge {
  font-size: 11px;
  margin-left: 4px;
  color: var(--c-awaiting-user);
}

/* === Active stage === */
.stage-item.active {
  background: linear-gradient(90deg, rgba(229, 212, 66, 0.06), transparent);
}
.stage-item.active::before {
  content: "";
  position: absolute;
  left: -14px;
  top: 0;
  bottom: 0;
  width: 2px;
  background: var(--amber);
  box-shadow: 0 0 8px var(--amber);
  animation: activeBar 2.4s ease-in-out infinite;
}
.stage-item.active::after {
  content: "";
  position: absolute;
  left: 0;
  right: 0;
  top: 0;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(229, 212, 66, 0.7), transparent);
  animation: scanlineX 3s linear infinite;
  pointer-events: none;
}

@keyframes activeBar {
  0%, 100% { box-shadow: 0 0 4px var(--amber); opacity: 0.85; }
  50%      { box-shadow: 0 0 12px var(--amber); opacity: 1; }
}
@keyframes scanlineX {
  0%   { transform: translateY(0); opacity: 0; }
  10%  { opacity: 1; }
  90%  { opacity: 1; }
  100% { transform: translateY(34px); opacity: 0; }
}
```

- [ ] **Step 5: Create `plan-panel.css`**

```css
/* pkg/web/dashboard/public/skins/base/plan-panel.css
   Detail panel: header, status badges, plan review, comment form, action buttons. */

/* === Detail panel header === */
.detail-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--ink-dim);
  font-size: 13px;
  letter-spacing: 0.15em;
  text-transform: uppercase;
}

.detail-content.hidden { display: none; }

.detail-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 18px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--mint-soft);
  position: relative;
}

.detail-header h2 {
  font-size: 14px;
  font-weight: 500;
  letter-spacing: 0.25em;
  text-transform: uppercase;
  color: var(--ink-hi);
  flex: 1;
}

/* Ornament в углу */
.ornament {
  width: 32px;
  height: 32px;
  flex-shrink: 0;
  opacity: 0.5;
  animation: spinSlow 22s linear infinite;
}
.ornament svg {
  display: block;
  width: 100%;
  height: 100%;
}

/* === Status badges === */
.status-badge {
  display: inline-block;
  padding: 2px 10px;
  border: 1px solid currentColor;
  background: transparent;
  font-size: 9px;
  font-weight: 600;
  letter-spacing: 0.25em;
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

/* Pulsing badges for "running" states */
.status-badge[data-status="running"],
.status-badge[data-status="planning"],
.status-badge[data-status="revising"],
.status-badge[data-status="retrying"],
.status-badge[data-status="awaiting_user_input"] {
  animation: badgePulse 2.2s ease-in-out infinite;
}

@keyframes badgePulse {
  0%, 100% { box-shadow: inset 0 0 0 0 rgba(255, 255, 255, 0); }
  50%      { box-shadow: inset 0 0 0 1px currentColor; }
}

/* === Sections inside detail === */
.section {
  margin-bottom: 18px;
}
.section h3 {
  font-size: 10px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.22em;
  color: var(--ink-hi);
  margin-bottom: 8px;
}

.empty-hint {
  color: var(--ink-dim);
  font-size: 11px;
  font-style: italic;
  letter-spacing: 0.05em;
}

/* === Plan block === */
.markdown-body {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  padding: 12px 14px;
  max-height: 420px;
  overflow-y: auto;
  font-size: 11px;
  line-height: 1.6;
}

.markdown-body h1,
.markdown-body h2,
.markdown-body h3 {
  margin-top: 12px;
  margin-bottom: 6px;
  color: var(--ink-hi);
  letter-spacing: 0.08em;
}
.markdown-body h1 { font-size: 14px; }
.markdown-body h2 { font-size: 12px; text-transform: uppercase; letter-spacing: 0.15em; }
.markdown-body h3 { font-size: 11px; text-transform: uppercase; letter-spacing: 0.15em; color: var(--mint); }

.markdown-body p { margin-bottom: 6px; }

.markdown-body ul,
.markdown-body ol {
  margin-left: 20px;
  margin-bottom: 6px;
}
.markdown-body li::marker { color: var(--ink-dim); }

.markdown-body code {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  padding: 0 4px;
  font-family: inherit;
  font-size: 10.5px;
  color: var(--mint);
}

.markdown-body pre {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  padding: 10px 12px;
  margin-bottom: 8px;
  overflow-x: auto;
}
.markdown-body pre code {
  background: transparent;
  border: none;
  padding: 0;
  color: var(--ink);
  font-size: 11px;
}

.markdown-body strong {
  color: var(--ink-hi);
  font-weight: 600;
}

/* Checkboxes in markdown */
.cb {
  display: inline-block;
  width: 12px;
  height: 12px;
  text-align: center;
  font-size: 10px;
  line-height: 12px;
  margin-right: 4px;
  vertical-align: middle;
  font-family: inherit;
}
.cb-done {
  background: var(--mint);
  color: var(--bg);
  border: 1px solid var(--mint);
}
.cb-open {
  background: transparent;
  border: 1px solid var(--ink-dim);
  color: transparent;
}

/* === Plan line-by-line review === */
#plan-content {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  padding: 10px 6px;
  max-height: 420px;
  overflow-y: auto;
}

.plan-line {
  display: grid;
  grid-template-columns: 36px 1fr 24px;
  gap: 2px;
  align-items: flex-start;
  padding: 1px 6px;
  cursor: pointer;
  font-size: 11px;
  line-height: 1.6;
  position: relative;
  transition: background 0.1s;
}
.plan-line:hover {
  background: rgba(111, 212, 204, 0.06);
}
.plan-line.has-comment {
  background: rgba(229, 212, 66, 0.07);
  border-left: 2px solid var(--amber);
  margin-left: -2px;
  padding-left: 8px;
}
.plan-line.has-comment:hover {
  background: rgba(229, 212, 66, 0.12);
}

.line-num {
  text-align: right;
  padding-right: 8px;
  color: var(--ink-dim);
  font-size: 10px;
  opacity: 0.5;
  user-select: none;
  transition: color 0.1s, opacity 0.1s;
}
.plan-line:hover .line-num {
  color: var(--mint);
  opacity: 1;
}

.line-content {
  min-width: 0;
  word-wrap: break-word;
}
.line-content > p { margin: 0; }
.line-content h1 { color: var(--ink-hi); font-size: 14px; letter-spacing: 0.08em; }
.line-content h2 { color: var(--ink-hi); font-size: 12px; text-transform: uppercase; letter-spacing: 0.15em; }
.line-content h3 { color: var(--mint); font-size: 11px; text-transform: uppercase; letter-spacing: 0.15em; }
.line-content code {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  padding: 0 4px;
  color: var(--mint);
  font-size: 10.5px;
}
.line-content pre {
  background: var(--bg);
  border: 1px solid var(--mint-soft);
  padding: 8px 10px;
  margin: 4px 0;
  overflow-x: auto;
}
.line-content strong { color: var(--ink-hi); font-weight: 600; }
.line-content li { margin-left: 18px; }

.line-comment-marker {
  text-align: center;
  font-size: 9px;
  color: var(--amber);
  opacity: 0;
  transition: opacity 0.15s;
  user-select: none;
}
.plan-line:hover .line-comment-marker {
  opacity: 0.6;
}
.plan-line.has-comment .line-comment-marker {
  opacity: 1;
}

/* === Special plan sections === */
.plan-section-wrapper {
  margin: 8px 0;
  padding: 8px 0 8px 12px;
  border-left: 3px solid var(--mint);
  background: rgba(111, 212, 204, 0.04);
}
.plan-section-wrapper.plan-section-assumptions {
  border-left-color: var(--amber);
  background: rgba(229, 212, 66, 0.05);
}
.plan-section-wrapper.plan-section-criteria {
  border-left-color: var(--mint);
  background: rgba(111, 212, 204, 0.05);
}

.plan-section-wrapper .section-header {
  cursor: pointer;
  user-select: none;
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.2em;
  color: var(--ink-hi);
}
.plan-section-wrapper.plan-section-assumptions .section-header { color: var(--amber); }
.plan-section-wrapper.plan-section-criteria    .section-header { color: var(--mint); }

.plan-section-wrapper .section-header .toggle {
  font-size: 11px;
  display: inline-block;
  transition: transform 0.2s;
}
.plan-section-wrapper.collapsed .section-header .toggle {
  transform: rotate(-90deg);
}
.plan-section-wrapper.collapsed .plan-section-body { display: none; }

/* === Inline comment form === */
.line-comment-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  grid-column: 1 / -1;
  margin: 6px 0 8px 36px;
  padding: 10px 12px;
  background: var(--bg);
  border: 1px solid var(--amber);
}

.line-comment-form textarea {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 11px;
  resize: vertical;
  min-height: 56px;
}
.line-comment-form textarea:focus {
  outline: 1px solid var(--mint);
  border-color: var(--mint);
}

.line-comment-form .comment-actions {
  display: flex;
  gap: 8px;
}

.line-comment-display {
  border-color: var(--mint-soft);
  background: var(--bg-elev);
}

.comment-hint {
  margin-top: 8px;
  font-size: 10px;
  letter-spacing: 0.1em;
  color: var(--amber);
  text-transform: uppercase;
  font-style: normal;
}

/* === Action buttons === */
.btn {
  background: transparent;
  border: 1px solid var(--mint);
  color: var(--mint);
  padding: 7px 18px;
  font-family: inherit;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.25em;
  text-transform: uppercase;
  cursor: pointer;
  position: relative;
  transition: all 0.15s;
}
.btn:hover:not(:disabled) {
  box-shadow: 0 0 10px currentColor;
}
.btn:disabled {
  opacity: 0.3;
  cursor: not-allowed;
}

.btn-approve {
  background: var(--mint);
  color: var(--bg);
  border-color: var(--mint);
}
.btn-approve:hover:not(:disabled) {
  background: var(--ink-hi);
  border-color: var(--ink-hi);
  color: var(--bg);
}

.btn-revise {
  border-color: var(--amber);
  color: var(--amber);
}

.btn-retry {
  border-color: var(--coral);
  color: var(--coral);
}

.btn-send {
  background: var(--violet);
  color: var(--bg);
  border-color: var(--violet);
}

.btn-cancel {
  border-color: var(--ink-dim);
  color: var(--ink-dim);
}

.actions-row {
  display: flex;
  gap: 10px;
  margin-bottom: 10px;
}
```

- [ ] **Step 6: Create `log-panel.css`**

```css
/* pkg/web/dashboard/public/skins/base/log-panel.css
   Stage execution log viewer. */

.log-content {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  padding: 12px 14px;
  font-family: inherit;
  font-size: 11px;
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
```

- [ ] **Step 7: Create `dialog-channel.css`**

```css
/* pkg/web/dashboard/public/skins/base/dialog-channel.css
   Interactive Q&A dialog panel. */

#dialog-section {
  position: relative;
  border: 1px solid rgba(183, 135, 255, 0.3);
  padding: 14px;
  background:
    radial-gradient(circle at top right, rgba(183, 135, 255, 0.08), transparent 60%);
}
#dialog-section::before,
#dialog-section::after {
  content: "";
  position: absolute;
  width: 10px;
  height: 10px;
  border: 1px solid var(--violet);
}
#dialog-section::before { top: -1px; left: -1px; border-right: 0; border-bottom: 0; }
#dialog-section::after  { bottom: -1px; right: -1px; border-left: 0; border-top: 0; }

#dialog-section h3 {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0;
  letter-spacing: 0.25em;
  color: var(--violet);
  text-transform: uppercase;
  margin-bottom: 12px;
  padding-bottom: 10px;
  border-bottom: 1px dashed rgba(183, 135, 255, 0.25);
}
#dialog-section h3 .sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
#dialog-section h3::before {
  content: "";
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--violet);
  box-shadow: 0 0 8px var(--violet);
  animation: wsBlink 1.6s ease-in-out infinite;
  flex-shrink: 0;
}
#dialog-section h3::after {
  content: "DIALOG \00b7 CHANNEL OPEN";
  font-size: 10px;
  letter-spacing: 0.25em;
}

/* === History === */
.dialog-history {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 14px;
}
.dialog-history.collapsed { display: none; }

.dialog-history .qa {
  border-left: 2px solid rgba(111, 212, 204, 0.2);
  padding-left: 12px;
}
.dialog-history .qa .q {
  font-size: 11px;
  color: var(--ink);
  margin-bottom: 4px;
}
.dialog-history .qa .q::before {
  content: "Q \00b7 ";
  color: var(--mint);
  font-weight: 600;
  letter-spacing: 0.2em;
}
.dialog-history .qa .a {
  font-size: 10.5px;
  color: var(--qa-answer);
  padding-left: 16px;
  position: relative;
}
.dialog-history .qa .a::before {
  content: "A \00b7 ";
  color: var(--amber);
  font-weight: 600;
  letter-spacing: 0.2em;
  margin-right: 2px;
}

.dialog-history .agent-msg {
  border-left: 2px solid rgba(186, 148, 255, 0.25);
  padding-left: 12px;
  font-size: 11px;
  color: var(--ink);
  white-space: pre-wrap;
  overflow-wrap: break-word;
}
.dialog-history .agent-msg.md {
  white-space: normal;
}

.phase-divider {
  font-size: 9px;
  text-transform: uppercase;
  letter-spacing: 0.22em;
  color: var(--ink-dim);
  border-bottom: 1px dashed rgba(111, 212, 204, 0.12);
  padding-bottom: 4px;
  margin: 8px 0;
}

/* === Pending question === */
.dialog-pending {
  position: relative;
  border: 1px solid var(--violet);
  padding: 14px 16px;
  margin-bottom: 12px;
  background: linear-gradient(180deg, rgba(183, 135, 255, 0.10), rgba(183, 135, 255, 0.02));
  overflow: hidden;
  animation: pendingBreathe 3.4s ease-in-out infinite;
}

@keyframes pendingBreathe {
  0%, 100% {
    box-shadow:
      inset 0 0 18px rgba(183, 135, 255, 0.08),
      0 0 12px rgba(183, 135, 255, 0.10);
  }
  50% {
    box-shadow:
      inset 0 0 30px rgba(183, 135, 255, 0.16),
      0 0 22px rgba(183, 135, 255, 0.22);
  }
}

@keyframes dialogFlash {
  0% {
    box-shadow:
      0 0 0 2px var(--violet),
      0 0 50px rgba(183, 135, 255, 0.9),
      inset 0 0 30px rgba(183, 135, 255, 0.3);
  }
  40% {
    box-shadow:
      0 0 0 1px rgba(183, 135, 255, 0.6),
      0 0 30px rgba(183, 135, 255, 0.5),
      inset 0 0 20px rgba(183, 135, 255, 0.15);
  }
  100% {
    box-shadow:
      0 0 0 0 transparent,
      0 0 0 transparent,
      inset 0 0 0 transparent;
  }
}

.dialog-flash {
  animation: dialogFlash 2.5s ease-out forwards;
}

.dialog-pending .dialog-question {
  font-size: 12px;
  line-height: 1.55;
  color: var(--ink-hi);
  margin-bottom: 14px;
}

/* === Options with auto-letter hotkeys via CSS counter === */
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
  border: 1px solid rgba(183, 135, 255, 0.55);
  color: var(--dialog-option-text);
  padding: 6px 14px 6px 8px;
  font-family: inherit;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  cursor: pointer;
  transition: all 0.15s;
  animation: dialogOptionAppear 240ms ease-out backwards;
}
.dialog-pending .dialog-options button::before {
  content: counter(opt, upper-alpha) " ";
  color: var(--amber);
  margin-right: 6px;
  font-weight: 700;
}
.dialog-pending .dialog-options button:hover {
  border-color: var(--violet);
  color: var(--ink-hi);
  box-shadow: 0 0 8px rgba(183, 135, 255, 0.45);
}
.dialog-pending .dialog-options button.selected {
  background: rgba(183, 135, 255, 0.18);
  border-color: var(--violet);
  color: var(--ink-hi);
  box-shadow: 0 0 12px rgba(183, 135, 255, 0.5);
}
.dialog-pending .dialog-options.dimmed button:not(.selected) {
  opacity: 0.4;
}

@keyframes dialogOptionAppear {
  from { transform: translateY(4px); opacity: 0; }
  to   { transform: translateY(0); opacity: 1; }
}

/* === Custom textarea === */
.dialog-pending .dialog-custom {
  width: 100%;
  min-height: 60px;
  background: var(--panel-bg);
  border: 1px solid rgba(111, 212, 204, 0.2);
  color: var(--ink);
  padding: 8px 10px;
  font-family: inherit;
  font-size: 11px;
  resize: vertical;
}
.dialog-pending .dialog-custom::placeholder {
  color: var(--ink-dim);
  letter-spacing: 0.05em;
}
.dialog-pending .dialog-custom:focus {
  outline: 1px solid var(--violet);
  border-color: var(--violet);
}
.dialog-pending .dialog-custom:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

/* === Dialog actions === */
.dialog-pending .dialog-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-top: 10px;
}
.dialog-pending .btn-cancel-dialog {
  background: transparent;
  border: 1px solid rgba(255, 107, 90, 0.55);
  color: var(--btn-cancel-dialog-text);
  padding: 7px 18px;
  font-family: inherit;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.25em;
  text-transform: uppercase;
  cursor: pointer;
}
.dialog-pending .btn-cancel-dialog:hover {
  box-shadow: 0 0 8px var(--coral);
}

/* === "Agent is waiting" indicator === */
.typing-indicator {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 9px;
  letter-spacing: 0.25em;
  color: var(--violet);
  text-transform: uppercase;
}
.typing-indicator .blink {
  display: inline-block;
  width: 6px;
  height: 11px;
  background: var(--violet);
  animation: caret 1s steps(1) infinite;
}
@keyframes caret { 50% { opacity: 0; } }

/* === Toggle button === */
.dialog-toggle {
  background: transparent;
  border: none;
  color: var(--ink-dim);
  font-family: inherit;
  font-size: 10px;
  letter-spacing: 0.2em;
  text-transform: uppercase;
  cursor: pointer;
  padding: 4px 0;
  margin-top: 4px;
}
.dialog-toggle:hover { color: var(--mint); }

/* Override dialog-pending hidden */
.dialog-pending.hidden { display: none !important; }
```

- [ ] **Step 8: Create `event-feed.css`**

```css
/* pkg/web/dashboard/public/skins/base/event-feed.css
   Live event feed + supervisor decision markers. */

#feed-content {
  font-size: 11px;
  line-height: 1.5;
}

.feed-entry {
  display: grid;
  grid-template-columns: 38px 1fr;
  gap: 8px;
  padding: 5px 0;
  border-bottom: 1px solid rgba(111, 212, 204, 0.05);
  word-break: break-word;
}
.feed-entry:hover {
  background: rgba(111, 212, 204, 0.05);
}

.feed-time {
  color: var(--ink-dim);
  font-size: 10px;
  text-align: right;
  padding-right: 2px;
  letter-spacing: 0.05em;
}

.feed-msg {
  color: var(--ink);
  font-size: 11px;
  line-height: 1.5;
}
.feed-msg.action { color: var(--ink-dim); }
.feed-msg.error  { color: var(--coral); }

/* Supervisor decisions — выделены из общего лога: акцентный фон + левая полоса. */
.feed-entry.supervisor {
  background: rgba(192, 132, 252, 0.12);
  border-left: 3px solid var(--supervisor-accent);
  padding-left: 6px;
  margin: 2px 0;
}
.feed-entry.supervisor:hover {
  background: rgba(192, 132, 252, 0.18);
}
.feed-msg.supervisor {
  color: var(--supervisor-accent);
  font-weight: 600;
}

/* Supervisor-decision: точка в углу статус-бейджа + поповер причины по клику. */
.status-badge-wrap {
  position: relative;
  display: inline-flex;
}
.supervisor-decision {
  display: contents;
}
.supervisor-dot {
  position: absolute;
  top: -3px;
  right: -3px;
  width: 8px;
  height: 8px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 0 6px currentColor;
  cursor: pointer;
}
.supervisor-dot.standard { color: var(--amber); }
.supervisor-dot.autonomous { color: var(--supervisor-accent); }

.supervisor-popover {
  position: absolute;
  top: calc(100% + 6px);
  right: -3px;
  z-index: 50;
  width: max-content;
  max-width: 280px;
  padding: 8px 10px;
  border-radius: 8px;
  border: 1px solid var(--supervisor-accent);
  background: rgba(192, 132, 252, 0.12);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
  color: var(--ink-hi);
  font-size: 11px;
  line-height: 1.4;
  text-align: left;
}
.supervisor-popover-title {
  margin-bottom: 4px;
  font-weight: 600;
  color: var(--supervisor-accent);
}
.supervisor-popover-reason {
  font-weight: 400;
  opacity: 0.9;
  white-space: normal;
}

.feed-stage-badge {
  display: inline;
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--amber);
  margin-right: 6px;
}
.feed-stage-badge::after {
  content: " ·";
  color: var(--ink-dim);
  margin: 0 4px 0 2px;
}

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
```

- [ ] **Step 9: Create `markdown.css`**

```css
/* pkg/web/dashboard/public/skins/base/markdown.css
   Shared markdown rendering (plan, agent messages, dialog history). */

.md p { margin: 6px 0; }
.md > :first-child { margin-top: 0; }
.md > :last-child { margin-bottom: 0; }

.md table {
  border-collapse: collapse;
  margin: 8px 0;
  font-size: 12px;
}
.md th, .md td {
  border: 1px solid var(--mint-soft);
  padding: 4px 10px;
  text-align: left;
}
.md th {
  color: var(--ink-hi);
  background: var(--bg-elev);
  text-transform: uppercase;
  font-size: 10px;
  letter-spacing: 0.08em;
}
.md tr:nth-child(even) td { background: var(--grid); }

.md blockquote {
  margin: 8px 0;
  padding: 2px 12px;
  border-left: 2px solid var(--mint);
  color: var(--ink-dim);
}

.md em { color: var(--ink-hi); font-style: italic; }

.md a { color: var(--mint); text-decoration: underline; }
.md a:hover { color: var(--ink-hi); }

.md ul, .md ol { margin: 4px 0; padding-left: 22px; }
.md ul ul, .md ol ol, .md ul ol, .md ol ul { margin: 2px 0; }

.md hr {
  border: none;
  border-top: 1px solid var(--mint-soft);
  margin: 12px 0;
}
```

- [ ] **Step 10: Sanity-check all 9 files were written correctly**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard/public/skins/base
wc -l reset.css header.css layout.css stages-list.css plan-panel.css log-panel.css dialog-channel.css event-feed.css markdown.css
grep -c '^}' *.css
```
Expected: all 9 files non-empty, `wc -l` totals roughly matching the original ~1450-line structural portion of `style.css` (minus the deleted dead usage-panel block and the design-tokens/`:root` block, which are no longer here), no command errors.

- [ ] **Step 11: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/public/skins/base
git commit -m "feat(dashboard): базовые CSS-партиалы скинов по компонентам"
```

---

## Task 3: CSS — `novacorps` skin

**Files:**
- Create: `pkg/web/dashboard/public/skins/novacorps/index.css`

**Interfaces:**
- Consumes: all 9 `base/*.css` partials from Task 2 (via `@import`).
- Produces: the token set listed in Task 2's Interfaces, for both `data-theme="dark"` (values byte-identical to today's `public/style.css` `:root` block) and `data-theme="light"` (newly authored — see rationale below).

- [ ] **Step 1: Create `novacorps/index.css`**

```css
/* pkg/web/dashboard/public/skins/novacorps/index.css — Nova Corps skin
   Hi-tech holographic UI: neon accents, scanlines, ray-sweep decor.
   Structure comes from ../base/*.css; this file supplies only tokens + decor. */

@import url("../base/reset.css");
@import url("../base/header.css");
@import url("../base/layout.css");
@import url("../base/stages-list.css");
@import url("../base/plan-panel.css");
@import url("../base/log-panel.css");
@import url("../base/dialog-channel.css");
@import url("../base/event-feed.css");
@import url("../base/markdown.css");

:root[data-theme="dark"] {
  /* base surfaces — идентичны текущему style.css:23-56 */
  --bg:        #08141a;
  --bg-elev:   #0c1c24;
  --panel-bg:  rgba(8, 20, 26, 0.88);

  /* text */
  --ink:       #cfeeea;
  --ink-hi:    #e7faf7;
  --ink-dim:   #4a8a85;

  /* accents */
  --mint:      #6fd4cc;
  --mint-soft: rgba(111, 212, 204, 0.22);
  --grid:      rgba(111, 212, 204, 0.04);
  --amber:     #e5d442;
  --violet:    #b787ff;
  --coral:     #ff6b5a;

  /* dialog/supervisor accents (dark-mode-tuned pale hues, как раньше) */
  --qa-answer:              #9fe0d8;
  --dialog-option-text:     #d4baff;
  --btn-cancel-dialog-text: #ff8a7a;
  --supervisor-accent:      #c084fc;

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

  /* legacy aliases used by inline styles */
  --text:    var(--ink);
  --text-dim: var(--ink-dim);
}

:root[data-theme="light"] {
  /* base surfaces — новая светлая палитра, того же характера (тёплый нейтраль + teal) */
  --bg:        #f4f9f8;
  --bg-elev:   #ffffff;
  --panel-bg:  rgba(255, 255, 255, 0.88);

  /* text */
  --ink:       #16302c;
  --ink-hi:    #08201c;
  --ink-dim:   #5c8f88;

  /* accents — сдвинуты темнее относительно dark-варианта для контраста на светлом фоне */
  --mint:      #1f8f86;
  --mint-soft: rgba(31, 143, 134, 0.18);
  --grid:      rgba(31, 143, 134, 0.05);
  --amber:     #a6790a;
  --violet:    #7c3aed;
  --coral:     #c2410c;

  /* dialog/supervisor accents — переподобраны под светлый фон */
  --qa-answer:              #0f766e;
  --dialog-option-text:     #6d28d9;
  --btn-cancel-dialog-text: #b91c1c;
  --supervisor-accent:      #9333ea;

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

  /* legacy aliases used by inline styles */
  --text:    var(--ink);
  --text-dim: var(--ink-dim);
}

/* === Scanlines overlay on body (novacorps-only decor) === */
body::after {
  content: "";
  position: fixed;
  inset: 0;
  pointer-events: none;
  z-index: 100;
  background-image: repeating-linear-gradient(
    0deg,
    rgba(255, 255, 255, 0.014) 0px,
    rgba(255, 255, 255, 0.014) 1px,
    transparent 1px,
    transparent 3px
  );
}

/* === Yellow scanner ray on grid lines (novacorps-only decor) === */
.ray {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
  z-index: 0;
  -webkit-mask-image:
    linear-gradient(rgba(0, 0, 0, 1) 1px, transparent 1px),
    linear-gradient(90deg, rgba(0, 0, 0, 1) 1px, transparent 1px);
  -webkit-mask-size: 24px 24px;
          mask-image:
    linear-gradient(rgba(0, 0, 0, 1) 1px, transparent 1px),
    linear-gradient(90deg, rgba(0, 0, 0, 1) 1px, transparent 1px);
          mask-size: 24px 24px;
}
.ray::before {
  content: "";
  position: absolute;
  top: -40%;
  left: -40%;
  width: 60%;
  height: 180%;
  background: radial-gradient(
    ellipse at center,
    rgba(229, 212, 66, 0.55) 0%,
    rgba(229, 212, 66, 0.20) 25%,
    transparent 60%
  );
  transform: rotate(20deg) translate(-30%, -30%);
  filter: blur(2px);
  animation: raySweep 9s linear infinite;
  will-change: transform, opacity;
}

@keyframes raySweep {
  0%   { transform: rotate(20deg) translate(-30%, -30%); opacity: 0; }
  8%   { opacity: 1; }
  45%  { opacity: 1; }
  60%  { transform: rotate(20deg) translate(160%, 70%); opacity: 0; }
  100% { transform: rotate(20deg) translate(160%, 70%); opacity: 0; }
}
```

Note on the light palette: novacorps' dark palette is preserved byte-for-byte from today's `style.css`. The light values above are new (this skin never had a light mode before) — chosen to keep the same hue family (teal/amber/violet/coral) shifted to sit on a light neutral background with roughly WCAG-AA-ish contrast for body text (`--ink`/`--ink-hi` against `--bg`/`--bg-elev`); dim/secondary text and low-alpha accent washes elsewhere in the base partials are intentionally not pixel-audited for contrast — see Task 11's manual QA step.

- [ ] **Step 2: Verify the file parses as valid CSS structure (paren/brace balance)**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard/public/skins/novacorps
node -e "require('fs').readFileSync('index.css','utf8').split('{').length === require('fs').readFileSync('index.css','utf8').split('}').length || (()=>{throw new Error('brace mismatch')})()"
grep -c "data-theme" index.css
```
Expected: no error thrown (braces balanced); `data-theme` count is 2.

- [ ] **Step 3: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/public/skins/novacorps
git commit -m "feat(dashboard): novacorps как скин поверх base-партиалов, с dark+light токенами"
```

---

## Task 4: CSS — `goga` skin

**Files:**
- Create: `pkg/web/dashboard/public/skins/goga/index.css`

**Interfaces:**
- Consumes: all 9 `base/*.css` partials from Task 2.
- Produces: same token contract as Task 3, goga's own values.

- [ ] **Step 1: Create `goga/index.css`**

```css
/* pkg/web/dashboard/public/skins/goga/index.css — Goga skin
   Тёмная tech-тема по мотивам qarium.ru/goga: system sans-serif, плоские панели,
   без неоновой декорации novacorps. Структура — из ../base/*.css.

   Правила ниже (после @import) при равной специфичности с base побеждают за счёт
   более позднего порядка в каскаде — .theme-goga-скоупинг больше не нужен. */

@import url("../base/reset.css");
@import url("../base/header.css");
@import url("../base/layout.css");
@import url("../base/stages-list.css");
@import url("../base/plan-panel.css");
@import url("../base/log-panel.css");
@import url("../base/dialog-channel.css");
@import url("../base/event-feed.css");
@import url("../base/markdown.css");

:root[data-theme="dark"] {
  /* base surfaces — идентичны текущему style-goga.css:14-30 */
  --bg:        #0A0E1A;
  --bg-elev:   #121830;
  --panel-bg:  var(--bg-elev);

  /* text */
  --ink:       #FFFFFF;
  --ink-hi:    #FFFFFF;
  --ink-dim:   #A0AEC0;

  /* accents: teal primary, blue secondary */
  --mint:      #20D4BF;
  --mint-soft: rgba(255, 255, 255, 0.08);
  --grid:      rgba(255, 255, 255, 0.03);
  --amber:     #3882F6;
  --violet:    #3882F6;
  --coral:     #FF6B5A;

  /* dialog/supervisor accents — goga их не переопределял и сегодня, сохраняем
     дословные значения novacorps (текущее поведение — унаследованные через
     старый @import — не меняется) */
  --qa-answer:              #9fe0d8;
  --dialog-option-text:     #d4baff;
  --btn-cancel-dialog-text: #ff8a7a;
  --supervisor-accent:      #c084fc;

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

  /* legacy aliases */
  --text:     var(--ink);
  --text-dim: var(--ink-dim);
}

:root[data-theme="light"] {
  /* base surfaces */
  --bg:        #F7F9FC;
  --bg-elev:   #FFFFFF;
  --panel-bg:  var(--bg-elev);

  /* text */
  --ink:       #0F172A;
  --ink-hi:    #000000;
  --ink-dim:   #64748B;

  /* accents: teal primary, blue secondary — сдвинуты темнее для контраста на белом */
  --mint:      #0E9488;
  --mint-soft: rgba(15, 23, 42, 0.06);
  --grid:      rgba(15, 23, 42, 0.03);
  --amber:     #2563EB;
  --violet:    #2563EB;
  --coral:     #E11D48;

  /* dialog/supervisor accents — переподобраны под светлый фон */
  --qa-answer:              #0f766e;
  --dialog-option-text:     #2563eb;
  --btn-cancel-dialog-text: #b91c1c;
  --supervisor-accent:      #7e22ce;

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

  /* legacy aliases */
  --text:     var(--ink);
  --text-dim: var(--ink-dim);
}

/* === goga-специфика поверх base ===
   Base-партиалы импортированы выше — эти два правила автоматически побеждают
   одноимённые правила base (равная специфичность #main/#header h1, более
   поздний порядок в каскаде), без .theme-goga-скоупинга старой архитектуры.
   Панели/ray/scanlines отдельно не переопределяются — они уже решены токенами
   выше (--panel-bg: var(--bg-elev); .ray/scanlines просто не существуют в
   base, значит под goga не рендерятся вовсе). */

/* Логотип-слово: goga показывает «goga», не «afm» (скрываем текст h1 и выводим
   свой через ::after, чтобы не делать React-компонент тема-зависимым). */
#header h1 {
  font-size: 0;
}
#header h1::after {
  content: 'goga';
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.3em;
  text-transform: lowercase;
  color: var(--mint);
}

/* Чистый фон goga: без novacorps-клетки (background-image на #main). */
#main {
  background-image: none;
  background: var(--bg-elev);
}
```

Note: three simplifications versus today's `style-goga.css`, all disclosed:
1. No `.theme-goga .ray { display: none }` override — `.ray` styling no longer exists in `base/*.css` at all (it moved into `novacorps/index.css` as novacorps-only decor), so under goga the empty `<div className="ray" />` (`src/app/App.tsx:121`) renders as a zero-height, unstyled block — harmless, no layout impact.
2. No `.theme-goga #stages-panel, #detail-panel, #feed-panel { background: var(--bg-elev) }` override — `base/layout.css` now reads `background: var(--panel-bg)`, and goga sets `--panel-bg: var(--bg-elev)` in both its token blocks, so the same flat-panel look falls out of the token definition alone.
3. No `.theme-goga` class-scoping on the two remaining overrides (wordmark, `#main` background) — since this file is only ever loaded when goga is the active skin, and its own rules are declared after the `@import`s (later in cascade order, same specificity), they win without needing extra specificity from a class selector.

- [ ] **Step 2: Verify the file parses as valid CSS structure**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard/public/skins/goga
node -e "require('fs').readFileSync('index.css','utf8').split('{').length === require('fs').readFileSync('index.css','utf8').split('}').length || (()=>{throw new Error('brace mismatch')})()"
grep -c "data-theme" index.css
```
Expected: no error; `data-theme` count is 2.

- [ ] **Step 3: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/public/skins/goga
git commit -m "feat(dashboard): goga как скин поверх base-партиалов, с dark+light токенами"
```

**STOP — before moving to Task 5, do Task 4b below.** `goga/index.css` as
written in Step 1 above does not yet match goga's actual current behavior
(quarium logo image, "QArium" wordmark, title/favicon overrides, and ~28
recolored accent washes) — these were reinstated from `theme-fix` after this
plan was first written. Task 4b (in the Addendum near the end of this file)
replaces this file's content and adds two new asset files before Task 5 runs.

---

## Task 5: React — FlowHeader logo color fix

**Files:**
- Modify: `pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx`

**Interfaces:**
- Consumes: `base/header.css` rules from Task 2 (`.logo .l-ring svg { stroke: var(--mint) }`, `.logo .l-arc svg { stroke: var(--amber) }`, `.logo .l-core svg { fill: var(--mint) }`).
- No prop/exported-signature change — `FlowHeader(props: FlowHeaderProps): ReactElement` unchanged.

- [ ] **Step 1: Remove the hardcoded inline colors**

In `pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx`, replace:

```tsx
      <span className="logo" aria-hidden="true">
        <span className="l-ring">
          <svg viewBox="0 0 24 24" fill="none" stroke="#6fd4cc" strokeWidth="1">
            <polygon points="12,1 22,7 22,17 12,23 2,17 2,7" />
          </svg>
        </span>
        <span className="l-arc">
          <svg viewBox="0 0 24 24" fill="none" stroke="#e5d442" strokeWidth="1" strokeLinecap="round">
            <path d="M 12 3 A 9 9 0 0 1 21 12" />
          </svg>
        </span>
        <span className="l-core">
          <svg viewBox="0 0 24 24" fill="#6fd4cc" stroke="none">
            <circle cx="12" cy="12" r="2.4" />
          </svg>
        </span>
      </span>
```

with:

```tsx
      <span className="logo" aria-hidden="true">
        <span className="l-ring">
          <svg viewBox="0 0 24 24" fill="none" strokeWidth="1">
            <polygon points="12,1 22,7 22,17 12,23 2,17 2,7" />
          </svg>
        </span>
        <span className="l-arc">
          <svg viewBox="0 0 24 24" fill="none" strokeWidth="1" strokeLinecap="round">
            <path d="M 12 3 A 9 9 0 0 1 21 12" />
          </svg>
        </span>
        <span className="l-core">
          <svg viewBox="0 0 24 24" stroke="none">
            <circle cx="12" cy="12" r="2.4" />
          </svg>
        </span>
      </span>
```

(Only the `stroke="#6fd4cc"`, `stroke="#e5d442"`, and `fill="#6fd4cc"` attributes are removed; everything else — `viewBox`, `fill="none"` on the ring/arc, `strokeWidth`, `strokeLinecap`, `stroke="none"` on the core, and all child shapes — is unchanged.)

- [ ] **Step 2: Run typecheck and existing tests**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard
npm run typecheck
npm test -- --run
```
Expected: PASS (no existing test asserts on the removed attributes — `FlowHeader.tsx` has no dedicated test file today).

- [ ] **Step 3: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx
git commit -m "fix(dashboard): цвета логотипа шапки через CSS-токены вместо хардкода в JSX"
```

---

## Task 6: React — `useThemeMode` hook

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-theme-mode/use-theme-mode.ts`
- Create: `pkg/web/dashboard/src/hooks/use-theme-mode/index.ts`
- Create: `pkg/web/dashboard/src/hooks/use-theme-mode/use-theme-mode.test.ts`

**Interfaces:**
- Produces: `useThemeMode(): { mode: ThemeMode; toggle: () => void }` where `type ThemeMode = 'dark' | 'light'`, exported from `src/hooks/use-theme-mode`.
- Consumes: `document.documentElement.dataset.theme`, already set before React mounts by the bootstrap script (Task 8).

(No CODEMANIFEST/`.usages` file — following the precedent of sibling hooks `use-attention` and `use-title-flash`, which also don't have one; only the hooks feeding the goga-cell contract system do.)

- [ ] **Step 1: Write the failing tests**

Create `pkg/web/dashboard/src/hooks/use-theme-mode/use-theme-mode.test.ts`:

```ts
import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useThemeMode } from './use-theme-mode'

describe('useThemeMode', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('data-theme')
    window.localStorage.clear()
  })

  it('читает начальный режим из data-theme, выставленного бутстрап-скриптом', () => {
    document.documentElement.dataset.theme = 'light'
    const { result } = renderHook(() => useThemeMode())
    expect(result.current.mode).toBe('light')
  })

  it('по умолчанию dark, если data-theme не light', () => {
    const { result } = renderHook(() => useThemeMode())
    expect(result.current.mode).toBe('dark')
  })

  it('toggle() переключает режим, атрибут на <html> и localStorage', () => {
    document.documentElement.dataset.theme = 'dark'
    const { result } = renderHook(() => useThemeMode())

    act(() => { result.current.toggle() })

    expect(result.current.mode).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(window.localStorage.getItem('afm-mode')).toBe('light')
  })

  it('toggle() дважды возвращает исходный режим', () => {
    document.documentElement.dataset.theme = 'dark'
    const { result } = renderHook(() => useThemeMode())

    act(() => { result.current.toggle() })
    act(() => { result.current.toggle() })

    expect(result.current.mode).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(window.localStorage.getItem('afm-mode')).toBe('dark')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard && npx vitest run src/hooks/use-theme-mode`
Expected: FAIL — cannot find module `./use-theme-mode` (file doesn't exist yet).

- [ ] **Step 3: Implement the hook**

Create `pkg/web/dashboard/src/hooks/use-theme-mode/use-theme-mode.ts`:

```ts
import { useCallback, useState } from 'react'

export type ThemeMode = 'dark' | 'light'

const STORAGE_KEY = 'afm-mode'

function readInitialMode(): ThemeMode {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
}

// Переключатель dark/light, независимый от активного скина (novacorps/goga/custom).
// Начальное значение уже выставлено инлайн-скриптом в index.html (до отрисовки
// CSS, без FOUC) — хук лишь читает его из data-theme и даёт toggle() для UI.
export function useThemeMode(): { mode: ThemeMode; toggle: () => void } {
  const [mode, setMode] = useState<ThemeMode>(readInitialMode)

  const toggle = useCallback(() => {
    setMode((prev) => {
      const next: ThemeMode = prev === 'dark' ? 'light' : 'dark'
      document.documentElement.dataset.theme = next
      window.localStorage.setItem(STORAGE_KEY, next)
      return next
    })
  }, [])

  return { mode, toggle }
}
```

Create `pkg/web/dashboard/src/hooks/use-theme-mode/index.ts`:

```ts
export { useThemeMode } from './use-theme-mode'
export type { ThemeMode } from './use-theme-mode'
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `npx vitest run src/hooks/use-theme-mode`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/src/hooks/use-theme-mode
git commit -m "feat(dashboard): хук useThemeMode для переключения dark/light"
```

---

## Task 7: React — Footer dark/light toggle button

**Files:**
- Modify: `pkg/web/dashboard/src/components/footer/Footer.tsx`
- Modify: `pkg/web/dashboard/src/components/footer/Footer.test.tsx`
- Modify: `pkg/web/dashboard/src/components/footer/CODEMANIFEST`

**Interfaces:**
- Consumes: `useThemeMode()` from Task 6, `.icon-btn` CSS class from `base/layout.css` (Task 2).
- No change to `FooterProps` — the button is self-contained, no new props threaded from `App.tsx`.

- [ ] **Step 1: Write the failing test**

In `pkg/web/dashboard/src/components/footer/Footer.test.tsx`, add `fireEvent` to the import and a `beforeEach`, then a new test:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { beforeEach, describe, expect, test } from 'vitest'
import type { Stage } from '../../types'
import { Footer } from './Footer'

describe('Footer', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('data-theme')
    window.localStorage.clear()
  })

  test('renders done/total progress and formatted elapsed', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 's2', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(<Footer stages={stages} startedAt="2026-07-10T10:00:00Z" elapsedMs={65000} />)

    expect(screen.getByText('1 / 2')).toBeInTheDocument()
    expect(screen.getByText('01:05')).toBeInTheDocument()
  })

  test('shows placeholder elapsed when startedAt is empty', () => {
    render(<Footer stages={[]} startedAt="" elapsedMs={0} />)

    // При пустом startedAt оба поля (started-at и elapsed) показывают плейсхолдер '--' —
    // проверяем именно elapsed по его id.
    expect(document.getElementById('elapsed')).toHaveTextContent('--')
  })

  test('toggles theme mode on click', () => {
    document.documentElement.dataset.theme = 'dark'
    render(<Footer stages={[]} startedAt="" elapsedMs={0} />)

    const button = screen.getByRole('button', { name: /light mode/i })
    fireEvent.click(button)

    expect(document.documentElement.dataset.theme).toBe('light')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard && npx vitest run src/components/footer`
Expected: FAIL — no button with accessible name matching `/light mode/i` found.

- [ ] **Step 3: Add the toggle button to Footer**

Replace the full contents of `pkg/web/dashboard/src/components/footer/Footer.tsx` with:

```tsx
import type { ReactElement } from 'react'
import type { Stage } from '../../types'
import { useThemeMode } from '../../hooks/use-theme-mode'

type FooterProps = {
  stages: Stage[]
  startedAt: string
  elapsedMs: number
}

// Футер: прогресс (доля done), время старта и elapsed. elapsed приходит уже готовым
// от useElapsed (тик каждую секунду). id progress-fill/progress-text/started-at/elapsed
// сохранены для тем. Плюс переключатель dark/light — не зависит от активного скина.
export function Footer({ stages, startedAt, elapsedMs }: FooterProps): ReactElement {
  const total = stages.length
  const done = stages.filter((stage) => stage.status === 'done').length
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  const { mode, toggle } = useThemeMode()

  return (
    <footer id="footer">
      <div className="footer-item">
        <span className="footer-label">Progress:</span>
        <div className="progress-bar">
          <div id="progress-fill" className="progress-fill" style={{ width: `${pct}%` }} />
        </div>
        <span id="progress-text">{`${done} / ${total}`}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Started:</span>
        <span id="started-at">{formatClock(startedAt)}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Elapsed:</span>
        <span id="elapsed">{startedAt === '' ? '--' : formatDuration(elapsedMs)}</span>
      </div>
      <div className="footer-item">
        <button
          type="button"
          className="icon-btn"
          onClick={toggle}
          aria-label={mode === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
        >
          {mode === 'dark' ? '☾' : '☀'}
        </button>
      </div>
    </footer>
  )
}

function formatClock(startedAt: string): string {
  if (startedAt === '') return '--'

  const parsed = new Date(startedAt)
  if (Number.isNaN(parsed.getTime())) return '--'

  return formatTime(parsed)
}

function formatTime(date: Date): string {
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map(pad).join(':')
}

function formatDuration(ms: number): string {
  const sec = Math.floor(ms / 1000)
  const hours = Math.floor(sec / 3600)
  const minutes = Math.floor((sec % 3600) / 60)
  const seconds = sec % 60

  if (hours > 0) return `${hours}:${pad(minutes)}:${pad(seconds)}`

  return `${pad(minutes)}:${pad(seconds)}`
}

function pad(value: number): string {
  return value < 10 ? `0${value}` : String(value)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `npx vitest run src/components/footer`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Update the CODEMANIFEST requirements**

In `pkg/web/dashboard/src/components/footer/CODEMANIFEST`, in the `Requirements` list under the `Footer(...)` entry, add one line:

```
    Requirements:
    - Прогресс — доля стадий со статусом done от общего числа stages
    - startedAt и elapsed отображаются в человекочитаемом формате; elapsed пересчитывается вызывающим хуком
      секундомера каждую секунду (см. useElapsed)
    - Футер содержит кнопку-переключатель dark/light (useThemeMode), не связанную с props компонента
```

- [ ] **Step 6: Run typecheck**

Run: `npm run typecheck`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/src/components/footer
git commit -m "feat(dashboard): кнопка переключения dark/light в футере"
```

---

## Task 8: HTML — dark/light bootstrap script + skin href

**Files:**
- Modify: `pkg/web/dashboard/index.dev.html`

**Interfaces:**
- Produces: `document.documentElement.dataset.theme` set to `"dark"` or `"light"` synchronously before the stylesheet `<link>` is requested; default stylesheet href pointing at `skins/novacorps/index.css`.

- [ ] **Step 1: Update `index.dev.html`**

Replace the full contents of `pkg/web/dashboard/index.dev.html` with:

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>afm Dashboard</title>
    <link rel="icon" type="image/svg+xml" href="/favicon.svg">
    <script>
      (function () {
        var stored = null;
        try { stored = localStorage.getItem('afm-mode'); } catch (e) {}
        var mode = stored === 'dark' || stored === 'light'
          ? stored
          : (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
        document.documentElement.dataset.theme = mode;
      })();
    </script>
    <link rel="stylesheet" href="/skins/novacorps/index.css">
</head>
<body class="theme-novacorps">
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
</body>
</html>
```

(The bootstrap `<script>` runs before the stylesheet `<link>` is parsed — classic scripts execute synchronously as encountered, so `data-theme` is on `<html>` before the browser requests/applies the CSS. `try/catch` around `localStorage.getItem` guards against browsers that throw on access in some private-browsing configurations.)

- [ ] **Step 2: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/index.dev.html
git commit -m "feat(dashboard): бутстрап-скрипт data-theme без FOUC + ссылка на novacorps-скин"
```

---

## Task 9: Build — regenerate dashboard artifacts, remove old theme files

**Files:**
- Delete: `pkg/web/dashboard/public/style.css`
- Delete: `pkg/web/dashboard/public/style-goga.css`
- Delete: `pkg/web/dashboard/style.css` (stale build-output copy)
- Delete: `pkg/web/dashboard/style-goga.css` (stale build-output copy)
- Regenerate (via `npm run build`): `pkg/web/dashboard/index.html`, `pkg/web/dashboard/skins/**`, `pkg/web/dashboard/assets/*`

**Interfaces:**
- Produces: `pkg/web/dashboard/skins/` (root-level build-output copy of `public/skins/`) that `pkg/web/embed.go` will embed in Task 10; `pkg/web/dashboard/index.html` containing the bootstrap script and `href="./skins/novacorps/index.css"`.

- [ ] **Step 1: Run frontend verification before touching build output**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard
npm run typecheck
npm test -- --run
```
Expected: PASS (confirms Tasks 5-8 didn't break anything before we regenerate the build).

- [ ] **Step 2: Remove the old monolithic theme files (source and stale build copies)**

```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard
rm public/style.css public/style-goga.css
rm style.css style-goga.css
```

- [ ] **Step 3: Rebuild**

```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard
npm run build
```
Expected: exits 0. This runs `restore-index` (copies `index.dev.html` → `index.html`), `clean:assets` (removes old `assets/`), then `vite build` (rewrites `index.html` with relative `./` paths and a fresh bundle hash, copies `public/*` — including the new `skins/` tree — into the directory root).

- [ ] **Step 4: Verify the build output**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard
ls skins/base skins/novacorps skins/goga
grep -o 'href="[^"]*style[^"]*"' index.html; grep -o 'href="[^"]*index.css"' index.html
grep -o 'class="theme-[a-z]*"' index.html
grep -c "data-theme" index.html
test -f style.css && echo "REGRESSION: style.css still present" || echo "OK: no stale style.css"
```
Expected:
- `skins/base` lists the 9 partials from Task 2; `skins/novacorps` and `skins/goga` each list `index.css`.
- The `href="...index.css"` grep prints `href="./skins/novacorps/index.css"`; the `style` grep prints nothing (no more `style.css`/`style-goga.css` references).
- `class="theme-novacorps"` present.
- `data-theme` count ≥ 1 (from the bootstrap script).
- `OK: no stale style.css` printed.

- [ ] **Step 5: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add -A pkg/web/dashboard
git status  # проверить: удалены только style.css/style-goga.css (оба места), остальное — новые/пересобранные файлы
git commit -m "build(dashboard): пересборка фронтенда — удалены старые style.css/style-goga.css, добавлены skins/"
```

---

## Task 10: Go — embed.go + server.go skin selection, `skin_dir` override, tests

**Files:**
- Modify: `pkg/web/embed.go`
- Modify: `pkg/server/server.go`
- Modify: `pkg/server/server_test.go`
- Modify: `cmd/afm/run.go:237-252`

**Interfaces:**
- Consumes: `pkg/web/dashboard/skins/**` (embedded by Task 9's build output), `config.Config.SkinDir` (Task 1).
- Produces: `server.Config.SkinDir string`; `server.New` selects `novacorps`/`goga`/an external `skin_dir` and rewrites `index.html`'s stylesheet href, body class, and favicon href accordingly; `/skins/custom/*` route serving `os.DirFS(SkinDir)` when active.

- [ ] **Step 1: Update the embed directive**

In `pkg/web/embed.go`, replace:

```go
//go:embed dashboard/index.html dashboard/favicon.svg dashboard/quarium-logo.png dashboard/style.css dashboard/style-goga.css dashboard/assets
var embedded embed.FS
```

with:

```go
//go:embed dashboard/index.html dashboard/favicon.svg dashboard/quarium-logo.png dashboard/skins dashboard/assets
var embedded embed.FS
```

- [ ] **Step 2: Confirm the package still builds (embed path now resolves against Task 9's output)**

Run: `cd /Users/alexander.kopichin/work/flowManager && go build ./pkg/web/...`
Expected: PASS (no "pattern dashboard/skins: no matching files found" error — Task 9 must have completed first).

- [ ] **Step 3: Write the failing server tests**

Replace the three existing theme tests in `pkg/server/server_test.go` (`TestServer_IndexDefaultTheme`, `TestServer_IndexGogaTheme`, `TestServer_ServesGogaStylesheet`, currently at lines 89-164) with:

```go
func TestServer_IndexDefaultTheme(t *testing.T) {
	srv := New(Config{})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/novacorps/index.css"`) {
		t.Error("default скин должен ссылаться на ./skins/novacorps/index.css")
	}
	if !strings.Contains(body, `class="theme-novacorps"`) {
		t.Error("default скин должен ставить class theme-novacorps")
	}
	if !strings.Contains(body, `href="./favicon.svg"`) {
		t.Error("default скин должен использовать общий favicon.svg")
	}
	if !strings.Contains(body, `id="root"`) {
		t.Error("index должен содержать точку монтирования React (#root)")
	}
	if strings.Contains(body, "skins/goga") || strings.Contains(body, "skins/custom") {
		t.Error("default скин не должен ссылаться на goga/custom")
	}
}

func TestServer_IndexGogaTheme(t *testing.T) {
	srv := New(Config{Theme: themeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="theme-goga"`) {
		t.Error("goga скин должен ставить class theme-goga")
	}
	if strings.Contains(body, "theme-novacorps") {
		t.Error("goga скин не должен содержать theme-novacorps")
	}
	if !strings.Contains(body, `href="./skins/goga/index.css"`) {
		t.Error("goga скин должен ссылаться на ./skins/goga/index.css")
	}
	if strings.Contains(body, `href="./skins/novacorps/index.css"`) {
		t.Error("goga скин не должен ссылаться на дефолтный novacorps")
	}
}

func TestServer_ServesGogaStylesheet(t *testing.T) {
	srv := New(Config{Theme: themeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/skins/goga/index.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /skins/goga/index.css: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--mint") {
		t.Error("skins/goga/index.css должен определять CSS-токен --mint")
	}
}

func TestServer_ServesBaseSkinPartial(t *testing.T) {
	srv := New(Config{})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/skins/base/header.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /skins/base/header.css: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), ".logo") {
		t.Error("skins/base/header.css должен содержать структурные правила .logo")
	}
}

func TestServer_SkinDirOverridesTheme(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#123456;}`), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{Theme: themeGoga, SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/custom/index.css"`) {
		t.Error("skin_dir должен побеждать theme: ожидался href на ./skins/custom/index.css")
	}
	if !strings.Contains(body, `class="theme-custom"`) {
		t.Error("skin_dir должен ставить class theme-custom")
	}

	cssReq := httptest.NewRequest("GET", "/skins/custom/index.css", nil)
	cssW := httptest.NewRecorder()
	handler.ServeHTTP(cssW, cssReq)
	if cssW.Code != http.StatusOK {
		t.Fatalf("GET /skins/custom/index.css: got %d, want 200", cssW.Code)
	}
	if !strings.Contains(cssW.Body.String(), "#123456") {
		t.Error("GET /skins/custom/index.css должен отдавать содержимое из SkinDir, а не embed")
	}
}

func TestServer_SkinDirMissingIndexFallsBack(t *testing.T) {
	dir := t.TempDir() // пустая директория, index.css нет

	srv := New(Config{Theme: themeGoga, SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `href="./skins/goga/index.css"`) {
		t.Error("без index.css в skin_dir должен быть fallback на встроенный скин из theme")
	}
	if !strings.Contains(body, `class="theme-goga"`) {
		t.Error("fallback должен ставить class theme-goga, не theme-custom")
	}

	cssReq := httptest.NewRequest("GET", "/skins/custom/index.css", nil)
	cssW := httptest.NewRecorder()
	handler.ServeHTTP(cssW, cssReq)
	if cssW.Code != http.StatusNotFound {
		t.Errorf("/skins/custom/ не должен монтироваться при невалидном skin_dir: got %d, want 404", cssW.Code)
	}
}

func TestServer_SkinDirCustomFavicon(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg></svg>"), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `href="./skins/custom/favicon.svg"`) {
		t.Error("skin_dir со своим favicon.svg должен переопределять ссылку на иконку")
	}
}

func TestServer_SkinDirWithoutFaviconUsesDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `href="./favicon.svg"`) {
		t.Error("skin_dir без favicon.svg должен оставлять дефолтную иконку")
	}
}
```

Add `"os"` and `"path/filepath"` to the import block at the top of `pkg/server/server_test.go` (currently `net/http`, `net/http/httptest`, `strings`, `testing`).

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/server/... -run TestServer -v`
Expected: FAIL — compile error (`Config` has no field `SkinDir`) and/or assertion failures against the still-old `href="./style.css"` behavior.

- [ ] **Step 5: Implement the skin-selection logic in `server.go`**

Replace the full contents of `pkg/server/server.go` with:

```go
package server

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

// Имена скинов. themeGoga/themeNovacorps соответствуют pkg/config.Config.EffectiveTheme();
// themeCustom — активный skin_dir.
const (
	themeGoga      = "goga"
	themeNovacorps = "novacorps"
	themeCustom    = "custom"
)

// Имена файлов внутри директории скина (встроенной или skin_dir).
const (
	skinIndexFile   = "index.css"
	skinFaviconFile = "favicon.svg"
)

// defaultFaviconHref — общий дефолтный favicon, используется, когда у активного
// скина нет своего favicon.svg.
const defaultFaviconHref = "./favicon.svg"

// customSkinRoute — префикс маршрута, отдающего skin_dir с диска.
const customSkinRoute = "/skins/custom/"

// skinHrefFor возвращает относительный href на стиль встроенного скина по имени.
func skinHrefFor(name string) string {
	return "./skins/" + name + "/" + skinIndexFile
}

// skinFaviconHrefFor возвращает относительный href на favicon скина по имени.
func skinFaviconHrefFor(name string) string {
	return "./skins/" + name + "/" + skinFaviconFile
}

// Server is the HTTP server for the dashboard and API.
type Server struct {
	runDir           string
	stageInteractive map[string]bool // id стадии → interactive (статический конфиг флоу)
	store            *state.Store
	uiBus            *orchestrator.UIBus
	approveFn        func(ctx context.Context, stageID string) error
	reviseFn         func(ctx context.Context, stageID, feedback string) error
	retryFn          func(ctx context.Context, stageID string) error
	dialogAnswerFn   func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn   func(stageID string) error
	theme            string       // "goga" или "" (default novacorps)
	indexBytes       []byte       // предподготовленный index.html (с заменами скина/favicon)
	fileServer       http.Handler // отдаёт встроенную статику (skins/, assets, ...)
	customSkinServer http.Handler // отдаёт /skins/custom/* с диска; nil, если skin_dir не активен
	httpSrv          *http.Server
}

// Config holds server settings.
type Config struct {
	Port             int
	RunDir           string
	StageInteractive map[string]bool
	Store            *state.Store
	UIBus            *orchestrator.UIBus
	ApproveFn        func(ctx context.Context, stageID string) error
	ReviseFn         func(ctx context.Context, stageID, feedback string) error
	RetryFn          func(ctx context.Context, stageID string) error
	DialogAnswerFn   func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn   func(stageID string) error
	Theme            string
	SkinDir          string
}

// New creates a Server.
func New(cfg Config) *Server {
	s := &Server{
		runDir:           cfg.RunDir,
		stageInteractive: cfg.StageInteractive,
		store:            cfg.Store,
		uiBus:            cfg.UIBus,
		approveFn:        cfg.ApproveFn,
		reviseFn:         cfg.ReviseFn,
		retryFn:          cfg.RetryFn,
		dialogAnswerFn:   cfg.DialogAnswerFn,
		dialogCancelFn:   cfg.DialogCancelFn,
		theme:            cfg.Theme,
		fileServer:       http.FileServer(http.FS(web.FS)),
	}

	skinName := s.builtinSkinName()
	skinHref := skinHrefFor(skinName)
	faviconHref := s.embeddedFaviconHref(skinName)

	// skin_dir полностью подменяет активный скин (аналогично prompts_dir):
	// нужен index.css внутри директории, иначе — предупреждение и fallback на
	// встроенный скин (theme/novacorps). Дашборд не критичен для работы флоу
	// (в отличие от промптов), поэтому сервер не падает при плохом skin_dir.
	if cfg.SkinDir != "" {
		if _, err := os.Stat(filepath.Join(cfg.SkinDir, skinIndexFile)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skin_dir %q has no %s, using skin %q\n", cfg.SkinDir, skinIndexFile, skinName)
		} else {
			s.customSkinServer = http.FileServer(http.FS(os.DirFS(cfg.SkinDir)))
			skinName = themeCustom
			skinHref = skinHrefFor(themeCustom)
			faviconHref = defaultFaviconHref
			if _, err := os.Stat(filepath.Join(cfg.SkinDir, skinFaviconFile)); err == nil {
				faviconHref = skinFaviconHrefFor(themeCustom)
			}
		}
	}

	// Предподготовка index.html: подменяем ссылку на CSS, класс body и (если у
	// активного скина есть свой favicon) ссылку на иконку. Замены строковые —
	// сами файлы скина отдаст fileServer/customSkinServer позже. Ошибка чтения
	// embed невозможна на практике, но обрабатываем: при nil serveIndex
	// делегирует на fileServer.
	indexBytes, err := fs.ReadFile(web.FS, "index.html")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read embedded index.html: %v\n", err)
	} else {
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+skinHrefFor(themeNovacorps)+`"`), []byte(`href="`+skinHref+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`class="theme-`+themeNovacorps+`"`), []byte(`class="theme-`+skinName+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+defaultFaviconHref+`"`), []byte(`href="`+faviconHref+`"`))
		s.indexBytes = indexBytes
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
	if s.customSkinServer != nil {
		mux.Handle(customSkinRoute, http.StripPrefix(customSkinRoute, s.customSkinServer))
	}
	mux.HandleFunc("/", s.serveStatic)

	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// builtinSkinName нормализует Theme до имени встроенного скина: "goga" или
// default "novacorps".
func (s *Server) builtinSkinName() string {
	if s.theme == themeGoga {
		return themeGoga
	}
	return themeNovacorps
}

// embeddedFaviconHref возвращает href на favicon встроенного скина, если он
// его переопределяет (skins/<name>/favicon.svg существует в embed), иначе
// общий дефолт.
func (s *Server) embeddedFaviconHref(skinName string) string {
	if _, err := fs.Stat(web.FS, "skins/"+skinName+"/"+skinFaviconFile); err == nil {
		return skinFaviconHrefFor(skinName)
	}
	return defaultFaviconHref
}

// serveStatic отдаёт index.html (с подставленным скином) для "/" и "/index.html",
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

func (s *Server) routeStages(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/plan"):
		s.handlePlan(w, r)
	case strings.HasSuffix(path, "/log"):
		s.handleLog(w, r)
	case strings.HasSuffix(path, "/supervisor"):
		s.handleSupervisor(w, r)
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
		s.handleApprove(w, r)
	case strings.HasSuffix(path, "/revise") && r.Method == http.MethodPost:
		s.handleRevise(w, r)
	case strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.handleRetry(w, r)
	case strings.HasSuffix(path, "/dialog") && r.Method == http.MethodGet:
		s.handleDialogGet(w, r)
	case strings.HasSuffix(path, "/dialog/answer") && r.Method == http.MethodPost:
		s.handleDialogAnswer(w, r)
	case strings.HasSuffix(path, "/dialog/cancel") && r.Method == http.MethodPost:
		s.handleDialogCancel(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Handler returns the HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.httpSrv.Handler
}

// Start starts the HTTP server. Returns actual address.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	go func() { _ = s.httpSrv.Serve(ln) }()
	return ln.Addr().String(), nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
```

- [ ] **Step 6: Update the stale dashboard asset inventory doc**

`pkg/web/dashboard/.usages/dashboard_assets.md` documents the embedded asset layout and still lists `style.css`/`style-goga.css` (now deleted in Task 9) plus an inaccurate `//go:embed dashboard/*` snippet that doesn't even match today's real directive. Fix only the parts this plan touches — leave the pre-existing `app.js`/Vite-rewrite narrative alone (unrelated drift, out of scope here).

In `pkg/web/dashboard/.usages/dashboard_assets.md`, replace:

```
├── style.css           # dashboard styles (default novacorps theme)
├── style-goga.css      # goga theme styles (linked server-side when theme=goga, see pkg/server)
├── quarium-logo.png    # quarium logo in the goga theme header
```

with:

```
├── skins/              # skin system: skins/base/*.css (shared structural partials, one per
│                       # dashboard component) + skins/<name>/index.css (novacorps/goga —
│                       # tokens for data-theme=dark/light + decor, @import base) + optional
│                       # skins/<name>/favicon.svg override. Server picks the active skin
│                       # (theme:/skin_dir: config, skin_dir fully overrides) and rewrites
│                       # index.html's stylesheet href/body class/favicon href accordingly
│                       # (see pkg/server/server.go)
├── quarium-logo.png    # unused legacy asset (kept embedded, not referenced by any component)
```

And replace the embed.go snippet later in the same file:

```go
//go:embed dashboard/*
var embedded embed.FS

var FS, _ = fs.Sub(embedded, "dashboard")
```

with (matching the real, explicit-file-list directive, not a `dashboard/*` wildcard):

```go
//go:embed dashboard/index.html dashboard/favicon.svg dashboard/quarium-logo.png dashboard/skins dashboard/assets
var embedded embed.FS

var FS, _ = fs.Sub(embedded, "dashboard")
```

- [ ] **Step 7: Wire `SkinDir` through `cmd/afm/run.go`**

In `cmd/afm/run.go`, in the `server.Config{...}` literal (around line 237-252), add `SkinDir` right after `Theme`:

```go
				srv := server.New(server.Config{
					Port:             cfg.Server.GetPort(),
					RunDir:           runDir,
					StageInteractive: stageInteractive,
					Store:            store,
					Theme:            cfg.EffectiveTheme(),
					SkinDir:          cfg.SkinDir,
					UIBus:            orch.UIBus(),
					ApproveFn:        orch.Approve,
					ReviseFn:         orch.Revise,
					RetryFn:          orch.Retry,
```

(everything else in that literal is unchanged)

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/server/... -run TestServer -v`
Expected: PASS (all 9 tests: the 3 rewritten + `TestServer_ServesBaseSkinPartial`, `TestServer_SkinDirOverridesTheme`, `TestServer_SkinDirMissingIndexFallsBack`, `TestServer_SkinDirCustomFavicon`, `TestServer_SkinDirWithoutFaviconUsesDefault`, plus the two other now-in-file theme tests already counted).

- [ ] **Step 9: Run the full Go test suite and build**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager
go build ./...
go vet ./...
go test ./...
```
Expected: all PASS, no regressions in `pkg/config`, `pkg/server`, `cmd/afm`, or elsewhere.

- [ ] **Step 10: Run the linter**

Run: `cd /Users/alexander.kopichin/work/flowManager && golangci-lint run ./pkg/server/... ./pkg/web/... ./pkg/config/... ./cmd/afm/...`
Expected: no findings. If `goconst` or another linter flags something, fix it (e.g. extract any remaining duplicated literal into a constant) and re-run.

- [ ] **Step 11: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/embed.go pkg/server/server.go pkg/server/server_test.go cmd/afm/run.go
git commit -m "feat(server): выбор скина (theme/skin_dir) и раздача /skins/*, включая внешний override"
```

**STOP — before moving to Task 11, do Task 10b below.** `server.go` as written
above does not yet implement per-skin `<title>` override or favicon files
other than `.svg` (goga's actual favicon is `quarium-logo.png`, a raster
image, and it also needs `<title>QArium</title>`). Task 10b (in the Addendum)
extends `New()` with `title.txt`/extension-aware favicon logic before Task 11
runs.

---

## Task 11: Final integration verification

**Files:** none (verification only)

- [ ] **Step 1: Full backend suite**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager
go build ./...
go vet ./...
golangci-lint run
go test ./...
```
Expected: all clean/PASS.

- [ ] **Step 2: Full frontend suite**

Run:
```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard
npm run typecheck
npm test -- --run
```
Expected: all PASS.

- [ ] **Step 3: Manual browser QA — built-in skins × both modes**

Build the `afm` binary and run it against a scratch flow (or reuse an existing `.afm` run dir), then in a browser:

```bash
cd /Users/alexander.kopichin/work/flowManager
go build -o /tmp/afm-skins-check ./cmd/afm
/tmp/afm-skins-check --dir <some-existing-.afm-parent-dir> serve   # or whatever subcommand starts the dashboard server on this branch
```

For each of the 4 combinations (novacorps/goga × dark/light):
1. Load the dashboard (set `theme: goga` in `.afm/config.yaml` for the goga runs, remove/empty for novacorps).
2. Confirm the layout, panels, stages list, dialog, log, and event feed all render without console errors and without visibly broken (illegible or solid-black-box) elements.
3. Click the footer's dark/light toggle button (☾/☀ icon) and confirm the whole page switches palette immediately, panels/textareas do not turn into unstyled dark boxes, and reloading the page keeps the last-picked mode (localStorage persistence).
4. Confirm goga still shows the "goga" wordmark (not "afm") and novacorps still shows its neon scanlines/ray-sweep decoration, in both modes.

- [ ] **Step 4: Manual smoke test — external `skin_dir`**

```bash
mkdir -p /tmp/my-custom-skin
cat > /tmp/my-custom-skin/index.css <<'EOF'
@import url("http://localhost:9876/skins/base/reset.css");
:root[data-theme="dark"] { --bg:#000; --bg-elev:#111; --panel-bg:#111; --ink:#0f0; --ink-hi:#0f0; --ink-dim:#080; --mint:#0f0; --mint-soft:rgba(0,255,0,0.2); --grid:rgba(0,255,0,0.05); --amber:#ff0; --violet:#f0f; --coral:#f00; --qa-answer:#0f0; --dialog-option-text:#0f0; --btn-cancel-dialog-text:#f00; --supervisor-accent:#f0f; --c-pending:var(--ink-dim); --c-planning:var(--mint); --c-awaiting:var(--amber); --c-revising:var(--amber); --c-ready:var(--mint); --c-running:var(--amber); --c-done:var(--mint); --c-failed:var(--coral); --c-retrying:var(--amber); --c-awaiting-user:var(--violet); --text:var(--ink); --text-dim:var(--ink-dim); }
:root[data-theme="light"] { --bg:#fff; --bg-elev:#eee; --panel-bg:#eee; --ink:#000; --ink-hi:#000; --ink-dim:#333; --mint:#080; --mint-soft:rgba(0,128,0,0.2); --grid:rgba(0,128,0,0.05); --amber:#a60; --violet:#808; --coral:#a00; --qa-answer:#080; --dialog-option-text:#808; --btn-cancel-dialog-text:#a00; --supervisor-accent:#808; --c-pending:var(--ink-dim); --c-planning:var(--mint); --c-awaiting:var(--amber); --c-revising:var(--amber); --c-ready:var(--mint); --c-running:var(--amber); --c-done:var(--mint); --c-failed:var(--coral); --c-retrying:var(--amber); --c-awaiting-user:var(--violet); --text:var(--ink); --text-dim:var(--ink-dim); }
EOF
```

Set `skin_dir: /tmp/my-custom-skin` in the test flow's `.afm/config.yaml`, restart the dashboard, and confirm:
1. The page loads in a stark black/green (dark) or white/dark-green (light) palette, not novacorps/goga colors.
2. `curl -s http://localhost:9876/ | grep -o 'class="theme-[a-z]*"'` prints `class="theme-custom"`.
3. Removing `index.css` from `/tmp/my-custom-skin` and restarting falls back to the `theme:` value (or novacorps) with a `warning: skin_dir ... has no index.css` line on stderr, and the dashboard still loads correctly.

- [ ] **Step 5: Clean up scratch artifacts**

```bash
rm -rf /tmp/my-custom-skin /tmp/afm-skins-check
```

- [ ] **Step 6: Final full-repo commit check**

```bash
cd /Users/alexander.kopichin/work/flowManager
git status
git log --oneline -12
```
Expected: working tree clean (everything from Tasks 1-10 committed), 10-11 new commits since the plan started, all in Russian, no `Co-Authored-By` trailers.

---

## Addendum (2026-07-20, after review): quarium logo, QArium wordmark, title/favicon

After this plan was first written, a separate set of historical commits was
cherry-picked from `theme-fix` onto this branch, giving the goga skin an
actual logo image (`quarium-logo.png`), a two-tone "QArium" wordmark, and
`<title>`/favicon overrides — none of which this plan's Task 4 (goga
skin) or Task 10 (server.go) accounted for. This addendum supersedes the
affected parts. Run Task 4b right after Task 4 (before Task 5), and Task 10b
right after Task 10 (before Task 11) — see the "STOP" notes inline above.

### Task 4b: goga skin — quarium logo, QArium wordmark, accent-wash overrides

**Files:**
- Modify: `pkg/web/dashboard/public/skins/goga/index.css` (replaces the override section written in Task 4 Step 1)
- Create: `pkg/web/dashboard/public/skins/goga/quarium-logo.png` (copy of the existing `pkg/web/dashboard/quarium-logo.png`)
- Create: `pkg/web/dashboard/public/skins/goga/favicon.png` (same file, duplicated under the favicon convention name — see Task 10b)
- Create: `pkg/web/dashboard/public/skins/goga/title.txt`

**Interfaces:**
- Consumes: `quarium-logo.png` bytes (existing repo asset, unchanged).
- Produces: the on-disk convention Task 10b reads — `title.txt` (first line = `<title>` text) and `favicon.png`/`favicon.svg` (mutually — whichever exists, `.svg` checked first) inside a skin directory.

- [ ] **Step 1: Copy the quarium logo asset into the goga skin, twice (logo + favicon)**

```bash
cd /Users/alexander.kopichin/work/flowManager
cp pkg/web/dashboard/quarium-logo.png pkg/web/dashboard/public/skins/goga/quarium-logo.png
cp pkg/web/dashboard/quarium-logo.png pkg/web/dashboard/public/skins/goga/favicon.png
```

(Two copies, not a symlink: the skin directory is served standalone by the
embedded `web.FS`/`os.DirFS`, and keeping it a plain flat copy avoids any
symlink-resolution edge cases in `embed.FS`, which does not support symlinks.)

- [ ] **Step 2: Create `title.txt`**

```bash
printf 'QArium' > pkg/web/dashboard/public/skins/goga/title.txt
```

- [ ] **Step 3: Replace the override section of `goga/index.css`**

Replace everything in `pkg/web/dashboard/public/skins/goga/index.css` from
the `/* === goga-специфика поверх base === */` comment (written in Task 4
Step 1) to the end of the file with:

```css
/* === goga-специфика поверх base ===
   Base-партиалы импортированы выше — эти правила автоматически побеждают
   одноимённые правила base (равная специфичность, более поздний порядок в
   каскаде), без .theme-goga-скоупинга старой архитектуры. */

/* Логотип-слово: goga показывает двухцветный wordmark «QArium» (как на
   qarium.ru) — «QA» цветом --mint (teal), «rium» цветом --ink (белый),
   системным sans-serif. Текст h1 («afm») скрыт (font-size:0); слово
   собирается из ::before/::after, чтобы не делать React-компонент
   тема-зависимым. */
#header h1 {
  font-size: 0;
  letter-spacing: 0;
  text-transform: none;
}
#header h1::before {
  content: 'QA';
  font-size: 13px;
  font-weight: 600;
  font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  color: var(--mint);
}
#header h1::after {
  content: 'rium';
  font-size: 13px;
  font-weight: 600;
  font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  color: var(--ink);
}

/* Логотип: заменяем анимированный SVG-шестигранник на quarium-картинку. */
.logo > span {
  display: none;
}
.logo {
  background-image: url('./quarium-logo.png');
  background-size: contain;
  background-repeat: no-repeat;
  background-position: center;
}

/* Чистый фон goga: без novacorps-клетки (background-image на #main). */
#main {
  background-image: none;
  background: var(--bg-elev);
}

/* Хедер и футер — тот же --bg-elev что и панели (в novacorps они темнее через --bg). */
#header,
#footer {
  background: var(--bg-elev);
}

/* Текст PROGRESS/STARTED/ELAPSED — mint, как «QA» в хедере. */
#progress-text,
#started-at,
#elapsed {
  color: var(--mint);
}

/* === Перекраска хардкодных novacorps low-alpha заливок под goga-палитру.
   Эти оттенки не токенизированы в base/*.css (сознательное решение дизайна —
   см. спек, "Что НЕ делаем"), поэтому goga переопределяет каждый селектор
   явно. Nova Corps: rgba(111,212,204,X) = mint, rgba(229,212,66,X) = amber.
   Goga:            rgba(32,212,191,X)  = mint, rgba(56,130,246,X)  = amber. */

::selection { background: rgba(56, 130, 246, 0.35); }

/* base/layout.css: footer progress bar, jump-latest, icon-btn, attention */
.progress-bar { background: rgba(32, 212, 191, 0.12); }
.progress-fill { background: linear-gradient(90deg, rgba(56, 130, 246, 0.15), var(--amber)); }
.jump-latest { background: rgba(32, 212, 191, 0.15); }
.icon-btn { border-color: rgba(32, 212, 191, 0.4); }
@keyframes attentionPulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(56, 130, 246, 0); }
  50%      { box-shadow: 0 0 12px 2px rgba(56, 130, 246, 0.65); }
}

/* base/stages-list.css */
.stage-item { border-bottom-color: rgba(32, 212, 191, 0.06); }
.stage-item:hover { background: rgba(32, 212, 191, 0.06); }
.stage-item.active {
  background: linear-gradient(90deg, rgba(56, 130, 246, 0.06), transparent);
}
.stage-item.active::after {
  background: linear-gradient(90deg, transparent, rgba(56, 130, 246, 0.7), transparent);
}

/* base/plan-panel.css */
.plan-line:hover { background: rgba(32, 212, 191, 0.06); }
.plan-line.has-comment { background: rgba(56, 130, 246, 0.07); }
.plan-line.has-comment:hover { background: rgba(56, 130, 246, 0.12); }
.plan-section-wrapper { background: rgba(32, 212, 191, 0.04); }
.plan-section-wrapper.plan-section-assumptions { background: rgba(56, 130, 246, 0.05); }
.plan-section-wrapper.plan-section-criteria { background: rgba(32, 212, 191, 0.05); }

/* base/dialog-channel.css — .dialog-pending .dialog-custom's *background* is
   already goga-correct via --panel-bg: var(--bg-elev) (set in the token
   block above), only its hardcoded border-color still needs recoloring. */
.dialog-history .qa { border-left-color: rgba(32, 212, 191, 0.2); }
.phase-divider { border-bottom-color: rgba(32, 212, 191, 0.12); }
.dialog-pending .dialog-custom { border-color: rgba(32, 212, 191, 0.2); }

/* base/event-feed.css */
.feed-entry { border-bottom-color: rgba(32, 212, 191, 0.05); }
.feed-entry:hover { background: rgba(32, 212, 191, 0.05); }
```

(The historical `style-goga.css` also recolored `.usage-toggle:hover` and
`.usage-metric:hover` — both belonged to the "Consumption panel" CSS block
that Task 2 deliberately deleted as dead code (no React component renders
it). Omitted here for the same reason, not an oversight.)

- [ ] **Step 4: Sanity-check**

```bash
cd /Users/alexander.kopichin/work/flowManager/pkg/web/dashboard/public/skins/goga
ls quarium-logo.png favicon.png title.txt
cat title.txt
node -e "require('fs').readFileSync('index.css','utf8').split('{').length === require('fs').readFileSync('index.css','utf8').split('}').length || (()=>{throw new Error('brace mismatch')})()"
```
Expected: all three files exist, `title.txt` prints `QArium` (no trailing
newline issue — `printf` was used, not `echo`), no brace-mismatch error.

- [ ] **Step 5: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/web/dashboard/public/skins/goga
git commit -m "feat(dashboard): goga — quarium-лого, QArium wordmark, title/favicon, перекраска акцентных заливок"
```

### Task 10b: server.go — per-skin `<title>` and non-svg favicon

**Files:**
- Modify: `pkg/server/server.go` (extends `New()` and `embeddedFaviconHref` from Task 10)
- Modify: `pkg/server/server_test.go`

**Interfaces:**
- Produces: `<title>` is replaced with the contents of `skins/<name>/title.txt` when present (embedded or custom skin_dir); favicon lookup now tries `favicon.svg` then `favicon.png`, with `<link type="...">` matching the found extension.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/server/server_test.go`:

```go
func TestServer_IndexGogaTheme_TitleAndFavicon(t *testing.T) {
	srv := New(Config{Theme: themeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `<title>QArium</title>`) {
		t.Error("goga скин должен подставлять <title>QArium</title> из title.txt")
	}
	if strings.Contains(body, `<title>afm Dashboard</title>`) {
		t.Error("goga скин не должен оставлять дефолтный <title>")
	}
	if !strings.Contains(body, `type="image/png" href="./skins/goga/favicon.png"`) {
		t.Error("goga скин должен использовать растровый favicon.png с type=image/png")
	}
}

func TestServer_ServesGogaLogoAsset(t *testing.T) {
	srv := New(Config{Theme: themeGoga})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/skins/goga/quarium-logo.png", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /skins/goga/quarium-logo.png: got %d, want 200", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("quarium-logo.png должен быть непустым")
	}
}

func TestServer_SkinDirCustomTitle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "title.txt"), []byte("My Skin"), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `<title>My Skin</title>`) {
		t.Error("skin_dir с title.txt должен подставлять свой <title>")
	}
}

func TestServer_SkinDirWithoutTitleKeepsDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.css"), []byte(`:root[data-theme="dark"]{--mint:#000;}`), 0644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{SkinDir: dir})
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `<title>afm Dashboard</title>`) {
		t.Error("skin_dir без title.txt должен оставлять дефолтный <title>")
	}
}
```

Note: `TestServer_SkinDirCustomFavicon` (written in Task 10 Step 3) already
covers a `.svg`-favicon custom skin; it stays as-is — the extension-aware
lookup below still finds `.svg` first, matching that test's expectation.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/server/... -run 'TestServer_IndexGogaTheme_TitleAndFavicon|TestServer_ServesGogaLogoAsset|TestServer_SkinDirCustomTitle|TestServer_SkinDirWithoutTitleKeepsDefault' -v`
Expected: FAIL (title/favicon.png behavior not implemented yet; goga's
`favicon.svg` lookup in `embeddedFaviconHref` finds nothing since goga now
only has `favicon.png`, so today it'd fall back to the plain default favicon
instead of `favicon.png`, and no `title.txt` handling exists at all).

- [ ] **Step 3: Extend `server.go`**

Add a new constant and a small favicon-lookup helper, and wire `title.txt`
into `New()`. In `pkg/server/server.go`, replace the constants block:

```go
// Имена файлов внутри директории скина (встроенной или skin_dir).
const (
	skinIndexFile   = "index.css"
	skinFaviconFile = "favicon.svg"
)
```

with:

```go
// Имена файлов внутри директории скина (встроенной или skin_dir).
const skinIndexFile = "index.css"

// skinFaviconCandidates — имена favicon-файлов скина в порядке поиска;
// расширение первого найденного определяет <link type="..."> (svg/png).
var skinFaviconCandidates = []string{"favicon.svg", "favicon.png"}

const skinTitleFile = "title.txt"

// faviconMIME возвращает MIME-тип favicon по имени файла (расширению).
func faviconMIME(name string) string {
	if strings.HasSuffix(name, ".png") {
		return "image/png"
	}
	return "image/svg+xml"
}
```

Replace `skinFaviconHrefFor`:

```go
func skinFaviconHrefFor(name string) string {
	return "./skins/" + name + "/" + skinFaviconFile
}
```

with a pair of extension-aware helpers:

```go
// findSkinFavicon ищет favicon скина через statFn (embed или диск) по
// skinFaviconCandidates. Возвращает относительный href, MIME и true, если
// нашёл; иначе false — вызывающий использует дефолт.
func findSkinFavicon(base string, statFn func(name string) bool) (href, mime string, found bool) {
	for _, name := range skinFaviconCandidates {
		if statFn(name) {
			return base + "/" + name, faviconMIME(name), true
		}
	}
	return "", "", false
}
```

Replace `embeddedFaviconHref`:

```go
func (s *Server) embeddedFaviconHref(skinName string) string {
	if _, err := fs.Stat(web.FS, "skins/"+skinName+"/"+skinFaviconFile); err == nil {
		return skinFaviconHrefFor(skinName)
	}
	return defaultFaviconHref
}
```

with:

```go
func (s *Server) embeddedFavicon(skinName string) (href, mime string, found bool) {
	return findSkinFavicon("./skins/"+skinName, func(name string) bool {
		_, err := fs.Stat(web.FS, "skins/"+skinName+"/"+name)
		return err == nil
	})
}
```

In `New()`, replace:

```go
	skinName := s.builtinSkinName()
	skinHref := skinHrefFor(skinName)
	faviconHref := s.embeddedFaviconHref(skinName)
```

with:

```go
	skinName := s.builtinSkinName()
	skinHref := skinHrefFor(skinName)
	faviconHref, faviconMimeType, faviconFound := s.embeddedFavicon(skinName)
	if !faviconFound {
		faviconHref, faviconMimeType = defaultFaviconHref, "image/svg+xml"
	}
	titleText := ""
	if data, err := fs.ReadFile(web.FS, "skins/"+skinName+"/"+skinTitleFile); err == nil {
		titleText = strings.TrimSpace(string(data))
	}
```

In the `cfg.SkinDir != ""` block, replace:

```go
		} else {
			s.customSkinServer = http.FileServer(http.FS(os.DirFS(cfg.SkinDir)))
			skinName = themeCustom
			skinHref = skinHrefFor(themeCustom)
			faviconHref = defaultFaviconHref
			if _, err := os.Stat(filepath.Join(cfg.SkinDir, skinFaviconFile)); err == nil {
				faviconHref = skinFaviconHrefFor(themeCustom)
			}
		}
```

with:

```go
		} else {
			s.customSkinServer = http.FileServer(http.FS(os.DirFS(cfg.SkinDir)))
			skinName = themeCustom
			skinHref = skinHrefFor(themeCustom)
			faviconHref, faviconMimeType = defaultFaviconHref, "image/svg+xml"
			if href, mime, ok := findSkinFavicon("./skins/custom", func(name string) bool {
				_, err := os.Stat(filepath.Join(cfg.SkinDir, name))
				return err == nil
			}); ok {
				faviconHref, faviconMimeType = href, mime
			}
			titleText = ""
			if data, err := os.ReadFile(filepath.Join(cfg.SkinDir, skinTitleFile)); err == nil {
				titleText = strings.TrimSpace(string(data))
			}
		}
```

Finally, in the `indexBytes` replacement block, replace:

```go
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+skinHrefFor(themeNovacorps)+`"`), []byte(`href="`+skinHref+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`class="theme-`+themeNovacorps+`"`), []byte(`class="theme-`+skinName+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+defaultFaviconHref+`"`), []byte(`href="`+faviconHref+`"`))
		s.indexBytes = indexBytes
```

with:

```go
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`href="`+skinHrefFor(themeNovacorps)+`"`), []byte(`href="`+skinHref+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`class="theme-`+themeNovacorps+`"`), []byte(`class="theme-`+skinName+`"`))
		indexBytes = bytes.ReplaceAll(indexBytes,
			[]byte(`type="image/svg+xml" href="`+defaultFaviconHref+`"`),
			[]byte(`type="`+faviconMimeType+`" href="`+faviconHref+`"`))
		if titleText != "" {
			indexBytes = bytes.ReplaceAll(indexBytes,
				[]byte(`<title>afm Dashboard</title>`), []byte(`<title>`+titleText+`</title>`))
		}
		s.indexBytes = indexBytes
```

(This changes the favicon `bytes.ReplaceAll` search string from
`href="./favicon.svg"` to the full `type="image/svg+xml" href="./favicon.svg"`
tag, since the `type` attribute must change together with `href` — a plain
`href`-only replace would leave a `type="image/svg+xml"` next to a `.png`
href for goga.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/server/... -run 'TestServer' -v`
Expected: PASS — all tests from Task 10 and this addendum, including the full
suite (no regressions in `TestServer_IndexDefaultTheme`, `TestServer_SkinDirCustomFavicon`, etc.)

- [ ] **Step 5: Full verification**

```bash
cd /Users/alexander.kopichin/work/flowManager
go build ./... && go vet ./... && go test ./... && golangci-lint run ./pkg/server/...
```
Expected: all clean.

- [ ] **Step 6: Commit**

```bash
cd /Users/alexander.kopichin/work/flowManager
git add pkg/server/server.go pkg/server/server_test.go
git commit -m "feat(server): per-skin <title> (title.txt) и favicon.png/.svg по расширению"
```

### Updated Task 11 manual QA step

When doing Task 11 Step 3 (manual browser QA), also confirm for goga
specifically: the browser tab title reads "QArium", the favicon is the
quarium logo image (not the default afm hex favicon), and the header shows
the quarium logo picture (not an SVG hex ring) next to a two-tone
"QA"+"rium" wordmark — this was visually confirmed once already against a
real Docker build during this addendum's own review, so Task 11 is
re-confirming, not discovering fresh.

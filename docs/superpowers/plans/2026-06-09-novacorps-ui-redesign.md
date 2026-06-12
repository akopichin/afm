# Nova Corps UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Перерисовать визуальный слой веб-дашборда flowManager в эстетике hi-tech holographic UI (Nova Corps / Territory Studio), сохранив всю функциональность.

**Architecture:** Полная замена `pkg/web/style.css`, точечные дополнения в `pkg/web/index.html` (декоративные SVG, контейнер для анимированного фона), минимальные правки в `pkg/web/app.js` (компактный формат времени в feed, опции диалога с горячими буквами через CSS counter). JS-логика не трогается. Все анимации — CSS keyframes, без JS. Поддержка `prefers-reduced-motion`.

**Tech Stack:** HTML, CSS (vanilla, vars, animations, grid), vanilla JS. Никаких сборщиков, фреймворков, npm. Бэкенд (Go) не трогается. Web-ассеты эмбедятся через `embed.FS` — после каждого изменения нужен `make build`.

**Spec:** `docs/superpowers/specs/2026-06-09-novacorps-ui-redesign-design.md`

---

## File Structure

| Файл | Действие | Размер изменения |
|---|---|---|
| `pkg/web/style.css` | заменить целиком | ~780 → ~900 строк |
| `pkg/web/index.html` | добавить SVG-лого, .ray div, ornament SVG | +~30 строк |
| `pkg/web/favicon.svg` | переписать | ~25 строк |
| `pkg/web/app.js` | 2 точечные правки (feed time, dialog options) | ~25 строк |

JS-классы и id, на которые завязан `app.js`, **сохраняются** — `#stages-list`, `#feed-content`, `.stage-item`, `.status-dot`, `[data-status]`, `.dialog-pending`, `.dialog-options`, `.dialog-custom`, `.btn-send`, `.btn-cancel-dialog`, `.plan-line`, `.line-num`, `.line-content`, `.line-comment-marker`, `.line-comment-form`, `.plan-section-wrapper`, `.feed-entry`, `.feed-time`, `.feed-stage-badge`, `.feed-msg`, `.qa`, `.phase-divider`, `.dialog-history`, `.status-badge`, и т.д.

## Iteration Workflow

Web-ассеты эмбедятся через Go. Для проверки изменений:

```bash
make build                                          # пересобрать
./bin/flowmanager run example-flow-interactive.yaml # запуск с интерактивным flow
# -> откроется http://localhost:9876
```

После любого изменения в `pkg/web/*` — рестартить.

Чтобы прервать запущенный flow: `Ctrl+C`. Чтобы очистить рабочую папку: `rm -rf .flowManager/runs/`.

Для visual QA понадобятся два flow-файла:
- `example-flow-interactive.yaml` — для проверки диалога
- `example-flow.yaml` — для проверки многостадийного потока с параллельностью

---

## Task 1: CSS-фундамент — палитра, типографика, базовые ресеты, reduced-motion

Цель: заменить старую `style.css` на чистую заготовку с CSS-переменными, шрифтом, базовыми стилями и блоком `@media prefers-reduced-motion`. После этой задачи дашборд будет выглядеть голым (без рамок, без layout), но функционально работать.

**Files:**
- Replace: `pkg/web/style.css`

- [x] **Step 1: Заменить style.css на новую базу**

Содержимое `pkg/web/style.css`:

```css
/* ============================================================
   flowManager Dashboard — Nova Corps theme
   Hi-tech holographic UI, vanilla CSS, no frameworks
   ============================================================ */

/* === Reduced motion gate — должен быть в начале === */
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

/* === Design tokens === */
:root {
  /* base surfaces */
  --bg:        #08141a;
  --bg-elev:   #0c1c24;

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

  /* legacy aliases used by app.js inline styles */
  --text:    var(--ink);
  --text-dim: var(--ink-dim);
  --border:  var(--mint-soft);
  --accent:  var(--mint);
  --card:    var(--bg-elev);
  --radius:  0;
}

/* === Base === */
html, body {
  height: 100%;
  font-family: "JetBrains Mono", "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
  font-size: 11px;
  line-height: 1.5;
  background: var(--bg);
  color: var(--ink);
  -webkit-font-smoothing: antialiased;
}

body.theme-novacorps {
  /* hook for future themes */
}

a { color: var(--mint); text-decoration: none; }
a:hover { text-decoration: underline; }

::selection { background: rgba(229, 212, 66, 0.35); color: var(--ink-hi); }

.hidden { display: none !important; }
```

- [x] **Step 2: Добавить body class**

В `pkg/web/index.html` найти `<body>` и заменить на `<body class="theme-novacorps">`.

- [x] **Step 3: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

- [x] **Step 3: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Открыть http://localhost:9876. Ожидаемое: тёмный bg `#08141a`, моноширинный текст, элементы расположены вертикально (никаких рамок/layout пока), все клики работают.

- [x] **Step 4: Commit**

```bash
git add pkg/web/style.css pkg/web/index.html
git commit -m "feat(web): база CSS — палитра, типографика, reduced-motion"
```

---

## Task 2: Layout grid + панели с засечёнными углами

Цель: вернуть 3-колоночный layout с панелями. Панели получают тонкие cyan-рамки и засечённые углы.

**Files:**
- Modify: `pkg/web/style.css` (добавить блок layout в конец)

- [x] **Step 1: Добавить layout в style.css**

В конец `pkg/web/style.css` добавить:

```css
/* === Header === */
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

/* === Main layout === */
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
  background: rgba(8, 20, 26, 0.55);
  border: 1px solid var(--mint-soft);
  padding: 14px;
  overflow-y: auto;
  min-height: 0;
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
  grid-template-columns: 1fr auto auto;
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

.progress-bar {
  width: 160px;
  height: 4px;
  background: rgba(111, 212, 204, 0.12);
  position: relative;
  overflow: hidden;
}
.progress-fill {
  position: absolute;
  inset: 0 auto 0 0;
  width: 0%;
  background: var(--amber);
  transition: width 0.3s;
}

#progress-text,
#started-at,
#elapsed {
  color: var(--ink-hi);
  letter-spacing: 0.1em;
}
```

- [x] **Step 2: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Ожидаемое: 3 колонки (стадии · детали · feed), у каждой панели — тонкая cyan-рамка с маленькими засечками в углах. На фоне `#main` — точечная сетка 24×24. Header и footer — горизонтальные полосы с границами.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat(web): layout с 3 панелями и засечёнными углами"
```

---

## Task 3: Анимированный фоновый луч (.ray) + сканлайны

Цель: добавить контейнер `.ray` поверх `#main` с анимированным жёлтым лучом, который проходит по линиям grid каждые ~9с. Плюс лёгкие горизонтальные сканлайны на фоне всего экрана.

**Files:**
- Modify: `pkg/web/index.html` (добавить `<div class="ray">`)
- Modify: `pkg/web/style.css` (добавить стили + keyframes)

- [x] **Step 1: Добавить .ray в index.html**

Найти строку `<main id="main">` и сразу после неё добавить:

```html
<main id="main">
    <div class="ray" aria-hidden="true"></div>
    <aside id="stages-panel">
```

(существующая разметка остаётся, просто перед `<aside>` появляется `<div class="ray">`)

- [x] **Step 2: Добавить стили и анимации в конец style.css**

```css
/* === Scanlines overlay on body === */
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

/* === Yellow scanner ray on grid lines === */
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

/* Panes над .ray */
#stages-panel,
#detail-panel,
#feed-panel {
  z-index: 1;
}
```

- [x] **Step 3: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Ожидаемое: на фоне `#main` каждые ~9с проходит жёлтый луч диагонально слева-сверху вправо-вниз. Луч виден **только на линиях сетки** (маска). На всём экране — еле заметные горизонтальные сканлайны.

Проверка reduced motion: в macOS System Settings → Accessibility → Display → Reduce Motion → ON. После релоада страницы луч должен быть статичен (не двигаться).

- [x] **Step 4: Commit**

```bash
git add pkg/web/index.html pkg/web/style.css
git commit -m "feat(web): сканирующий жёлтый луч по grid-фону"
```

---

## Task 4: Header — SVG-логотип с анимациями + brand + WS-индикатор

Цель: заменить голый `<h1>flowManager</h1>` на композицию из анимированного SVG-логотипа (гекс + дуга + ядро), brand-текста, flow-имени и WS-индикатора со свечением.

**Files:**
- Modify: `pkg/web/index.html`
- Modify: `pkg/web/style.css`

- [x] **Step 1: Обновить шапку в index.html**

Найти блок `<header id="header">...</header>` и заменить целиком:

```html
<header id="header">
    <span class="logo" aria-hidden="true">
        <span class="l-ring">
            <svg viewBox="0 0 24 24" fill="none" stroke="#6fd4cc" stroke-width="1">
                <polygon points="12,1 22,7 22,17 12,23 2,17 2,7"/>
            </svg>
        </span>
        <span class="l-arc">
            <svg viewBox="0 0 24 24" fill="none" stroke="#e5d442" stroke-width="1" stroke-linecap="round">
                <path d="M 12 3 A 9 9 0 0 1 21 12"/>
            </svg>
        </span>
        <span class="l-core">
            <svg viewBox="0 0 24 24" fill="#6fd4cc" stroke="none">
                <circle cx="12" cy="12" r="2.4"/>
            </svg>
        </span>
    </span>
    <h1>flowManager</h1>
    <div id="flow-name" class="flow-name"></div>
    <div id="ws-status" class="ws-status disconnected" title="WebSocket">OFFLINE</div>
</header>
```

(Text внутри `#ws-status` меняем с `WS` на `OFFLINE` — стартовое состояние.)

Обновить `pkg/web/app.js` — на строках 893–904 заменить:

```js
ws.onopen = function () {
    $wsStatus.textContent = "WS";
    $wsStatus.className = "ws-status connected";
    reconnectDelay = 1000;
};

ws.onclose = function () {
    $wsStatus.textContent = "WS";
    $wsStatus.className = "ws-status disconnected";
    setTimeout(connectWS, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 10000);
};
```

На:

```js
ws.onopen = function () {
    $wsStatus.textContent = "LINK";
    $wsStatus.className = "ws-status connected";
    reconnectDelay = 1000;
};

ws.onclose = function () {
    $wsStatus.textContent = "OFFLINE";
    $wsStatus.className = "ws-status disconnected";
    setTimeout(connectWS, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 10000);
};
```

- [x] **Step 2: Добавить стили лого + анимации в конец style.css**

```css
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

- [x] **Step 3: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Ожидаемое:
- В шапке слева — анимированный логотип (гекс крутится медленно, янтарная дуга крутится в обратную сторону быстрее, ядро пульсирует)
- Текст `FLOWMANAGER` крупно, с разрядкой
- Справа — индикатор `LINK` (зелёный) с пульсирующей точкой когда WS подключён, или `OFFLINE` (coral) когда нет

- [x] **Step 4: Commit**

```bash
git add pkg/web/index.html pkg/web/style.css pkg/web/app.js
git commit -m "feat(web): анимированный SVG-лого и WS-индикатор в шапке"
```

---

## Task 5: Список стадий — ring-маркеры со всеми 10 статусами

Цель: оформить `.stage-item` и `.status-dot` (превращается в "ring") с полной картой цветов для всех статусов. Без активного состояния (оно в следующей задаче).

**Files:**
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить стили списка стадий в конец style.css**

```css
/* === Stages list === */
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
  overflow: hidden;
  transition: background 0.15s;
}

.stage-item:hover {
  background: rgba(111, 212, 204, 0.06);
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
```

Заметка: `currentColor` в `box-shadow` для pulse работает через CSS-color наследование. Если не работает корректно — можно задать конкретные `rgba` в keyframes (см. таску 6 для активного состояния).

- [x] **Step 2: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Ожидаемое: список стадий с кольцами разного цвета по статусу. Кольца у `running`/`planning`/`retrying` пульсируют. Текст в UPPERCASE с разрядкой.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat(web): ring-маркеры статусов стадий со всеми состояниями"
```

---

## Task 6: Активный стейдж — пульсирующий кант + бегущая scanline

Цель: оформить `.stage-item.active` с пульсирующим жёлтым/фиолетовым (в зависимости от статуса) кантом слева и бегущей сверху-вниз scanline по строке.

**Files:**
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить стили активного стейджа**

В конец style.css добавить:

```css
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

- [x] **Step 2: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Ожидаемое: при клике на стадию — она подсвечивается лёгким жёлтым фоном, слева появляется пульсирующая жёлтая полоса со свечением, сверху по строке периодически (каждые 3с) проходит горизонтальная линия.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat(web): анимированный кант и scanline у активной стадии"
```

---

## Task 7: Детальная панель — заголовок + ornament SVG + status badges

Цель: оформить шапку детальной панели (h2 + status badge), добавить декоративный SVG-ornament (компасная роза) в углу, оформить status-badges для всех 10 статусов.

**Files:**
- Modify: `pkg/web/index.html`
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить ornament SVG в index.html**

Найти `<div class="detail-header">` и добавить ornament после `<span id="detail-status">`:

```html
<div class="detail-header">
    <h2 id="detail-title"></h2>
    <span id="detail-status" class="status-badge"></span>
    <span class="ornament" aria-hidden="true">
        <svg viewBox="0 0 100 100" fill="none" stroke="#6fd4cc" stroke-width="1">
            <circle cx="50" cy="50" r="46"/>
            <circle cx="50" cy="50" r="32"/>
            <path d="M50 6 L52 50 L50 94 L48 50 Z" fill="#6fd4cc" stroke="none"/>
            <path d="M6 50 L50 48 L94 50 L50 52 Z" fill="#6fd4cc" stroke="none"/>
            <circle cx="50" cy="50" r="3" fill="#e5d442" stroke="none"/>
        </svg>
    </span>
</div>
```

- [x] **Step 2: Стили шапки детальной панели + status-badges**

В конец style.css:

```css
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
```

- [x] **Step 3: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Кликнуть стадию. Ожидаемое: справа от названия стадии — outline-бейдж со статусом, в правом углу шапки — медленно крутящийся компас. Бейджи у running/awaiting_user_input имеют мягкий пульс свечения.

- [x] **Step 4: Commit**

```bash
git add pkg/web/index.html pkg/web/style.css
git commit -m "feat(web): шапка детальной панели с ornament и status badges"
```

---

## Task 8: План — line numbers, markdown, special sections, inline comments

Цель: оформить блок плана `#plan-content` с построчным ревью — нумерация, разметка markdown (h1-h3, code, pre, lists, checkboxes, bold), highlight для строк с комментариями, форма inline-комментария, специальные секции (`## Assumptions`, `## Acceptance Criteria`).

**Files:**
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить стили плана в конец style.css**

```css
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
```

- [x] **Step 2: Build + проверка awaiting_approval**

```bash
make build
./bin/flowmanager run example-flow.yaml
```

Дождаться, пока стадия попадёт в `awaiting_approval`. Открыть её в дашборде. Ожидаемое:
- План отрендерен с нумерацией строк слева
- Hover на строке — подсветка + точка-маркер справа
- Клик на строке — открывается форма комментария
- Введите текст, нажмите «Добавить» — строка подсвечивается жёлтым, появляется отображение комментария
- Кнопка «Отправить правку (1)» становится активна

Если в плане есть `## Assumptions` или `## Acceptance Criteria` — секция должна быть выделена цветной левой полосой и сворачивается кликом на заголовок.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat(web): план с построчным ревью, markdown и inline-комментариями"
```

---

## Task 9: Кнопки действий — approve / revise / retry / cancel

Цель: оформить все кнопки в outline-стиле с CTA-заливкой у primary, цветовая дифференциация по типу действия.

**Files:**
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить стили кнопок в style.css**

```css
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

#feedback-section {
  margin-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
#feedback-section.hidden { display: none; }

#feedback-text {
  background: var(--bg-elev);
  border: 1px solid var(--mint-soft);
  color: var(--ink);
  padding: 10px;
  font-family: inherit;
  font-size: 11px;
  resize: vertical;
  min-height: 70px;
}
#feedback-text:focus {
  outline: 1px solid var(--mint);
  border-color: var(--mint);
}
```

- [x] **Step 2: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow.yaml
```

Дойти до `awaiting_approval`. Ожидаемое: кнопка `ОДОБРИТЬ` (с заливкой mint), кнопка `ОТПРАВИТЬ ПРАВКУ` (outline amber). Когда стадия `failed` — `ПОПРОБОВАТЬ ЕЩЁ РАЗ` (outline coral). При hover — каждая кнопка получает свечение в своём цвете.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat(web): action-кнопки в outline-стиле с цветовой картой"
```

---

## Task 10: Лог стадии

Цель: оформить блок `#log-content` (`<pre>` с raw-логом).

**Files:**
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить стили лога**

```css
/* === Log section === */
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

- [x] **Step 2: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow.yaml
```

Кликнуть стадию в `running`. Лог должен быть моноширинным, чёрный фон, текст ink с error-строками в coral.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat(web): стили блока лога стадии"
```

---

## Task 11: Диалог — канал, история, pending с дыханием и сканлайном, опции с горячими буквами

Цель: оформить `#dialog-section` как «канал связи» в фиолетовых тонах, история Q/A с разделителями фаз, pending-блок с пульсирующим свечением и пробегающим сканлайном, опции с буквами A/B/C через CSS counter, индикатор «agent is waiting» с мигающей кареткой.

**Files:**
- Modify: `pkg/web/style.css`

- [x] **Step 1: Изменить кнопку dialog-toggle на UPPERCASE и заменить текст**

В `pkg/web/app.js` на строках 547–549:

```js
$dialogToggle.textContent = $dialogHistory.classList.contains("collapsed")
    ? "Развернуть историю ▾"
    : "Свернуть историю ▴";
```

Заменить на:

```js
$dialogToggle.textContent = $dialogHistory.classList.contains("collapsed")
    ? "▾ РАЗВЕРНУТЬ ИСТОРИЮ"
    : "▴ СВЕРНУТЬ ИСТОРИЮ";
```

И в `pkg/web/index.html` заменить текст кнопки `<button id="dialog-toggle"...>`:

```html
<button id="dialog-toggle" class="dialog-toggle hidden">▴ СВЕРНУТЬ ИСТОРИЮ</button>
```

И заменить заголовок диалога — найти:

```html
<div id="dialog-section" class="section hidden">
    <h3>Диалог</h3>
```

Заменить на:

```html
<div id="dialog-section" class="section hidden">
    <h3>● Канал связи</h3>
```

И заменить кнопки внутри dialog-pending — найти:

```html
<button class="btn btn-send">Отправить</button>
<button class="btn btn-cancel-dialog">Отменить стейдж</button>
```

Заменить на:

```html
<button class="btn btn-send">▸ ОТПРАВИТЬ</button>
<button class="btn btn-cancel-dialog">ОТМЕНИТЬ СТЕЙДЖ</button>
```

И добавить индикатор `agent is waiting` после кнопок:

```html
<div class="dialog-actions">
    <button class="btn btn-send">▸ ОТПРАВИТЬ</button>
    <button class="btn btn-cancel-dialog">ОТМЕНИТЬ СТЕЙДЖ</button>
    <span class="typing-indicator">AGENT IS WAITING <span class="blink"></span></span>
</div>
```

- [x] **Step 2: Добавить стили диалога в style.css**

```css
/* === Dialog section === */
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
  font-size: 10px;
  letter-spacing: 0.25em;
  color: var(--violet);
  text-transform: uppercase;
  margin-bottom: 12px;
  padding-bottom: 10px;
  border-bottom: 1px dashed rgba(183, 135, 255, 0.25);
}
#dialog-section h3::before {
  /* Replace the literal ● in h3 with pulsing dot */
  content: "";
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--violet);
  box-shadow: 0 0 8px var(--violet);
  animation: wsBlink 1.6s ease-in-out infinite;
  flex-shrink: 0;
}
/* hide the original "●" text — we use ::before pseudo */
#dialog-section h3 {
  font-size: 0;
}
#dialog-section h3::after {
  content: "DIALOG · CHANNEL OPEN";
  font-size: 10px;
  letter-spacing: 0.25em;
}

/* === History === */
.dialog-history {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 280px;
  overflow-y: auto;
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
  content: "Q · ";
  color: var(--mint);
  font-weight: 600;
  letter-spacing: 0.2em;
}
.dialog-history .qa .a {
  font-size: 10.5px;
  color: #9fe0d8;
  padding-left: 16px;
  position: relative;
}
.dialog-history .qa .a::before {
  content: "A · ";
  color: var(--amber);
  font-weight: 600;
  letter-spacing: 0.2em;
  margin-right: 2px;
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

.dialog-pending::after {
  content: "";
  position: absolute;
  left: 0;
  right: 0;
  top: 0;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(183, 135, 255, 0.9), transparent);
  animation: pendingScan 3.2s linear infinite;
  pointer-events: none;
}

@keyframes pendingScan {
  0%   { transform: translateY(0); opacity: 0; }
  8%   { opacity: 1; }
  92%  { opacity: 1; }
  100% { transform: translateY(180px); opacity: 0; }
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
  color: #d4baff;
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
  color: #fff;
  box-shadow: 0 0 8px rgba(183, 135, 255, 0.45);
}
.dialog-pending .dialog-options button.selected {
  background: rgba(183, 135, 255, 0.18);
  border-color: var(--violet);
  color: #fff;
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
  background: rgba(8, 20, 26, 0.6);
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
  color: #ff8a7a;
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

- [x] **Step 3: Build + проверка интерактивного flow**

```bash
make build
rm -rf .flowManager/runs/  # очистить старые runs
./bin/flowmanager run example-flow-interactive.yaml
```

Дождаться, пока стадия попадёт в `awaiting_user_input`. Открыть её в дашборде. Ожидаемое:
- Секция «● DIALOG · CHANNEL OPEN» с пульсирующей фиолетовой точкой
- Pending-блок «дышит» (свечение пульсирует каждые 3.4с)
- По pending пробегает горизонтальная фиолетовая линия каждые 3.2с
- Опции имеют префиксы `A`, `B`, `C`, `D` жёлтым
- Hover/click на опции работают как раньше
- Textarea «или вписать свой ответ» с фокусом подсвечивается фиолетовым
- Внизу — «AGENT IS WAITING ▌» с мигающей кареткой
- Кнопка «▸ ОТПРАВИТЬ» — заливка violet, «ОТМЕНИТЬ СТЕЙДЖ» — outline coral

Отправить ответ — он должен попасть в историю с префиксом `A ·` и янтарным цветом.

- [x] **Step 4: Commit**

```bash
git add pkg/web/index.html pkg/web/style.css pkg/web/app.js
git commit -m "feat(web): диалог в стиле канала связи с фиолетовой подсветкой"
```

---

## Task 12: Лента событий — относительное время + компактная вёрстка

Цель: переделать рендер feed-entry — время становится коротким относительным (`02s`/`4m`/`1h`), бейдж стадии становится inline-частью сообщения. Текст сообщения занимает почти всю ширину.

**Files:**
- Modify: `pkg/web/app.js` (функция `addFeedEntry`, новые хелперы)
- Modify: `pkg/web/style.css`

- [x] **Step 1: Добавить хелпер `formatRelativeTime` в app.js**

В `pkg/web/app.js` найти `function formatDuration(ms)` (около строки 804) и добавить **сразу после** неё:

```js
function formatRelativeTime(unixMs) {
    var diffSec = Math.max(0, Math.floor((Date.now() - unixMs) / 1000));
    if (diffSec < 60)     return diffSec + "s";
    if (diffSec < 3600)   return Math.floor(diffSec / 60) + "m";
    if (diffSec < 86400)  return Math.floor(diffSec / 3600) + "h";
    return Math.floor(diffSec / 86400) + "d";
}
```

- [x] **Step 2: Переписать `addFeedEntry` в app.js**

Найти `function addFeedEntry(ev)` (около строки 921) и заменить его на:

```js
function addFeedEntry(ev) {
    var div = document.createElement("div");
    div.className = "feed-entry";

    var ts = Date.now();
    div.dataset.ts = ts;

    var stageID = ev.stage_id || "";
    var msg = "";
    var msgClass = "feed-msg";
    var statusClass = "";

    switch (ev.type) {
        case "stage_status_changed":
            msg = "→ " + (ev.data || "");
            statusClass = "status-" + (ev.data || "");
            break;
        case "agent_completed":
            msg = "агент " + (ev.data || "") + " завершён";
            break;
        case "agent_action":
            var actionData = ev.data || {};
            var tool = actionData.tool || "";
            var detail = actionData.detail || "";
            msg = tool + (detail ? ": " + detail : "");
            msgClass = "feed-msg action";
            break;
        case "approved":
            msg = "одобрено";
            statusClass = "status-awaiting_approval";
            break;
        case "revised":
            msg = "правки: " + (ev.data || "");
            msgClass = "feed-msg error";
            statusClass = "status-revising";
            break;
        case "retry_scheduled":
            msg = "повтор: " + (ev.data || "");
            statusClass = "status-retrying";
            break;
        case "retry_exhausted":
            msg = "попытки исчерпаны";
            statusClass = "status-failed";
            msgClass = "feed-msg error";
            break;
        case "manual_retry":
            msg = "ручной повтор";
            statusClass = "status-retrying";
            break;
        case "ask_user":
            msg = "вопрос агенту";
            statusClass = "status-awaiting_user_input";
            break;
        case "user_answered":
            msg = "ответ пользователя";
            statusClass = "status-running";
            break;
        default:
            msg = ev.type;
    }

    var badgeHTML = stageID
        ? '<span class="feed-stage-badge ' + statusClass + '">' + escapeHTML(stageID) + "</span>"
        : "";

    div.innerHTML =
        '<span class="feed-time">' + formatRelativeTime(ts) + "</span>" +
        '<span class="' + msgClass + '">' + badgeHTML + escapeHTML(msg) + "</span>";

    $feedContent.appendChild(div);
    $feedContent.scrollTop = $feedContent.scrollHeight;
}
```

Главные отличия: добавили `data-ts`, убрали `feed-stage-badge` как отдельную колонку — теперь он встроен внутрь span сообщения, время форматируется через `formatRelativeTime`.

- [x] **Step 3: Добавить таймер обновления времени в feed**

В `pkg/web/app.js` найти раздел инициализации (в самом низу IIFE, перед закрытием — рядом с `loadState()` или после WS-инициализации). Добавить таймер, который обновляет `.feed-time` каждые 5 секунд:

```js
setInterval(function () {
    var nodes = $feedContent.querySelectorAll(".feed-entry");
    for (var i = 0; i < nodes.length; i++) {
        var ts = parseInt(nodes[i].dataset.ts, 10);
        if (!isNaN(ts)) {
            var t = nodes[i].querySelector(".feed-time");
            if (t) t.textContent = formatRelativeTime(ts);
        }
    }
}, 5000);
```

Если рядом есть существующий `setInterval(updateElapsed, 1000)` — можно поставить рядом с ним.

- [x] **Step 4: Стили feed в style.css**

```css
/* === Event feed === */
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

- [x] **Step 5: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow.yaml
```

Открыть дашборд. Ожидаемое:
- В ленте справа каждая запись: `02s` (или `4m`/`1h`) | `BACKEND · → done` где `BACKEND` — мелкий цветной бейдж inline с текстом сообщения
- Сообщения занимают почти всю ширину панели
- При новых событиях — относительное время обновляется (через 5с увидишь, что `02s` стало `07s`)

- [x] **Step 6: Commit**

```bash
git add pkg/web/app.js pkg/web/style.css
git commit -m "feat(web): компактная лента событий с относительным временем"
```

---

## Task 13: Футер — прогресс-бар с shimmer и пульсирующим кончиком

Цель: оформить прогресс-бар с бегущим shimmer-световым бликом и пульсирующим жёлтым кончиком.

**Files:**
- Modify: `pkg/web/style.css`

- [x] **Step 1: Заменить стили прогресса в style.css**

Найти существующий блок стилей `.progress-bar` / `.progress-fill` (добавленный в Task 2) и **заменить** на:

```css
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
/* shimmer over the fill */
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
/* pulsing tip at the end of fill */
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
```

- [x] **Step 2: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow.yaml
```

Ожидаемое: прогресс-бар в футере — заполненная часть с янтарным градиентом, по ней бегает блестящая полоса, на конце — пульсирующий 2px кончик со свечением.

- [x] **Step 3: Commit**

```bash
git add pkg/web/style.css
git commit -m "feat(web): анимированный прогресс-бар в футере"
```

---

## Task 14: Favicon

Цель: заменить `pkg/web/favicon.svg` на новый — гексагон с янтарной дугой и mint-ядром.

**Files:**
- Replace: `pkg/web/favicon.svg`

- [x] **Step 1: Записать новый favicon.svg**

Содержимое `pkg/web/favicon.svg`:

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
  <rect width="24" height="24" fill="#08141a"/>
  <polygon points="12,2 21,7 21,17 12,22 3,17 3,7"
           fill="none" stroke="#6fd4cc" stroke-width="1.2"/>
  <path d="M 12 4 A 8 8 0 0 1 20 12"
        fill="none" stroke="#e5d442" stroke-width="1.2" stroke-linecap="round"/>
  <circle cx="12" cy="12" r="2.4" fill="#6fd4cc"/>
</svg>
```

- [x] **Step 2: Build + проверка**

```bash
make build
./bin/flowmanager run example-flow.yaml
```

Открыть дашборд. Во вкладке браузера — новая иконка (гексагон с янтарной дугой).

- [x] **Step 3: Commit**

```bash
git add pkg/web/favicon.svg
git commit -m "feat(web): новая favicon в стиле логотипа"
```

---

## Task 15: Финальный визуальный QA + правки

Цель: пройтись по всем состояниям дашборда, проверить, что ничего не сломалось, поправить мелочи.

**Files:**
- Modify: `pkg/web/style.css` или другие — по мере необходимости

- [x] **Step 1: Подготовить два flow для тестирования**

Проверить, что доступны:

```bash
ls example-flow.yaml example-flow-interactive.yaml
```

Если нет — создать стандартные.

- [x] **Step 2: Чек-лист визуальных состояний**

```bash
rm -rf .flowManager/runs/
make build
./bin/flowmanager run example-flow-interactive.yaml
```

Пройтись по чек-листу. Для каждого пункта — открыть состояние, посмотреть, отметить ✓ / ✗:

- [x] Стадия `pending` — пустое кольцо `var(--ink-dim)`, без анимации
- [x] Стадия `planning` — mint-кольцо с pulse
- [x] Стадия `awaiting_approval` — amber-кольцо без анимации, бейдж amber-outline
- [x] Стадия `running` — amber-кольцо с pulse
- [x] Стадия `done` — mint-кольцо, заливка mint
- [x] Стадия `failed` — coral-кольцо, бейдж coral, кнопка retry с coral
- [x] Стадия `awaiting_user_input` — violet-кольцо с pulse + badge `💬`
- [x] Активная стадия — пульсирующая жёлтая полоса слева, scanline пробегает сверху вниз
- [x] Шапка — анимированный лого крутится, WS-индикатор пульсирует когда подключён
- [x] Фон — жёлтый луч проходит по grid каждые ~9с
- [x] План открывается с нумерацией строк, при hover — подсветка
- [x] Клик на строку в `awaiting_approval` — открывается форма комментария
- [x] Добавил комментарий — строка подсвечена жёлтым, есть точка-маркер
- [x] Кнопка «Отправить правку (N)» меняет цифру
- [x] Special sections `## Assumptions` / `## Acceptance Criteria` — сворачиваются по клику
- [x] Approve — стадия переходит в `ready` → `running`
- [x] Диалог открывается — pending-блок дышит, scanline пробегает
- [x] Опции диалога имеют буквы `A B C D`, выбор работает, custom textarea дисэйблит опции
- [x] «AGENT IS WAITING» с мигающей кареткой
- [x] Отмена диалога — стадия в `failed`
- [x] Лента событий — относительное время (`02s`/`4m`), inline-бейджи стадий, цвет бейджа = цвет статуса
- [x] Прогресс-бар — shimmer бегает, кончик пульсирует
- [x] Favicon во вкладке — гексагон

- [x] **Step 3: Проверить prefers-reduced-motion**

macOS: Settings → Accessibility → Display → Reduce Motion → ON.

В Chrome / Safari релоадить http://localhost:9876. Ожидаемое:
- Лого статичен
- Кольца не пульсируют
- Луч на фоне не двигается (или стоит в одной позиции)
- Прогресс-бар без shimmer
- Диалог без дыхания и scanline
- Цвета и статусы по-прежнему видны корректно

Выключить настройку обратно.

- [x] **Step 4: Проверить на разной ширине окна**

Сузить окно до 1024px — 3 колонки должны остаться, но столбцы стадий и feed могут стать тесными. Это допустимо. Если визуально совсем плохо при <900px — добавить media query (опционально, можно отложить).

- [x] **Step 5: Зафиксировать найденные проблемы**

Если что-то не работает — поправить прямо здесь:

```bash
# fix in pkg/web/style.css or pkg/web/app.js
make build
# re-test
```

Когда всё работает —

- [x] **Step 6: Финальный commit**

```bash
git add pkg/web/  # на случай мелких правок
git diff --cached --stat
git commit -m "fix(web): мелкие правки после визуального QA"  # если были правки
```

Если правок не было — этот шаг пропустить.

---

## Self-Review Checklist

- [x] **Spec coverage** — каждый компонент из спека покрыт задачей:
  - Палитра/типографика → Task 1
  - Layout/панели → Task 2
  - Background grid/ray → Task 3
  - Логотип/шапка → Task 4
  - Стадии (все 10 статусов) → Task 5, 6
  - Детальная панель + ornament + badges → Task 7
  - План + markdown + comments + special sections → Task 8
  - Кнопки → Task 9
  - Лог → Task 10
  - Диалог (history + pending + options + textarea + indicator) → Task 11
  - Feed (relative time + compact) → Task 12
  - Footer/progress → Task 13
  - Favicon → Task 14
  - Reduced motion → Task 1 (гейт в начале CSS), проверка в Task 15
  - QA → Task 15

- [x] **Placeholder scan** — каждый CSS-блок выписан полностью, каждый шаг имеет верифицируемое ожидание, никаких TBD/TODO.

- [x] **Type/selector consistency** — все JS-классы (`stage-item`, `status-dot`, `feed-entry`, `feed-time`, `feed-stage-badge`, `feed-msg`, `dialog-pending`, `dialog-options`, `dialog-custom`, `btn-send`, `btn-cancel-dialog`, `plan-line`, `line-num`, `line-content`, `line-comment-marker`, `line-comment-form`, `plan-section-wrapper`, `qa`, `phase-divider`, `status-badge`, `progress-fill`, `progress-text`) сохранены, CSS их таргетит. JS добавляет/убирает только классы `connected`/`disconnected`/`active`/`has-comment`/`selected`/`hidden`/`collapsed`/`dimmed`/`status-X` — все они учтены в CSS.

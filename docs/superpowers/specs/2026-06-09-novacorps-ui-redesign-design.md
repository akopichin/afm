# Дизайн: Hi-tech редизайн веб-дашборда (Nova Corps style)

**Дата:** 2026-06-09
**Статус:** Утверждён к реализации
**Файлы:** `pkg/web/index.html`, `pkg/web/style.css`, `pkg/web/favicon.svg`, точечно `pkg/web/app.js`

## Цель

Переписать визуальный стиль веб-дашборда `flowManager` в эстетике футуристичного holographic UI (референс — Nova Corps интерфейс Territory Studio для Guardians of the Galaxy): тонкие линии на тёмно-зелёно-чёрном фоне, мятный cyan как основной цвет, янтарный жёлтый как акцент, фиолетовый для интерактивных диалогов, моноширинный шрифт, гексагональные/круглые декоративные элементы, ненавязчивые анимации.

Полная функциональная парность с текущим дашбордом — все фичи, кнопки, состояния и поведение остаются. Меняется только визуальный слой и добавляются декоративные элементы.

## Не-цели

- Реструктурирование DOM или изменение API.
- Светлая тема, переключатель тем.
- Перенос на фреймворк, npm, сборщик. Проект сознательно без зависимостей — это сохраняется.
- Новые фичи дашборда (новые состояния, новые виджеты с данными). Только декоративные элементы.

## Объём изменений

| Файл | Изменение |
|---|---|
| `pkg/web/style.css` | Полная замена. ~780 строк → ~900–1000 строк. |
| `pkg/web/index.html` | Точечные добавления: SVG-логотип в шапке, `.ray` контейнер, ornament SVG в детальной панели, hex-маркеры. Существующие id/классы не меняются. |
| `pkg/web/favicon.svg` | Перерисовать под гексагон с дугой и ядром. |
| `pkg/web/app.js` | Без изменений семантики. Если для какого-то нового класса понадобится точка интеграции — добавляется минимально. |

## Дизайн-система

### Палитра

```css
:root {
  /* base */
  --bg:        #08141a;  /* основа — почти чёрный с зелёным отливом */
  --bg-elev:   #0c1c24;  /* приподнятые поверхности */
  --ink:       #cfeeea;  /* основной текст */
  --ink-hi:    #e7faf7;  /* высокий контраст / заголовки */
  --ink-dim:   #4a8a85;  /* приглушённый текст, метки, line numbers */

  /* accents */
  --mint:      #6fd4cc;  /* основной cyan: линии, done, primary CTA */
  --mint-soft: rgba(111, 212, 204, 0.22);  /* border на панелях */
  --grid:      rgba(111, 212, 204, 0.04);  /* фоновая сетка */
  --amber:     #e5d442;  /* running, выделение строк, прогресс, hotkeys */
  --violet:    #b787ff;  /* interactive / awaiting_user_input */
  --coral:     #ff6b5a;  /* failed / cancel */

  /* status map */
  --c-pending:           var(--ink-dim);
  --c-planning:          var(--mint);
  --c-awaiting:          var(--amber);
  --c-revising:          var(--amber);
  --c-ready:             var(--mint);
  --c-running:           var(--amber);
  --c-done:              var(--mint);
  --c-failed:            var(--coral);
  --c-retrying:          var(--amber);
  --c-awaiting-user:     var(--violet);
}
```

### Типографика

Единый моноширинный стек:

```css
font-family: "JetBrains Mono", "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
```

Размеры:
- `11px` — база, line-height 1.5
- `10px` — метки, ленты, статус-бейджи
- `9px` — micro-labels (tag-метки в заголовках панелей)
- `12px` — текст вопросов в диалоге
- `14px` — h2 заголовки секций детальной панели
- `18px` — h1 (зарезервирован, в текущем UI не используется заметно)

Все заголовки и метки — UPPERCASE с `letter-spacing` от `0.18em` до `0.3em`. Обычные строки плана/лога — без преобразований.

### Геометрия

- Все скругления `0` (углы прямые) — кроме status-rings (`border-radius: 50%`) и progress-fill (внутри track).
- Границы панелей — 1px stroke `var(--mint-soft)`.
- На каждой панели — засечённые углы (notched corners) через псевдоэлементы:

```css
.pane::before, .pane::after {
  content: ""; position: absolute;
  width: 10px; height: 10px; border: 1px solid var(--mint);
}
.pane::before { top: -1px; left: -1px; border-right: 0; border-bottom: 0; }
.pane::after  { bottom: -1px; right: -1px; border-left: 0; border-top: 0; }
```

### Фон

Слой 1 — grid 24×24 (CSS gradient):
```css
background-image:
  linear-gradient(var(--grid) 1px, transparent 1px),
  linear-gradient(90deg, var(--grid) 1px, transparent 1px);
background-size: 24px 24px;
```

Слой 2 — горизонтальные scanlines 3px (CSS repeating-linear-gradient, opacity ~0.014).

Слой 3 — анимированный жёлтый луч `.ray` (см. секцию «Анимации»).

## Раскладка

Структура остаётся 3-колоночной как сейчас:

```
+--------------------------------------------------------+
|  HEADER · logo · brand · flow-id · run/stages · WS    |
+----------+----------------------------+----------------+
| STAGES   |   DETAIL                   |   EVENT FEED   |
|  240px   |   1fr                      |   280px        |
|          |                            |                |
|          |                            |                |
+----------+----------------------------+----------------+
|  FOOTER · progress bar · started · elapsed · host     |
+--------------------------------------------------------+
```

Существующие id (`stages-panel`, `detail-panel`, `feed-panel`, `header`, `footer`) и контейнеры (`#stages-list`, `#detail-content`, `#feed-content`, `#progress-fill`) сохраняются.

## Компоненты

### Заголовок

```
[Logo] FLOWMANAGER  // <flow-id>   RUN <N>   STAGES <N>   ●LINK
```

- **Логотип** — новый SVG 22×22 в шапке (был только `<h1>` текст). Три слоя:
  - Внешнее кольцо: гексагон stroke `var(--mint)`
  - Янтарная дуга: четверть окружности stroke `var(--amber)`
  - Ядро: круг fill `var(--mint)` радиус 2.4
  Все три анимируются (см. ниже).
- **Brand** `FLOWMANAGER` — uppercase, letter-spacing 0.3em, цвет `var(--ink-hi)`.
- **Flow-id** — формата `// <name>-<timestamp>`, цвет `var(--mint)`.
- **RUN N · STAGES N** — meta с цифрами в `var(--amber)`.
- **WS-индикатор** — точка `var(--mint)` со свечением + надпись `LINK` / `OFFLINE`. Класс `.connected` / `.disconnected` уже ставится в app.js — переопределяется CSS.

### Список стадий

Каждая стадия:
```
[ring]  STAGE·NAME              [12s]
        stage NN · <status>
```

- **Ring** — круг 14×14 с 1px border текущего статусного цвета. Внутри — заливка 8×8 того же цвета.
- **Name** — UPPERCASE через `text-transform`, `letter-spacing: 0.05em`. Текст в DOM не меняем — никаких замен пробелов на `·`. Letter-spacing достаточно, чтобы имя выглядело «технологично».
- **Code** (subtitle) — `stage NN · <статус>` мелким `var(--ink-dim)`.

**Активная стадия** (выделена кликом):
- Лёгкий gradient-фон из цвета статуса (например, `linear-gradient(90deg, rgba(229,212,66,0.06), transparent)` для running)
- Слева — пульсирующий кант 2px со свечением
- Сверху по строке — пробегающая 1px scanline тем же цветом (анимация `scanlineX`)

**Статусы и цвета** — таблица:

| Status | Цвет | Анимация ring |
|---|---|---|
| `pending` | `var(--ink-dim)` | нет, ring пустой (без заливки) |
| `planning` | `var(--mint)` | пульс свечения 1.6s |
| `awaiting_approval` | `var(--amber)` | нет |
| `revising` | `var(--amber)` | пульс 1.6s |
| `ready` | `var(--mint)` | нет |
| `running` | `var(--amber)` | пульс 1.6s |
| `done` | `var(--mint)` | нет |
| `failed` | `var(--coral)` | нет |
| `retrying` | `var(--amber)` | пульс 1.6s |
| `awaiting_user_input` | `var(--violet)` | пульс 1.8s |

### Детальная панель

Шапка стадии:
```
STAGE · NAME           [STATUS BADGE]
```

- h2 — UPPERCASE `var(--ink-hi)`, разделитель `·`.
- Status badge — outline-стиль: 1px border статусного цвета, padding `2px 10px`, font-size 9px, letter-spacing 0.25em. У running/retrying/awaiting_user_input — мягкая `badgePulse` анимация на box-shadow.

**Ornament** (новый декор) — в правом верхнем углу панели SVG 36×36: концентрические кольца + крестовая «компасная роза» в `var(--mint)`, центральная точка `var(--amber)`. Медленно вращается (22s оборот).

**Секция плана**:
- Контейнер `.plan-block` — 1px border `var(--mint-soft)`, padding `12px 14px`.
- Строка `.plan-line` — grid `28px 1fr`:
  - line-number `var(--ink-dim)`, моноширинный, выравнивание right
  - текст `var(--ink)` с inline-разметкой (см. ниже)
- Hover — фон `rgba(15, 52, 96, 0.5)` → заменить на `rgba(111, 212, 204, 0.08)`.
- Строка с inline-комментарием (`.has-comment`) — фон `rgba(229, 212, 66, 0.08)`, левая граница 2px `var(--amber)`.
- Inline-форма комментария (`.line-comment-form`) — фон `var(--bg-elev)`, 1px border `var(--amber)`, текстарея с фокусом на `var(--mint)`.

**Markdown в плане** (рендерится через `inlineFormat`/`renderPlanReview` в app.js):
- `**bold**` → `var(--ink-hi)` weight 600
- `` `code` `` inline → фон `var(--bg-elev)`, padding `1px 5px`, цвет `var(--mint)`
- ``` ```block``` ``` → `<pre>` с 1px border `var(--mint-soft)`, фон `var(--bg-elev)`, моноширинный, padding 12px
- `[x]` checkbox → 14×14 квадрат с заливкой `var(--mint)` и галочкой
- `[ ]` checkbox → 14×14 квадрат outline `var(--ink-dim)`
- h1/h2/h3 — `var(--ink-hi)`, uppercase для h2/h3, без uppercase для h1
- `ul`/`ol` — маркеры `var(--mint-dim)`

**Special sections** (`## Assumptions`, `## Acceptance Criteria`):
- Контейнер с левой границей 3px:
  - Assumptions: `var(--amber)`, фон `rgba(229, 212, 66, 0.05)`
  - Acceptance Criteria: `var(--mint)`, фон `rgba(111, 212, 204, 0.05)`
- Заголовок — `cursor: pointer`, при клике сворачивается (механика уже в JS — `collapsed` класс).

**Кнопки действий** (Approve/Revise/Retry):
```css
.btn {
  background: transparent;
  border: 1px solid var(--mint);
  color: var(--mint);
  padding: 6px 18px;
  font-family: inherit;
  font-size: 10px;
  letter-spacing: 0.25em;
  text-transform: uppercase;
}
.btn.btn-approve { background: var(--mint); color: var(--bg); }       /* CTA */
.btn.btn-revise  { border-color: var(--amber); color: var(--amber); }
.btn.btn-retry   { border-color: var(--coral); color: var(--coral); }
.btn:hover       { box-shadow: 0 0 8px currentColor; }
```

### Секция диалога (`#dialog-section`)

Видна только при `interactive: true` + статус `awaiting_user_input`. Структура остаётся как есть: `#dialog-history` + `#dialog-pending` + `#dialog-toggle`.

**Канал связи** (новая обёртка `.dialog-channel` либо стилизация существующего `#dialog-section`):
- 1px border `rgba(183, 135, 255, 0.3)`
- Засечённые углы в `var(--violet)`
- Внутренний `radial-gradient` от верхнего правого угла — лёгкий фиолетовый свет
- Заголовок канала: `● DIALOG · CHANNEL OPEN · CH-<id>` — uppercase, letter-spacing 0.25em, цвет `var(--violet)`. Точка пульсирует.

**История** (`.dialog-history` → `.qa` элементы):
- Левая граница 2px `rgba(111, 212, 204, 0.2)`, padding-left 12px
- Префикс `Q ·` для вопроса (`var(--mint)`), префикс `A` для ответа (`var(--amber)`)
- `.phase-divider` — uppercase, dashed bottom border `rgba(111, 212, 204, 0.12)`

**Pending question** (`.dialog-pending`):
- 1px border `var(--violet)`
- Фон — лёгкий вертикальный gradient `rgba(183, 135, 255, 0.10)` → `rgba(183, 135, 255, 0.02)`
- box-shadow внутренний + внешний — пульсирует (анимация `pendingBreathe` 3.4s)
- Сверху по блоку пробегает 1px scanline в `var(--violet)` (анимация `pendingScan` 3.2s)
- `.q-label` — micro-метка `QUESTION · NN · ID <q-id>` сверху
- `.question` — текст вопроса 12px `var(--ink-hi)`
- `.dialog-options` — flex-wrap кнопок:
  ```css
  .dialog-options button {
    background: transparent;
    border: 1px solid rgba(183, 135, 255, 0.55);
    color: #d4baff;
    padding: 6px 14px;
    font-size: 10px; letter-spacing: 0.18em; text-transform: uppercase;
  }
  .dialog-options button .key { color: var(--amber); margin-right: 8px; }
  .dialog-options button:hover { border-color: var(--violet); color: #fff; box-shadow: 0 0 8px rgba(183,135,255,0.45); }
  .dialog-options button.selected {
    background: rgba(183, 135, 255, 0.18);
    border-color: var(--violet);
    box-shadow: 0 0 12px rgba(183, 135, 255, 0.5);
  }
  ```
  Кнопки получают «горячие буквы» (A/B/C/D) как префикс — рендерится в `app.js` при вставке варианта.
- `.dialog-custom` (textarea) — фон `rgba(8, 20, 26, 0.6)`, 1px border `rgba(111, 212, 204, 0.2)`, focus `var(--violet)`.
- `.dialog-actions` — кнопки `[▸ ОТПРАВИТЬ]` (CTA фиолетовый), `[ОТМЕНИТЬ СТЕЙДЖ]` (outline coral).
- Справа в actions — индикатор `AGENT IS WAITING ▌` с мигающей кареткой (CSS, без JS).

### Лог стадии

`#log-content` (`<pre>`):
- Моноширинный 12px
- Фон `var(--bg-elev)`, 1px border `var(--mint-soft)`
- max-height 300px со скроллом
- `.log-text-line` — `var(--ink)`
- `.log-error-line` — `var(--coral)`

### Лента событий (правая панель)

Каждая запись:
```
[ts]  [SRC] message text taking the rest of the row
```

- Grid `36px 1fr` (а не текущий `auto auto 1fr`)
- `.feed-time` (`.ts`) — `var(--ink-dim)`, **компактный формат** (относительное время `02s` / `4m` / `1h`). Парсится из существующего timestamp в JS — добавить хелпер `formatRelativeTime(ts)`.
- В колонке сообщения первым идёт inline-бейдж стадии `.feed-stage` цвета её статуса, потом `·`, потом текст.
- `.feed-stage-badge` (текущий блок 40px фиксированной ширины) — удалить, бейдж становится inline-частью сообщения.
- При hover — фон `rgba(111, 212, 204, 0.06)`.

### Футер

```
PROGRESS · 04 / 07  [████████░░░░░]   STARTED · 14:30  ELAPSED · 00:01:24   HOST · LOCAL · 9876
```

- **Прогресс-трек** — 4px высота, фон `rgba(111, 212, 204, 0.12)`, без скруглений.
- **Progress fill** — gradient `transparent → var(--amber)`. Сверху бежит shimmer-полоса (см. анимации).
- **Tip** — 2px вертикальная полоса справа от fill с пульсирующим свечением `var(--amber)`.
- **HOST** — не добавляем (нет источника данных в app.js, добавлять серверный реквизит ради декора не стоит). В футере остаётся: PROGRESS, STARTED, ELAPSED. Если позже захочется HOST — добавим отдельно.

## Анимации

Каталог. Все через CSS keyframes, без JS.

| Имя | Длит. | Цель | Эффект |
|---|---|---|---|
| `spinSlow` | 14s | `.logo .l-ring` | rotate(360deg) внешнего кольца |
| `spinFast` | 4s | `.logo .l-arc` | rotate(-360deg) янтарной дуги |
| `corePulse` | 2.4s | `.logo .l-core` | scale 1↔1.15 + opacity 0.55↔1 ядра |
| `wsBlink` | 2s | `.ws-status.connected`, dialog dot | box-shadow свечение |
| `raySweep` | 9s | `.ray::before` | translate диагонального жёлтого луча по grid |
| `activeBar` | 2.4s | `.stage-item.active::before` | пульс свечения канта |
| `scanlineX` | 3s | `.stage-item.active::after` | сверху вниз 1px линия |
| `runRing` | 1.6s | running/retrying/revising rings и badges | расходящееся свечение (заменяет старую `pulse`) |
| `askPulse` | 1.8s | `.s-ask .ring`, dialog badge | расходящееся свечение violet |
| `badgePulse` | 2-2.4s | active status badges | inset box-shadow дыхание |
| `shimmer` | 2.2s | `.progress-fill::before` | бегущая блестка по fill |
| `tipPulse` | 1.2s | `.progress-fill::after` | пульсирующий кончик прогресса |
| `pendingBreathe` | 3.4s | `.dialog-pending` | пульсирующий box-shadow |
| `pendingScan` | 3.2s | `.dialog-pending::after` | сканлайн сверху вниз |
| `caret` | 1s | `.typing-indicator .blink` | step blink каретки |

### Сканирующий луч (`.ray`)

Технически — отдельный `<div class="ray">` поверх контента, `pointer-events: none`. Псевдоэлемент `::before` — большой эллиптический radial-gradient `var(--amber)`, повёрнутый на 20deg, движется по `translate` от верхнего левого угла к нижнему правому, с fade-in/out внутри ключевых кадров `raySweep`. Контейнер `.ray` имеет CSS mask по той же 24×24 сетке — луч видно **только на линиях grid**, что и создаёт ощущение «энергии бежит по проводнику».

```css
.ray {
  position: absolute; inset: 0;
  pointer-events: none; overflow: hidden;
  -webkit-mask-image:
    linear-gradient(rgba(0,0,0,1) 1px, transparent 1px),
    linear-gradient(90deg, rgba(0,0,0,1) 1px, transparent 1px);
  -webkit-mask-size: 24px 24px;
          mask-image:
    linear-gradient(rgba(0,0,0,1) 1px, transparent 1px),
    linear-gradient(90deg, rgba(0,0,0,1) 1px, transparent 1px);
          mask-size: 24px 24px;
}
.ray::before {
  content: ""; position: absolute;
  top: -40%; left: -40%; width: 60%; height: 180%;
  background: radial-gradient(ellipse at center,
    rgba(229, 212, 66, 0.55) 0%,
    rgba(229, 212, 66, 0.20) 25%,
    transparent 60%);
  transform: rotate(20deg);
  animation: raySweep 9s linear infinite;
  filter: blur(2px);
}
```

### Reduced motion

Один блок в начале CSS — глобально режет анимации до 0.01ms и блокирует повтор. Цвета и статусные индикаторы остаются — пользователь видит всё, просто без движения.

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}
```

## Изменения в HTML

`pkg/web/index.html` — добавляются:

1. **В шапке** перед `<h1>`:
   ```html
   <span class="logo" aria-hidden="true">
     <span class="l-ring"><svg viewBox="0 0 24 24"><polygon points="12,1 22,7 22,17 12,23 2,17 2,7"/></svg></span>
     <span class="l-arc"><svg viewBox="0 0 24 24"><path d="M 12 3 A 9 9 0 0 1 21 12"/></svg></span>
     <span class="l-core"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="2.4"/></svg></span>
   </span>
   ```
   `<h1>` остаётся (текст «flowManager» меняется на «FLOWMANAGER»).

2. **В `<main>` или в `<body>`** — `<div class="ray" aria-hidden="true"></div>` поверх контента.

3. **В `#detail-content`** (внутри `.detail-header`) — `<span class="ornament" aria-hidden="true">...SVG...</span>`.

4. **Hex-маркеры** в шапке — небольшие декоративные иконки рядом с метриками RUN/STAGES. Реализуются через `::before` псевдоэлементы в CSS, без HTML-изменений.

5. **На `<body>`** — класс `theme-novacorps` (на случай будущих тем).

## Изменения в `app.js`

Минимальные:

1. **Формат времени в feed** — добавить хелпер `formatRelativeTime(unixSec)`, возвращающий `02s` / `4m` / `1h ago`. Обновляется на каждый тик `setInterval`.
2. **Структура feed-entry** — изменить шаблон строки (сейчас `time | stage-badge | text` → `time | (stage-badge inline + text)`). Это локально в функции рендера feed.
3. **Опции диалога** — при рендере добавлять префикс с буквой `<span class="key">A</span>`. Логика выбора не меняется.
4. **Текст в шапке `<h1>`** — оставить как есть (`flowManager`), uppercase делается через CSS `text-transform: uppercase`.

Всё остальное — `lineComments`, `dialogState`, WS-обработчики, polling — без изменений.

## Favicon

`pkg/web/favicon.svg` — перерисовать:
- Гексагон stroke `#6fd4cc`
- Янтарная дуга stroke `#e5d442`
- Центральная точка fill `#6fd4cc`
- Фон `#08141a`

## Тестирование

1. Открыть дашборд при запущенном flow с тремя стадиями (одна done, одна running, одна pending) — проверить статус-кольца, scanline на active, прогресс-бар.
2. Открыть стадию в `awaiting_approval` — проверить план, inline-комментарии (клик по строке → форма → отправка).
3. Открыть стадию в `failed` — проверить кнопку retry и coral-цвет.
4. Запустить interactive flow (`example-flow-interactive.yaml`) — проверить диалог, выбор опции, custom textarea, отмену, сворачивание истории.
5. Включить системную настройку «уменьшить движение» (macOS: Settings → Accessibility → Display) — убедиться, что все scanline/breathe/ray анимации остановлены, статусы видны.
6. Открыть на узком окне (≤1024px) — проверить, что 3-колонка не ломается до неприлично узких столбцов. Если ломается — добавить media query (минорный вопрос, можно отложить).
7. WS-разрыв и реконнект — индикатор `LINK` → `OFFLINE` цвет и анимация.

## Открытые мелочи (не блокеры)

- Шрифт «JetBrains Mono» по умолчанию не установлен на маках — без него падаем на `SF Mono`/`Menlo`, что тоже моноширинно и выглядит хорошо. Если захочется гарантировать конкретный шрифт — можно подключить через embed (но это вес и зависимость). Решение: оставить системный fallback.
- На очень слабых машинах непрерывные scanline-анимации могут чуть кушать CPU. Все анимации работают на `transform`/`opacity` — GPU-ускоренные, должно быть незаметно. Если станет проблемой — добавим throttling через `animation-play-state` при hidden tab (можно через `document.visibilitychange`).

## Следующий шаг

После одобрения этого спека — написать имплементационный план (через `superpowers:writing-plans`), который разбьёт работу на дискретные шаги (палитра/типографика → шапка с логотипом → панели → стадии → план → диалог → анимации → reduced-motion → ручные тесты).

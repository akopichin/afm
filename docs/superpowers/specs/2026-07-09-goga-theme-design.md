# Дизайн: вторая тема дашборда «goga»

- Дата: 2026-07-09
- Статус: одобрен, ожидает плана реализации
- Ветка: `theme`

## Контекст

У дашборда afm (`pkg/web/dashboard/`) одна тема — тёмная hi-tech «Nova Corps»
(`style.css`), захардкожена в `index.html:10` как `<body class="theme-novacorps">`.
CSS-токены объявлены в `:root` (`style.css:23-56`); в CSS много хардкоженных
`rgba(111,212,204,…)` (mint) и `rgba(8,20,26,…)` (фон панелей) вне токенов.
Шрифт — моноширинный (JetBrains Mono), base 11px. Много неон-декора: scanlines,
ray-sweep, shimmer, notched corners, spin-logo.

Конфиг (`pkg/config/config.go`) не содержит поля темы; эндпоинта `/api/config` нет;
`app.js` конфиг не читает. Проброса темы в браузер не существует — его нужно создать.

**Цель:** вторая тема, визуально максимально похожая на сайт goga
(https://qarium.ru/goga), включаемая флажком `theme: goga` в `~/.afm/config.yaml`.

**Выбор глубины:** отдельный самодостаточный CSS-файл `style-goga.css` (стиль с нуля,
не переиспользует структуру Nova Corps). Nova Corps (`style.css`) НЕ трогается.

## Референс: визуал сайта goga

Тёмная, чистый tech-стиль (Tailwind). Палитра:

| Назначение | HEX |
|---|---|
| Фон страницы | `#0A0E1A` |
| Фон карточек | `#121830` |
| Текст основной | `#FFFFFF` |
| Текст muted | `#A0AEC0` |
| Акцент primary (brand-teal) | `#20D4BF` |
| Акцент secondary (brand-blue) | `#3882F6` |
| Бордюр карточек | `rgba(255,255,255,0.05)` |

- Шрифт: system sans-serif (`ui-sans-serif, system-ui, sans-serif, …`); код — monospace.
- Сайт: base 16px, h1 60px. Для плотного дашборда берём base 13px.
- border-radius: карточки 12px, кнопки 8px, бейджи pill (`9999px`), код 4px.
- Градиенты мягкие (radial-glow teal); header — glass (`backdrop-filter: blur(12px)`);
  box-shadow почти нет. Без scanlines/неона.

## Решение (выбранный подход)

**Доставка темы — server-side replace в index.html (подход A).**

Сервер при отдаче `/` (и `/index.html`) подставляет `style-goga.css` + класс
`theme-goga` вместо `style.css` + `theme-novacorps`, если активна тема goga.
Без FOUC, без нового эндпоинта, без JS-логики темы. Default (Nova Corps) отдаётся
как есть — нулевой overhead, `style.css` и `index.html` для default не меняются.

Дополнительно — мини-правка `app.js`: палитра графика потребления (`USAGE_COLORS`,
сейчас хардкод mint) читается из CSS-токенов с fallback на mint, чтобы график стал
teal в goga и не изменился в Nova Corps.

## Детали по секциям

### 1. Конфигурация (`pkg/config/config.go`)

- Новое top-level поле `Config.Theme string` (`yaml:"theme"`). Соответствует
  буквально `theme: goga` из запроса. Поле кладётся на верхний уровень `Config`
  (не в `ServerConfig`), чтобы совпадать с записью `theme:` в YAML.
- `mergeFile` (`config.go:172`): добавить
  `if overlay.Theme != "" { dst.Theme = overlay.Theme }` — тот же nil-паттерн, что у
  соседних полей (только непустое значение перетирает). `Default()` тему не задаёт
  (пусто = default).
- Новый метод `(c Config) EffectiveTheme() string`:
  - `t := strings.ToLower(strings.TrimSpace(c.Theme))`
  - `t == "goga"` → вернуть `"goga"`
  - иначе (пусто / `"novacorps"` / опечатка / любой другой) → если `c.Theme != ""`
    и не `novacorps`: `fmt.Fprintf(os.Stderr, "warning: unknown theme %q, using novacorps\n", c.Theme)`;
    вернуть `"novacorps"`.
- Проброс в `run.go:189` (`server.Config{...}`): добавить `Theme: cfg.EffectiveTheme()`.
- `server.Config` (`pkg/server/server.go:31`): новое поле `Theme string`.
- `server.New` (`server.go:49`): сохранить `theme: cfg.Theme` в `Server`.

### 2. Доставка темы (`pkg/server/server.go`)

- `server.go:67`: заменить
  `mux.Handle("/", http.FileServer(http.FS(web.FS)))` на
  `mux.HandleFunc("/", s.serveStatic)`.
- `serveStatic`:
  - если `r.URL.Path == "/" || r.URL.Path == "/index.html"` → вызвать `s.serveIndex(w, r)`;
  - иначе → `s.fileServer.ServeHTTP(w, r)`, где `s.fileServer` создаётся один раз в
    `New` (`http.FileServer(http.FS(web.FS))`), не на каждый запрос.
- В `New`: прочитать `indexBytes, err := web.FS.ReadFile("index.html")`. При ошибке
  (невозможна для embed, но защищаемся) — залогировать в stderr и оставить
  `indexBytes = nil`. В `serveIndex` проверять: если `indexBytes == nil`, делегировать
  на `s.fileServer.ServeHTTP(w, r)` (защита от регрессии embed), не паниковать.
- Если `cfg.Theme == "goga"`:
  - `indexBytes = bytes.ReplaceAll(indexBytes, []byte("href=\"style.css\""), []byte("href=\"style-goga.css\""))`
  - `indexBytes = bytes.ReplaceAll(indexBytes, []byte("class=\"theme-novacorps\""), []byte("class=\"theme-goga\""))`
- Закешировать в `s.indexBytes`.
- `serveIndex`: `w.Header().Set("Content-Type", "text/html; charset=utf-8"); w.Write(s.indexBytes)`.
- `style-goga.css` отдаётся `fileServer` автоматически (попадает в существующий
  `//go:embed dashboard/*` в `pkg/web/embed.go`).
- В `server.go` добавить импорт `bytes`. Структура `Server` получает поля
  `theme string`, `indexBytes []byte`, `fileServer http.Handler`.

Подстроки замен уникальны и встречаются в `index.html` ровно по одному разу
(`href="style.css"` — строка 8, `class="theme-novacorps"` — строка 10), других
совпадений нет — `bytes.ReplaceAll` безопасен.

### 3. goga-CSS (`pkg/web/dashboard/style-goga.css`)

Самодостаточный полный CSS: reset + `:root` токены + все правила для всех
DOM-классов из `index.html` и `app.js`. Не наследует правила из `style.css`
(при теме goga грузится только `style-goga.css`).

**Токены** (те же имена, что в Nova Corps — обязательно, иначе inline-стили `app.js`
`var(--c-awaiting)` / `var(--text)` в `appendCommentDisplay` сломаются):

```
--bg:        #0A0E1A;
--bg-elev:   #121830;
--ink:       #FFFFFF;
--ink-hi:    #FFFFFF;
--ink-dim:   #A0AEC0;
--mint:      #20D4BF;   /* primary teal */
--mint-soft: rgba(255, 255, 255, 0.08);  /* бордюры панелей */
--grid:      rgba(255, 255, 255, 0.03);  /* почти невидимая сетка фона */
--amber:     #3882F6;   /* brand-blue — маппинг «бывший жёлтый secondary» */
--violet:    #3882F6;   /* диалог → blue */
--coral:     #FF6B5A;   /* failed/retry — красный */
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
/* новый токен для графика (читается app.js) */
--usage-grid: rgba(255, 255, 255, 0.06);
```

**База:** system sans-serif
(`ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif`),
base 13px, line-height 1.55. (Сайт goga использует 16px, но дашборд плотнее — 13px.) Код-блоки/лог/`pre`/`code` — monospace стек
(`ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace`).

**Эстетика goga:**
- border-radius: панели/карточки 12px, кнопки 8px, бейджи pill (`9999px`), инлайн-код 4px.
- Без scanlines / ray-sweep / shimmer / notched corners / spin-logo. Чистый фон,
  мягкие бордюры `rgba(255,255,255,0.08)`.
- Header — glass: фон `rgba(10,14,26,0.8)` + `backdrop-filter: blur(12px)`.
- Box-shadow почти нет; допускается мягкое свечение `0 0 10px rgba(32,212,191,…)`
  на hover активных состояний (опционально, умеренно).

**Кнопки:**
- `.btn-approve` — primary: bg `--mint`, текст `--bg`, border `--mint`.
- `.btn-revise` — outline blue (`--amber`).
- `.btn-retry` — outline red (`--coral`).
- `.btn-send` — teal (bg `--mint`, тёмный текст).
- `.btn-cancel` / `.btn-cancel-dialog` — muted outline (`--ink-dim`).

**Анимации:** минимизированы. Оставить мягкие hover-transitions и лёгкий пульс
`running` / `awaiting_user_input` у `.status-dot` (без неона). В начале файла —
`@media (prefers-reduced-motion: reduce)` gate (как в `style.css:7-13`).

**Покрытие классов** (полный список из `index.html` + `app.js`):
- layout: `#header`/`.logo`/`.flow-name`/`.ws-status`(+`.connected`/`.disconnected`),
  `#main`/`.ray`(скрыт), `#stages-panel`/`#detail-panel`/`#feed-panel`, `#footer`/
  `.footer-item`/`.footer-label`, `.progress-bar`/`.progress-fill` (teal→blue градиент,
  без shimmer).
- stages: `#stages-list`, `.stage-item`(+`.active`), `.stage-label`/`.stage-id`/
  `.stage-name`, `.status-dot[data-status=…]` (10 статусов), `.dialog-badge`.
- detail: `.detail-empty`/`.detail-content`(+`.hidden`), `.detail-header`/
  `.ornament`, `.status-badge[data-status=…]`, `.section`/`.section h3`,
  `.empty-hint`, `.markdown-body` (+ h1-h3, p, ul/ol, code, pre, strong, tables),
  `.cb`/`.cb-done`/`.cb-open`.
- plan review: `#plan-content`, `.plan-line`(+`.has-comment`), `.line-num`/
  `.line-content`/`.line-comment-marker`, `.line-comment-form`/
  `.line-comment-display`/`.comment-actions`, `.plan-section-wrapper`
  (+`.plan-section-assumptions`/`.plan-section-criteria`)/`.section-header`/
  `.plan-section-body`/`.toggle`(+`.collapsed`).
- actions: `.btn`/`.btn-approve`/`.btn-revise`/`.btn-retry`/`.btn-send`/`.btn-cancel`,
  `.actions-row`, `.comment-hint`.
- log: `.log-content`(+`:empty`), `.log-text-line`/`.log-error-line`.
- dialog: `#dialog-section`(+`.dialog-flash`), `.dialog-history`(+`.collapsed`)/
  `.qa`/`.q`/`.a`/`.agent-msg`(+`.md`)/`.phase-divider`, `.dialog-pending`/
  `.dialog-question`/`.dialog-options`/button(+`.selected`, опции)/`.dialog-custom`/
  `.dialog-actions`/`.typing-indicator`/`.blink`, `.dialog-toggle`, `.sr-only`.
- feed: `#feed-content`, `.feed-entry`/`.feed-time`/`.feed-msg`(+`.action`/`.error`),
  `.feed-stage-badge`(+`status-*`).
- markdown shared: `.md` (p, table, th/td, blockquote, em, a, ul/ol, hr).
- usage panel: `.usage-panel`(+`.open`)/`.usage-toggle`/`.usage-toggle-arrow`/
  `.usage-panel-body`/`.usage-panel-head`/`.usage-total`/`.usage-controls`/
  `.usage-metric-switch`/`.usage-metric`(+`.active`/`.hidden`)/`.usage-stage-filter`/
  `.usage-stage-label`/`#usage-stage-select`/`.usage-chart-wrap`/`.usage-chart`/
  `.usage-empty`/`.usage-meta`.
- утилиты: `.hidden`, `a`, `::selection`.

### 4. app.js — палитра графика theme-aware (`app.js:75-80`)

Заменить хардкод `USAGE_COLORS` на чтение CSS-токенов с fallback на текущие
mint-значения (поведение Nova Corps сохраняется):

```js
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

Nova Corps: `--mint`/`--amber`/`--ink-dim` определены (те же значения), `--usage-grid`
нет → fallback на mint → график не меняется. goga: `--mint=#20D4BF`,
`--amber=#3882F6`, `--ink-dim=#A0AEC0`, `--usage-grid=rgba(255,255,255,0.06)` →
график teal/blue. Это единственная правка `app.js`.

### 5. Обработка ошибок / крайние случаи

- `web.FS.ReadFile("index.html")` ошибка (невозможна для embed на практике) →
  `serveIndex` делегирует на `fileServer` (защита от регрессии embed), не паникует.
- Неизвестное `theme:` → warning в stderr + novacorps (секция 1).
- `server.port == 0` (дашборд выключен): тема не используется, проброс безвреден
  (`server.New` не вызывается при `port == 0` — `run.go:188`).
- `bytes.ReplaceAll` безопасен: подстроки уникальны (см. секцию 2).
- `prefers-reduced-motion`: обе темы имеют gate; goga и так почти без анимаций.

### 6. Тестирование (следовать паттернам `pkg/config/config_test.go`, `pkg/server/server_test.go`)

- **config**: `mergeFile` с `theme: goga` → `cfg.Theme == "goga"` (и непустое перетирает,
  пустое — нет). `EffectiveTheme()`: `"goga"`→`"goga"`, `""`→`"novacorps"`,
  `"novacorps"`→`"novacorps"`, `"GOGA"`→`"goga"` (case-insensitive), `"unknown"`→
  `"novacorps"`.
- **server**: handler `/` при `Theme="goga"` отдаёт HTML, содержащий `style-goga.css`
  и `theme-goga`, и НЕ содержащий `style.css` / `theme-novacorps`; при `Theme=""`
  (default) — наоборот (`style.css` / `theme-novacorps`). Проверить, что
  `GET /style-goga.css` → 200 (отдаётся FileServer). Использовать `s.Handler()`
  (`server.go:102`) для тестов без реального listen.
- **app.js**: ручная проверка (vanilla JS без build-шага); убедиться, что
  `USAGE_COLORS` читает токены и fallback работает (открыть дашборд, открыть панель
  потребления, сверить цвета графика с темой).
- Линт: `go vet ./...` + `golangci-lint run` (revive/govet/errcheck/unused/gosec/
  staticcheck/copyloopvar/goconst/ineffassign). Импорт `bytes` используется — без
  unused. errcheck: `w.Write` попадает под исключение `Write` в `.golangci.yml`.
  Go-версию в `go.mod` (1.26) НЕ трогать (правило из CLAUDE.md).

### 7. Что НЕ делаем (YAGNI)

- Не добавляем runtime-переключатель тем в UI — тема статична из конфига при старте
  сервера.
- Не делаем эндпоинт `/api/config` (не нужен для подхода A).
- Не трогаем `style.css`, `index.html` (для default), `favicon.svg`,
  `markdown-it.min.js`, `pkg/web/embed.go`.
- Не реорганизуем существующий CSS/структуру — только новый файл `style-goga.css`
  и мини-правки проброса конфига/сервера + правка `USAGE_COLORS` в `app.js`.

## Файлы для изменения

| Файл | Изменение |
|---|---|
| `pkg/config/config.go` | +`Theme string` в `Config`; мерж в `mergeFile`; `EffectiveTheme()` |
| `pkg/server/server.go` | +`Theme` в `Config`/`Server`; `serveStatic`/`serveIndex`; `bytes` replace; `fileServer` |
| `cmd/afm/run.go` | проброс `Theme: cfg.EffectiveTheme()` в `server.Config` (строка ~189) |
| `pkg/web/dashboard/style-goga.css` | новый полный CSS (goga-эстетика) |
| `pkg/web/dashboard/app.js` | `USAGE_COLORS` → чтение CSS-токенов с fallback (строки 75-80) |
| `pkg/config/config_test.go` | тесты `Theme`/`EffectiveTheme()` |
| `pkg/server/server_test.go` | тесты handler `/` для goga и default |

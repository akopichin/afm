# Дизайн: скины дашборда (skins) с внешним override и dark/light режимом

- Дата: 2026-07-20
- Статус: одобрен, ожидает плана реализации
- Ветка: `ux-improvements`

## Контекст

Дашборд afm (`pkg/web/dashboard/`) сейчас имеет 2 темы, захардкоженные в Go:
`public/style.css` (Nova Corps, ~1835 строк — вся структурная разметка + токены)
и `public/style-goga.css` (94 строки — `@import url("./style.css")` + свои
design-токены + пара точечных selector-override под `.theme-goga`). Выбор темы —
поле `Config.Theme` (`theme: goga` в `.afm/config.yaml`), нормализуемое
`EffectiveTheme()` до одного из двух известных имён. Доставка — server-side
строковая замена в `index.html` (`href="./style.css"` → `href="./style-goga.css"`,
`class="theme-novacorps"` → `class="theme-goga"`) при старте сервера, без FOUC и
без JS-логики. CSS полностью независим от React-сборки — `main.tsx` его не
импортирует, это чистая статика, отдаваемая `http.FileServer` поверх embed.FS.

Своей темы «подложить» нельзя — только два хардкод-имени. Dark/light режима нет:
обе темы — фиксированные тёмные палитры. Аналог уже существует для промптов:
`prompts_dir` (`assets.ReadPrompt(name, overrideDir)`) — если задан, ПОЛНОСТЬЮ
подменяет embedded-файлы на файлы с диска, без слияния.

**Цель:** превратить темы в скины (skin) — так же выбираемые в конфиге, но:
1. подменяемые внешней директорией (`skin_dir`, аналогично `prompts_dir`);
2. по-настоящему собираемые из переиспользуемых компонентов, чтобы кто-то мог
   перекрасить/поправить существующие куски и получить новый скин;
3. поддерживающие dark/light как отдельную ось поверх скина, переключаемую в UI.

## Архитектура

### Конфигурация

- `theme:` в YAML остаётся как есть (без переименования в `skin:` — сознательное
  решение, чтобы не ломать существующие `.afm/config.yaml`). Выбирает один из
  ВСТРОЕННЫХ скинов (`novacorps` — default, `goga`).
- Новое поле `SkinDir string` (`yaml:"skin_dir"`), тот же merge-паттерн, что у
  `PromptsDir` (`config.go:282`): непустое значение overlay перетирает. Если
  `skin_dir` задан — он ПОЛНОСТЬЮ подменяет активный скин (никакого слияния с
  `theme`), ровно как `prompts_dir` подменяет промпты. Если заданы оба —
  `skin_dir` побеждает; при старте один раз в stderr: предупреждение, что `theme`
  игнорируется, пока задан `skin_dir`.

### Файловый слой скинов

```
pkg/web/dashboard/skins/
  base/                    ← нейтральные структурные партиалы, по одному на
                             компонент дашборда (без цвета/декора)
    reset.css               (box-sizing reset + prefers-reduced-motion gate)
    layout.css              (#main grid, resizable panels, footer bar,
                              auto-scroll, attention-сигнал)
    header.css              (#header, .logo/.l-ring/.l-arc/.l-core — БЕЗ цвета,
                              цвет через var(--mint)/var(--amber), см. ниже)
    stages-list.css
    plan-panel.css          (detail header, status badges, plan block,
                              line-by-line review, comment form, action buttons,
                              markdown)
    log-panel.css
    dialog-channel.css      (dialog section, history, pending question, опции,
                              textarea, actions, typing indicator, toggle)
    event-feed.css
  novacorps/
    index.css               (@import ../base/*.css поимённо + :root[data-theme=
                              dark] и :root[data-theme=light] токены + СВОЯ
                              неоновая декорация: scanlines overlay, yellow
                              scanner ray, spin-логотип — переехали сюда из base,
                              т.к. это не структура, а декор конкретного скина)
    favicon.svg              (текущий дефолтный, просто перемещённый файл)
  goga/
    index.css               (@import ../base/*.css + свои dark/light токены +
                              точечные goga-only override: hidden wordmark → CSS
                              ::after 'goga', плоские панели, glass header —
                              то же, что уже есть в style-goga.css сегодня, просто
                              base теперь чист, без .ray/scanlines, которые раньше
                              приходилось прятать через .theme-goga .ray{display:none})
```

Никакого build-шага для CSS не добавляется — `@import` разрешается браузером в
рантайме, как уже делает сегодняшний `style-goga.css`. Кастомный скин может
`@import url("/skins/base/header.css")` (абсолютный путь) и переопределить только
токены/декор — это и есть механизм «собрать свой скин из существующих
компонентов».

### Доставка (Go)

- `pkg/web/embed.go`: `//go:embed dashboard/skins` вместо
  `dashboard/style.css dashboard/style-goga.css` (директория embed'ится целиком).
- `pkg/server/server.go`: маршрут `/skins/` отдаёт embedded-дерево (как сегодня
  `fileServer` отдаёт `style.css`). Если `cfg.SkinDir != ""`:
  - при старте проверяется `filepath.Join(SkinDir, "index.css")` существует;
    если нет — `fmt.Fprintf(os.Stderr, "warning: skin_dir %q has no index.css, using default skin\n", ...)`
    и сервер работает как при пустом `SkinDir` (fallback на `theme`/novacorps) —
    дашборд не критичен для работы флоу, в отличие от промптов, поэтому не валим
    старт;
  - если файл есть — дополнительно монтируется `/skins/custom/` через
    `http.FileServer(http.FS(os.DirFS(cfg.SkinDir)))` (живой диск, не embed);
  - `index.html`: `href` стиля → `/skins/custom/index.css`; `<link rel="icon">`
    → `/skins/custom/favicon.svg`, если такой файл существует в `SkinDir`, иначе
    остаётся дефолтный `favicon.svg`; body-класс → `theme-custom` (для скоупинга
    собственных override-правил кастомного скина, если он в них нуждается).
- Без `skin_dir`: `href` стиля → `/skins/<theme>/index.css` (`theme` = результат
  `EffectiveTheme()`), body-класс → `theme-<theme>`, favicon — как в
  `skins/<theme>/favicon.svg`, если файл есть, иначе дефолт. Логика замен та же
  строковая `bytes.ReplaceAll`, что и сегодня — просто параметризована именем
  активного скина вместо жёсткого `"goga"`.

### Dark/light режим

Ось, независимая от скина. Каждый `index.css` скина ОБЯЗАН определить оба блока
токенов — `:root[data-theme="dark"] { --bg: ...; ... }` и
`:root[data-theme="light"] { --bg: ...; ... }` (автор скина полностью
контролирует обе палитры; никакой автогенерации/инверсии).

- **Бутстрап без FOUC:** инлайн `<script>` в `<head>` `index.html`, ДО `<link
  rel="stylesheet">`, синхронно выставляет `data-theme` на `<html>`:
  `localStorage.getItem('afm-mode')`, иначе
  `matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'`.
- **React-хук** `src/hooks/use-theme-mode/` (структура папки — как у соседних
  хуков: `index.ts`, `use-theme-mode.ts`, `.test.ts`, `CODEMANIFEST`): читает
  текущее значение `document.documentElement.dataset.theme` как начальное
  состояние, отдаёт `{ mode, toggle }`. `toggle()` меняет атрибут на
  `<html>` и пишет `localStorage['afm-mode']`. Живые изменения OS-темы после
  первого ручного переключения не отслеживаются (простое и предсказуемое
  поведение — выбор пользователя один раз зафиксирован).
- **UI-переключатель:** небольшая кнопка в Footer (`src/components/footer/`),
  рядом с существующими footer-item — самое нейтральное место, не конкурирует с
  header (flow-name/ws-status). Точное визуальное оформление — деталь
  реализации, не архитектуры.

### Точечный фикс, обнаруженный в ходе анализа

`FlowHeader.tsx` хардкодит цвета SVG-логотипа инлайн (`stroke="#6fd4cc"`,
`stroke="#e5d442"`) вместо токенов `var(--mint)`/`var(--amber)` — из-за этого
логотип-«шестигранник» сегодня рисуется в цветах Nova Corps даже под темой goga.
Это прямо мешает цели «перекрасить компонент → новый скин», поэтому исправляется
в рамках этой работы: цвета переезжают в `base/header.css` как
class-scoped-правила (`.l-ring svg { stroke: var(--mint); }` и т.д.), инлайн
`stroke`/`fill` из JSX убираются.

## Обработка ошибок и краевые случаи

- `skin_dir` задан, но директории/`index.css` нет → warning в stderr, fallback на
  `theme` (или default novacorps, если `theme` тоже не задан). Сервер не падает.
- `skin_dir` и `theme` заданы одновременно → `skin_dir` побеждает, one-line
  warning в stderr, что `theme` игнорируется.
- `index.css` кастомного скина ссылается через `@import` на несуществующий
  `base`-партиал → браузер получит 404 на этот конкретный `@import`, остальной
  CSS скина применяется как есть. Без Go-валидации — та же логика, что и у любой
  сегодняшней ручной CSS-опечатки.
- `favicon.svg` отсутствует в скине (embedded или custom) → используется общий
  дефолтный `favicon.svg` (текущий, не входит в состав скина).
- `server.port == 0` (дашборд выключен) → скины не используются, `server.New`
  не вызывается, ветка безвредна (как и для `theme` сегодня).
- Скин определил только `data-theme="dark"` (забыл `light`, или наоборот) → без
  Go-валидации; в непокрытом режиме токены просто не переопределены (следующий
  по каскаду `:root` без атрибута, если скин его объявил, либо не окрашенные
  элементы) — авторская ответственность, как и сегодняшнее требование к goga
  использовать те же имена токенов, что и novacorps.
- Неизвестное `theme:` (не `novacorps`/`goga`) → как сегодня: warning в stderr +
  fallback на `novacorps`.

## Тестирование

- **Go, `pkg/config`:** merge `skin_dir` (непустое перетирает, пустое — нет);
  сценарий «оба поля заданы» не валится при `Load`/`mergeFile` (сама логика
  выбора — на стороне `server`, не `config`).
- **Go, `pkg/server`:** таблица случаев для `/`:
  - `Theme=""` → `/skins/novacorps/index.css` + `theme-novacorps`;
  - `Theme="goga"` → `/skins/goga/index.css` + `theme-goga`;
  - `SkinDir=<tmpdir с index.css>` → `/skins/custom/index.css` + `theme-custom`,
    и `GET /skins/custom/index.css` отдаёт содержимое из `tmpdir`, а не embed;
  - `SkinDir=<tmpdir без index.css>` → fallback на `Theme`/novacorps + проверка,
    что предупреждение ушло в stderr (или в лог-хук, если такой есть в тестах
    сервера);
  - `SkinDir` и `Theme="goga"` одновременно → `SkinDir` побеждает.
  - `GET /skins/<name>/index.css`, `GET /skins/base/<partial>.css` → 200 из
    embed (регрессия embed-путей после смены `//go:embed` директивы).
- **React:** `use-theme-mode.test.ts` (jsdom, мок `localStorage` и
  `matchMedia`) — дефолт из `matchMedia`, если `localStorage` пуст; persisted
  значение из `localStorage` побеждает; `toggle()` меняет
  `document.documentElement.dataset.theme` и пишет `localStorage`.
- **CSS:** без автотестов (как и сегодня) — ручная визуальная сверка
  `novacorps`/`goga` до и после рефакторинга (скриншот-diff глазами: раскладка,
  цвета, декор не должны отличаться от текущего состояния).
- Линт: `go vet ./...` + `golangci-lint run` (тот же набор, что в
  `2026-07-09-goga-theme-design.md`). Go-версию в `go.mod` не трогать (правило
  из CLAUDE.md).

## Что НЕ делаем (YAGNI)

- Не переименовываем `theme:` в `skin:` в YAML (осознанное решение пользователя).
- Не добавляем `manifest.json`/`skin.json` — метаданные (favicon, wordmark) через
  соглашение об именах файлов и обычный CSS (`::after { content: '...' }`), без
  парсинга JSON на сервере.
- Не добавляем сборку/бандлинг CSS (PostCSS/Vite-плагин) — многофайловость
  скинов разрешается нативным `@import` браузера.
- Не создаём третий «образцовый» скин как proof-of-concept — только архитектура
  + перенос `novacorps` и `goga` на неё.
- Не отслеживаем живые изменения `prefers-color-scheme` после того, как
  пользователь один раз явно переключил режим.
- Не делаем skin-picker UI в дашборде (скин выбирается только конфигом,
  `theme`/`skin_dir` на старте сервера).

## Файлы для изменения

| Файл | Изменение |
|---|---|
| `pkg/config/config.go` | +`SkinDir string` (`yaml:"skin_dir"`) в `Config`; merge в `mergeFile` |
| `pkg/server/server.go` | параметризация выбора скина (embedded `theme` vs `skin_dir`), монтирование `/skins/custom/`, fallback+warning при отсутствии `index.css`, favicon-override |
| `pkg/web/embed.go` | `//go:embed dashboard/skins` вместо `style.css`/`style-goga.css` |
| `pkg/web/dashboard/skins/base/*.css` | новые партиалы — разбор текущего `style.css` по компонентам, БЕЗ novacorps-декора |
| `pkg/web/dashboard/skins/novacorps/index.css` | `@import` партиалов base + токены dark/light + перенесённый неоновый декор |
| `pkg/web/dashboard/skins/goga/index.css` | `@import` партиалов base + свои токены dark/light + существующие goga-override |
| `pkg/web/dashboard/index.html` | инлайн bootstrap-скрипт для `data-theme` до `<link rel="stylesheet">` |
| `pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx` | убрать хардкод `stroke`/`fill`, использовать классы |
| `pkg/web/dashboard/src/hooks/use-theme-mode/` | новый хук + тест |
| `pkg/web/dashboard/src/components/footer/Footer.tsx` | кнопка-переключатель dark/light |
| `pkg/config/config_test.go` | тесты merge `SkinDir` |
| `pkg/server/server_test.go` | таблица случаев выбора скина (см. «Тестирование») |
| `pkg/web/dashboard/public/style.css`, `style-goga.css` | удаляются (заменены на `skins/`) |

## Дополнение (2026-07-20, после ревью): quarium-логотип, title, favicon-картинка

После первого одобрения этого спека на ветке `theme-fix` был найден и перенесён
(cherry-pick) более ранний набор коммитов, которые дают goga собственный
логотип-картинку (`quarium-logo.png`), двухцветный wordmark «QArium» (вместо
простого текста «goga») и переопределение `<title>`/favicon. На момент
первого одобрения эта работа не была на ветке и не учитывалась в контракте
скина — ниже фиксируются возникшие из-за неё изменения контракта.

**Что реально сейчас в `style-goga.css` (актуальное поведение goga, из
которого исходит планирование задач ниже):**
- `#header h1` полностью скрыт (`font-size: 0`); текст «QA» (цвет `--mint`) и
  «rium» (цвет `--ink`) рисуются через `::before`/`::after` системным
  sans-serif — а не через один `::after` с текстом «goga», как в
  первоначальной версии спека.
- `.logo > span` (все дочерние SVG-спаны анимированного шестигранника)
  скрыты; сам `.logo` получает `background-image: url('./quarium-logo.png')`
  — логотип теперь картинка, а не перекрашенный SVG.
- `#header`/`#footer` — фон `var(--bg-elev)`, не `var(--bg)`; текст
  `#progress-text`/`#started-at`/`#elapsed` — `var(--mint)`.
- `pkg/server/server.go`: при `theme=="goga"` дополнительно подменяются
  `<title>afm Dashboard</title>` → `<title>QArium</title>` и
  `type="image/svg+xml" href="./favicon.svg"` →
  `type="image/png" href="./quarium-logo.png"` (favicon — не svg, а
  растровая картинка, с другим `type` в `<link>`).
- ~28 хардкодных низко-альфовых акцентных заливок (rgba mint/amber washes —
  hover-фоны стадий/plan-line/event-feed, границы, `::selection`, keyframe
  `attentionPulse`) явно перекрашены под goga-палитру (`rgba(32,212,191,X)`/
  `rgba(56,130,246,X)` вместо novacorps `rgba(111,212,204,X)`/
  `rgba(229,212,66,X)`). Это НЕ токенизировано (см. «Что НЕ делаем» —
  дизайн сознательно не токенизирует все низко-альфовые декоративные
  заливки) — goga просто переопределяет каждый селектор отдельно, тем же
  приёмом, что и `#main`/`#header h1`.

**Изменения контракта скина, вытекающие из этого:**
1. **`<title>`** — новый опциональный файл скина `title.txt` (первая
   строка = текст `<title>`). Если файла нет — `<title>` не подменяется
   (остаётся `afm Dashboard`). Встроенный `skins/goga/title.txt` содержит
   `QArium`; `skins/novacorps/` файла не имеет.
2. **Favicon — не только svg.** Расширение файла (`.svg` или `.png`)
   определяет `type` в `<link rel="icon">` (`image/svg+xml` /
   `image/png`). Конвенция ищет `favicon.svg` ИЛИ `favicon.png` (в этом
   порядке) в директории скина; embed для `skins/goga/` получает
   `favicon.png` — копию `quarium-logo.png` (тот же файл, что и лого,
   дублируется под именем `favicon.png`, чтобы не плодить особый случай
   «goga favicon = его же лого-файл» в коде сервера).
3. **Логотип может быть картинкой, не только перекрашенным SVG.** Это не
   требует нового кода на сервере — скин просто добавляет свои CSS-правила
   (`.logo > span { display:none }` + `background-image` на `.logo`) в
   свой `index.css`, ровно как уже делает для `#main`/`#header h1`. Сама
   картинка (`quarium-logo.png`) кладётся рядом с `index.css` внутри
   `skins/goga/` и раздаётся тем же `/skins/goga/*` маршрутом, что и CSS.
4. **Акцентные low-alpha заливки остаются хардкодом в base-партиалах**
   (решение спека не пересматривается) — goga просто переопределяет их
   явно в своём `index.css`, отдельным блоком по образцу текущего
   `style-goga.css:129-180`. Конкретное распределение по новым
   base-партиалам фиксируется в implementation-плане (см. план, Task 4).

Дизайн-намерение (base = нейтральная структура, скин = токены + декор)
не меняется — просто у декора появляется больше форм (картинка-лого,
title, favicon-картинка), все реализуемые тем же «скин переопределяет
после `@import`» механизмом, без нового кода в server.go кроме
title/favicon-расширения, описанного выше.

# Дизайн: кофейно-ламповый скин «coffee» для дашборда

**Дата:** 2026-07-24
**Статус:** согласован, готов к плану реализации

## Цель

Новый скин дашборда afm с тёплой кофейной палитрой и «ламповой» (свечение
радиолампы) эстетикой вместо холодного neon-скина novacorps. Скин становится
дефолтным. Мокапы согласованы (артефакт с тремя направлениями + сравнение
диалогового акцента).

## Как устроены скины (контекст)

Скин — это один файл `index.css`, который:
1. `@import`-ит все `base/*.css` (структура/лейаут — общая для всех скинов);
2. задаёт **только палитру токенов** (CSS custom properties) в блоках
   `:root[data-theme="dark"]` и `:root[data-theme="light"]`;
3. переопределяет немногочисленные декор-правила и захардкоженные в base
   низкоальфовые заливки (те, что не токенизированы) — так же, как это делает
   скин `goga`.

Скин выбирается строкой `<link rel="stylesheet">` в `index.dev.html`.
`scripts/restore-index.js` копирует `index.dev.html` → `index.html` перед
`vite`/`vite build`, поэтому **источник правды — `index.dev.html`**; собранный
`index.html` (его встраивает `pkg/web`) регенерируется сборкой.

Тема (light/dark) переключается атрибутом `data-theme` на `<html>` (хук
`use-theme-mode`) — скин обязан задавать обе палитры.

Как и все ассеты дашборда, файлы скина существуют в двух местах:
`public/skins/…` (источник, dev) и `skins/…` (билд-выход, который `//go:embed`
встраивает). `vite build` копирует `public/*` → корень. Правки вносим в
`public/skins/coffee/`, финальное состояние `skins/coffee/` даёт сборка.

## Палитра

### Dark — «Ламповый» (вариант B)

```
--bg:        #150d07
--bg-elev:   #1e140b
--panel-bg:  rgba(21, 13, 7, 0.92)

--ink:       #f0e2c6
--ink-hi:    #fff3d6
--ink-dim:   #a07d4f

--mint:      #e8b25e   /* карамель — ready/done/planning */
--mint-soft: rgba(255, 154, 52, 0.22)
--grid:      rgba(255, 180, 90, 0.05)
--amber:     #ff9a34   /* нить накала — running/attention/retrying/awaiting_approval */
--violet:    #a6c188   /* матча — awaiting_user / диалог */
--coral:     #e85a28   /* обжаренный красный — failed */

--qa-answer:              #d8b98a
--dialog-option-text:     #c2d6a6   /* светлая матча, в тон диалогу */
--btn-cancel-dialog-text: #ff8a5a
--supervisor-accent:      #f0b45e
```

### Light — «Латте» (вариант C)

```
--bg:        #efe4d4
--bg-elev:   #fbf5ec
--panel-bg:  rgba(251, 245, 236, 0.94)

--ink:       #3c2c20
--ink-hi:    #241812
--ink-dim:   #937b63

--mint:      #a4682a
--mint-soft: rgba(60, 44, 32, 0.14)
--grid:      rgba(60, 44, 32, 0.03)
--amber:     #c07f24
--violet:    #5e7f3c   /* тёмная матча — читаема на кремовом, тот же смысл */
--coral:     #bf4a24

--qa-answer:              #7a5320
--dialog-option-text:     #5e7f3c
--btn-cancel-dialog-text: #b23a1a
--supervisor-accent:      #9a6a1e
```

Status-map (`--c-*`) — как в base/novacorps: `pending→--ink-dim`,
`planning/ready/done→--mint`, `running/awaiting/revising/retrying→--amber`,
`awaiting-user→--violet`, `failed→--coral`. Плюс legacy-алиасы
`--text→--ink`, `--text-dim→--ink-dim`.

## Декор («ламповость»)

Тёплое янтарное свечение, всё на `box-shadow`/градиентах:

- `#main`: вместо neon-сетки novacorps — мягкий тёплый radial-glow сверху-слева
  поверх `--bg` (`radial-gradient(120% 90% at 20% -10%, rgba(255,154,52,0.10),
  transparent 55%)`), в dark. В light — ровный `--bg` без свечения.
- Янтарный `box-shadow`-bloom на активных элементах: индикатор выбранной стадии
  (`.stage-item.active::before`), `.progress-fill`, `.brew`-подобные полосы,
  пульсирующие «running»-точки статусов. Тактично, не перегружая.
- `prefers-reduced-motion` уже уважается base-CSS (там `animation`-ы); наши
  добавки — статические тени, отдельного гашения не требуют.

## Перекраска захардкоженных заливок base

В `base/*.css` часть цветов не токенизирована (сознательное решение — заливки в
neon-цветах novacorps: `rgba(111,212,204,X)` teal, `rgba(229,212,66,X)` жёлтый,
`rgba(183,135,255,X)` фиолетовый). Скин `coffee`, как и `goga`, переопределяет
их под тёплую палитру. Перечень селекторов (по образцу `goga/index.css`):

- `::selection`
- `base/layout.css`: `.progress-bar`, `.progress-fill`, `.jump-latest`,
  `.icon-btn`, `@keyframes attentionPulse`
- `base/stages-list.css`: `.stage-item` (border/hover), `.stage-item.active`
  (bg + `::before` glow), `.stage-item.active::after` (scanline)
- `base/plan-panel.css`: `.plan-line:hover`, `.plan-line.has-comment`,
  `.plan-section-wrapper` (+ `.plan-section-assumptions` / `.plan-section-criteria`)
- `base/dialog-channel.css`: `.dialog-history .qa`, `.phase-divider`,
  `.dialog-pending .dialog-custom` border; **плюс захардкоженные фиолетовые
  акценты** `#dialog-section` (border + radial), `h3::before` (точка),
  `@keyframes pendingBreathe` / `dialogFlash` (тени) — перекрасить в
  матчу/янтарь под кофейную тему (в отличие от goga, который оставил их
  фиолетовыми).
- `base/event-feed.css`: `.feed-entry` (border/hover)

Большинство правил тема-агностичны (rgba кофейных акцентов работают на обоих
фонах, как у goga). Если на светлом фоне какая-то заливка окажется слишком
блёклой/тёмной — точечно завести её под `:root[data-theme="light"] …`.

## Что НЕ делаем

- **Логотип не трогаем.** `#header h1` / `.logo` не переопределяем — остаётся
  штатный afm-вордмарк с анимированным гексагоном (в отличие от goga, который
  подменяет его на QArium-wordmark + картинку). Кофейная чашка ☕ из мокапа была
  иллюстрацией карточки и в реальный логотип не добавляется.
- Компоненты (React/TSX) и `base/*.css` не меняем — только новый файл скина и
  строка `<link>`.
- Скины `novacorps` и `goga` остаются в репозитории (не удаляем).

## Файлы

Создать:
- `pkg/web/dashboard/public/skins/coffee/index.css` — сам скин (источник).

Изменить:
- `pkg/web/dashboard/index.dev.html` — строка `<link>` skin:
  `/skins/novacorps/index.css` → `/skins/coffee/index.css`.

Регенерируется сборкой (`npm run build`), коммитим вместе:
- `pkg/web/dashboard/skins/coffee/index.css` (копия из public/)
- `pkg/web/dashboard/index.html` (собранный, со ссылкой на coffee + хэш ассета)
- при необходимости — `pkg/web/dashboard/assets/*` (если хэш изменится)

## Тестирование и проверка

- `npm run build` (сборка проходит, `skins/coffee/` и `index.html`
  регенерированы), `npm run typecheck`, `npm test` (существующие тесты зелёные —
  скин это CSS, автотесты его вид не покрывают).
- `go build ./pkg/web/... ./cmd/...` (embed компилируется с новыми ассетами).
- Визуальная проверка: запустить дашборд, убедиться что дефолт — coffee;
  проверить обе темы (toggle `data-theme`), различимость статусов
  (done=карамель, running=оранж, awaiting_user=матча, failed=красный), диалог,
  план с код-блоком и таблицей, ленту, футер-прогресс.

## Открытые вопросы

Нет. Все решения приняты: dark=B (матча-violet), light=C, coffee по умолчанию,
логотип не трогаем.

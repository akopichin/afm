# Дизайн: микроанимации дашборда (A–D)

**Дата:** 2026-07-24
**Статус:** согласован (мокап-демо одобрен), готов к плану реализации

## Цель

Ненавязчивые микроанимации, которые **несут информацию** (что изменилось, куда
смотреть, что идёт работа), а не украшают. Принципы:

- **one-shot переходы на событие** (150–600 мс) вместо новых бесконечных циклов;
- всё через токены (`var(--*)` / `currentColor`) → одинаково работает во всех трёх
  скинах (coffee/goga/novacorps) без изменений в скинах;
- живёт в общих `base/*.css` (структура) + минимальные триггеры-классы в React;
- без layout-shift (только `transform`/`opacity`/`box-shadow`/`border-color`/`background`);
- **reduced-motion уже покрыт глобально** в `base/reset.css`
  (`@media (prefers-reduced-motion: reduce)` гасит все анимации/переходы) — отдельные
  guard'ы не нужны.

Демо согласовано (интерактивный артефакт с группами A–D).

## Что уже есть (не трогаем / переиспользуем)

- **A3 (прогресс-бар)** уже реализован: `.progress-fill` имеет `transition: width .4s`,
  `::before { animation: shimmer }` и `::after { tipPulse }`. **Изменений не требуется.**
- **B3** — keyframe `dialogFlash` и класс `.dialog-flash` уже определены в
  `base/dialog-channel.css`, но не триггерятся из React. Нужно только повесить класс.
- reduced-motion — глобальный guard в `base/reset.css`.

## Анимации

### A. Ясность статуса и потока

- **A1 — переход статус-точки в done.** Когда стадия переходит в `done`, её точка в
  сайдбаре делает one-shot: `hollow → fill` (заливка `::after` scale 0→1), проявляется
  галочка `✓`, короткая вспышка `box-shadow: currentColor`. Требует: разметка `<span
  class="chk">✓</span>` внутри `.status-dot`; JS в `StagesList` — отследить предыдущий
  статус каждой стадии и на переходе в `done` навесить one-shot класс `completing`.
- **A2 — вход новых строк в event feed.** CSS-анимация `feed-in` (fade + `translateY(-6px)`,
  250 мс) на `.feed-entry`. Только CSS: React монтирует лишь реально новые строки (стабильный
  key), поэтому анимируются только новые (начальный батч — один раз, ок). Разметку не меняем.
- **A4 — вход активной стадии.** Когда выбор/активность переходит на стадию, `.stage-item.active`
  проигрывает one-shot `fade-in` (opacity + лёгкий `translateX`). Только CSS (анимация
  срабатывает при добавлении класса `active`).

### B. Отклик на действия

- **B1 — success-морф кнопок Approve/Send/Revision.** На клик кнопка на ~1.2 с получает
  класс `ok`: ripple из центра + подпись морфит в галочку («✓ Approved»/«✓ Sent»).
  Требует: в `PlanPanel` (Approve, Send revision) и `DialogChannel` (▸ SEND, Send feedback)
  добавить декоративные спаны (`.ripple`, вторая подпись `.done-label`) и локальное
  состояние «только что нажато». **Декоративные спаны — `aria-hidden="true"`**, чтобы
  доступное имя кнопки осталось прежним («Approve», «▸ SEND» и т.д.) и существующие тесты
  (`getByRole('button',{name:...})`) не сломались.
- **B2 — «pop» маркера комментария + въезд границы.** При добавлении комментария к строке
  (класс `has-comment`) маркер `.line-comment-marker` (`●`) проигрывает one-shot `pop`
  (scale .3→1.4→1), а левая янтарная граница/отступ строки въезжает через `transition`
  вместо мгновенного скачка. Только CSS (общие классы `.plan-line`/`.line-comment-marker`
  используются и планом, и диалогом → покрывает оба). Разметку не меняем.
- **B3 — glow рамки диалога при новом вопросе.** Когда появляется новый pending-вопрос
  (меняется `pending.id`), на `.dialog-pending` навешивается one-shot класс `dialog-flash`
  (keyframe уже есть). JS в `DialogChannel`.

### C. Атмосфера

- **C1 — кроссфейд темы.** На ключевых поверхностях (`body`/`#main`, панели-контейнеры,
  `.stage-item`, `.markdown-body`, `.status-badge`, кнопки, бордеры) добавить
  `transition: background-color .28s, border-color .28s, color .28s`, чтобы переключение
  light/dark было плавным, а не резким. Reduced-motion гасит (уже глобально).
- **C2 — индикатор «агент думает».** Когда выбранная стадия в статусе `running`, в шапке
  детали (рядом со статус-бейджем) показывается компактный индикатор `thinking` (три
  точки с мягким `blink`). Требует: условный рендер в `App` (detail header) при
  `selectedStage.status === 'running'` + стили. Дополняет существующий `badgePulse`
  running-бейджа (не заменяет).
- **C3 — пульс при реконнекте.** Когда WS не подключён (`connected === false` →
  `.ws-status.disconnected`), точка/индикатор соединения мягко пульсирует (coral `recon`).
  Только CSS в `base/header.css` (FlowHeader уже ставит класс `disconnected`). Разметку не
  меняем.

### D. Связи потока (коннекторы)

- Тонкие вертикальные коннекторы между стадиями: `.stage-item:not(:last-child)::before` —
  линия от точки к следующей (цвет `--mint-soft`/`--soft`). Одноразовое «пробегание»
  импульса (`::after` с `travel` keyframe, точка `--amber`, translateY сверху вниз) при
  переходе стадии в `done` — переиспользует ту же JS-детекцию перехода, что и A1 (класс
  `travel` на стадии на время анимации). Только CSS + тот же триггер, что A1.

## Кроссскинность

Все правила — в `base/*.css` на токенах/`currentColor`. `goga` и `novacorps` наследуют
анимации автоматически (они `@import`-ят те же base-файлы), отдельных правок в скинах не
требуется. Проверить визуально во всех трёх.

## Файлы

**CSS — `public/skins/base/*.css`** (правим источник; `skins/base/*.css` регенерирует сборка):
- `stages-list.css` — A1 (dot completing + `.chk`), A4 (active fade-in), D (коннекторы + travel).
- `event-feed.css` — A2 (feed-in).
- `plan-panel.css` — B1 (success-морф кнопок), B2 (pop маркера + transition границы), C2 (стили thinking-индикатора), C1-переходы для панелей/строк.
- `dialog-channel.css` — B1 (кнопки), B3 использует существующий `dialogFlash`.
- `header.css` — C3 (пульс `.ws-status.disconnected`), C1-переход шапки.
- `layout.css` / `reset.css` — C1 (кроссфейд-переходы на общих поверхностях).

**React — `src/…`:**
- `components/stages-list/StagesList.tsx` — A1/D: prev-status per stage → one-shot `completing`+`travel`; разметка `.chk` в точке.
- `components/dialog-channel/DialogChannel.tsx` — B3 (flash на новый pending.id), B1 (морф SEND/Send feedback).
- `components/plan-panel/PlanPanel.tsx` — B1 (морф Approve/Send revision).
- `app/App.tsx` — C2 (thinking-индикатор в detail header при running).

**Регенерируется сборкой (коммитим):** `skins/base/*.css`, `index.html`, `assets/*` при изменении бандла.

## Что НЕ делаем

- Не добавляем новых бесконечных циклов (кроме уже существующих) — только one-shot на событие
  (исключения C2 thinking и C3 reconnect — они «пока идёт состояние», по природе циклические,
  но тихие и ограничены по времени состоянием).
- Не трогаем A3 (прогресс уже готов), не трогаем скины coffee/goga/novacorps (анимации в base).
- Не меняем доступные имена кнопок (декоративные спаны `aria-hidden`).
- Не меняем версию go.mod; логотип не трогаем.

## Тестирование и проверка

- `npm run typecheck`, `npm test` — существующие тесты зелёные (accessible-имена кнопок
  сохранены; StagesList/EventFeed/Dialog/Plan структурные ассерты не сломаны). Точечные тесты
  добавляем там, где есть JS-логика с наблюдаемым классом (StagesList prev-status → `completing`;
  DialogChannel flash на новый pending; кнопки — `ok` класс на клик).
- `npm run build`, `go build ./pkg/web/... ./cmd/...`.
- Визуальная проверка: прогнать демо-техники на реальном дашборде (mock-preview) во всех трёх
  скинах и в reduced-motion (анимации гаснут, функциональность цела).

## Открытые вопросы

Нет. A–D берём полностью; goga/novacorps наследуют; A3 уже готов.

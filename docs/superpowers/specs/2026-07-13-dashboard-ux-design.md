# Dashboard UX batch — дизайн

**Дата:** 2026-07-13
**Контекст:** React-дашборд afm (`pkg/web/dashboard`). Пять улучшений UX/надёжности поверх существующей композиции (`src/app/App.tsx`, хуки в `src/hooks/`, бэкенд `pkg/server/websocket.go`).

## Цели

1. WebSocket всегда активен (keepalive + автореконнект).
2. Панели resizable: тянем границы колонок (`stages | central | feed`) и разделители `plan/dialog/log` внутри central. Размеры сохраняются.
3. Maximize: иконка разворачивает панель (plan/dialog/feed) на весь экран.
4. Чёткий сигнал «ждёт пользователя», видимый даже если пользователь проскроллил или ушёл на другую вкладку.
5. Auto-scroll в конец для диалога и фида.

## Не-цели (YAGNI)

- Кастомное перетаскивание/докинг панелей (свободное позиционирование) — не делаем.
- Звуковые уведомления.
- Persist состояния maximize — оно эфемерное.
- Resizable отдельных элементов внутри панелей (только панели целиком).

## Принятые решения (из brainstorming)

- **Фича 1:** полный ping/pong — бэкенд (gorilla) + клиент (watchdog). Браузерный WS API не позволяет слать ping-фреймы, поэтому keepalive инициирует сервер; клиентский автореконнект (уже есть) подхватывает обрыв.
- **Фича 2:** «полная сетка» — 3 resizable-колонки + vertical-resizable `plan/dialog/log` внутри central. Размеры в localStorage.
- **Фича 3:** maximize для plan/dialog/feed через portal-overlay.
- **Фича 4:** триггеры — `awaiting_user_input` и `awaiting_approval`; сигнал = пульс (сайдбар+шапка+свечение панели) + мигание title в фоновой вкладке + автоскролл центральной колонки к ожидающей панели.
- **Лейаут:** библиотека `react-resizable-panels`.

---

## Архитектура. Новые и изменяемые файлы

```
pkg/web/dashboard/
  src/
    components/
      layout/
        DashboardLayout.tsx        # НОВОЕ: вложенные <PanelGroup>
        Maximizable.tsx            # НОВОЕ: context + portal-overlay
      panel-frame/
        PanelFrame.tsx             # НОВОЕ: шапка панели (заголовок + иконка maximize)
    hooks/
      use-attention/use-attention.ts          # НОВОЕ
      use-title-flash/use-title-flash.ts      # НОВОЕ
      use-stick-to-bottom/use-stick-to-bottom.ts  # НОВОЕ
      use-event-feed/use-event-feed.ts        # ПРАВКИ: watchdog + heartbeat-фильтр
    app/App.tsx                               # ПРАВКИ: использовать DashboardLayout, attention
    components/stages-list/StagesList.tsx     # ПРАВКИ: data-attention
    components/flow-header/FlowHeader.tsx     # ПРАВКИ: индикатор attention
    components/plan-panel/PlanPanel.tsx       # ПРАВКИ: PanelFrame, attention-свечение, maximize
    components/dialog-channel/DialogChannel.tsx  # ПРАВКИ: PanelFrame, attention, maximize, stick-to-bottom
    components/event-feed/EventFeedPanel.tsx  # ПРАВКИ: PanelFrame, maximize, stick-to-bottom
    components/log-panel/LogPanel.tsx         # ПРАВКИ: PanelFrame без maximize (log не в списке maximize-панелей)
  package.json                                # +react-resizable-panels
pkg/server/websocket.go                       # ПРАВКИ: ping/pong + heartbeat + read-deadline
```

---

## Фича 1. WebSocket keepalive

### Бэкенд (`pkg/server/websocket.go`)

Переписать обработчик соединения на каноническую gorilla-схему «один писатель через канал» (без `sync.Mutex`):

- **reader-горутина:**
  - `conn.SetReadDeadline(time.Now().Add(pongWait))`
  - `conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })`
  - цикл `conn.ReadMessage()` — дренирует входящие фреймы (контент игнорируем; нужны сами read-вызовы, чтобы gorilla обрабатывала control-фреймы pong/close). При ошибке (дедлайн/закрытие) — выход.
- **writer-горутина:** `select` по трём каналам:
  - `events` (подписка `uiBus`) → `conn.WriteMessage(TextMessage, data)`
  - `ticker` (каждые `pingPeriod`) → `conn.WriteControl(PingMessage, …, writeWait)` **и** app-level `{"type":"heartbeat"}` TextMessage
  - `done` (закрытие) → выход
- Reader при выходе закрывает `done` и `conn`; writer при выходе тоже.
- **Overflow-close (1008):** существующая проверка `uiBus.SubscriberDroppedCount(id) > 10` переносится в ветку `case ev := <-events:` writer-горутины (перед `WriteMessage`): при превышении — `WriteMessage(CloseMessage, FormatCloseMessage(1008, "event queue overflow"))` и выход. Существующий `pkg/server/websocket_test.go` (если покрывает overflow) должен остаться зелёным.
- **Константы:** `pongWait = 60s`, `pingPeriod = 30s` (< `pongWait`), `writeWait = 10s`.
- **Поведение:** мёртвый клиент не отвечает pong → read-deadline срабатывает → reader выходит → conn закрывается → браузер получает onclose.

### Клиент (`use-event-feed.ts`)

- Существующий экспоненциальный backoff-reconnect по `onclose` **сохраняется** без изменений.
- **Watchdog:** переменная `lastMessageAt`, `setInterval(5s)` — если `now - lastMessageAt > 75s` (>2× heartbeat) → `socket.close()`. Это триггерит `onclose` → реконнект. Так клиент ловит «мёртвый сервер» быстрее браузерного TCP-таймаута.
- `lastMessageAt` обновляется в `onopen` и в каждом `onmessage`.
- **Фильтр heartbeat:** в `onmessage` распарсенный `type === 'heartbeat'` обновляет только `lastMessageAt` и **не** добавляется в массив `events` (иначе засорит ленту).
- На реконнекте/возобновлении — App уже ре-запрашивает `/api/status` по значимым событиям; watchdog-обрыв тоже приведёт к `onclose` → переподключение → свежие события.

---

## Фичи 2+3. Resizable layout + maximize

### Лейаут (`components/layout/DashboardLayout.tsx`)

Заменяет статический CSS-grid `#main` вложенными `<PanelGroup>` из `react-resizable-panels`:

```
<PanelGroup direction="horizontal" autoSaveId="afm-cols">
  <Panel order={1} minSize={12}><StagesList/></Panel>
  <PanelResizeHandle/>
  <Panel order={2}>
    <PanelGroup direction="vertical" autoSaveId="afm-rows">
      <Panel order={1} minSize={15}><PlanPanel/></Panel>
      <PanelResizeHandle/>
      <Panel order={2} minSize={15}><DialogChannel/></Panel>
      <PanelResizeHandle/>
      <Panel order={3} minSize={10}><LogPanel/></Panel>
    </PanelGroup>
  </Panel>
  <PanelResizeHandle/>
  <Panel order={3} minSize={12}><EventFeedPanel/></Panel>
</PanelGroup>
```

- `autoSaveId` → размеры автоматически сохраняются в localStorage (без нашего кода).
- `minSize` — в процентах; предотвращает схлопывание.
- `<PanelResizeHandle>` — стилизован в духе темы: тонкая фиолетовая полоса, толще/ярче при hover, cursor `col-resize`/`row-resize`.
- Существующие селекторы `#main`, `#detail-panel` сохраняются как классы-обёртки (минимум правок CSS).

### Maximize (`components/layout/Maximizable.tsx`)

- React-context: `MaximizeContext` со значением `maximizedKey: string | null` и функцией `toggle(key)`.
- Компонент `<Maximizable id="…">` оборачивает панель:
  - В обычном режиме рендерит `children` inline.
  - Если `maximizedKey === id` — рендерит `children` через **`createPortal`** в фиксированный fullscreen-overlay (`position:fixed; inset:0; z-index:top`), с кнопкой ✕ и обработчиком `Esc`.
  - Portal сохраняет React-инстанс компонента → внутреннее состояние (скролл, введённый текст, выбор опции) **не теряется** при развороте/сворачивании.
- В шапку каждой максимизируемой панели (`PanelFrame`, см. ниже) добавляется иконка ⛶ → `toggle(id)`.
- Состояние maximize **не persists** (эфемерное).
- Максимизируемые: PlanPanel (`plan`), DialogChannel (`dialog`), EventFeedPanel (`feed`).

### PanelFrame (`components/panel-frame/PanelFrame.tsx`)

Общая шапка панели: заголовок + опциональная иконка maximize. Унифицирует `h3`-заголовки существующих панелей. Принимает `title`, `maximizeId?`, `attention?` (для свечения), `actions?` (слоты под кастомные кнопки, напр. «jump to latest» из фичи 5).

---

## Фича 4. Attention

### `useAttention(stage)` → `{ needsAttention: boolean, kind: 'dialog' | 'plan' | null }`

```ts
kind = stage.status === 'awaiting_user_input' ? 'dialog'
     : stage.status === 'awaiting_approval'   ? 'plan'
     : null
needsAttention = kind !== null
```

Глобальный флаг «любая стадия ждёт» — `stages.some(s => ATTENTION_STATUSES.has(s.status))`, где `ATTENTION_STATUSES = { awaiting_user_input, awaiting_approval }`.

### Сигналы

- **Сайдбар (`StagesList`):** элемент стадии с `needsAttention` → атрибут `data-attention` → CSS pulse-анимация (`@keyframes` на бордере/фоне).
- **Шапка (`FlowHeader`):** пульсирующая точка-индикатор, если любая стадия ждёт.
- **Свечение панели:** на выбранной стадии — PlanPanel (если `kind==='plan'`) или DialogChannel (если `kind==='dialog'`) получает CSS-класс `attention` (светящийся бордер `box-shadow` в цвете `--c-awaiting`).
- **`useTitleFlash(active: boolean)`:**
  - слушает `visibilitychange`;
  - когда `document.hidden && active` → `document.title` мигает каждые ~1.5с между исходным заголовком и `'⚠ Нужно действие — afm Dashboard'`;
  - при `!hidden` или `!active` → восстанавливает исходный title и останавливает интервал.
- **Автоскролл центральной колонки:** при **переходе** стадии в awaiting (изменение `kind` с `null` на значение) — `panelRef.current?.scrollIntoView({ behavior:'smooth', block:'nearest' })` к ожидающей панели. Срабатывает один раз на смену состояния, не мешая ручному скроллу в остальное время.

### Координация с фичей 5

`scrollIntoView` attention срабатывает только на **смене** состояния (effect с зависимостью от `kind`). Stick-to-bottom (фича 5) работает непрерывно на приросте контента. Конфликта нет: attention — разовый скролл при входе в awaiting, stick-to-bottom — следование за хвостом.

---

## Фича 5. Auto-scroll (stick-to-bottom)

### `useStickToBottom(ref)` → `{ stick: boolean, jumpToBottom: () => void }`

- На `scroll`-событии контейнера: `stick = (scrollHeight - scrollTop - clientHeight) < 40`.
- При `stick === true` и появлении нового контента — `el.scrollTop = el.scrollHeight`.
- При `stick === false` — скролл не трогаем; компонент показывает кнопку «↓ к последнему» → `jumpToBottom()` выставляет `stick=true` и скручивает вниз.
- Использование MutationObserver/`useLayoutEffect` по зависимостям `children` для докрутки.
- **Применяется к:** `DialogChannel` (область истории+pending) и `EventFeedPanel`.

---

## Тесты

### Бэкенд (`pkg/server/websocket_test.go`, gorilla + nettest)

- Поднимаем `Server`, подключаемся ws-клиентом, **не отвечаем на ping** (эмулируем мёртвый клиент) → в пределах `pongWait` (тест с укороченными константами или инжектированным таймаутом) соединение закрывается сервером.
- Проверка, что при подключении клиент периодически получает `{"type":"heartbeat"}`.
- Overflow-close (1008) — если есть существующий тест, сохранить.

### Фронтенд (vitest, jsdom)

- `use-event-feed`: watchdog-реконнект (мок `WebSocket` + `vi.useFakeTimers`), heartbeat-фильтр (heartbeat не попадает в `events`), сохранение существующего backoff-теста.
- `use-title-flash`: эмуляция `visibilitychange` + проверка `document.title`.
- `use-stick-to-bottom`: mock `scrollTop/scrollHeight` + `ResizeObserver` → проверка докрутки и паузы.
- `DashboardLayout`: рендерятся `<PanelResizeHandle>` для каждого разделителя.
- `Maximizable`: `toggle` → контент появляется в overlay (portal); `Esc` → скрывается; состояние дочернего компонента сохраняется (напр. введённый текст).
- `useAttention`: корректный `kind` по статусу стадии.
- `StagesList`: `data-attention` ставится на awaiting-стадию.

---

## Порядок реализации

1. **Фича 1 (ws keepalive)** — независима, бэкенд + клиент. Фундамент надёжности канала.
2. **Фича 5 (auto-scroll)** — маленький переиспользуемый хук, базис для диалога/фида.
3. **Фичи 2+3 (layout + maximize)** — структурное изменение композиции; добавляется зависимость `react-resizable-panels`.
4. **Фича 4 (attention)** — поверх лейаута (свечение панелей, scrollIntoView) и хуков.

Каждая фича — отдельный коммит; после каждой — `npm test` + `make build` (веб вкомпилируется) + ручная проверка в браузере.

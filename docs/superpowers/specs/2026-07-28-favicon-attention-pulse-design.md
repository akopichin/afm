# Дизайн: пульсирующая иконка вкладки при ожидании действия пользователя

**Дата:** 2026-07-28
**Статус:** согласован, готов к плану реализации

## Цель

Сейчас, если пользователь уходит с вкладки дашборда (переключился на другую
вкладку/приложение) пока стадия ждёт ответа/апрува, единственный сигнал —
мигающий `document.title` (`useTitleFlash`). Его видно, только если вкладка
попадает в поле зрения (например, при просмотре списка вкладок). Иконка
вкладки (favicon) при этом остаётся статичной — глазом не зацепить.

Добавляем: пока хотя бы одна стадия текущего прогона ждёт действия
пользователя (`awaiting_user_input`/`awaiting_approval`) И вкладка в фоне —
favicon мигает амберным бейджем-кружком поверх текущей иконки. Работает с
любой иконкой (дефолтная `favicon.svg` или скиновая), потому что бейдж
рисуется поверх ЛЮБОЙ текущей иконки через canvas, а не заменяет её на
отдельно нарисованный asset.

## Что уже есть (переиспользуем)

- **`useAttention`/`anyAwaiting`** (`hooks/use-attention`) — уже вычисляют,
  ждёт ли хоть одна стадия действия. `anyAwaiting(stages)` уже используется
  для точки в шапке (`FlowHeader attention={anyAwaiting(stages)}`) — это тот
  же сигнал, что нужен для favicon (не привязан к выбранной сейчас стадии).
- **`useTitleFlash`** (`hooks/use-title-flash`) — эталон паттерна: активируется
  только пока `document.hidden`, восстанавливает исходное состояние по
  `visibilitychange`/размонтированию. Новый хук копирует эту структуру.
- **`var(--amber)`** и keyframe `attentionPulse` (`skins/base/layout.css`) —
  существующий визуальный язык «ждём действия» в проекте. Берём тот же цвет
  (через `getComputedStyle`, чтобы бейдж совпадал с активным скином), но
  анимация favicon — это JS-тоггл `href`, а не CSS-анимация (у canvas-иконки
  нет DOM-элемента для CSS).

## Компоненты

### 1. `compositeAttentionBadge(iconHref: string): Promise<string>`

Чистая (в разумных пределах — работает с DOM Image/Canvas, но без побочных
эффектов на состояние приложения) async-функция в
`hooks/use-favicon-pulse/composite-attention-badge.ts`:

1. Грузит `iconHref` в `new Image()`.
2. Рисует на offscreen `<canvas>` (32×32): само изображение на весь канвас,
   затем кружок-бейдж в правом нижнем углу — тёмное кольцо-обводка (контраст
   на любом фоне иконки) + заливка `var(--amber)` (через
   `getComputedStyle(document.documentElement).getPropertyValue('--amber')`,
   фолбэк `#e5d442`, если переменная почему-то пуста).
3. Возвращает `canvas.toDataURL()`.
4. Если загрузка изображения падает (`onerror`) — реджектит промис; вызывающий
   код это учитывает (пункт 2 ниже) и просто не пульсирует, без падения.

Кешируется по `iconHref` на уровне модуля (`Map<string, Promise<string>>`),
чтобы при частых переключениях фокуса вкладки не перерисовывать canvas каждый
раз заново — только при первом обращении к конкретному href.

### 2. `useFaviconPulse(active: boolean): void`

Хук в `hooks/use-favicon-pulse/use-favicon-pulse.ts`, структурно копирует
`useTitleFlash`:

```
useEffect(() => {
  if (!active) return
  const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) return
  const original = link.href
  let toggle = false
  let timer: ReturnType<typeof setInterval> | undefined
  let badgeHref: string | null = null
  let cancelled = false

  const stop = () => {
    if (timer !== undefined) clearInterval(timer)
    timer = undefined
    link.href = original
  }
  const onVisibility = () => {
    if (!document.hidden) { stop(); return }
    void compositeAttentionBadge(original).then((href) => {
      if (cancelled) return
      badgeHref = href
      timer = setInterval(() => {
        toggle = !toggle
        link.href = toggle ? badgeHref! : original
      }, PULSE_INTERVAL_MS)
    }).catch(() => { /* без бейджа — просто не пульсируем */ })
  }

  document.addEventListener('visibilitychange', onVisibility)
  if (document.hidden) onVisibility()
  return () => {
    cancelled = true
    document.removeEventListener('visibilitychange', onVisibility)
    stop()
  }
}, [active])
```

`PULSE_INTERVAL_MS = 700` (полный цикл вкл/выкл ~1.4с — близко к ритму
существующего `attentionPulse`, 1.2–1.6с).

### 3. Wiring в `App.tsx`

Рядом с существующим:
```
useTitleFlash(attention.needsAttention)
```
добавляется:
```
useFaviconPulse(anyAttention)
```
(`anyAttention` уже вычислен на строке ~70 для точки в шапке — просто ещё один
потребитель того же сигнала).

## Тестирование

`compositeAttentionBadge` использует `Image`/`canvas`, которые в jsdom
(тестовое окружение проекта) не работают из коробки — не тестируем пиксельно,
это визуальный код. `useFaviconPulse` тестируется отдельно: в тестах
`compositeAttentionBadge` мокается (`vi.mock`) так, чтобы резолвиться сразу
известной строкой — дальше проверяется ТОЛЬКО таймер/visibility-логика
(fake timers, `document.hidden`), тем же паттерном, что уже есть в
`use-title-flash.test.ts`:

- `active=false` → `link.href` не меняется.
- `active=true` + `document.hidden=true` → после advance таймера `href`
  становится бейджем, после следующего тика — снова original.
- `visibilitychange` → `hidden=false` останавливает тоггл и восстанавливает
  original.
- cleanup (unmount) восстанавливает original и чистит таймер.

## Edge cases

- **Нет `<link rel="icon">` в DOM** — хук молча ничего не делает (сегодня
  тег всегда есть в `index.html`, но защита не помешает).
- **Ошибка загрузки/декодирования иконки в canvas** — фича молча
  деградирует (нет пульса), без ошибок в консоли, влияющих на остальной UI.
- **Favicon меняется в рантайме** (сегодня не происходит, `href` статичен во
  всех скинах) — кеш композиции ключуется по фактическому `href`, так что если
  это когда-то станет динамическим, старый кеш просто не заматчится и
  посчитается заново.
- **prefers-reduced-motion** — это чисто CSS-медиа-запрос, favicon не CSS;
  но при `document.hidden` анимацию всё равно никто не видит "живьём" в
  моменте (только предыдущий рендер), поэтому отдельного guard'а не требуется
  ­— решение согласовано с тем, что и `useTitleFlash` его не имеет.

## Out of scope

- Разные бейджи/иконки для `awaiting_user_input` vs `awaiting_approval` —
  не запрашивалось, оба случая покрывает единый `anyAttention`.
- Favicon для нескольких открытых вкладок дашборда одновременно (разные
  runs/стадии в разных вкладках) — каждая вкладка независима, это и так работает
  by design (эффект per-tab через `document`/`link` каждой вкладки).

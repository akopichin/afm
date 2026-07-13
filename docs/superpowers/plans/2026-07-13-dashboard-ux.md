# Dashboard UX batch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Пять улучшений React-дашборда afm — ws keepalive, resizable-лейаут, maximize панелей, attention-сигнал, auto-scroll.

**Architecture:** Бэкенд `websocket.go` переписывается на «одного писателя» (gorilla ping/pong + heartbeat + read-deadline). Фронт добавляет хуки (`useStickToBottom`, `useAttention`, `useTitleFlash`, watchdog в `use-event-feed`), обёртки (`DashboardLayout` на `react-resizable-panels`, `Maximizable` через portal, `PanelFrame`) и CSS-сигналы attention. Порядок: фича 1 → 5 → 2+3 → 4.

**Tech Stack:** Go 1.26 + gorilla/websocket; React 18 + TypeScript + Vite + vitest(jsdom) + `react-resizable-panels`.

## Global Constraints

- Go 1.26; `golangci-lint run ./...` чисто; `go test ./pkg/server/` зелёный.
- React 18 + TS strict (`npm run typecheck` чисто); `npm test` (vitest, jsdom) зелёный; все новые хуки/компоненты имеют тесты.
- Коммиты на русском, БЕЗ `Co-Authored-By`.
- `make build` (web → go) должен собирать всё; после каждой фичи — `npm test` + `make build` + ручная проверка в браузере.
- Спека: `docs/superpowers/specs/2026-07-13-dashboard-ux-design.md`.

---

## File Structure

**Создать:**
- `pkg/server/websocket_keepalive_test.go` — тесты keepalive (heartbeat, silent-client-close).
- `pkg/web/dashboard/src/components/layout/DashboardLayout.tsx` — вложенные `<PanelGroup>`.
- `pkg/web/dashboard/src/components/layout/Maximizable.tsx` — context + portal-overlay.
- `pkg/web/dashboard/src/components/panel-frame/PanelFrame.tsx` — шапка панели (title + maximize).
- `pkg/web/dashboard/src/hooks/use-attention/use-attention.ts` — `{needsAttention, kind}`.
- `pkg/web/dashboard/src/hooks/use-attention/use-attention.test.ts`
- `pkg/web/dashboard/src/hooks/use-title-flash/use-title-flash.ts`
- `pkg/web/dashboard/src/hooks/use-title-flash/use-title-flash.test.ts`
- `pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.ts`
- `pkg/web/dashboard/src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts`

**Изменить:**
- `pkg/server/websocket.go` — ping/pong + heartbeat + read-deadline.
- `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts` — watchdog + heartbeat-фильтр.
- `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts` — тесты watchdog/heartbeat.
- `pkg/web/dashboard/src/app/App.tsx` — MaximizeProvider + DashboardLayout + attention + title-flash.
- `pkg/web/dashboard/src/components/stages-list/StagesList.tsx` — `data-attention`.
- `pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx` — индикатор attention.
- `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx` — PanelFrame + maximize + attention.
- `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx` — PanelFrame + maximize + attention + stick-to-bottom.
- `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx` — PanelFrame + maximize + stick-to-bottom.
- `pkg/web/dashboard/src/components/log-panel/LogPanel.tsx` — PanelFrame (без maximize).
- `pkg/web/dashboard/public/style.css` — resize-хендлы, maximize-overlay, attention-анимации, panel-frame.
- `pkg/web/dashboard/package.json` — `+react-resizable-panels`.

---

## Task 1: WebSocket keepalive (бэкенд)

**Files:**
- Modify: `pkg/server/websocket.go`
- Test: `pkg/server/websocket_keepalive_test.go`

**Interfaces:**
- Produces: пакетные переменные `wsPongWait`, `wsPingPeriod`, `wsWriteWait` (time.Duration) — тесты их переопределяют.

- [ ] **Step 1: Написать падающий тест (heartbeat приходит)**

`pkg/server/websocket_keepalive_test.go`:
```go
package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocket_SendsHeartbeat(t *testing.T) {
	origPing := wsPingPeriod
	wsPingPeriod = 40 * time.Millisecond
	defer func() { wsPingPeriod = origPing }()

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.Contains(string(msg), `"type":"heartbeat"`) {
			return // success
		}
	}
	t.Fatalf("heartbeat not received within timeout")
}

func TestWebSocket_ClosesSilentClient(t *testing.T) {
	origPong, origPing := wsPongWait, wsPingPeriod
	wsPongWait = 150 * time.Millisecond
	wsPingPeriod = 50 * time.Millisecond
	defer func() { wsPongWait, wsPingPeriod = origPong, origPing }()

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Намеренно не читаем > pongWait → сервер не получает pong → рвёт соединение.
	time.Sleep(500 * time.Millisecond)

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatalf("expected server to close silent client, but read succeeded")
	}
}
```

- [ ] **Step 2: Запустить — должно упасть (нет wsPingPeriod/heartbeat)**

Run: `cd /Users/alexander.kopichin/work/flowManager && go test ./pkg/server/ -run TestWebSocket_ -v`
Expected: FAIL (`wsPingPeriod undefined` / compile error).

- [ ] **Step 3: Реализовать keepalive в websocket.go**

Полностью заменить тело файла `pkg/server/websocket.go`:
```go
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/akopichin/afm/pkg/orchestrator"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Таймауты keepalive. pingPeriod < pongWait, иначе сервер порвёт живых клиентов.
// Вынесены в переменные пакета для тестов (см. websocket_keepalive_test.go).
var (
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 30 * time.Second
	wsWriteWait  = 10 * time.Second
)

// handleWebSocket апгрейдит соединение и стримит UIBus-события клиенту.
// readPump детектит «мёртвого» клиента по таймауту pong и рвёт соединение;
// writePump — единственный писатель (gorilla требует одного писателя), шлёт
// события, ping (control-фрейм → browser auto-pong) и app-level heartbeat
// (text — JS-видимый сигнал для клиентского watchdog).
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	id, ch := s.uiBus.Subscribe(64)
	defer s.uiBus.Unsubscribe(id)

	done := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		s.writePump(conn, id, ch, done)
	}()
	s.readPump(conn)
	close(done)
	<-writerDone
}

// readPump дренирует входящие фреймы. Нужны сами read-вызовы, чтобы gorilla
// обрабатывала pong/close. PongHandler сбрасывает read-deadline; если pong
// перестал приходить (клиент умер) — дедлайн срабатывает, ReadMessage вернёт
// ошибку, readPump выйдет → соединение закроется.
func (s *Server) readPump(conn *websocket.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump — единственный писатель в conn. Select по событиям / тикеру / done.
func (s *Server) writePump(conn *websocket.Conn, id uint64, ch <-chan orchestrator.Event, done <-chan struct{}) {
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()

	prevDrops := uint64(0)
	heartbeat, _ := json.Marshal(map[string]string{"type": "heartbeat"})

	for {
		select {
		case <-done:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if drops := s.uiBus.SubscriberDroppedCount(id); drops > prevDrops {
				prevDrops = drops
				if drops > 10 {
					_ = conn.WriteMessage(
						websocket.CloseMessage,
						websocket.FormatCloseMessage(1008, "event queue overflow"),
					)
					return
				}
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			// ping — control-фрейм (клиент auto-pong); heartbeat — text (виден в JS).
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, heartbeat); err != nil {
				return
			}
		}
	}
}
```

- [ ] **Step 4: Запустить тесты — должны пройти**

Run: `go test ./pkg/server/ -run TestWebSocket_ -v`
Expected: PASS (heartbeat + silent-client-close). Существующий `TestWebSocket_ReceivesEvents` тоже зелёный (дефолтные таймауты).

- [ ] **Step 5: Линт**

Run: `bin/golangci-lint run ./pkg/server/...`
Expected: 0 issues.

- [ ] **Step 6: Коммит**

```bash
git add pkg/server/websocket.go pkg/server/websocket_keepalive_test.go
git commit -m "feat(server): keepalive вебсокета — ping/pong + heartbeat + read-deadline"
```

---

## Task 2: Клиентский watchdog + heartbeat-фильтр

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts`
- Test: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts` (создать при отсутствии)

**Interfaces:**
- Produces: `useEventFeed(url)` без изменений сигнатуры — внутренне фильтрует `{"type":"heartbeat"}` (в `events` не попадает) и реконнектит по watchdog.

- [ ] **Step 1: Написать падающий тест (heartbeat не попадает в events; watchdog рвёт тишину)**

`src/hooks/use-event-feed/use-event-feed.test.ts`:
```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useEventFeed } from './use-event-feed'

class FakeSocket {
  static last: FakeSocket | null = null
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  closed = false
  constructor(public url: string) { FakeSocket.last = this }
  close() { this.closed = true }
  fireOpen() { this.onopen?.() }
  fireMessage(data: unknown) { this.onmessage?.({ data: JSON.stringify(data) }) }
}

beforeEach(() => {
  vi.stubGlobal('WebSocket', FakeSocket)
  FakeSocket.last = null
})
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('useEventFeed', () => {
  it('фильтрует heartbeat из ленты событий', () => {
    const { result } = renderHook(() => useEventFeed('ws://x'))
    act(() => FakeSocket.last!.fireOpen())
    act(() => {
      FakeSocket.last!.fireMessage({ type: 'heartbeat' })
      FakeSocket.last!.fireMessage({ type: 'stage_status_changed', stage_id: 's1' })
    })
    expect(result.current.events).toHaveLength(1)
    expect(result.current.events[0].type).toBe('stage_status_changed')
  })

  it('watchdog закрывает соединение после длительной тишины', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useEventFeed('ws://x'))
    act(() => FakeSocket.last!.fireOpen())
    act(() => { vi.advanceTimersByTime(80_000) }) // > WATCHDOG_SILENCE_MS
    expect(FakeSocket.last!.closed).toBe(true)
    expect(result.current.connected).toBe(false)
  })
})
```

- [ ] **Step 2: Запустить — упадёт (watchdog/фильтр не реализованы)**

Run: `cd pkg/web/dashboard && npm test -- use-event-feed`
Expected: FAIL.

- [ ] **Step 3: Реализовать watchdog + heartbeat-фильтр**

Полностью заменить `src/hooks/use-event-feed/use-event-feed.ts`:
```ts
import { useEffect, useState } from 'react'
import type { AfmEvent } from '../../types'

// Подписка на WebSocket /ws с автопереконнектом и keepalive.
// Реконнект: экспоненциальный backoff (1с → 10с) по onclose (как в app.js).
// Watchdog: нет сообщений (событий ИЛИ heartbeat) > 75с → принудительный close
// → срабатывает onclose → реконнект. Так клиент ловит «мёртвый сервер» быстрее
// TCP-таймаута. Heartbeat от сервера в ленту событий НЕ попадает (liveness only).
const INITIAL_RECONNECT_DELAY_MS = 1000
const MAX_RECONNECT_DELAY_MS = 10000
const MAX_EVENTS = 200
const WATCHDOG_INTERVAL_MS = 5000
const WATCHDOG_SILENCE_MS = 75000

export function useEventFeed(url: string): { events: AfmEvent[]; connected: boolean } {
  const [events, setEvents] = useState<AfmEvent[]>([])
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    let socket: WebSocket | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined
    let watchdogTimer: ReturnType<typeof setInterval> | undefined
    let reconnectDelay = INITIAL_RECONNECT_DELAY_MS
    let lastMessageAt = Date.now()
    let cancelled = false

    function connect() {
      socket = new WebSocket(url)
      lastMessageAt = Date.now()

      socket.onopen = () => {
        if (cancelled) return
        setConnected(true)
        reconnectDelay = INITIAL_RECONNECT_DELAY_MS
      }

      socket.onclose = () => {
        if (cancelled) return
        setConnected(false)
        reconnectTimer = setTimeout(connect, reconnectDelay)
        reconnectDelay = Math.min(reconnectDelay * 2, MAX_RECONNECT_DELAY_MS)
      }

      socket.onmessage = (message) => {
        if (cancelled) return
        lastMessageAt = Date.now()

        let raw: unknown
        try {
          raw = JSON.parse(message.data as string)
        } catch {
          return
        }
        if (isHeartbeat(raw)) return

        const event = toEvent(raw)
        setEvents((prev) => [...prev, event].slice(-MAX_EVENTS))
      }
    }

    watchdogTimer = setInterval(() => {
      if (cancelled) return
      if (Date.now() - lastMessageAt > WATCHDOG_SILENCE_MS) {
        socket?.close() // → onclose → backoff-реконнект
      }
    }, WATCHDOG_INTERVAL_MS)

    connect()

    return () => {
      cancelled = true
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
      if (watchdogTimer !== undefined) clearInterval(watchdogTimer)
      socket?.close()
    }
  }, [url])

  return { events, connected }
}

function isHeartbeat(raw: unknown): boolean {
  return typeof raw === 'object' && raw !== null && (raw as { type?: unknown }).type === 'heartbeat'
}

function toEvent(raw: unknown): AfmEvent {
  const obj = isRecord(raw) ? raw : {}
  return {
    type: typeof obj.type === 'string' ? obj.type : '',
    payload: obj.data,
    stageId: typeof obj.stage_id === 'string' ? obj.stage_id : '',
    timestamp: new Date().toISOString(),
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}
```

- [ ] **Step 4: Тесты — проходят**

Run: `npm test -- use-event-feed`
Expected: PASS (фильтр heartbeat + watchdog close).

- [ ] **Step 5: typecheck + Коммит**

```bash
npm run typecheck
git add pkg/web/dashboard/src/hooks/use-event-feed/
git commit -m "feat(dashboard): watchdog вебсокета и фильтр heartbeat в use-event-feed"
```

---

## Task 3: useStickToBottom (auto-scroll)

**Files:**
- Create: `src/hooks/use-stick-to-bottom/use-stick-to-bottom.ts`
- Test: `src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts`

**Interfaces:**
- Produces: `useStickToBottom<T extends HTMLElement>(): { ref: RefObject<T>; stick: boolean; jumpToBottom: () => void }`.

- [ ] **Step 1: Падающий тест**

`src/hooks/use-stick-to-bottom/use-stick-to-bottom.test.ts`:
```ts
import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useStickToBottom } from './use-stick-to-bottom'

describe('useStickToBottom', () => {
  it('stick=true по умолчанию; jumpToBottom выставляет stick=true', () => {
    const { result } = renderHook(() => useStickToBottom<HTMLDivElement>())
    expect(result.current.stick).toBe(true)
    expect(typeof result.current.jumpToBottom).toBe('function')
    expect(result.current.ref.current).toBeNull()
  })
})
```

- [ ] **Step 2: Run — FAIL**

Run: `npm test -- use-stick-to-bottom`
Expected: FAIL (модуль не найден).

- [ ] **Step 3: Реализация**

`src/hooks/use-stick-to-bottom/use-stick-to-bottom.ts`:
```ts
import { useCallback, useEffect, useRef, useState } from 'react'

const STICK_THRESHOLD_PX = 40

// Держит скролл-контейнер прижатым к низу, пока пользователь сам не уехал вверх.
// stick=true → MutationObserver докручивает вниз при росте контента.
// stick=false → не трогаем скролл; jumpToBottom() возвращается к хвосту.
export function useStickToBottom<T extends HTMLElement>(): {
  ref: React.RefObject<T>
  stick: boolean
  jumpToBottom: () => void
} {
  const ref = useRef<T>(null)
  const [stick, setStick] = useState(true)
  const stickRef = useRef(true)
  stickRef.current = stick

  const jumpToBottom = useCallback(() => {
    const el = ref.current
    if (el === null) return
    el.scrollTop = el.scrollHeight
    setStick(true)
  }, [])

  useEffect(() => {
    const el = ref.current
    if (el === null) return

    const onScroll = () => {
      const near = el.scrollHeight - el.scrollTop - el.clientHeight < STICK_THRESHOLD_PX
      setStick(near)
    }
    const obs = new MutationObserver(() => {
      if (stickRef.current) el.scrollTop = el.scrollHeight
    })

    el.addEventListener('scroll', onScroll, { passive: true })
    obs.observe(el, { childList: true, subtree: true, characterData: true })
    return () => {
      el.removeEventListener('scroll', onScroll)
      obs.disconnect()
    }
  }, [])

  return { ref, stick, jumpToBottom }
}
```

- [ ] **Step 4: Run — PASS**

Run: `npm test -- use-stick-to-bottom`
Expected: PASS.

- [ ] **Step 5: Коммит**

```bash
git add pkg/web/dashboard/src/hooks/use-stick-to-bottom/
git commit -m "feat(dashboard): хук useStickToBottom (auto-scroll к хвосту)"
```

---

## Task 4: Применить stick-to-bottom к диалогу и фиду

**Files:**
- Modify: `src/components/dialog-channel/DialogChannel.tsx`
- Modify: `src/components/event-feed/EventFeedPanel.tsx`

**Interfaces:**
- Consumes: `useStickToBottom` (Task 3).

- [ ] **Step 1: DialogChannel — обернуть историю+pending в scroll-контейнер с хуком**

В `DialogChannel.tsx`:
- импорт: `import { useStickToBottom } from '../../hooks/use-stick-to-bottom'`
- в компоненте: `const feed = useStickToBottom<HTMLDivElement>()`
- обернуть блок `<div id="dialog-history">…</div>` + `{pending !== null && …}` в общий scroll-контейнер:
```tsx
<div id="dialog-scroll" className="dialog-scroll" ref={feed.ref}>
  <div id="dialog-history" className={`dialog-history${historyCollapsed ? ' collapsed' : ''}`}>
    {renderHistory(entries)}
  </div>
  {pending !== null && ( /* …текущий pending… */ )}
</div>
```
- кнопка «↓ к последнему» когда `!feed.stick` (рядом с actions или поверх контейнера):
```tsx
{!feed.stick && (
  <button type="button" className="jump-latest" onClick={feed.jumpToBottom}>↓ к последнему</button>
)}
```

- [ ] **Step 2: EventFeedPanel — применить хук к списку событий**

В `EventFeedPanel.tsx`: импорт хука, `const feed = useStickToBottom<HTMLDivElement>()`, навесить `ref={feed.ref}` на scroll-контейнер ленты, добавить кнопку «↓ к последнему» при `!feed.stick`.

- [ ] **Step 3: CSS (style.css)**

```css
.dialog-scroll, .event-feed-scroll { overflow-y: auto; position: relative; }
.jump-latest {
  position: sticky; bottom: 6px; margin-left: auto; display: block;
  background: rgba(111,212,204,0.15); border: 1px solid var(--teal);
  color: var(--ink); padding: 3px 8px; font-size: 10px; cursor: pointer;
}
```

- [ ] **Step 4: Ручная проверка + typecheck**

Run: `npm run typecheck` + `make build` + открыть дашборд, запустить флоу с диалогом — убедиться, что лента/диалог докручиваются вниз и кнопка появляется при скролле вверх.
Expected: автоскролл работает.

- [ ] **Step 5: Коммит**

```bash
git add pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx \
        pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx \
        pkg/web/dashboard/public/style.css
git commit -m "feat(dashboard): auto-scroll диалога и фида к хвосту (useStickToBottom)"
```

---

## Task 5: Зависимость + PanelFrame + Maximizable

**Files:**
- Create: `src/components/panel-frame/PanelFrame.tsx`
- Create: `src/components/layout/Maximizable.tsx`
- Modify: `pkg/web/dashboard/package.json`
- Test: `src/components/layout/Maximizable.test.tsx`

**Interfaces:**
- Produces: `MaximizeProvider`, `useMaximize(): {maximizedKey, toggle}`, `Maximizable({id, children})`; `PanelFrame({title, maximizeId?, attention?, actions?, children})`.

- [ ] **Step 1: Поставить зависимость**

Run: `cd pkg/web/dashboard && npm install react-resizable-panels`
Expected: пакет в `dependencies`, `package-lock.json` обновлён.

- [ ] **Step 2: Падающий тест Maximizable**

`src/components/layout/Maximizable.test.tsx`:
```tsx
import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MaximizeProvider, Maximizable, useMaximize } from './Maximizable'

function Toggle({ id }: { id: string }) {
  const { toggle } = useMaximize()
  return <button onClick={() => toggle(id)}>toggle</button>
}

describe('Maximizable', () => {
  it('рендерит inline; по toggle уходит в overlay-портал; Esc сворачивает', () => {
    const { container } = render(
      <MaximizeProvider>
        <Maximizable id="plan">
          <p>plan-content</p>
        </Maximizable>
        <Toggle id="plan" />
      </MaximizeProvider>,
    )
    expect(container.querySelector('.maximize-overlay')).toBeNull()
    expect(screen.getByText('plan-content')).toBeInTheDocument()

    fireEvent.click(screen.getByText('toggle'))
    expect(document.querySelector('.maximize-overlay')).not.toBeNull()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(document.querySelector('.maximize-overlay')).toBeNull()
  })
})
```

- [ ] **Step 3: Run — FAIL**

Run: `npm test -- Maximizable`
Expected: FAIL (модуль не найден).

- [ ] **Step 4: Реализовать Maximizable**

`src/components/layout/Maximizable.tsx`:
```tsx
import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

type MaximizeState = { maximizedKey: string | null; toggle: (key: string) => void }
const MaximizeContext = createContext<MaximizeState>({ maximizedKey: null, toggle: () => {} })

export function MaximizeProvider({ children }: { children: ReactNode }) {
  const [maximizedKey, setMaximizedKey] = useState<string | null>(null)
  const toggle = useCallback((key: string) => {
    setMaximizedKey((cur) => (cur === key ? null : key))
  }, [])
  useEffect(() => {
    if (maximizedKey === null) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMaximizedKey(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [maximizedKey])
  return <MaximizeContext.Provider value={{ maximizedKey, toggle }}>{children}</MaximizeContext.Provider>
}

export function useMaximize(): MaximizeState {
  return useContext(MaximizeContext)
}

// Максимизация через портал: инстанс компонента сохраняется (состояние не теряется).
export function Maximizable({ id, children }: { id: string; children: ReactNode }) {
  const { maximizedKey } = useMaximize()
  if (maximizedKey !== id) return <>{children}</>
  return createPortal(
    <div className="maximize-overlay" role="dialog" aria-modal="true">{children}</div>,
    document.body,
  )
}
```

- [ ] **Step 5: Реализовать PanelFrame**

`src/components/panel-frame/PanelFrame.tsx`:
```tsx
import { type ReactNode } from 'react'
import { useMaximize } from '../layout/Maximizable'

type PanelFrameProps = {
  title: string
  maximizeId?: string
  attention?: boolean
  actions?: ReactNode
  children: ReactNode
}

export function PanelFrame({ title, maximizeId, attention, actions, children }: PanelFrameProps) {
  const { maximizedKey, toggle } = useMaximize()
  const maximized = maximizeId !== undefined && maximizedKey === maximizeId
  return (
    <section
      className={`panel-frame${attention ? ' attention' : ''}`}
      data-panel={maximizeId ?? undefined}
    >
      <header className="panel-frame-header">
        <h3>{title}</h3>
        <div className="panel-frame-actions">
          {actions}
          {maximizeId !== undefined && (
            <button
              type="button"
              className="icon-btn"
              aria-label={maximized ? 'Свернуть' : 'Развернуть'}
              onClick={() => toggle(maximizeId)}
            >
              {maximized ? '✕' : '⛶'}
            </button>
          )}
        </div>
      </header>
      <div className="panel-frame-body">{children}</div>
    </section>
  )
}
```

- [ ] **Step 6: Run — PASS**

Run: `npm test -- Maximizable`
Expected: PASS (toggle → overlay, Esc → схлоп).

- [ ] **Step 7: Коммит**

```bash
git add pkg/web/dashboard/package.json pkg/web/dashboard/package-lock.json \
        pkg/web/dashboard/src/components/layout/Maximizable.tsx \
        pkg/web/dashboard/src/components/layout/Maximizable.test.tsx \
        pkg/web/dashboard/src/components/panel-frame/PanelFrame.tsx
git commit -m "feat(dashboard): react-resizable-panels + PanelFrame + Maximizable (portal)"
```

---

## Task 6: DashboardLayout + проводка в App

**Files:**
- Create: `src/components/layout/DashboardLayout.tsx`
- Modify: `src/app/App.tsx`

**Interfaces:**
- Consumes: `PanelFrame`/`Maximizable` (Task 5), существующие панели.
- Produces: `<DashboardLayout {stages,plan,dialog,log,feed}/>`, App обёрнут в `<MaximizeProvider>`.

- [ ] **Step 1: Реализовать DashboardLayout**

`src/components/layout/DashboardLayout.tsx`:
```tsx
import { type ReactNode } from 'react'
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels'

type Props = {
  stages: ReactNode
  stageHeader: ReactNode   // заголовок выбранной стадии (имя + статус) — над plan
  plan: ReactNode
  dialog: ReactNode
  log: ReactNode
  feed: ReactNode
}

export function DashboardLayout({ stages, stageHeader, plan, dialog, log, feed }: Props) {
  return (
    <PanelGroup direction="horizontal" autoSaveId="afm-cols">
      <Panel order={1} minSize={12}>{stages}</Panel>
      <PanelResizeHandle className="resize-handle resize-handle-v" />
      <Panel order={2}>
        <div className="detail-column">
          <div id="detail-header" className="detail-header">{stageHeader}</div>
          <PanelGroup direction="vertical" autoSaveId="afm-rows" className="detail-rows">
          <Panel order={1} minSize={15}>{plan}</Panel>
          <PanelResizeHandle className="resize-handle resize-handle-h" />
          <Panel order={2} minSize={15}>{dialog}</Panel>
          <PanelResizeHandle className="resize-handle resize-handle-h" />
          <Panel order={3} minSize={10}>{log}</Panel>
          </PanelGroup>
        </div>
      </Panel>
      <PanelResizeHandle className="resize-handle resize-handle-v" />
      <Panel order={3} minSize={12}>{feed}</Panel>
    </PanelGroup>
  )
}
```

- [ ] **Step 2: Обновить App.tsx — MaximizeProvider + DashboardLayout**

В `src/app/App.tsx`:
- импорты: `MaximizeProvider` и `DashboardLayout` из `../components/layout`.
- заменить блок `<main id="main">…<StagesList/>…<section id="detail-panel">…</section>…<EventFeedPanel/></main>` на:
```tsx
<main id="main">
  <div className="ray" aria-hidden="true" />
  <MaximizeProvider>
    <DashboardLayout
      stages={<StagesList stages={stages} selectedStageId={selectedStageId} onSelect={setSelectedStageId} />}
      stageHeader={
        selectedStage === null ? null : (
          <>
            <h2 id="detail-title">{selectedStage.name !== '' ? selectedStage.name : selectedStage.id}</h2>
            <span id="detail-status" className="status-badge" data-status={selectedStage.status}>
              {STAGE_STATUS_LABELS[selectedStage.status]}
            </span>
          </>
        )
      }
      plan={<PlanPanel stage={selectedStage} />}
      dialog={<DialogChannel stage={selectedStage} />}
      log={<LogPanel entries={logEntries} />}
      feed={<EventFeedPanel events={events} />}
    />
  </MaximizeProvider>
</main>
```
- `FlowHeader`, `Footer`, `ConsumptionPanel` вне `<main>` — без изменений. Детали `detail-empty`/`detail-content`/`detail-header` перенести внутрь PlanPanel/DialogChannel (или оставить заголовок стадии в App, передавая в layout) — минимально: оставить выбранную стадию как сейчас, панели сами рендерят свой контент.

- [ ] **Step 3: CSS — resize-хендлы + panel-frame + maximize-overlay**

В `public/style.css`:
```css
#main { display: flex; min-height: 0; }
.detail-column { display: flex; flex-direction: column; min-height: 0; height: 100%; }
.detail-rows { flex: 1 1 0; min-height: 0; }   /* вертикальный PanelGroup занимает остаток высоты */
.resize-handle { flex: 0 0 4px; background: rgba(183,135,255,0.2); transition: background 0.15s; }
.resize-handle:hover, .resize-handle[data-resize-handle-active] { background: var(--violet); }
.resize-handle-v { cursor: col-resize; width: 4px; }
.resize-handle-h { cursor: row-resize; height: 4px; }
.panel-frame { display: flex; flex-direction: column; min-height: 0; height: 100%; }
.panel-frame-header { display: flex; align-items: center; justify-content: space-between; }
.panel-frame-body { flex: 1; overflow: auto; min-height: 0; }
.icon-btn { background: transparent; border: 1px solid rgba(111,212,204,0.4); color: var(--ink); cursor: pointer; font-size: 12px; line-height: 1; padding: 2px 6px; }
.icon-btn:hover { border-color: var(--teal); }
.maximize-overlay { position: fixed; inset: 0; z-index: 1000; background: var(--bg); overflow: auto; padding: 16px; }
```
(Точные CSS-переменные темы — `--violet`, `--teal`, `--ink`, `--bg` — уже есть в style.css; сверить по файлу.)

- [ ] **Step 4: typecheck + сборка + ручная проверка**

Run: `npm run typecheck && make build`, открыть дашборд.
Expected: 3 колонки тянутся, внутри central — plan/dialog/log тянутся по вертикали, размеры сохраняются после перезагрузки (localStorage `afm-cols`/`afm-rows`).

- [ ] **Step 5: Коммит**

```bash
git add pkg/web/dashboard/src/components/layout/DashboardLayout.tsx \
        pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/public/style.css
git commit -m "feat(dashboard): resizable-лейаут на react-resizable-panels (DashboardLayout)"
```

---

## Task 7: Maximize-кнопки в Plan/Dialog/EventFeed

**Files:**
- Modify: `src/components/plan-panel/PlanPanel.tsx`
- Modify: `src/components/dialog-channel/DialogChannel.tsx`
- Modify: `src/components/event-feed/EventFeedPanel.tsx`
- Modify: `src/components/log-panel/LogPanel.tsx`

**Interfaces:**
- Consumes: `PanelFrame`, `Maximizable` (Task 5).

- [ ] **Step 1: Обернуть PlanPanel в Maximizable + PanelFrame**

В `PlanPanel.tsx` корневой рендер:
```tsx
<Maximizable id="plan">
  <PanelFrame title="Plan" maximizeId="plan">
    {/* существующее содержимое плана */}
  </PanelFrame>
</Maximizable>
```
(imports: `Maximizable` из `../layout/Maximizable`, `PanelFrame` из `../panel-frame/PanelFrame`.)

- [ ] **Step 2: DialogChannel — `id="dialog"`**

В `DialogChannel.tsx` обернуть `<section id="dialog-section">…</section>` в `<Maximizable id="dialog">` и использовать `<PanelFrame title="Communication channel" maximizeId="dialog">` как шапку (заменить существующий пустой `<h3>`).

- [ ] **Step 3: EventFeedPanel — `id="feed"`**

Обернуть в `<Maximizable id="feed"><PanelFrame title="Event feed" maximizeId="feed">…</PanelFrame></Maximizable>`.

- [ ] **Step 4: LogPanel — PanelFrame без maximize**

В `LogPanel.tsx`: `<PanelFrame title="Log">…</PanelFrame>` (без `maximizeId`).

- [ ] **Step 5: typecheck + сборка + ручная проверка**

Run: `npm run typecheck && make build`, открыть дашборд.
Expected: иконки ⛶ в шапках Plan/Dialog/Feed; клик разворачивает на весь экран (состояние/скролл сохраняются), ✕/Esc сворачивают.

- [ ] **Step 6: Коммит**

```bash
git add pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx \
        pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx \
        pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx \
        pkg/web/dashboard/src/components/log-panel/LogPanel.tsx
git commit -m "feat(dashboard): maximize панелей plan/dialog/feed через PanelFrame+Maximizable"
```

---

## Task 8: useAttention + useTitleFlash

**Files:**
- Create: `src/hooks/use-attention/use-attention.ts` + `.test.ts`
- Create: `src/hooks/use-title-flash/use-title-flash.ts` + `.test.ts`

**Interfaces:**
- Produces: `useAttention(stage: Stage|null): {needsAttention, kind:'dialog'|'plan'|null}`; `ATTENTION_STATUSES`; `anyAwaiting(stages)`; `useTitleFlash(active: boolean): void`.

- [ ] **Step 1: Падающий тест useAttention**

`src/hooks/use-attention/use-attention.test.ts`:
```ts
import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAttention, anyAwaiting } from './use-attention'
import type { Stage } from '../../types'

const stage = (status: Stage['status']): Stage =>
  ({ id: 's', name: 'n', status, updatedAt: '' })

describe('useAttention', () => {
  it('dialog для awaiting_user_input, plan для awaiting_approval, null иначе', () => {
    expect(renderHook(() => useAttention(stage('awaiting_user_input'))).result.current).toEqual({ needsAttention: true, kind: 'dialog' })
    expect(renderHook(() => useAttention(stage('awaiting_approval'))).result.current).toEqual({ needsAttention: true, kind: 'plan' })
    expect(renderHook(() => useAttention(stage('running'))).result.current).toEqual({ needsAttention: false, kind: null })
    expect(renderHook(() => useAttention(null)).result.current).toEqual({ needsAttention: false, kind: null })
  })
  it('anyAwaiting ищет по массиву стадий', () => {
    expect(anyAwaiting([stage('running'), stage('awaiting_user_input')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('done')])).toBe(false)
  })
})
```

- [ ] **Step 2: Реализация useAttention**

`src/hooks/use-attention/use-attention.ts`:
```ts
import { useMemo } from 'react'
import type { Stage, StageStatus } from '../../types'

export type AttentionKind = 'dialog' | 'plan'
export type Attention = { needsAttention: boolean; kind: AttentionKind | null }

export const ATTENTION_STATUSES: ReadonlySet<StageStatus> = new Set<StageStatus>([
  'awaiting_user_input',
  'awaiting_approval',
])

export function anyAwaiting(stages: Stage[]): boolean {
  return stages.some((s) => ATTENTION_STATUSES.has(s.status))
}

export function useAttention(stage: Stage | null): Attention {
  return useMemo(() => {
    if (stage === null) return { needsAttention: false, kind: null }
    const kind: AttentionKind | null =
      stage.status === 'awaiting_user_input' ? 'dialog'
      : stage.status === 'awaiting_approval' ? 'plan'
      : null
    return { needsAttention: kind !== null, kind }
  }, [stage])
}
```

- [ ] **Step 3: Падающий тест useTitleFlash**

`src/hooks/use-title-flash/use-title-flash.test.ts`:
```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useTitleFlash } from './use-title-flash'

describe('useTitleFlash', () => {
  beforeEach(() => { Object.defineProperty(document, 'hidden', { value: true, configurable: true }); document.title = 'afm Dashboard' })
  afterEach(() => { vi.useRealTimers() })
  it('мигает title когда вкладка скрыта и active=true', () => {
    vi.useFakeTimers()
    renderHook(() => useTitleFlash(true))
    const orig = 'afm Dashboard'
    expect(document.title).toBe(orig)
    vi.advanceTimersByTime(1500)
    expect(document.title).not.toBe(orig) // сменился на flash
  })
  it('не мигает при active=false', () => {
    vi.useFakeTimers()
    renderHook(() => useTitleFlash(false))
    vi.advanceTimersByTime(3000)
    expect(document.title).toBe('afm Dashboard')
  })
})
```

- [ ] **Step 4: Реализация useTitleFlash**

`src/hooks/use-title-flash/use-title-flash.ts`:
```ts
import { useEffect } from 'react'

const FLASH_TITLE = '⚠ Нужно действие — afm Dashboard'
const FLASH_INTERVAL_MS = 1500

// Мигает document.title когда вкладка в фоне и active=true (стадия ждёт юзера).
// При возврате на вкладку (visibilitychange) — восстанавливает исходный title.
export function useTitleFlash(active: boolean): void {
  useEffect(() => {
    if (!active) return
    const original = document.title
    let toggle = false
    let timer: ReturnType<typeof setInterval> | undefined

    const stop = () => {
      if (timer !== undefined) clearInterval(timer)
      timer = undefined
      document.title = original
    }
    const onVisibility = () => {
      if (document.hidden) {
        timer = setInterval(() => {
          toggle = !toggle
          document.title = toggle ? FLASH_TITLE : original
        }, FLASH_INTERVAL_MS)
      } else {
        stop()
      }
    }

    document.addEventListener('visibilitychange', onVisibility)
    if (document.hidden) onVisibility()
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      stop()
    }
  }, [active])
}
```

- [ ] **Step 5: Run — PASS**

Run: `npm test -- use-attention use-title-flash`
Expected: PASS.

- [ ] **Step 6: Коммит**

```bash
git add pkg/web/dashboard/src/hooks/use-attention/ pkg/web/dashboard/src/hooks/use-title-flash/
git commit -m "feat(dashboard): хуки useAttention и useTitleFlash (сигнал «ждёт пользователя»)"
```

---

## Task 9: Проводка attention (sidebar/header/glow/scroll/title)

**Files:**
- Modify: `src/app/App.tsx`
- Modify: `src/components/stages-list/StagesList.tsx`
- Modify: `src/components/flow-header/FlowHeader.tsx`
- Modify: `src/components/plan-panel/PlanPanel.tsx`, `dialog-channel/DialogChannel.tsx` (attention-флаг в PanelFrame)
- Modify: `public/style.css`

**Interfaces:**
- Consumes: `useAttention`, `anyAwaiting`, `useTitleFlash` (Task 8), `PanelFrame.attention`.

- [ ] **Step 1: App — посчитать attention + title-flash + scrollIntoView**

В `src/app/App.tsx`:
- импорты: `useAttention`, `anyAwaiting`, `useTitleFlash`, `useRef`, `useEffect`.
- после `selectedStage`:
```tsx
const attention = useAttention(selectedStage)
const anyAttention = anyAwaiting(stages)
useTitleFlash(attention.needsAttention)
```
- `scrollIntoView` при входе в awaiting (refs на панели — пробросить через `planRef`/`dialogRef`, или навесить id и искать):
```tsx
const lastKind = useRef<typeof attention.kind>(null)
useEffect(() => {
  if (attention.kind !== null && lastKind.current !== attention.kind) {
    // PanelFrame ставит data-panel={maximizeId}; PanelFrame без maximizeId (Log) его не имеет.
    const sel = `[data-panel="${attention.kind}"]`
    document.querySelector(sel)?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
  }
  lastKind.current = attention.kind
}, [attention.kind])
```
- передать `attention`/`kind` в `PlanPanel`/`DialogChannel` (через props) и `anyAttention` в `FlowHeader`.

- [ ] **Step 2: StagesList — data-attribute на awaiting-стадии**

В `StagesList.tsx` для элемента стадии:
```tsx
<li data-attention={ATTENTION_STATUSES.has(stage.status) ? 'true' : undefined} …>
```
(импорт `ATTENTION_STATUSES` из `../../hooks/use-attention`.)

- [ ] **Step 3: FlowHeader — индикатор-точка**

В `FlowHeader.tsx` принять `attention?: boolean`; рендерить пульсирующую точку когда true:
```tsx
{attention && <span className="attention-dot" aria-label="Нужно действие" />}
```

- [ ] **Step 4: PanelFrame.attention в Plan/Dialog**

PlanPanel передаёт `attention={attention?.kind === 'plan'}` в `PanelFrame`; DialogChannel — `attention={attention?.kind === 'dialog'}` (props `attention?: Attention` на этих панелях).

- [ ] **Step 5: CSS — attention-анимации**

В `public/style.css`:
```css
@keyframes attentionPulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(229,212,66,0.0); }
  50%      { box-shadow: 0 0 12px 2px rgba(229,212,66,0.65); }
}
.panel-frame.attention { animation: attentionPulse 1.6s ease-in-out infinite; border: 1px solid var(--amber); }
.stages-list [data-attention='true'] { animation: attentionPulse 1.6s ease-in-out infinite; }
.attention-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--amber); animation: attentionPulse 1.2s ease-in-out infinite; }
```
(переменная темы `--amber` уже есть.)

- [ ] **Step 6: typecheck + сборка + ручная проверка**

Run: `npm run typecheck && make build`, запустить флоу с диалогом/планом.
Expected: при awaiting — пульсирует элемент стадии в сайдбаре, точка в шапке, светится панель plan/dialog, центральная колонка скроллится к ней; в фоновой вкладке мигает title.

- [ ] **Step 7: Полный прогон тестов + линт**

Run: `cd pkg/web/dashboard && npm test` + `cd ../.. && bin/golangci-lint run ./pkg/... && go test ./pkg/server/`
Expected: всё зелёное.

- [ ] **Step 8: Коммит**

```bash
git add pkg/web/dashboard/src/app/App.tsx \
        pkg/web/dashboard/src/components/stages-list/StagesList.tsx \
        pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx \
        pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx \
        pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx \
        pkg/web/dashboard/public/style.css
git commit -m "feat(dashboard): attention-сигнал — пульс сайдбар/шапка/панель + title-flash + автоскролл"
```

---

## Финальная проверка

- [ ] `cd pkg/web/dashboard && npm run typecheck && npm test` — зелёные.
- [ ] `go test ./pkg/server/` и `bin/golangci-lint run ./pkg/...` — зелёные.
- [ ] `make build` — веб пересобирается и вкомпиливается.
- [ ] Ручной смок дашборда: resize сохраняется, maximize план/диалог/фид, при awaiting — пульс+title+скролл, ws переподключается после `kill -9` соединения / ухода ноутбука в сон (визуально по индикатору connected в шапке).

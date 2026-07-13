Domain: паттерны React-компонентов для переписывания дашборда afm — функциональные компоненты и хуки, без классов.

## Общий принцип

Только функциональные компоненты + хуки. Никаких классовых компонентов, никакого Redux/Zustand/Context для
глобального состояния — состояние живёт в хуках верхнего компонента (`App`) и спускается пропсами, как это
было в едином `app.js`.

## Структура компонента

```tsx
// src/components/StagesList.tsx
type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
}

export function StagesList({ stages, selectedStageId, onSelect }: StagesListProps) {
  return (
    <ul id="stages-list">
      {stages.map((stage) => (
        <li
          key={stage.id}
          className={stage.id === selectedStageId ? 'selected' : ''}
          onClick={() => onSelect(stage.id)}
        >
          {stage.name}
        </li>
      ))}
    </ul>
  )
}
```

Именованные экспорты — по умолчанию (совпадает с общим конвеншеном проекта: default export только для
главной сущности модуля, для React это применимо к `App` в `main.tsx`).

## Хуки для поллинга и WebSocket

Логика опроса стадий/событий и WebSocket-подписки инкапсулируется в кастомные хуки, а не разбрасывается по
`useEffect` внутри компонентов представления:

```tsx
// src/hooks/useStagePolling.ts
export function useStagePolling(intervalMs: number) {
  const [stages, setStages] = useState<Stage[]>([])

  useEffect(() => {
    let cancelled = false

    async function poll() {
      const response = await fetch('/api/status')
      const data = await response.json()
      if (!cancelled) setStages(data)
    }

    poll()
    const timer = setInterval(poll, intervalMs)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [intervalMs])

  return stages
}
```

`cancelled`-флаг обязателен: React 18 `StrictMode` в dev-режиме монтирует эффекты дважды, и без флага
возможна запись состояния от "устаревшего" запроса после отмены.

## WebSocket-хук

Переподключение с экспоненциальным backoff (старт 1с, удвоение, cap 10с) и статус соединения — часть
текущего поведения `connectWS()` в `app.js`, обязательны к переносу для полного паритета:

```tsx
// src/hooks/useEventFeed.ts
export function useEventFeed(url: string) {
  const [events, setEvents] = useState<AfmEvent[]>([])
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    let socket: WebSocket
    let reconnectDelay = 1000
    let reconnectTimer: ReturnType<typeof setTimeout>
    let cancelled = false

    function connect() {
      socket = new WebSocket(url)

      socket.onopen = () => {
        setConnected(true)
        reconnectDelay = 1000
      }
      socket.onclose = () => {
        setConnected(false)
        if (cancelled) return
        reconnectTimer = setTimeout(connect, reconnectDelay)
        reconnectDelay = Math.min(reconnectDelay * 2, 10000)
      }
      socket.onmessage = (message) => {
        const event = JSON.parse(message.data)
        setEvents((prev) => [...prev, event])
      }
    }

    connect()
    return () => {
      cancelled = true
      clearTimeout(reconnectTimer)
      socket.close()
    }
  }, [url])

  return { events, connected }
}
```

## Темы (novacorps / goga)

Тема определяется сервером (`pkg/server` инжектирует `style-goga.css` при `theme=goga`) и не управляется
React-состоянием — глобальные CSS-файлы (`style.css`, `style-goga.css`) подключаются как есть в `index.html`
или через `public/`, классы компонентов остаются теми же, что и в текущей вёрстке, чтобы существующие
селекторы тем продолжали работать без изменений.

## markdown-it

Оборачивается в тонкий компонент. Библиотека переходит с вендоренного `public/markdown-it.min.js` на
обычную npm-зависимость (`markdown-it` в `package.json`) — `import` резолвится штатно через `node_modules`,
без alias-хаков в `vite.config.ts`, `public/markdown-it.min.js` и `<script>`-тег в `index.html` удаляются.
Конфигурация парсера сохраняется дословно:

```tsx
// src/components/Markdown.tsx
import MarkdownIt from 'markdown-it'

const md = new MarkdownIt({ html: false, linkify: true })

export function Markdown({ source }: { source: string }) {
  return <div dangerouslySetInnerHTML={{ __html: md.render(source) }} />
}
```

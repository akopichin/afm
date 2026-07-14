# react (react@18)

React-конвенции дашборда afm. Аудитория: клеточки `pkg/web/dashboard/src/` (компоненты
`src/components/*`, хуки `src/hooks/*`, корневая композиция `src/app`).

## Только функциональные компоненты

Компонент — именованный экспорт функции, возвращающей `ReactElement`. Пропсы описаны локальным
типом `XxxProps`; состояние живёт в хуках верхнего уровня и пробрасывается вниз пропсами
(см. `src/components/footer/Footer.tsx`):

```tsx
import type { ReactElement } from 'react'
import type { Stage } from '../../types'

type FooterProps = {
  stages: Stage[]
  elapsedMs: number
}

export function Footer({ stages, elapsedMs }: FooterProps): ReactElement {
  const total = stages.length
  const done = stages.filter((s) => s.status === 'done').length
  return <footer id="footer">…</footer>
}
```

## Состояние — в кастомных хуках, без Redux/Zustand/Context

Каждая зона ответственности — свой хук (`useStatus`, `useEventFeed`, `useStageLog`, `useElapsed`,
`useUsageData`). Хук владеет `useState`/`useEffect`/`useRef` и возвращает плоский объект данных
плюс действия (например `refresh()`). Корневая композиция `App` вызывает хуки и распределяет
данные по дочерним компонентам пропсами. Глобальные стейт-менеджеры и `React.Context` НЕ
используются — единый источник состояния это хук + пропсы.

```ts
export function useStatus(): FlowStatus & { refresh: () => void } {
  const [status, setStatus] = useState<FlowStatus>(EMPTY_STATUS)
  const cancelledRef = useRef(false)
  // …поллинг в useEffect, стабильный refresh через useCallback
  return { ...status, refresh }
}
```

## Жизненный цикл и сайд-эффекты

`useEffect` — для подписок и таймеров; очистка обязательна (`clearInterval`, флаг `cancelledRef`,
чтобы не трогать state после размонтирования). Стабильные колбэки — через `useCallback`, чтобы
`refresh` не пересоздавался на каждом рендере и не перезапускал зависимые эффекты.

## Клетка = каталог + barrel `index.ts`

Каждая клеточка — отдельный каталог с `index.ts` barrel-фасадом, реэкспортирующим публичное API.
Потребители импортируют из корня клетки (`from '../../types'`), а не из внутренних файлов.

## DOM-id сохраняются для тем

Стабильные `id` элементов (`progress-fill`, `detail-title`, …) — точки привязки CSS-тем
(novacorps / goga) и якоря запросов в тестах. Не удалять и не переименовывать без правок стилей.

## Особенности

- React 18 (`react@^18`, `react-dom@^18`); сборка — Vite. Правится только `src/`, собранный
  бандл в корне `pkg/web/dashboard/` руками не трогается (см. клеточку `pkg/web/dashboard`).
- Резайз панелей — через `react-resizable-panels`.
- Рендеринг markdown планов/фидбека — через `markdown-it` (клеточка `plan-panel`).

Domain: рендер футера дашборда (прогресс/старт/elapsed) для корневой композиции App.

## Базовое использование

```tsx
import { Footer } from '../../components/footer'
import { useStatus } from '../../hooks/use-status'
import { useElapsed } from '../../hooks/use-elapsed'

function App() {
  const { stages, startedAt } = useStatus()
  const elapsedMs = useElapsed(startedAt)
  return <Footer stages={stages} startedAt={startedAt} elapsedMs={elapsedMs} />
}
```

## Особенности

- Footer — чистый презентационный компонент; elapsed приходит уже готовым (elapsedMs) от хука useElapsed,
  который сам тикает каждую секунду. Таким образом elapsed обновляется плавно раз в секунду (полное
  соответствие elapsedTimer в текущем app.js), а не только при ре-рендере родителя.
- Прогресс считается по стадиям со статусом done.

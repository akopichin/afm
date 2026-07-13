Domain: секундомер прошедшего от старта времени для футера дашборда.

## Базовое использование

```tsx
import { useElapsed } from '../../hooks/use-elapsed'
import { Footer } from '../../components/footer'

function DashboardFooter({ stages, startedAt }: { stages: Stage[]; startedAt: string }) {
  const elapsedMs = useElapsed(startedAt)
  return <Footer stages={stages} elapsedMs={elapsedMs} />
}
```

## Особенности

- Хук сам держит односекундный интервал и перевычисляет значение каждую секунду — счётчик не «застывает» между
  тиками поллинга стадий (полное соответствие elapsedTimer в текущем app.js).
- Если startedAt пустой/невалидный — elapsedMs равен 0.

Domain: тестирование React-компонентов дашборда afm через Vitest + React Testing Library — замена Jest для
этой конкретной клеточки.

## Почему не Jest

Общий конвеншен проекта (`conventions.md`) предписывает Jest. Для Vite-based React-SPA Vitest используется
как отраслевой стандарт вместо Jest: тот же движок трансформации (esbuild/Vite), нет отдельной ESM-конфигурации
(`--experimental-vm-modules`), совместимость с `jsdom`. Это точечное отступление от `conventions.md`,
действующее только внутри `pkg/web/dashboard` — API самих тестов (`describe`/`test`/`expect`) совместим с
Jest на уровне синтаксиса.

## Конфигурация

```ts
// vite.config.ts (тот же файл, что и сборка)
export default defineConfig({
  // ...
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
  },
})
```

```ts
// src/test/setup.ts
import '@testing-library/jest-dom/vitest'
```

## Структура и именование тестов

Совпадает с общим конвеншеном проекта: тест лежит рядом с исходником.

- `src/components/StagesList.tsx` → `src/components/StagesList.test.tsx`
- `src/hooks/useStagePolling.ts` → `src/hooks/useStagePolling.test.ts`

## Паттерн теста компонента

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, test, expect } from 'vitest'
import { StagesList } from './StagesList'

describe('StagesList', () => {
  test('calls onSelect with clicked stage id', () => {
    const stages = [{ id: 's1', name: 'Propose', status: 'pending' as const, updatedAt: '' }]
    const onSelect = vi.fn()

    render(<StagesList stages={stages} selectedStageId={null} onSelect={onSelect} />)
    fireEvent.click(screen.getByText('Propose'))

    expect(onSelect).toHaveBeenCalledWith('s1')
  })
})
```

## Мок fetch и WebSocket в хуках

Внешние границы (`fetch`, `WebSocket`) — единственное место для моков, как и в общем конвеншене
(`conventions.md`: "Mock at the module boundary, not at the implementation level"):

```ts
import { renderHook, waitFor } from '@testing-library/react'
import { vi, test, expect } from 'vitest'
import { useStagePolling } from './useStagePolling'

test('useStagePolling loads stages from /api/status', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({ json: () => Promise.resolve([{ id: 's1', name: 'Propose' }]) }),
  )

  const { result } = renderHook(() => useStagePolling(1000))

  await waitFor(() => expect(result.current).toHaveLength(1))
})
```

## Команды

| Назначение               | Команда                  |
|---------------------------|--------------------------|
| Запустить все тесты       | `npx vitest run`         |
| Запустить конкретный файл | `npx vitest run <path>`  |
| Тесты с покрытием         | `npx vitest run --coverage` |

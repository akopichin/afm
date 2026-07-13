Domain: TypeScript-конфигурация и типизация для React-дашборда afm — строгий режим, без `any`, типы API-ответов.

## tsconfig

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "isolatedModules": true,
    "skipLibCheck": true,
    "types": ["vite/client", "vitest/globals"]
  },
  "include": ["src"]
}
```

`strict: true` обязателен — заменяет отсутствие типов в текущем vanilla-JS проекте на компилятором
проверяемые гарантии. `any` запрещён везде, кроме явно задокументированных мест разбора внешнего JSON
(см. ниже).

## Типы API-ответов

Все структуры, приходящие с backend (`/api/stages`, `/api/events`, `/api/usage`, `WS /ws`), описываются
как именованные типы рядом с хуком, который их потребляет:

```ts
// src/types/stage.ts
// Полный набор статусов стадии afm (done — завершена, не completed; см. statusLabels в app.js).
export type StageStatus =
  | 'pending' | 'planning' | 'awaiting_approval' | 'revising' | 'ready'
  | 'running' | 'done' | 'failed' | 'retrying' | 'awaiting_user_input'

export type Stage = {
  id: string
  name: string
  status: StageStatus
  updatedAt: string
}
```

## Разбор непроверенного JSON

`fetch(...).json()` возвращает `any` — типизировать сразу в точке получения, не пропускать `any` дальше по
цепочке вызовов:

```ts
async function fetchStages(): Promise<Stage[]> {
  const response = await fetch('/api/stages')
  const data: unknown = await response.json()
  return data as Stage[] // единственная разрешённая точка утверждения типа для внешнего JSON
}
```

Если требуется рантайм-валидация (а не только компайл-тайм утверждение), использовать `zod` — но только по
явному запросу задачи, не добавлять зависимость проактивно.

## Именование

- Типы и интерфейсы — `PascalCase`, без префикса `I`
- Файлы компонентов — `PascalCase.tsx` (совпадает с именем экспортируемого компонента)
- Файлы хуков, утилит, типов — `kebab-case.ts`, совместимо с общим конвеншеном проекта (`kebab-case` для
  модулей)

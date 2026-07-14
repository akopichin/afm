# typescript (TypeScript 5, strict)

TypeScript-конвенции дашборда afm. Аудитория: клеточки `pkg/web/dashboard/src/`. Это сознательное
отклонение от общих JS/Jest-конвенций проекта в пользу TypeScript + Vitest, ограниченное рамками
rewrite дашборда.

## strict-режим

`tsconfig.json` включает `strict`, `noUncheckedIndexedAccess`, `noUnusedLocals`,
`noUnusedParameters`, `isolatedModules`, `forceConsistentCasingInFileNames`. Следствия: индекс
массива/объекта даёт `T | undefined` (обязана обрабатывать `undefined`), неиспользуемые
параметры и переменные — ошибка компиляции. Локальная проверка типов: `npm run typecheck`
(`tsc --noEmit`).

## Единственная точка приведения внешнего JSON

Ответ `fetch().json()` типизируется как `unknown` и приводится к доменному типу в одной
функции-нормализаторе с runtime-проверками (type guards), а не через `as`/`any`. Это локализует
недоверенный ввод в одном месте клеточки (см. `src/hooks/use-status/use-status.ts`):

```ts
const data: unknown = await response.json()
setStatus(normalizeStatus(data))

function normalizeStatus(raw: unknown): FlowStatus {
  const obj = isRecord(raw) ? raw : {}
  const flowName = typeof obj.flow_name === 'string' ? obj.flow_name : ''
  // …остальные поля с такими же проверками
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}
```

## Именование и объединения

- Типы — PascalCase; функции и переменные — camelCase.
- Перечислимые множества — литеральные объединения (`'pending' | 'running' | …`), сопровождаемые
  `readonly`-массивом значений (`STAGE_STATUSES`) и type guard-проверкой (`isStageStatus`).
- Импорт только типов — через `import type` (требование `isolatedModules`):
  `import type { Stage } from '../../types'`.

## Barrel-реэкспорт типов

Клеточка `src/types` реэкспортирует все общие типы через `index.ts`; потребители берут их из
`'../../types'`, а не из отдельных файлов.

## Особенности

- Компиляция — только на проверку (`noEmit`): бандл собирает Vite, `tsc` используется как
  тайпчекер.
- Тесты — Vitest (`globals: true`, jsdom); паттерны `vi.stubGlobal('WebSocket', …)` и
  `vi.spyOn(globalThis, 'fetch')`, запросы в DOM — по стабильным `id`.

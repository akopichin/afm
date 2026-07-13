Domain: Vite как сборщик статического React-SPA дашборда afm — конфигурация сборки без SSR, вывод в корень `dashboard/`.

## Назначение

Vite используется только как dev-server + статический сборщик. Никакого SSR, никакого Node-рантайма в проде —
результат сборки - это `index.html` + `assets/*.js` + `assets/*.css`, которые отдаются как есть через
`pkg/web/embed.go` (`//go:embed dashboard/*` + `fs.Sub(..., "dashboard")`).

## Расположение исходников и вывода

```
dashboard/
├── src/                # исходники React (main.tsx, App.tsx, components/, hooks/)
├── public/             # статика без обработки (favicon.svg, quarium-logo.png, markdown-it.min.js)
├── index.html          # шаблон, Vite инжектирует сюда собранные теги <script>/<link>
├── vite.config.ts
├── tsconfig.json
├── package.json
└── (после сборки) assets/*.js, assets/*.css — генерируются в тот же dashboard/, поверх исходников
```

Ключевое требование: `build.outDir` должен указывать на сам `dashboard/` (а не на поддиректорию `dist/`),
чтобы `embed.go` продолжал работать без изменений.

## Конфигурация

```ts
// vite.config.ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: './', // относительные пути к ассетам — бандл может отдаваться с любого префикса пути на сервере
  build: {
    outDir: '.', // собирать прямо в dashboard/, не в dashboard/dist
    emptyOutDir: false, // не удалять src/, public/, конфиги при пересборке
    assetsDir: 'assets',
  },
})
```

`emptyOutDir: false` обязателен — иначе Vite при каждой сборке будет стирать `src/`, `public/` и конфиги,
которые лежат в той же директории, что и вывод.

## Dev-режим и проксирование API/WS

В деле дашборд общается с `pkg/server` через `fetch /api/*` и `WebSocket /ws`. В dev-режиме Vite dev-server
должен проксировать эти пути на запущенный backend, чтобы не требовать CORS-настроек на сервере:

```ts
export default defineConfig({
  // ...
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
})
```

Порт backend уточняется по месту запуска `pkg/server` в конкретном окружении.

## Команды

| Назначение          | Команда           |
|---------------------|--------------------|
| Dev-сервер          | `npm run dev`     |
| Продакшн-сборка     | `npm run build`   |
| Просмотр сборки     | `npm run preview` |

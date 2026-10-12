import { copyFileSync } from 'node:fs'

// Восстанавливает исходный dev-шаблон index.html из index.dev.html. После сборки index.html остаётся
// собранным — именно его встраивает pkg/web.
copyFileSync('index.dev.html', 'index.html')

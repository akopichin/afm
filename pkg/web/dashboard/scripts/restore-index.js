import { copyFileSync } from 'node:fs'

// Восстанавливает исходный dev-шаблон index.html из index.dev.html.
//
// Причина: vite build с outDir='.' перезаписывает index.html собранной версией
// (со ссылкой ./assets/index-<hash>.js). Без восстановления повторная сборка
// падала бы — clean:assets удаляет assets/, и Vite не находит точку входа в index.html.
// Поэтому restore-index запускается перед `vite` (dev) и `vite build` (через npm-скрипты).
// После сборки index.html остаётся собранным — именно его встраивает pkg/web.
copyFileSync('index.dev.html', 'index.html')

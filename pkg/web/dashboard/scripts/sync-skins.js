import { cpSync, rmSync } from 'node:fs'

// Зеркалит skins/ в public/skins/ перед каждой сборкой.
//
// Причина: index.dev.html ссылается на скин абсолютным путём
// (/skins/coffee/index.css). Если Vite не находит совпадения в publicDir,
// он на build трактует этот <link> как обычный CSS-модуль и бандлит его в
// хэшированный assets/index-*.css — а pkg/server/server.go делает runtime
// string-replace "skins/coffee/" на реальную тему конфига в embedded
// index.html, и после такого бандлинга ему просто нечего заменять:
// переключение тем (goga/novacorps/custom) ломается для всех, кроме дефолтной.
// Наличие public/skins/ с тем же путём заставляет Vite отдавать этот <link>
// как обычный static passthrough (publicDir-копия без обработки) — то, что
// нужно. Синхронизация здесь гарантирует, что public/skins/ никогда не
// отстанет от настоящего skins/ (было: копия от одного момента в истории,
// молча перетиравшая актуальный skins/ при каждой сборке содержимым старой
// версии — баг, найденный при верификации разбиения pkg/orchestrator).
rmSync('public/skins', { recursive: true, force: true })
cpSync('skins', 'public/skins', { recursive: true })

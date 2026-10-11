import { chmodSync, cpSync, readdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'

// Зеркалит skins/ в public/skins/ перед каждой сборкой.
//
// Причина: index.dev.html ссылается на скин абсолютным путём
// (/skins/coffee/index.css). Если Vite не находит совпадения в publicDir,
// он на build трактует этот <link> как обычный CSS-модуль и бандлит его в
// хэшированный assets/index-*.css — а pkg/server/server.go делает runtime
// string-replace "skins/graphite/" на реальную тему конфига в embedded
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

// Normalize perms on the generated mirror to 0644 files / 0755 dirs. cpSync copies
// the SOURCE file mode verbatim (no umask), so any drift in skins/ perms — or a
// restrictive mode produced by the build environment (e.g. a Docker dev-pipeline run
// that rebuilds the dashboard) — would otherwise propagate here. If a copied file
// lands as 0200 (write-only), git can no longer READ it to hash it and reports the
// tracked file as spuriously "modified" even though its content is identical. Forcing
// standard, readable perms makes public/skins environment-independent and keeps
// `git status` clean across host and container builds.
function normalizePerms(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name)
    if (entry.isDirectory()) {
      chmodSync(p, 0o755)
      normalizePerms(p)
    } else {
      chmodSync(p, 0o644)
    }
  }
}
chmodSync('public/skins', 0o755)
normalizePerms('public/skins')

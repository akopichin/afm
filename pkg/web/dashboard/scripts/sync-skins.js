import { chmodSync, cpSync, readdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'

// Зеркалит skins/ в public/skins/ перед каждой сборкой. Причина: index.dev.html ссылается на скин
// абсолютным путём (/skins/coffee/index.css).
rmSync('public/skins', { recursive: true, force: true })
cpSync('skins', 'public/skins', { recursive: true })

// Normalize perms on the generated mirror to 0644 files / 0755 dirs.
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

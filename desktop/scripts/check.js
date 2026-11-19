import { execFileSync } from 'node:child_process';
import { readdirSync } from 'node:fs';
for (const dir of ['src', 'renderer', 'scripts', 'test']) {
  for (const file of readdirSync(dir).filter(f => /\.(js|cjs)$/.test(f))) {
    execFileSync(process.execPath, ['--check', dir + '/' + file], { stdio: 'inherit' });
  }
}

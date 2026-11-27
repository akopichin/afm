import path from 'node:path';
import { access, stat } from 'node:fs/promises';
import { constants } from 'node:fs';
export async function runtimeEnvironment(env, home, platform = process.platform) {
  const dirs = (env.PATH || '').split(path.delimiter).filter(path.isAbsolute);
  const candidates = platform === 'darwin'
    ? [path.join(home,'.docker/bin'), '/Applications/Docker.app/Contents/Resources/bin', '/usr/local/bin', '/opt/homebrew/bin']
    : ['/usr/local/bin', '/usr/bin'];
  const next = {...env, AFM_DESKTOP_MANAGED_RUNTIME:'1'};
  for (const dir of [...new Set([...dirs,...candidates])]) {
    const file = path.join(dir,'docker');
    try {
      await access(file,constants.X_OK);
      if (!(await stat(file)).isFile()) continue;
    } catch { continue; }
    if (!dirs.includes(dir)) next.PATH = [env.PATH,dir].filter(Boolean).join(path.delimiter);
    break;
  }
  return next;
}

import { chmod, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { desktopDir } from '../files.js';

export const wrapperPath = home => path.join(desktopDir(home), 'bin', 'afm-stage-chrome');

const quote = value => "'" + String(value).replaceAll("'", "'\\''") + "'";

// AFM launches the "Chrome" executable named by AFM_CHROME_EXECUTABLE. This shell wrapper starts the companion
// with the app's own runtime (no Node install needed) and hands it the broker file; it holds no secret.
export async function writeWrapper(home, { node, script, env = {} }, broker) {
  const file = wrapperPath(home);
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  const vars = Object.entries(env).map(([name, value]) => `${name}=${quote(value)} `).join('');
  const body = `#!/bin/sh\n# AFM Desktop stage browser companion.\nexec env ${vars}${quote(node)} ${quote(script)} ${quote('--desktop-broker=' + broker)} "$@"\n`;
  await writeFile(file, body, { mode: 0o700 });
  await chmod(file, 0o700);
  return file;
}

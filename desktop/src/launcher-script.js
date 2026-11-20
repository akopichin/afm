import { chmod, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

const quote = value => "'" + String(value).replaceAll("'", "'\\''") + "'";

export async function writeLauncherScript(file, { node, script, env = {} }, args = []) {
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  const vars = Object.entries(env).map(([name, value]) => `${name}=${quote(value)} `).join('');
  const body = `#!/bin/sh\n# AFM Desktop launcher.\nexec env ${vars}${quote(node)} ${quote(script)} ${args.map(quote).join(' ')} "$@"\n`;
  await writeFile(file, body, { mode: 0o700 });
  await chmod(file, 0o700);
  return file;
}

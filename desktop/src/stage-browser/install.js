import { chmod, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { desktopDir } from '../files.js';

export const wrapperPath = home => path.join(desktopDir(home), 'bin', 'afm-stage-chrome');

const quote = value => "'" + String(value).replaceAll("'", "'\\''") + "'";

async function writeScript(file, { node, script, env = {} }, args = []) {
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  const vars = Object.entries(env).map(([name, value]) => `${name}=${quote(value)} `).join('');
  const body = `#!/bin/sh\n# AFM Desktop launcher.\nexec env ${vars}${quote(node)} ${quote(script)} ${args.map(quote).join(' ')} "$@"\n`;
  await writeFile(file, body, { mode: 0o700 });
  await chmod(file, 0o700);
  return file;
}

// AFM launches the "Chrome" executable named by AFM_CHROME_EXECUTABLE. This shell wrapper starts the companion
// with the app's own runtime (no Node install needed) and hands it the broker file; it holds no secret.
export const writeWrapper = (home, launcher, broker) => writeScript(wrapperPath(home), launcher, ['--desktop-broker=' + broker]);

// AFM looks for `chrome-devtools-mcp` on PATH and never installs it; this wrapper runs the copy shipped in the
// app with the app's own runtime, so a clean Mac needs no Node or npm.
export const mcpWrapperPath = home => path.join(desktopDir(home), 'bin', 'chrome-devtools-mcp');
export const writeMcpWrapper = (home, mcp) => writeScript(mcpWrapperPath(home), mcp);

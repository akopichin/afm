import { execFile } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { promisify, parseEnv } from 'node:util';
import path from 'node:path';

const exec = promisify(execFile);
const variable = /^[A-Za-z_][A-Za-z0-9_]*$/;
const reserved = /^(ELECTRON_|AFM_DESKTOP_|AFM_STAGE_BROWSER_)/;
export const validVariable = name => typeof name === 'string' && variable.test(name) && !reserved.test(name);

export async function desktopEnvironment(prefs, home, base = process.env) {
  const { environment } = await prefs.get();
  const { shell, file, variables } = environment;
  let imported = {};
  if (shell !== 'none') {
    const executable = shell === 'zsh' && base.SHELL?.endsWith('/zsh') || shell === 'bash' && base.SHELL?.endsWith('/bash')
      ? base.SHELL : '/bin/' + shell;
    try {
      const { stdout } = await exec(executable, ['-ic', 'printf "\\000AFM_ENV\\000"; /usr/bin/env -0'],
        {cwd: home, env: base, timeout: 5000, maxBuffer: 1 << 20, killSignal: 'SIGKILL'});
      const marker = stdout.lastIndexOf('\0AFM_ENV\0');
      if (marker < 0) throw Error('Environment output is missing.');
      imported = Object.fromEntries(stdout.slice(marker + 9).split('\0')
        .filter(value => value.includes('=')).map(value => [value.slice(0, value.indexOf('=')), value.slice(value.indexOf('=') + 1)]));
    } catch { throw Error('Could not load the exported environment from .' + shell + 'rc.'); }
  }
  let extra = {};
  if (file) {
    const resolved = file.startsWith('~/') ? path.join(home, file.slice(2)) : file;
    try { extra = parseEnv(await readFile(resolved, 'utf8')); }
    catch { throw Error('Could not read the environment file selected in Desktop Settings.'); }
  }
  return {...base, ...Object.fromEntries(Object.entries({...imported, ...extra, ...variables}).filter(([name]) => validVariable(name)))};
}

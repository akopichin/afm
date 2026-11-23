import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFile, stat } from 'node:fs/promises';
import { promisify, parseEnv } from 'node:util';
import path from 'node:path';

const exec = promisify(execFile);
const variable = /^[A-Za-z_][A-Za-z0-9_]*$/;
const reserved = /^(ELECTRON_|AFM_DESKTOP_|AFM_STAGE_BROWSER_)/;
export const validVariable = name => typeof name === 'string' && variable.test(name) && !reserved.test(name);

const STARTUP_FILES = ['.zshenv', '.zprofile', '.zshrc', '.zlogin', '.bash_profile', '.bashrc', '.profile'];
const MAX_SNAPSHOTS = 8;
// Enough to discover a redirected startup directory and survive one edit made while the shell was reading.
const MAX_ATTEMPTS = 3;
const ENV_MARKER = '\0AFM_ENV\0';
const ZDOTDIR_MARKER = '\0AFM_ZDOTDIR\0';
const snapshots = new Map();
const imports = new Map();
// refreshEnvironment() bumps it, so an import that was already running cannot store a stale result.
let generation = 0;
// Aborted on Quit, see stopEnvironmentImports(). Quit is final: nothing re-arms it.
const quit = new AbortController();

const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const fingerprint = file => stat(file).then(info => [info.mtimeMs, info.size], () => null);
const startupFiles = dirs => dirs.flatMap(dir => STARTUP_FILES.map(name => path.join(dir, name)));
const fingerprints = files => Promise.all(files.map(async file => [file, await fingerprint(file)]));
const unchanged = async watched => (await fingerprints(watched.map(([file]) => file))).every(([, current], index) => JSON.stringify(current) === JSON.stringify(watched[index][1]));

// Settings and the inherited environment. Files they point to are fingerprinted per import instead, see `watched`.
const snapshotKey = (environment, home, base) => hash([environment, home, Object.entries(base).sort()]);

// One run of the shell. The files it may read are fingerprinted BEFORE it starts, so an edit made while it runs is
// noticed afterwards. `dir` is the startup directory the shell ended up with; zsh can move it with ZDOTDIR.
async function importEnvironment({ shell, file, variables }, home, base, dirs, signal) {
  const resolvedFile = file && (file.startsWith('~/') ? path.join(home, file.slice(2)) : file);
  const watched = await fingerprints([...(shell === 'none' ? [] : startupFiles(dirs)), ...(resolvedFile ? [resolvedFile] : [])]);
  let imported = {};
  let dir;
  if (shell !== 'none') {
    const executable = shell === 'zsh' && base.SHELL?.endsWith('/zsh') || shell === 'bash' && base.SHELL?.endsWith('/bash')
      ? base.SHELL : '/bin/' + shell;
    signal.throwIfAborted();
    try {
      // ZDOTDIR is closed by its own NUL, so whatever the shell prints on exit is not taken as part of the path.
      const importing = exec(executable, ['-ic', 'printf "\\000AFM_ENV\\000"; /usr/bin/env -0; printf "\\000AFM_ZDOTDIR\\000%s\\000" "${ZDOTDIR-}"'],
        {cwd: home, env: base, timeout: 15000, maxBuffer: 1 << 20, killSignal: 'SIGKILL', signal});
      importing.child.stdin.end();
      // `signal` stops the wait at once but sends SIGTERM, which an interactive shell ignores, so the shell is killed here.
      const kill = () => importing.child.kill('SIGKILL');
      signal.addEventListener('abort', kill);
      const { stdout } = await importing.finally(() => signal.removeEventListener('abort', kill));
      const marker = stdout.lastIndexOf(ENV_MARKER);
      const redirect = stdout.indexOf(ZDOTDIR_MARKER, marker);
      const end = stdout.indexOf('\0', redirect + ZDOTDIR_MARKER.length);
      if (marker < 0 || redirect < 0 || end < 0) throw Error('Environment output is missing.');
      imported = Object.fromEntries(stdout.slice(marker + ENV_MARKER.length, redirect).split('\0')
        .filter(value => value.includes('=')).map(value => [value.slice(0, value.indexOf('=')), value.slice(value.indexOf('=') + 1)]));
      const shellDir = stdout.slice(redirect + ZDOTDIR_MARKER.length, end);
      dir = shellDir && path.resolve(home, shellDir);
    } catch {
      signal.throwIfAborted();
      throw Error('Could not load the exported environment from .' + shell + 'rc.');
    }
  }
  let extra = {};
  if (resolvedFile) {
    try { extra = parseEnv(await readFile(resolvedFile, 'utf8')); }
    catch { throw Error('Could not read the environment file selected in Desktop Settings.'); }
  }
  // Quit may have arrived while the file was read: a result finished after it is not used.
  signal.throwIfAborted();
  return {result: {...base, ...Object.fromEntries(Object.entries({...imported, ...extra, ...variables}).filter(([name]) => validVariable(name)))}, watched, dir};
}

// A result is kept only if none of its files changed while the shell ran. A newly found startup directory was not
// fingerprinted beforehand, so that run is repeated with it; later imports start from the known directories.
async function validatedImport(environment, home, base, dirs, signal) {
  for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
    const { result, watched, dir } = await importEnvironment(environment, home, base, dirs, signal);
    const found = dir && !dirs.includes(dir);
    if (found) dirs = [...dirs, dir];
    const kept = !found && await unchanged(watched);
    // Quit may have arrived while the files were checked: a result finished after it is not used.
    signal.throwIfAborted();
    if (kept) return {result, watched, dirs};
  }
  throw Error('Shell startup files kept changing while the environment was loading. Try again.');
}

// The result is kept in memory and shared by concurrent callers; a failure is never kept. Callers get their own copy.
export async function desktopEnvironment(prefs, home, base = process.env) {
  const { environment } = await prefs.get();
  const key = snapshotKey(environment, home, base);
  const kept = snapshots.get(key);
  if (kept && await unchanged(kept.watched)) return {...kept.result};
  let running = imports.get(key);
  if (!running) {
    const started = generation;
    const dirs = kept?.dirs ?? [...new Set([home, base.ZDOTDIR && path.resolve(home, base.ZDOTDIR)].filter(Boolean))];
    running = validatedImport(environment, home, base, dirs, quit.signal).then(entry => {
      if (quit.signal.aborted) throw quit.signal.reason;
      if (started === generation) {
        if (snapshots.size >= MAX_SNAPSHOTS) snapshots.clear();
        snapshots.set(key, entry);
      }
      return entry;
    }).finally(() => { if (imports.get(key) === running) imports.delete(key); });
    imports.set(key, running);
  }
  return {...(await running).result};
}

// Settings → Environment "Refresh": the next call imports again.
export function refreshEnvironment() {
  generation++;
  snapshots.clear();
  imports.clear();
}

// Quit: running startup shells are killed, their imports fail without a retry or a kept result, and no new shell starts,
// not even after a refresh. A result that is already kept is still returned, because it needs no shell.
export function stopEnvironmentImports() {
  quit.abort(Error('Desktop is quitting, so the shell environment was not loaded.'));
}

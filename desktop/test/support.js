import { mkdtemp, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import os from 'node:os';
import { setTimeout as delay } from 'node:timers/promises';

export async function eventually(get, { timeout = 20000, what = 'condition' } = {}) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const value = await get();
    if (value) return value;
    await delay(100);
  }
  throw Error('Timed out waiting for ' + what);
}

// A disposable HOME for a real AFM process, with Docker and the browser off.
export async function tempHome(t) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'desktop-'));
  await mkdir(path.join(home, '.afm'), { recursive: true });
  await writeFile(path.join(home, '.afm', 'config.yaml'), 'docker:\n  enabled: false\nbrowser:\n  enabled: false\n');
  const env = { PATH: process.env.PATH, HOME: home, USERPROFILE: home, AFM_DOCKER: '0' };
  t.after(async () => {
    await stopEverything(home);
    // AFM's pipeline snapshots are read-only.
    try { execFileSync('chmod', ['-R', 'u+rwX', home]); } catch { /* Nothing to unlock. */ }
    await rm(home, { recursive: true, force: true });
  });
  return { home, env };
}

// Kills the host and every run registered in this home, then waits until nothing answers.
export async function stopEverything(home) {
  const pids = [];
  for (const dir of ['active-runs']) {
    for (const file of await readdir(path.join(home, '.afm', dir)).catch(() => [])) {
      try { pids.push(JSON.parse(await readFile(path.join(home, '.afm', dir, file), 'utf8')).pid); } catch { /* Record already gone. */ }
    }
  }
  try { pids.push(JSON.parse(await readFile(path.join(home, '.afm', 'serve', 'host.json'), 'utf8')).pid); } catch { /* No host. */ }
  for (const pid of pids.filter(pid => pid > 1)) try { process.kill(pid, 'SIGTERM'); } catch { /* Already exited. */ }
  await delay(300);
}

export async function makeProject(home, name, { git = false, flows = {} } = {}) {
  const dir = path.join(home, name);
  await mkdir(path.join(dir, '.afm', 'flows'), { recursive: true });
  for (const [file, body] of Object.entries(flows)) await writeFile(path.join(dir, '.afm', 'flows', file), body);
  if (git) {
    const run = (...args) => execFileSync('git', ['-C', dir, ...args], { stdio: 'ignore' });
    run('init', '-q', '-b', 'main');
    run('config', 'user.email', 't@example.com'); run('config', 'user.name', 'T');
    await writeFile(path.join(dir, 'README.md'), 'hello\n');
    // Flows are committed so a worktree sees them.
    run('add', '-A', '-f'); run('commit', '-q', '-m', 'init');
  }
  return dir;
}

export const sleepFlow = (name, seconds = 30, extra = '') => `name: ${name}\n${extra}stages:\n  - id: wait\n    script: |\n      sleep ${seconds}\n`;

#!/usr/bin/env node
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { readdir } from 'node:fs/promises';
import { createServer } from 'node:net';
import { spawnSync } from 'node:child_process';

const desktop = fileURLToPath(new URL('../', import.meta.url));
const worktree = path.dirname(path.resolve(desktop));
const primary = path.dirname(path.dirname(worktree));
const runs = path.join(primary, '.afm', 'runs');
const pipeline = path.join(desktop, 'workflow');
const task = path.join(worktree, 'specs', 'desktop', 'plan.md');

function run(command, args, cwd, capture = false) {
  const result = spawnSync(command, args, {
    cwd, encoding: 'utf8', stdio: capture ? ['ignore', 'pipe', 'inherit'] : 'inherit',
    env: { ...process.env, AFM_BROWSER: '0' }, maxBuffer: 128 * 1024 * 1024
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw Error(command + ' ended with ' + (result.signal || result.status));
  return result.stdout?.trim();
}

async function runIds() {
  try {
    return (await readdir(runs, {withFileTypes: true})).filter(entry =>
      entry.isDirectory() && /^desktop-\d{8}-\d{6}-[a-z0-9]+$/.test(entry.name)).map(entry => entry.name);
  } catch (error) {
    if (error.code === 'ENOENT') return [];
    throw error;
  }
}

async function availablePort() {
  const server = createServer();
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  return String(port);
}

async function main() {
  const branch = run('git', ['branch', '--show-current'], worktree, true);
  if (process.platform !== 'darwin' || branch !== 'desktop' || path.basename(path.dirname(worktree)) !== '.worktrees')
    throw Error('Run the Mac launcher from the .worktrees/desktop checkout on branch desktop.');
  if (process.argv.slice(2).some(arg => arg !== '--inspect')) throw Error('Usage: overnight.js [--inspect]');
  run('afm', ['--dir', primary, 'validate', path.join(pipeline, 'flow.yaml')], primary);
  run('afm', ['--dir', primary, 'pipelines', 'inspect', pipeline], primary, true);
  if (process.argv.includes('--inspect')) {
    console.log('Validated desktop-dev for ' + worktree + '\nInitial task: ' + task);
    return;
  }
  const previous = new Set(await runIds());
  const port = await availablePort();
  console.log('Desktop development dashboard: http://127.0.0.1:' + port);
  run('afm', ['--dir', primary, 'run', '--pipeline', pipeline, '--worktree', 'desktop',
    '--mode', 'dashboard', '--port', port, '--exit-on-fail', '--task', '@' + task], primary);
  const created = (await runIds()).filter(id => !previous.has(id));
  if (created.length !== 1) throw Error('Could not identify the new Desktop run: ' + created.join(', '));
  const report = JSON.parse(run('afm', ['--dir', primary, 'report', created[0], '--format', 'json'], primary, true));
  if (report.run.id !== created[0] || report.run.status !== 'finished' ||
      !report.stages.some(stage => stage.id === 'deliver') || report.stages.some(stage => stage.status !== 'done'))
    throw Error('Desktop delivery is incomplete. Inspect run ' + created[0]);
  run('npm', ['ci', '--no-audit', '--no-fund'], desktop);
  // The sidecar is rebuilt natively from the final worktree, so the app never carries a stale or foreign AFM.
  for (const command of ['sidecar', 'check', 'test', 'perf', 'build', 'check:package', 'smoke:mac']) run('npm', ['run', command], desktop);
  console.log('Ready: ' + path.join(desktop, 'dist', 'AFM Desktop-darwin-' + process.arch, 'AFM Desktop.app'));
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });

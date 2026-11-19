import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

if (process.platform !== 'darwin') throw Error('Run the packaged window check on macOS.');
const contents = path.resolve('dist', 'AFM Desktop-darwin-' + process.arch, 'AFM Desktop.app/Contents');
const home = await mkdtemp(path.join(os.tmpdir(), 'afm-desktop-launch-'));
const env = {...process.env, HOME: home, AFM_DIR: home, AFM_DOCKER: '0'};
await mkdir(path.join(home, '.afm'));
const host = spawn(path.join(contents, 'Resources/afm'), ['serve', '--port', '0'], {cwd: home, env, stdio: 'ignore'});
const stopped = once(host, 'exit');
try {
  let ready = false;
  for (let i = 0; i < 50 && host.exitCode === null; i++) {
    try {
      ready = !!JSON.parse(await readFile(path.join(home, '.afm/serve/host.json'), 'utf8')).endpoint;
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
    if (ready) break;
    await delay(100);
  }
  if (!ready) throw Error('Bundled AFM could not start a temporary host.');
  const result = spawnSync(path.join(contents, 'MacOS/AFM Desktop'), [
    '--user-data-dir=' + path.join(home, 'profile'), '--desktop-launch-check'
  ], {cwd: home, env, encoding: 'utf8', timeout: 30000, killSignal: 'SIGKILL'});
  if (result.error) throw result.error;
  if (result.status !== 0 || !result.stdout.includes('AFM Desktop window and host ready'))
    throw Error('Native window failed to open (' + (result.signal || result.status) + '). ' + result.stderr);
  console.log(result.stdout.trim());
} finally {
  host.kill();
  await stopped;
  await rm(home, {recursive: true, force: true});
}

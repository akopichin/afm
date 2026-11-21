import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import { Host } from '../src/host.js';
import { Projects } from '../src/projects.js';
import { ServeConfig } from '../src/serve-config.js';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

if (process.platform !== 'darwin') throw Error('Run the packaged window check on macOS.');
const contents = path.resolve('dist', 'AFM Desktop-darwin-' + process.arch, 'AFM Desktop.app/Contents');
const home = await realpath(await mkdtemp(path.join(os.tmpdir(), 'afm-desktop-launch-')));
const env = {...process.env, HOME: home, AFM_DIR: home, AFM_DOCKER: '0', AFM_BROWSER: '0'};
const afmDir = path.join(home, '.afm');
await mkdir(path.join(afmDir, 'flows'), {recursive: true});
await writeFile(path.join(afmDir, 'flows', 'probe.yaml'), 'name: desktop-window-probe\nstages:\n  - id: visible-stage\n    script: sleep 60\n');
const projects = new Projects({ home, serveConfig: new ServeConfig(home) });
const project = await projects.add(home);
const host = spawn(path.join(contents, 'Resources/afm'), ['serve', '--port', '0'], {cwd: home, env, stdio: 'ignore'});
const stopped = once(host, 'exit');
const executable = path.join(contents, 'MacOS/AFM Desktop');

async function checkLibraryLinks() {
  const profile = '--user-data-dir=' + path.join(home, 'link-profile');
  const link = label => 'afm://library/install?' + new URLSearchParams({ source: 'https://example.invalid/' + label });
  const first = spawn(executable, [profile, '--desktop-library-link-check', link('cold')], { cwd: home, env });
  const ended = once(first, 'close');
  const cold = Promise.withResolvers();
  let stdout = '', stderr = '';
  first.stdout.setEncoding('utf8').on('data', chunk => {
    stdout += chunk;
    if (stdout.includes('Library link ready: cold')) cold.resolve();
  });
  first.stderr.setEncoding('utf8').on('data', chunk => { stderr += chunk; });
  const timeout = setTimeout(() => first.kill('SIGKILL'), 30000);
  try {
    await Promise.race([cold.promise, ended.then(() => { throw Error('Cold Library link failed: ' + stderr); })]);
    const second = spawnSync(executable, [profile, link('warm')], { cwd: home, env, encoding: 'utf8', timeout: 10000 });
    if (second.error || second.status !== 0) throw Error('Could not forward the warm Library link: ' + (second.error ?? second.stderr));
    const [code] = await ended;
    if (code !== 0 || !stdout.includes('Library link ready: warm')) throw Error('Warm Library link failed: ' + stderr);
    console.log(stdout.trim());
  } finally {
    clearTimeout(timeout);
    if (first.exitCode === null && first.signalCode === null) first.kill('SIGKILL');
    await ended.catch(() => {});
  }
}
const client = new Host(home, async () => { throw Error('The isolated host is unavailable.'); });
let run;
try {
  let ready = false;
  for (let i = 0; i < 210 && host.exitCode === null && !host.signalCode; i++) {
    try {
      ready = !!JSON.parse(await readFile(path.join(home, '.afm/serve/host.json'), 'utf8')).endpoint;
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
    if (ready) break;
    await delay(100);
  }
  if (!ready) throw Error('Bundled AFM could not start a temporary host (' + (host.signalCode || host.exitCode || 'timed out') + ').');
  await client.tool('afm_run_start', {request_id:'window-probe', project:project.id, flow:'probe.yaml', mode:'dashboard'});
  for (let i=0; i<100; i++) {
    run = (await client.runs()).find(row => row.run_id && row.launch?.state === 'ready');
    if (run) break;
    await delay(150);
  }
  if (!run) throw Error('Native dashboard test run failed to start.');
  const result = spawnSync(executable, [
    '--user-data-dir=' + path.join(home, 'profile'), '--desktop-launch-check', '--desktop-check-run=' + run.run_id
  ], {cwd: home, env, encoding: 'utf8', timeout: 30000, killSignal: 'SIGKILL'});
  if (result.error) throw result.error;
  if (result.status !== 0 || !result.stdout.includes('AFM Desktop window and host ready'))
    throw Error('Native window failed to open (' + (result.signal || result.status) + '). ' + result.stderr);
  console.log(result.stdout.trim());
  await checkLibraryLinks();
} finally {
  if (run) await client.tool('afm_run_cancel', {run_id:run.run_id}).catch(()=>{});
  await client.close();
  host.kill();
  await stopped;
  await rm(home, {recursive: true, force: true});
}

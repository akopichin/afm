import test from 'node:test';
import assert from 'node:assert/strict';
import { access, appendFile, mkdir, open, readFile, rm, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import path from 'node:path';
import { Prefs } from '../src/prefs.js';
import { desktopEnvironment, refreshEnvironment, stopEnvironmentImports } from '../src/environment.js';
import { eventually, tempHome } from './support.js';

const exec = promisify(execFile);

// Quit is terminal for the environment module, so each quit scenario gets a process of its own: this file is run
// again with only that test selected. Its output is shown when it fails.
const QUIT_SCENARIO = 'AFM_TEST_QUIT_SCENARIO';
function quitTest(name, body) {
  const selected = process.env[QUIT_SCENARIO];
  if (selected) {
    if (selected === name) test(name, body);
    return;
  }
  test(name, async () => {
    const pattern = '^' + name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$';
    // Without the runner's context variable the child reports plainly instead of in the runner's binary protocol.
    const { NODE_TEST_CONTEXT, ...env } = process.env;
    const { stdout } = await exec(process.execPath, ['--test-name-pattern=' + pattern, import.meta.filename],
      {env: {...env, [QUIT_SCENARIO]: name}, timeout: 60000})
      .catch(error => assert.fail(`${name} failed in its own process:\n${error.stdout}\n${error.stderr}`));
    assert.match(stdout, /^# pass 1$/m, 'the scenario did not run in its own process');
  });
}

// A FIFO as the environment file: the import can open it to read only after its shell exited, and the test can open
// it to write only once the import is reading. Writing to it lets the import go on.
async function fifoEnvironmentFile(fixture) {
  const file = path.join(fixture.home, 'gate.env');
  await exec('mkfifo', [file]);
  await fixture.prefs.updateEnvironment({file});
  return {
    whenRead: () => eventually(() => open(file, constants.O_WRONLY | constants.O_NONBLOCK).catch(() => null), {what: 'the import to read the environment file'}),
    release: async writer => { await writer.writeFile('FROM_FILE=1\n'); await writer.close(); },
  };
}

// Each import of the shell startup file appends a line to a counter file, so imports are counted for real.
async function shellFixture(t) {
  if (!await access('/bin/bash', constants.X_OK).then(() => true, () => false)) return t.skip('Requires bash');
  const {home} = await tempHome(t);
  const prefs = new Prefs(home);
  const counter = path.join(home, 'imports.log');
  const rc = path.join(home, '.bashrc');
  await writeFile(rc, `echo x >> "${counter}"\nexport AFM_CACHE_TEST=one\n`);
  await prefs.updateEnvironment({shell: 'bash'});
  const base = {PATH: '/usr/bin:/bin', HOME: home};
  return {home, prefs, rc, base,
    imports: async () => (await readFile(counter, 'utf8').catch(() => '')).split('\n').filter(Boolean).length,
    load: () => desktopEnvironment(prefs, home, base)};
}

test('a startup file reading from stdin receives EOF during provider discovery', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  await appendFile(fixture.rc, 'read -r interactive_answer || export AFM_IMPORT_NONINTERACTIVE=yes\n');
  assert.equal((await fixture.load()).AFM_IMPORT_NONINTERACTIVE, 'yes');
});

test('concurrent callers share one import and a repeat call imports nothing', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  refreshEnvironment();
  const [first, second] = await Promise.all([fixture.load(), fixture.load()]);
  assert.equal(first.AFM_CACHE_TEST, 'one');
  assert.equal(await fixture.imports(), 1);
  const third = await fixture.load();
  assert.equal(await fixture.imports(), 1);
  assert.deepEqual(third, first);
  first.AFM_CACHE_TEST = 'changed by a caller';
  assert.equal(second.AFM_CACHE_TEST, 'one');
  assert.equal((await fixture.load()).AFM_CACHE_TEST, 'one', 'callers got a shared object');
});

test('a changed preference, environment file, startup file or explicit refresh each import exactly once more', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  refreshEnvironment();
  await fixture.load();
  let expected = 1;
  const imported = async what => {
    const env = await fixture.load();
    await fixture.load();
    assert.equal(await fixture.imports(), ++expected, what);
    return env;
  };
  await fixture.prefs.updateEnvironment({name: 'MANUAL_VALUE', value: 'a'});
  assert.equal((await imported('a new variable')).MANUAL_VALUE, 'a');
  const file = path.join(fixture.home, 'desktop.env');
  await writeFile(file, 'FROM_FILE=1\n');
  await fixture.prefs.updateEnvironment({file});
  assert.equal((await imported('a selected environment file')).FROM_FILE, '1');
  await writeFile(file, 'FROM_FILE=22\n');
  assert.equal((await imported('an edited environment file')).FROM_FILE, '22');
  await appendFile(fixture.rc, 'export AFM_CACHE_TEST=two\n');
  assert.equal((await imported('an edited startup file')).AFM_CACHE_TEST, 'two');
  refreshEnvironment();
  await imported('an explicit refresh');
});

test('precedence is unchanged: manual variables beat the file, the file beats the shell', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  const file = path.join(fixture.home, 'desktop.env');
  await writeFile(file, 'AFM_CACHE_TEST=file\nONLY_FILE=1\n');
  await fixture.prefs.updateEnvironment({file});
  assert.equal((await fixture.load()).AFM_CACHE_TEST, 'file');
  await fixture.prefs.updateEnvironment({name: 'AFM_CACHE_TEST', value: 'manual'});
  const env = await fixture.load();
  assert.equal(env.AFM_CACHE_TEST, 'manual');
  assert.equal(env.ONLY_FILE, '1');
});

test('a failed import is not cached and is retried by the next call', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  await writeFile(fixture.rc, `echo x >> "${path.join(fixture.home, 'imports.log')}"\nexit 1\n`);
  await assert.rejects(fixture.load(), /Could not load the exported environment/);
  await assert.rejects(fixture.load(), /Could not load the exported environment/);
  assert.equal(await fixture.imports(), 2, 'the failure was cached');
  await writeFile(fixture.rc, `echo x >> "${path.join(fixture.home, 'imports.log')}"\nexport AFM_CACHE_TEST=fixed\n`);
  assert.equal((await fixture.load()).AFM_CACHE_TEST, 'fixed');
  assert.equal(await fixture.imports(), 3);
});

test('a refresh that arrives during an import does not let that import be kept', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  await appendFile(fixture.rc, 'sleep 0.4\n');
  const running = fixture.load();
  await eventually(async () => await fixture.imports() === 1, {what: 'the import to start'});
  refreshEnvironment();
  await running;
  await fixture.load();
  assert.equal(await fixture.imports(), 2);
});

// The shell itself moves its startup directory, as a ~/.zshenv that sets ZDOTDIR does. `after` runs once .zshrc was read.
async function redirectStartup(fixture, after = '') {
  const dir = path.join(fixture.home, 'zcfg');
  await mkdir(dir);
  const zshrc = path.join(dir, '.zshrc');
  await writeFile(zshrc, 'export AFM_REDIRECTED=one\n');
  await appendFile(fixture.rc, `ZDOTDIR="${dir}"\n. "$ZDOTDIR/.zshrc"\n${after}`);
  return zshrc;
}

test('editing a startup file that the shell itself redirected to another directory is noticed', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  const zshrc = await redirectStartup(fixture);
  refreshEnvironment();
  assert.equal((await fixture.load()).AFM_REDIRECTED, 'one');
  assert.equal((await fixture.load()).AFM_REDIRECTED, 'one');
  // The directory is only known after the first run has read it, so that run is repeated once with it fingerprinted.
  assert.equal(await fixture.imports(), 2);
  await writeFile(zshrc, 'export AFM_REDIRECTED=changed\n');
  assert.equal((await fixture.load()).AFM_REDIRECTED, 'changed');
  assert.equal(await fixture.imports(), 3);
});

test('a startup file edited while the shell was reading it is imported again, not kept with the old values', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  const hold = path.join(fixture.home, 'hold');
  const waiting = path.join(fixture.home, 'waiting');
  const zshrc = await redirectStartup(fixture, `while [ -e "${hold}" ]; do touch "${waiting}"; sleep 0.02; done\n`);
  refreshEnvironment();
  await fixture.load();
  assert.equal(await fixture.imports(), 2);
  await writeFile(hold, '');
  await writeFile(zshrc, 'export AFM_REDIRECTED=two\n');
  const loading = fixture.load();
  await eventually(() => access(waiting).then(() => true, () => false), {what: 'the shell to read .zshrc'});
  await writeFile(zshrc, 'export AFM_REDIRECTED=three\n');
  await rm(hold);
  assert.equal((await loading).AFM_REDIRECTED, 'three');
  assert.equal(await fixture.imports(), 4, 'the run that overlapped the edit is repeated once');
  assert.equal((await fixture.load()).AFM_REDIRECTED, 'three');
  assert.equal(await fixture.imports(), 4);
});

test('text the shell prints when it exits does not hide the redirected startup directory', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  const zshrc = await redirectStartup(fixture, `trap 'printf exit-banner' EXIT\n`);
  refreshEnvironment();
  assert.equal((await fixture.load()).AFM_REDIRECTED, 'one');
  await writeFile(zshrc, 'export AFM_REDIRECTED=changed\n');
  assert.equal((await fixture.load()).AFM_REDIRECTED, 'changed');
  assert.equal(await fixture.imports(), 3);
});

test('startup files that change on every run fail after a few attempts with a clear error that is not cached', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  const counter = path.join(fixture.home, 'imports.log');
  await appendFile(fixture.rc, `echo '# touched' >> "${fixture.rc}"\n`);
  refreshEnvironment();
  await assert.rejects(fixture.load(), /kept changing while the environment was loading/);
  assert.equal(await fixture.imports(), 3);
  await writeFile(fixture.rc, `echo x >> "${counter}"\nexport AFM_CACHE_TEST=steady\n`);
  assert.equal((await fixture.load()).AFM_CACHE_TEST, 'steady');
  assert.equal(await fixture.imports(), 4);
});

const alive = pid => { try { process.kill(pid, 0); return true; } catch { return false; } };

quitTest('quitting kills the shell of a running import, which is then neither retried nor kept', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  const hold = path.join(fixture.home, 'hold');
  const pids = path.join(fixture.home, 'pids');
  await writeFile(hold, '');
  // The redirected directory makes the import run the shell a second time once the first run ends.
  await redirectStartup(fixture, `echo $$ >> "${pids}"\nwhile [ -e "${hold}" ]; do sleep 0.02; done\n`);
  refreshEnvironment();
  const loading = fixture.load();
  const pid = Number(await eventually(() => readFile(pids, 'utf8').catch(() => ''), {what: 'the shell to start'}));
  stopEnvironmentImports();
  await assert.rejects(loading, /Desktop is quitting/);
  await eventually(() => !alive(pid), {what: 'the importing shell to be killed'});
  await rm(hold);
  await assert.rejects(fixture.load(), /Desktop is quitting/, 'a result was kept or a new shell started');
  assert.equal(await fixture.imports(), 1, 'the shell ran again after Quit');
});

quitTest('a refresh after Quit starts no shell', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  stopEnvironmentImports();
  refreshEnvironment();
  await assert.rejects(fixture.load(), /Desktop is quitting/, 'a refresh re-armed the imports after Quit');
  assert.equal(await fixture.imports(), 0);
});

quitTest('quitting after the shell exited, while the environment file is still read, keeps and returns nothing', async t => {
  const fixture = await shellFixture(t);
  if (!fixture) return;
  const gate = await fifoEnvironmentFile(fixture);
  const loading = fixture.load();
  const writer = await gate.whenRead();
  assert.equal(await fixture.imports(), 1);
  stopEnvironmentImports();
  await gate.release(writer);
  await assert.rejects(loading, /Desktop is quitting/, 'a result finished after Quit was returned');
  await assert.rejects(fixture.load(), /Desktop is quitting/, 'a result finished after Quit was kept');
  assert.equal(await fixture.imports(), 1);
});

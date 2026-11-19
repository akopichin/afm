import test from 'node:test';
import { EventEmitter } from 'node:events';
import { electronBackend } from '../src/stage-browser/electron-page.js';
import assert from 'node:assert/strict';
import { stat, readFile, writeFile, mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { eventually } from './support.js';
import { stageFixture, rawHello } from './stage-browser-support.js';
import { brokerFile } from '../src/stage-browser/broker.js';
import { createInspector } from '../src/stage-browser/bind.js';
import { stageURL } from '../src/stage-browser/cdp-router.js';
import { pipeFramer } from '../src/stage-browser/wire.js';
import { Host } from '../src/host.js';
import { Prefs } from '../src/prefs.js';

test('initial stage page exposes a parseable URL before it is announced to the MCP', async () => {
  const session = Object.assign(new EventEmitter(), {
    setPermissionRequestHandler() {}, webRequest: { onBeforeRequest() {} }, async clearStorageData() {},
  });
  class View {
    constructor() {
      let url = '';
      this.webContents = Object.assign(new EventEmitter(), {
        debugger: Object.assign(new EventEmitter(), { attach() {}, detach() {} }),
        setWindowOpenHandler() {}, isDestroyed: () => false, close() {},
        async loadURL(value) { url = value; }, getURL: () => url, getTitle: () => '',
      });
    }
    setBounds() {}
    setVisible() {}
  }
  const backend = electronBackend({ electron: { session: { fromPartition: () => session }, WebContentsView: View },
    window: () => null });
  try {
    const page = await backend.createPage('about:blank');
    assert.equal(new URL(page.info().url).href, 'about:blank');
  } finally { backend.dispose(); }
});

// Attaches like chrome-devtools-mcp (puppeteer): discover targets, auto-attach, then drive the page.
async function attach(client) {
  await client.ok('Browser.getVersion');
  await client.ok('Target.setDiscoverTargets', { discover: true });
  await client.ok('Target.setAutoAttach', { autoAttach: true, waitForDebuggerOnStart: false, flatten: true });
  const created = await client.event('Target.targetCreated');
  const attached = await client.event('Target.attachedToTarget');
  return { targetId: created.params.targetInfo.targetId, sessionId: attached.params.sessionId };
}

test('the host environment points runs at an executable wrapper and a private broker file', async t => {
  const fixture = await stageFixture(t);
  const env = await fixture.browsers.hostEnv();
  assert.equal(env.AFM_CHROME_EXECUTABLE, fixture.wrapper);
  assert.ok((await stat(fixture.wrapper)).mode & 0o100, 'AFM preflight needs an executable file');
  assert.equal((await stat(brokerFile(fixture.home))).mode & 0o077, 0, 'the broker file holds the token');
  assert.doesNotMatch(await readFile(fixture.wrapper, 'utf8'), new RegExp(fixture.browsers.token), 'the wrapper must not hold the token');
  assert.equal((await stat(fixture.browsers.dir)).mode & 0o077, 0, 'the socket directory is private');
});

test('a stage browser is created on AFM\'s first request and serves commands and events on its own page', async t => {
  const fixture = await stageFixture(t);
  const dir = await fixture.profile('live-run');
  const client = fixture.launch(dir);
  const version = await client.ok('Browser.getVersion');
  assert.match(version.product, /Fixture/);
  const { targetId, sessionId } = await attach(client);
  const [backend] = fixture.backends.values();
  assert.equal(backend.pages.length, 1, 'one page per stage, created for the first client');
  assert.equal(backend.pages[0].id, targetId);

  const answer = await client.ok('Runtime.evaluate', { expression: '1+1' }, sessionId);
  assert.equal(answer.result.value, `${targetId}:1+1`, 'the command reached this stage\'s page');
  assert.deepEqual(backend.pages[0].received.at(-1).sessionId, undefined, 'page-level sessions are not leaked to the page');

  const before = client.events.length;
  backend.pages[0].emit('event', 'Page.frameNavigated', { frame: { id: 'F' } });
  const event = await client.event('Page.frameNavigated', before);
  assert.equal(event.sessionId, sessionId, 'a page event is delivered on the client\'s session');

  await client.ok('Page.navigate', { url: 'http://127.0.0.1:18080/' }, sessionId);
  const changed = await client.event('Target.targetInfoChanged');
  assert.equal(changed.params.targetInfo.url, 'http://127.0.0.1:18080/', 'navigation is reflected to the client');
});

test('creating and closing pages stays inside the stage, and only web pages open', async t => {
  const fixture = await stageFixture(t);
  const client = fixture.launch(await fixture.profile('s1'));
  const { sessionId } = await attach(client);
  const [backend] = fixture.backends.values();

  const { targetId } = await client.ok('Target.createTarget', { url: 'https://example.test/' });
  assert.equal(backend.pages.length, 2);
  assert.equal((await client.ok('Target.getTargets')).targetInfos.length, 2);
  await client.ok('Target.attachToTarget', { targetId, flatten: true });
  for (const url of ['file:///etc/passwd', 'chrome://settings', 'javascript:alert(1)'])
    assert.match((await client.call('Target.createTarget', { url })).error.message, /Only http and https/);
  assert.equal(backend.pages.length, 2, 'a refused URL created nothing');

  await client.ok('Target.closeTarget', { targetId });
  assert.equal(backend.pages[1].closed, true);
  await client.event('Target.targetDestroyed');
  assert.equal((await client.ok('Target.getTargets')).targetInfos.length, 1);
  // The page owning the first session is still usable.
  assert.ok(await client.ok('Runtime.evaluate', { expression: 'x' }, sessionId));
});

test('commands that reach beyond the stage are refused', async t => {
  const fixture = await stageFixture(t);
  const client = fixture.launch(await fixture.profile('s1'));
  const { sessionId } = await attach(client);
  for (const method of ['Storage.getCookies', 'SystemInfo.getInfo', 'Browser.getBrowserCommandLine', 'Target.createBrowserContext', 'Target.exposeDevToolsProtocol'])
    assert.equal((await client.call(method)).error.code, -32601, method);
  assert.match((await client.call('Runtime.evaluate', { expression: 'x' }, 'NOT-A-SESSION')).error.message, /Session with given id not found/);
  assert.match((await client.call('Target.attachToTarget', { targetId: 'OTHER' })).error.message, /No target/);
  assert.match((await client.call('Target.createTarget', { url: 'about:blank', browserContextId: 'ELSEWHERE' })).error.message, /Unknown browser context/);
  assert.ok(await client.ok('Runtime.evaluate', { expression: 'x' }, sessionId), 'the stage still works after refusals');
});

test('simultaneous stages never share a page, a session or an event', async t => {
  const fixture = await stageFixture(t);
  const a = fixture.launch(await fixture.profile('stage-a'));
  const b = fixture.launch(await fixture.profile('stage-b'));
  const sa = await attach(a), sb = await attach(b);
  assert.notEqual(sa.targetId, sb.targetId);
  assert.equal(fixture.backends.size, 2);
  // Companions connect in any order, so find each stage's backend by its page.
  const owner = id => [...fixture.backends.values()].find(backend => backend.pages.some(page => page.id === id));
  const [backendA, backendB] = [owner(sa.targetId), owner(sb.targetId)];

  assert.equal((await a.ok('Runtime.evaluate', { expression: 'q' }, sa.sessionId)).result.value, `${sa.targetId}:q`);
  assert.equal((await b.ok('Runtime.evaluate', { expression: 'q' }, sb.sessionId)).result.value, `${sb.targetId}:q`);
  assert.match((await b.call('Runtime.evaluate', { expression: 'q' }, sa.sessionId)).error.message, /Session with given id not found/, 'stage A\'s session is useless to stage B');
  assert.match((await b.call('Target.attachToTarget', { targetId: sa.targetId })).error.message, /No target/);
  assert.equal(backendB.pages[0].received.filter(item => item.method === 'Runtime.evaluate').length, 1);

  const seen = b.events.length;
  backendA.pages[0].emit('event', 'Page.loadEventFired', {});
  await a.event('Page.loadEventFired');
  await new Promise(resolve => setTimeout(resolve, 100));
  assert.equal(b.events.slice(seen).some(item => item.method === 'Page.loadEventFired'), false);
});

test('a companion with a wrong token, protocol, profile or owner is refused', async t => {
  const fixture = await stageFixture(t);
  const dir = await fixture.profile('s1');
  const hello = { profile: dir, cwd: process.cwd(), pid: process.pid, url: 'about:blank' };
  assert.equal((await rawHello(fixture.browsers, hello, { token: 'guess' })).answer.ready, false);
  assert.equal(fixture.backends.size, 0, 'an unauthorized hello creates no page');
  const outside = await rawHello(fixture.browsers, { ...hello, profile: path.join(fixture.home, 'b-12345678__s1') });
  assert.match(outside.answer.error, /Not an AFM stage browser profile/);
  const noPid = await rawHello(fixture.browsers, { ...hello, pid: 1 });
  assert.match(noPid.answer.error, /process identity/);
  const file = await rawHello(fixture.browsers, { ...hello, url: 'file:///etc/passwd' });
  assert.match(file.answer.error, /Only http and https/);
  assert.equal(fixture.browsers.entries.size, 0, 'a refused page leaves no stage behind');

  const first = await rawHello(fixture.browsers, hello);
  assert.equal(first.answer.ready, true);
  const stolen = await rawHello(fixture.browsers, { ...hello, pid: process.pid + 1 });
  assert.match(stolen.answer.error, /another process/, 'another process cannot take over a stage\'s page');
  first.socket.destroy();
});

test('a silent connection is dropped and a stage count limit is enforced', async t => {
  const fixture = await stageFixture(t, { maxStages: 1 });
  const silent = (await import('node:net')).default.connect(fixture.browsers.socketPath);
  await new Promise(resolve => silent.on('close', resolve));
  const one = await rawHello(fixture.browsers, { profile: await fixture.profile('s1'), cwd: process.cwd(), pid: process.pid, url: 'about:blank' });
  assert.equal(one.answer.ready, true);
  const two = await rawHello(fixture.browsers, { profile: await fixture.profile('s2'), cwd: process.cwd(), pid: process.pid, url: 'about:blank' });
  assert.match(two.answer.error, /Too many/);
  one.socket.destroy();
});

test('a relaunched browser of the same stage gets its page back, with a clean session', async t => {
  const fixture = await stageFixture(t, { graceMs: 5000 });
  const dir = await fixture.profile('live-run');
  const first = fixture.launch(dir);
  const { targetId } = await attach(first);
  const [backend] = fixture.backends.values();
  backend.pages[0].url = 'http://127.0.0.1:18080/cart';
  first.child.kill('SIGKILL');
  await first.exited;
  await eventually(() => [...fixture.browsers.entries.values()][0]?.socket === null, { what: 'disconnect' });
  assert.equal(backend.disposed, false, 'the page survives the grace period');

  const second = fixture.launch(dir);
  const fresh = await second.ok('Target.getTargets');
  assert.deepEqual(fresh.targetInfos.map(item => [item.targetId, item.url, item.attached]), [[targetId, 'http://127.0.0.1:18080/cart', false]]);
  assert.equal(fixture.backends.size, 1, 'no second page was created');
  assert.equal(backend.pages.length, 1);
  const again = await attach(second);
  assert.equal(again.targetId, targetId);
  assert.match((await second.call('Runtime.evaluate', { expression: 'x' }, 'STALE-SESSION')).error.message, /not found/);
});

test('a disconnected stage browser is released after the grace period, and never for a stage that came back', async t => {
  const fixture = await stageFixture(t, { graceMs: 200 });
  const client = fixture.launch(await fixture.profile('s1'));
  await attach(client);
  const [backend] = fixture.backends.values();
  client.child.kill('SIGKILL');
  await eventually(() => backend.disposed, { what: 'release after grace' });
  assert.equal(fixture.browsers.entries.size, 0);
  assert.ok(backend.pages[0].closed);
});

test('Browser.close ends the stage: the page is closed and the companion exits', async t => {
  const fixture = await stageFixture(t);
  const client = fixture.launch(await fixture.profile('s1'));
  await attach(client);
  const [backend] = fixture.backends.values();
  await client.ok('Browser.close');
  assert.deepEqual(await client.exited, { code: 0, signal: null }, 'AFM sees its Chrome end');
  assert.equal(backend.disposed, true);
  assert.equal(fixture.browsers.entries.size, 0);
});

test('a page closed by the user reaches the client, and the stage keeps its other pages', async t => {
  const fixture = await stageFixture(t);
  const client = fixture.launch(await fixture.profile('s1'));
  const { targetId } = await attach(client);
  const { targetId: second } = await client.ok('Target.createTarget', { url: 'http://127.0.0.1:1/' });
  const [backend] = fixture.backends.values();
  const before = client.events.length;
  backend.pages[1].close();
  const gone = await client.event('Target.targetDestroyed', before);
  assert.equal(gone.params.targetId, second);
  assert.equal((await client.event('Target.detachedFromTarget', before)).params.targetId, second);
  assert.equal([...fixture.browsers.entries.values()][0].router.activePage.id, targetId, 'the remaining page becomes the visible one');
});

test('a stage becomes a Live tab once its run is identified, is shown by handle, and goes with its stage', async t => {
  let verdict = null;
  const fixture = await stageFixture(t, { inspect: async () => verdict });
  const client = fixture.launch(await fixture.profile('live-run'));
  await attach(client);
  assert.deepEqual(fixture.browsers.list(), [], 'a stage not tied to a run is not offered');

  await fixture.browsers.reconcile([]);
  assert.deepEqual(fixture.browsers.list(), []);
  verdict = { runKey: 'i1:r1', stageId: 'live-run', ended: false };
  await fixture.browsers.reconcile([]);
  const [item] = fixture.browsers.list();
  assert.deepEqual([item.runKey, item.stageId, item.pages], ['i1:r1', 'live-run', 1]);
  assert.match(item.handle, /^[0-9a-f]{24}$/);

  const [backend] = fixture.backends.values();
  fixture.browsers.place('unknown-handle', { x: 1, y: 2, width: 3, height: 4 });
  assert.equal(backend.shown.length, 0, 'a guessed handle shows nothing');
  fixture.browsers.place(item.handle, { x: 1, y: 2, width: 30, height: 40 });
  assert.deepEqual(backend.shown.at(-1), [backend.pages[0].id, { x: 1, y: 2, width: 30, height: 40 }]);
  fixture.browsers.place(item.handle, null);
  assert.ok(backend.hidden > 0, 'leaving the tab hides the page without closing it');
  assert.equal(backend.disposed, false);

  verdict = { ended: true };
  await fixture.browsers.reconcile([]);
  assert.equal(backend.disposed, true, 'the page is released when its stage ends');
  assert.deepEqual(fixture.browsers.list(), []);
  assert.deepEqual(await client.exited, { code: 0, signal: null });
});

test('reconciling costs nothing while no stage browser exists', async t => {
  const fixture = await stageFixture(t);
  await fixture.browsers.reconcile([{}]);
  assert.deepEqual(fixture.inspected, []);
});

test('closing the broker releases every page and removes its files', async t => {
  const fixture = await stageFixture(t);
  const a = fixture.launch(await fixture.profile('a')), b = fixture.launch(await fixture.profile('b'));
  await attach(a); await attach(b);
  await fixture.browsers.close();
  assert.ok([...fixture.backends.values()].every(backend => backend.disposed));
  assert.deepEqual([(await a.exited).code, (await b.exited).code], [0, 0]);
  await assert.rejects(stat(brokerFile(fixture.home)));
  await assert.rejects(stat(fixture.browsers.dir));
});

test('a companion that cannot find Desktop fails fast with a readable message', async t => {
  const fixture = await stageFixture(t);
  await fixture.browsers.close();
  const client = fixture.launch(await fixture.profile('s1'));
  assert.equal((await client.exited).code, 1);
  assert.match(client.stderr, /AFM Desktop is not running/);
});

test('stage URLs and pipe framing', () => {
  assert.equal(stageURL(undefined), 'about:blank');
  assert.equal(stageURL('http://127.0.0.1:18080/a b'), 'http://127.0.0.1:18080/a%20b');
  assert.throws(() => stageURL('data:text/html,x'), /Only http/);
  const framer = pipeFramer();
  assert.deepEqual(framer.push('{"id":1}\0{"id'), ['{"id":1}']);
  assert.deepEqual(framer.push('":2}\0'), ['{"id":2}']);
  assert.throws(() => pipeFramer().push('x'.repeat((8 << 20) + 1)), /size limit/);
});

// How a stage is tied to its run, with the run's own API faked.
function runsApi(stagesByRun) {
  return async url => {
    const { hostname, port, pathname } = new URL(url);
    const key = `${hostname}:${port}`;
    const stages = stagesByRun[key];
    const body = pathname === '/api/runtime' ? { run_id: key, instance_id: 'i-' + key } : { run_id: key, stages };
    return { ok: Boolean(stages), json: async () => body };
  };
}
const runAt = (id, root, state = 'running') => ({ key: `i-127.0.0.1:${id}:127.0.0.1:${id}`, runId: `127.0.0.1:${id}`, instanceId: `i-127.0.0.1:${id}`, apiUrl: `http://127.0.0.1:${id}`, projectRoot: root, state });
const entryOf = (extra = {}) => ({ stagePart: 'live-run', cwd: '/work/app', runKey: null, ...extra });

test('a stage is tied only to the single run in its folder that has started it', async () => {
  const stages = { '127.0.0.1:1': [{ id: 'live-run', status: 'running' }], '127.0.0.1:2': [{ id: 'live-run', status: 'running' }], '127.0.0.1:3': [{ id: 'live-run', status: 'pending' }] };
  const inspect = createInspector({ request: runsApi(stages) });
  const one = runAt(1, '/work/app'), two = runAt(2, '/work/app'), three = runAt(3, '/work/app'), elsewhere = runAt(2, '/work/other');
  assert.deepEqual(await inspect(entryOf(), [one, three, elsewhere]), { runKey: one.key, stageId: 'live-run', ended: false });
  assert.equal(await inspect(entryOf(), [one, two]), null, 'two candidate runs are ambiguous, so nothing is tied');
  assert.equal(await inspect(entryOf(), [three]), null, 'a stage that has not started is not tied');
  assert.deepEqual(await inspect(entryOf({ cwd: '/work/app/.worktrees/w1' }), [one]), { runKey: one.key, stageId: 'live-run', ended: false }, 'a worktree inside the project');
  assert.equal(await inspect(entryOf({ cwd: '/work/application' }), [one]), null, 'a sibling folder with the same prefix is another project');
});

test('a stage with unsafe characters in its id is matched through AFM\'s profile name', async () => {
  const run = runAt(1, '/work/app');
  const inspect = createInspector({ request: runsApi({ '127.0.0.1:1': [{ id: 'Live Run/1', status: 'running' }] }) });
  assert.equal((await inspect(entryOf({ stagePart: 'Live_Run_1' }), [run])).stageId, 'Live Run/1');
});

test('a tied stage never moves to another run and ends with its stage or run', async () => {
  const stages = { '127.0.0.1:1': [{ id: 'live-run', status: 'running' }], '127.0.0.1:2': [{ id: 'live-run', status: 'running' }] };
  const inspect = createInspector({ request: runsApi(stages) });
  const one = runAt(1, '/work/app'), two = runAt(2, '/work/app');
  const tied = entryOf({ runKey: one.key });
  assert.equal((await inspect(tied, [one, two])).runKey, one.key);
  stages['127.0.0.1:1'] = [{ id: 'live-run', status: 'done' }];
  assert.equal((await inspect(tied, [one, two])).ended, true, 'its stage finished');
  stages['127.0.0.1:1'] = [{ id: 'other', status: 'running' }];
  assert.equal(await inspect(tied, [one, two]), null, 'a stage the run no longer lists does not move to the other run');
  assert.deepEqual(await inspect(tied, [two]), { ended: true }, 'its run is gone');
  assert.deepEqual(await inspect(tied, [{ ...one, state: 'finished' }, two]), { ended: true }, 'its run finished');
});

test('the host Desktop starts gets the stage browser executable, and the preference can turn it off', async t => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'hostenv-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const fake = path.join(dir, 'afm');
  await writeFile(fake, `#!/bin/sh\nprintf '%s' "$AFM_CHROME_EXECUTABLE" > '${dir}/seen'\n`, { mode: 0o700 });
  const host = new Host(dir, async () => fake, { attempts: 4, interval: 30, env: async () => ({ PATH: process.env.PATH, AFM_CHROME_EXECUTABLE: '/the/wrapper' }) });
  await assert.rejects(host.connect(), /Cannot connect/);
  assert.equal(await eventually(() => readFile(path.join(dir, 'seen'), 'utf8').catch(() => null)), '/the/wrapper');

  const prefs = new Prefs(dir);
  assert.equal((await prefs.get()).stageBrowser, true, 'on by default');
  assert.equal((await prefs.set({ stageBrowser: false })).stageBrowser, false);
  await assert.rejects(prefs.set({ stageBrowser: 'yes' }), /Invalid preference/);
});

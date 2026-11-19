// Regressions for the independent review findings (runtime, interface, assistant and stage browser).
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { EventEmitter } from 'node:events';
import path from 'node:path';
import { JSDOM } from 'jsdom';
import { Desktop } from '../src/desktop.js';
import { afmBinary } from '../src/binary.js';
import { Host } from '../src/host.js';
import { StageRouter } from '../src/stage-browser/cdp-router.js';
import { stageRequestAllowed, electronBackend } from '../src/stage-browser/electron-page.js';
import { createInspector } from '../src/stage-browser/bind.js';
import { StageBrowsers } from '../src/stage-browser/broker.js';
import { Workspace } from '../src/workspace.js';
import { shortcutAction } from '../src/shortcuts.js';
import { eventually, makeProject, sleepFlow, tempHome } from './support.js';
import { FakeBackend, FakePage } from './stage-browser-support.js';
import { providerHome, makeAssistant, until } from './assistant-support.js';
import { fakeNative, loadShell, snapshotOf, projectRow, runRow, tick } from './shell-support.js';
import { setTimeout as delay } from 'node:timers/promises';

const fast = { timeout: 90000 };

async function open(t, { home, env }) {
  const desktop = new Desktop({ home, binary: afmBinary, env });
  t.after(() => desktop.close());
  return desktop;
}

// ---- RR-06: a host log that cannot be opened is an ordinary failure ----

test('a host log that cannot be opened fails the connection instead of crashing the app', async t => {
  const { home } = await tempHome(t);
  await mkdir(path.join(home, '.afm', 'desktop', 'host.log'), { recursive: true });
  const host = new Host(home, async () => '/nonexistent/afm', { attempts: 4, interval: 10 });
  await assert.rejects(host.connect(), error => error.code === 'host_unreachable' && /EISDIR/.test(error.message));
});

// ---- RR-03/05: request ids ----

test('different launches racing on one request id never merge', fast, async t => {
  const ctx = await tempHome(t);
  const folder = await makeProject(ctx.home, 'p', { flows: { 'a.yaml': sleepFlow('a') } });
  const desktop = await open(t, ctx);
  const project = await desktop.projects.add(folder);
  const request = { requestId: 'same', projectId: project.id, kind: 'Flow', file: 'a.yaml', task: 'A' };
  const results = await Promise.allSettled([desktop.runs.start(request), desktop.runs.start({ ...request, task: 'B' })]);
  assert.deepEqual(results.map(r => r.status), ['fulfilled', 'rejected']);
  assert.equal(results[1].reason.code, 'request_conflict');
  const equal = await Promise.all([desktop.runs.start(request), desktop.runs.start({ ...request })]);
  assert.equal(equal[0].launch_id, equal[1].launch_id);
});

test('a retry of an accepted request sends what was accepted, whatever changed since', fast, async t => {
  const ctx = await tempHome(t);
  const folder = await makeProject(ctx.home, 'repo', { git: true, flows: { 'b.yaml': sleepFlow('b'), 'c.yaml': sleepFlow('c') } });
  const desktop = await open(t, ctx);
  const project = await desktop.projects.add(folder);
  await desktop.prefs.set({ gitMode: 'current' });
  const b = { requestId: 'retry-settings', projectId: project.id, kind: 'Flow', file: 'b.yaml' };
  const first = await desktop.runs.start(b);
  await desktop.prefs.set({ gitMode: 'worktree' });
  assert.equal((await desktop.runs.start(b)).launch_id, first.launch_id, 'a changed Git preference is not applied to an accepted request');

  const c = { requestId: 'retry-file', projectId: project.id, kind: 'Flow', file: 'c.yaml' };
  await desktop.prefs.set({ gitMode: 'current' });
  const second = await desktop.runs.start(c);
  await rm(path.join(folder, '.afm', 'flows', 'c.yaml'));
  assert.equal((await desktop.runs.start(c)).launch_id, second.launch_id, 'a deleted flow does not hide an accepted launch');
  const reopened = await open(t, ctx);
  assert.equal((await reopened.runs.start(c)).launch_id, second.launch_id, 'also from a new Desktop on the same home');
});

// ---- RR-04: the selected project's own configuration decides the preview ----

test('a pipeline preview follows the selected project config, AFM_WORKTREES and the flow', fast, async t => {
  const ctx = await tempHome(t);
  const on = await makeProject(ctx.home, 'on', { git: true });
  const off = await makeProject(ctx.home, 'off', { git: true });
  await writeFile(path.join(on, '.afm', 'config.yaml'), 'worktrees:\n  enabled: true\n');
  const library = path.join(ctx.home, 'library', 'lib');
  await mkdir(library, { recursive: true });
  await writeFile(path.join(library, 'pipeline.yaml'), 'schema_version: 2\nname: lib\nversion: 1.0.0\nentrypoint: flow.yaml\n');
  await writeFile(path.join(library, 'flow.yaml'), sleepFlow('library-run'));
  const desktop = await open(t, ctx);
  const [pOn, pOff] = [await desktop.projects.add(on), await desktop.projects.add(off)];
  const [source] = [await desktop.sources.add({ location: path.join(ctx.home, 'library') })];
  const id = source.entries[0].id;
  await desktop.prefs.set({ gitMode: 'current' });
  const preview = project => desktop.runs.preview({ projectId: project.id, kind: 'Pipeline', file: id }).then(r => r.effective);
  assert.deepEqual([(await preview(pOn)).mode, (await preview(pOn)).source], ['worktree', 'config']);
  assert.equal((await preview(pOff)).mode, 'current');
  const saved = process.env.AFM_WORKTREES;
  try {
    process.env.AFM_WORKTREES = '0';
    assert.deepEqual([(await preview(pOn)).mode, (await preview(pOn)).source], ['current', 'env']);
    process.env.AFM_WORKTREES = '1';
    assert.equal((await preview(pOff)).mode, 'worktree');
  } finally { if (saved === undefined) delete process.env.AFM_WORKTREES; else process.env.AFM_WORKTREES = saved; }
});

// ---- RR-02: a host Desktop did not start needs the user's consent, then the same request repeats ----

test('the launcher asks before restarting a foreign host and repeats the same request on consent', async t => {
  const starts = [];
  const { w, get, native } = await (async () => {
    const native = fakeNative(snapshotOf([projectRow('a', [runRow('ra')])], []));
    Object.assign(native, {
      projectDetails: async () => ({ git: { git: false }, flows: [{ name: 'plain', file: 'plain.yaml', policy: null }], sources: [] }),
      preview: async () => ({ git: { git: false }, effective: { mode: 'current', source: 'folder', branch: null } }),
      start: async request => { starts.push(request); return request.confirmHostRestart ? { launch_id: 'L' } : { status: 'external_host', message: 'This AFM host was not started by Desktop.' }; },
    });
    const shell = await loadShell(t, { demo: false, native });
    shell.get('finish-setup')?.click();
    await tick();
    return { ...shell, native };
  })();
  const asked = [];
  w.confirm = message => { asked.push(message); return asked.length > 1; };
  w.shell.openLauncher(w.shell.projects[0]);
  await tick(30);
  get('launch-submit').click();
  await tick(30);
  assert.equal(starts.length, 1);
  assert.match(get('native-error').textContent, /left as it is/);
  assert.equal(get('launch').open, true, 'declining keeps the launcher and changes nothing');
  get('launch-submit').click();
  await tick(30);
  assert.equal(starts.length, 3);
  assert.equal(starts[2].confirmHostRestart, true);
  assert.equal(starts[2].requestId, starts[0].requestId);
  assert.match(asked[0], /not started by Desktop/);
  assert.equal(native && get('launch').open, false);
});

// ---- UI findings ----

test('the Alt+L shortcut hands the keyboard to the native Live view, and native views report it too', async t => {
  assert.equal(shortcutAction({ type: 'keyDown', alt: true, code: 'KeyL' }), 'live');
  const calls = [];
  const { w, d, get, key } = await loadShell(t, { demo: false, native: Object.assign(fakeNative(snapshotOf([], [])), { focusLive: async stage => { calls.push(stage); } }) });
  get('finish-setup')?.click();
  const tab = d.createElement('div');
  tab.id = 'native-live-tab';
  tab.dataset.stageBrowser = 'handle-1';
  d.body.append(tab);
  key('KeyL', 'l', { altKey: true });
  assert.deepEqual(calls, ['handle-1']);
  tab.remove();
  key('KeyL', 'l', { altKey: true });
  assert.equal(calls.length, 1, 'without a Live tab there is nothing to focus');
});

test('dashboard shortcuts attach on a page without #root, and Command-K ignores auto-repeat', async t => {
  const dom = new JSDOM('<body><p>error page</p></body>', { runScripts: 'outside-only', pretendToBeVisual: true });
  const messages = [];
  const w = dom.window;
  Object.defineProperty(w, 'parent', { value: { postMessage: m => messages.push(m) } });
  w.eval(await readFile(new URL('../src/dashboard-keys.cjs', import.meta.url), 'utf8'));
  w.dispatchEvent(new w.Event('DOMContentLoaded'));
  w.document.dispatchEvent(new w.KeyboardEvent('keydown', { code: 'KeyJ', altKey: true, bubbles: true }));
  assert.deepEqual(JSON.parse(JSON.stringify(messages)), [{ navigate: 'next' }]);

  const { d, get, key } = await loadShell(t, { platform: 'MacIntel' });
  get('finish-setup').click();
  key('KeyK', 'k', { metaKey: true }); assert.ok(get('picker').open);
  key('KeyK', 'k', { metaKey: true, repeat: true }); assert.ok(get('picker').open, 'a held key does not blink the picker');
});

test('a snapshot that changes a project keeps the keyboard focus on the same control', async t => {
  const native = fakeNative(snapshotOf([projectRow('a', [runRow('ra')]), projectRow('b', [runRow('rb')])], []));
  const { d, w } = await loadShell(t, { demo: false, native });
  d.getElementById('finish-setup')?.click();
  await tick();
  const tile = [...d.querySelectorAll('#screen [data-project]')].find(el => el.tagName === 'BUTTON');
  assert.ok(tile, 'Home has a focusable project control');
  tile.focus();
  assert.equal(d.activeElement, tile);
  native.publish(snapshotOf([projectRow('a', [runRow('ra'), runRow('rc')]), projectRow('b', [runRow('rb')])], []));
  await tick();
  assert.notEqual(d.activeElement, d.body);
  assert.equal(d.activeElement.dataset.project, tile.dataset.project);
});

test('the Live view steps aside while the assistant grip is dragged', async t => {
  const layouts = [];
  const native = Object.assign(fakeNative(snapshotOf([], [])), { liveLayout: async (bounds, stage) => { layouts.push([bounds, stage]); } });
  const { w, d, get } = await loadShell(t, { demo: false, native });
  get('finish-setup')?.click();
  const tab = d.createElement('div');
  tab.id = 'native-live-tab';
  tab.dataset.stageBrowser = 'h';
  tab.getBoundingClientRect = () => ({ x: 10, y: 20, width: 300, height: 200 });
  d.body.append(tab);
  await tick(30);
  assert.deepEqual(JSON.parse(JSON.stringify(layouts.at(-1)[0])), { x: 10, y: 20, width: 300, height: 200 });
  d.body.classList.add('sizing-assistant');
  await tick(30);
  assert.deepEqual(JSON.parse(JSON.stringify(layouts.at(-1))), [null, 'h'], 'the native view is hidden during the drag');
  d.body.classList.remove('sizing-assistant');
  await tick(30);
  assert.ok(layouts.at(-1)[0], 'and returns after it');
});

// ---- R2/R1: assistant credentials ----

const SECRET = 'file-credential-secret-12345';

test('a key read from a file or a custom environment name is passed to the CLI, and never shown or stored', async t => {
  const ctx = await providerHome(t, { FAKE_SECRET: SECRET, FAKE_SCENARIO: 'fail', CUSTOM_KEY: SECRET });
  const keyFile = path.join(ctx.home, 'key');
  await writeFile(keyFile, SECRET + '\n', { mode: 0o600 });
  const { assistant, events } = makeAssistant(t, ctx);
  for (const credential of [{ type: 'file', ref: keyFile }, { type: 'env', ref: 'CUSTOM_KEY' }]) {
    events.length = 0;
    await assistant.addProvider({ name: 'ref-' + credential.type, kind: 'claude', executable: path.join(ctx.bin, 'claude'), credential });
    const providers = (await assistant.state()).settings.providers;
    await assistant.configure({ provider: providers.at(-1).id });
    await assistant.send({ text: 'hello' });
    const failure = await until(events, 'error');
    assert.equal(failure.message.includes(SECRET), false, credential.type + ' leaked into the error');
    assert.match(failure.message, /\[hidden\]/);
    assert.equal(JSON.stringify(events).includes(SECRET), false);
  }
  const record = await ctx.invocations();
  assert.ok(record.filter(entry => entry.secretVars?.includes('ANTHROPIC_API_KEY')).length >= 2, 'the CLI received the key under its own variable');
  assert.equal((await readFile(path.join(ctx.home, '.afm/desktop/assistant-history.json'), 'utf8')).includes(SECRET), false);
});

// ---- R3: a stage page cannot reach AFM ----

test('a stage page may open web pages but never an AFM server', () => {
  const blocked = ['http://127.0.0.1:8020/mcp', 'http://localhost:8013', 'http://[::1]:8013/api/status'];
  for (const url of ['http://127.0.0.1:8013/', 'http://localhost:8013/api/status', 'ws://127.0.0.1:8020/mcp', 'http://[::1]:8020/mcp', 'http://localhost:8020/'])
    assert.equal(stageRequestAllowed(url, blocked), false, url);
  for (const url of ['http://127.0.0.1:3000/', 'https://example.com/', 'about:blank', 'data:text/html,x', 'blob:null/1'])
    assert.equal(stageRequestAllowed(url, blocked), true, url);
  for (const url of ['file:///etc/passwd', 'chrome://gpu', 'devtools://x', 'http://not a url']) assert.equal(stageRequestAllowed(url, blocked), false, url);
});

test('the Electron stage session applies the AFM server block to every request', async () => {
  let policy;
  const contents = Object.assign(new EventEmitter(), { setWindowOpenHandler() {}, isDestroyed: () => false, close() {}, getURL: () => '', getTitle: () => '',
    debugger: Object.assign(new EventEmitter(), { attach() {}, detach() {}, sendCommand: async () => ({}) }) });
  class View { constructor() { this.webContents = contents; } setBounds() {} setVisible() {} }
  const session = { setPermissionRequestHandler() {}, on() {}, webRequest: { onBeforeRequest: fn => { policy = fn; } }, clearStorageData: async () => {} };
  let afm = ['http://127.0.0.1:8013'];
  const backend = electronBackend({ electron: { WebContentsView: View, session: { fromPartition: () => session } }, window: () => null, blocked: () => afm });
  await backend.createPage('about:blank');
  const verdict = url => { let out; policy({ url }, r => { out = r; }); return out.cancel; };
  assert.equal(verdict('http://localhost:8013/'), true);
  assert.equal(verdict('http://127.0.0.1:3000/'), false);
  afm = ['http://127.0.0.1:3000'];
  assert.equal(verdict('http://127.0.0.1:3000/'), true, 'the list is read for every request');
  backend.dispose();
});

// ---- R4: an incomplete answer never ties a stage ----

test('a run that cannot answer, or too many candidates, leave a stage unbound', async () => {
  const run = (id, pid) => ({ key: `i${id}:r${id}`, runId: `r${id}`, instanceId: `i${id}`, apiUrl: `http://127.0.0.1:400${id}`, projectRoot: '/work/shared', state: 'running', pid });
  const request = async url => {
    if (url.includes(':4001')) throw Error('transient');
    const id = url.includes(':4002') ? 2 : 3;
    return { ok: true, json: async () => url.endsWith('/api/runtime') ? { run_id: `r${id}`, instance_id: `i${id}` } : { run_id: `r${id}`, stages: [{ id: 'live', status: 'running' }] } };
  };
  const entry = { stagePart: 'live', cwd: '/work/shared', runKey: null };
  const inspect = createInspector({ request });
  assert.equal(await inspect(entry, [run(1), run(2)]), null, 'the owner could not answer');
  assert.equal((await inspect(entry, [run(2)])).runKey, 'i2:r2', 'a sole, answering candidate still binds');
  assert.equal(await inspect(entry, Array.from({ length: 8 }, (_, i) => run(i + 2))), null, 'more candidates than can be checked');
});

// ---- R5/R6: the page session obeys the same rules, and an old client's answers are dropped ----

test('browser-wide commands are refused on a page session and a replaced client gets no late answer', async () => {
  const sent = [], page = new FakePage('about:blank');
  const router = new StageRouter({ createPage: async () => page, send: message => sent.push(message) });
  await router.open();
  await router.handle(JSON.stringify({ id: 1, method: 'Target.attachToTarget', params: { targetId: page.id } }));
  const sessionId = sent.at(-1).result.sessionId;
  for (const method of ['Storage.clearCookies', 'Browser.close', 'Target.createTarget', 'Target.closeTarget', 'Target.setDiscoverTargets', 'SystemInfo.getInfo']) {
    await router.handle(JSON.stringify({ id: 2, method, sessionId }));
    assert.equal(sent.at(-1).error.code, -32601, method);
  }
  assert.equal(page.received.length, 0, 'nothing reached the page');
  await router.handle(JSON.stringify({ id: 3, method: 'Target.setAutoAttach', params: { autoAttach: true }, sessionId }));
  assert.equal(page.received.at(-1).method, 'Target.setAutoAttach');

  let finish;
  page.send = () => new Promise(resolve => { finish = resolve; });
  const pending = router.handle(JSON.stringify({ id: 77, method: 'Runtime.evaluate', sessionId }));
  router.resetClient();
  const before = sent.length;
  finish({ result: { value: 'old' } });
  await pending;
  assert.equal(sent.length, before, 'an answer for a replaced client is dropped');
  router.dispose();
});

// ---- R7: a hidden window still releases finished stages ----

test('a finished stage frees its page while the window is hidden, and idle work stays zero without stages', async t => {
  let ended = false, inspections = 0;
  const browsers = new StageBrowsers({ home: path.join((await tempHome(t)).home), createBackend: () => new FakeBackend(), launcher: {},
    inspect: async () => { inspections++; return { runKey: 'A:a', stageId: 'live', ended }; } });
  const backend = new FakeBackend();
  const router = new StageRouter({ createPage: url => backend.createPage(url), send() {} });
  await router.open();
  browsers.entries.set('k', { key: 'k', runKey: 'A:a', stageId: 'live', router, backend });
  const overview = { host: { state: 'ready' }, projects: [{ id: 'p', runs: [{ key: 'A:a', state: 'running', attention: false }] }], unassigned: [], candidates: [] };
  const workspace = new Workspace({ desktop: { overview: async () => overview }, stageBrowsers: browsers, interval: 10, hiddenInterval: 30 });
  t.after(() => workspace.close());
  workspace.setVisible(true);
  await workspace.refresh();
  workspace.setVisible(false);
  ended = true;
  await eventually(() => browsers.entries.size === 0, { timeout: 2000, what: 'finished stage released while hidden' });
  assert.equal(backend.pages[0].closed, true);
  const settled = inspections;
  await delay(150);
  assert.equal(inspections, settled, 'no stage browsers left, so no background work');
  assert.equal(workspace.timer, null);
});

// ---- R8: quit waits for the provider to stop ----

test('closing the assistant waits until a provider that ignores SIGTERM is killed', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'hang' });
  const { assistant, events } = makeAssistant(t, ctx, { grace: 150 });
  await assistant.send({ text: 'hello' });
  await eventually(() => events.some(event => event.type === 'start'), { what: 'turn start' });
  const started = Date.now();
  await assistant.close();
  assert.ok(Date.now() - started < 5000);
  assert.equal(assistant.active.size, 0);
});

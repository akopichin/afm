import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { setTimeout as delay } from 'node:timers/promises';
import { Workspace, fetchAttention } from '../src/workspace.js';
import { DashboardOrigins, allowRequest } from '../src/protocol.js';
import { shortcutAction } from '../src/shortcuts.js';
import { runView } from '../src/runs.js';

// A run's own HTTP API: its identity and the stage list its dashboard shows.
async function fakeRun(t, { runId = 'r1', instanceId = 'i1', stages = [] } = {}) {
  const state = { runId, instanceId, stages, hits: 0, dialogs: {}, dialogHits: 0 };
  const server = http.createServer((req, res) => {
    state.hits++;
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/api/runtime') res.end(JSON.stringify({ run_id: state.runId, instance_id: state.instanceId, mode: 'dashboard', ready: true }));
    else if (req.url === '/api/status') res.end(JSON.stringify({ run_id: state.runId, stages: state.stages }));
    else if (req.url.endsWith('/dialog')) {
      state.dialogHits++;
      res.end(JSON.stringify(state.dialogs[req.url.split('/')[3]] ?? []));
    }
    else { res.statusCode = 404; res.end('{}'); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  state.url = `http://127.0.0.1:${server.address().port}`;
  return state;
}

const row = (fake, extra = {}) => runView({ run_id: fake.runId, instance_id: fake.instanceId, run_name: 'flow', project_root: '/p', mode: 'dashboard',
  api_url: fake.url, ownership: 'attached', liveness: 'active', run_status: 'running', attention_required: true, actions: [], ...extra });

function desktopWith(get) {
  const calls = { overview: 0 };
  return { calls, overview: async () => { calls.overview++; return get(); } };
}
const overviewOf = (runs, host = { state: 'connected', error: null }) => ({ host, projects: [{ id: 'p', path: '/p', name: 'p', runs }], unassigned: [], candidates: [] });

test('refresh after a connection waits for a snapshot taken after the current poll', async t => {
  const old = Promise.withResolvers();
  const run = { key: 'external', apiLive: false };
  const desktop = desktopWith(() => desktop.calls.overview === 1 ? old.promise : { ...overviewOf([]), unassigned: [run] });
  const workspace = new Workspace({ desktop });
  t.after(() => workspace.close());
  const poll = workspace.refresh();
  const fresh = workspace.refresh({ afterCurrent: true });
  old.resolve(overviewOf([]));
  assert.deepEqual((await poll).unassigned, []);
  assert.deepEqual((await fresh).unassigned, [run]);
});

test('notices name the exact stage, and only for the run that proves its identity', async t => {
  const fake = await fakeRun(t, { stages: [{ id: 'plan', status: 'done' }, { id: 'review', name: 'Review', status: 'awaiting_approval' }, { id: 'ask', status: 'awaiting_user_input' }] });
  const view = row(fake);
  const items = await fetchAttention(view);
  assert.deepEqual(items.map(i => [i.stageId, i.kind, i.runKey]), [['review', 'approval', 'i1:r1'], ['ask', 'question', 'i1:r1']]);
  fake.runId = 'other';
  await assert.rejects(fetchAttention(view), /Run changed/);
});

test('the same pending question keeps its identity across pause and a different question gets a new one', async t => {
  const fake = await fakeRun(t, { stages: [{id:'ask',status:'awaiting_user_input',updated_at:'t1'}] });
  const first = {id:'q1',phase:'autonomous_execution',ts:'asked-1',question:'Private prompt'};
  fake.dialogs.ask = [first];
  const workspace = new Workspace({desktop:desktopWith(() => overviewOf([row(fake)]))});
  t.after(() => workspace.close());
  const [question] = (await workspace.refresh()).attention;
  assert.ok(question.requestAt);
  assert.equal(JSON.stringify(question).includes('Private prompt'),false);
  await workspace.refresh();
  assert.equal(fake.dialogHits,1);
  fake.stages[0].status = 'paused';
  assert.deepEqual((await workspace.refresh()).attention,[]);
  fake.stages[0] = {id:'ask',status:'awaiting_user_input',updated_at:'t2'};
  assert.equal((await workspace.refresh()).attention[0].requestAt,question.requestAt);
  fake.dialogs.ask = [{...first,answer:'yes'},{id:'q2',phase:'autonomous_execution',ts:'asked-2'}];
  fake.stages[0].updated_at = 't3';
  assert.notEqual((await workspace.refresh()).attention[0].requestAt,question.requestAt);
});

test('the queue keeps a stable order, emits only real changes and never duplicates a notice', async t => {
  const fake = await fakeRun(t, { stages: [{ id: 'a', status: 'awaiting_approval' }] });
  const workspace = new Workspace({ desktop: desktopWith(() => overviewOf([row(fake)])) });
  t.after(() => workspace.close());
  const seen = [];
  workspace.onChange(snapshot => seen.push(snapshot.attention.map(i => i.id)));
  await workspace.refresh();
  await workspace.refresh();
  assert.equal(seen.length, 1, 'an identical result emitted another update');
  fake.stages = [{ id: 'b', status: 'awaiting_user_input' }, { id: 'a', status: 'awaiting_approval' }];
  await workspace.refresh();
  assert.deepEqual(seen.at(-1), ['i1:r1:a', 'i1:r1:b'], 'the earlier notice keeps its place');
  fake.stages = [{ id: 'b', status: 'awaiting_user_input' }];
  await workspace.refresh();
  assert.deepEqual(seen.at(-1), ['i1:r1:b']);
  fake.stages = [{ id: 'a', status: 'awaiting_approval' }, { id: 'b', status: 'awaiting_user_input' }];
  await workspace.refresh();
  assert.deepEqual(seen.at(-1), ['i1:r1:b', 'i1:r1:a'], 'a notice that returns is new work and goes last');
});

test('a dropped read or an unreachable host keeps the known notices in place', async t => {
  const fake = await fakeRun(t, { stages: [{ id: 'a', status: 'awaiting_approval' }] });
  const broken = http.createServer((_req, res) => { res.statusCode = 500; res.end(); });
  await new Promise(resolve => broken.listen(0, '127.0.0.1', resolve));
  t.after(() => broken.close());
  let overview = overviewOf([row(fake)]);
  const workspace = new Workspace({ desktop: desktopWith(() => overview) });
  t.after(() => workspace.close());
  const first = await workspace.refresh();
  assert.equal(first.attention.length, 1);
  overview = overviewOf([{ ...row(fake), apiUrl: `http://127.0.0.1:${broken.address().port}` }]);
  assert.deepEqual((await workspace.refresh()).attention, first.attention, 'one failed read dropped the notice');
  overview = overviewOf([], { state: 'offline', error: { code: 'host_unreachable', message: 'down' } });
  const offline = await workspace.refresh();
  assert.deepEqual(offline.attention, first.attention);
  assert.equal(offline.host.error.code, 'host_unreachable');
});

test('background work is single-flight, bounded and stops while the window is hidden', async t => {
  const fake = await fakeRun(t, { stages: [{ id: 'a', status: 'awaiting_approval' }] });
  const desktop = desktopWith(async () => { await delay(20); return overviewOf([row(fake)]); });
  const workspace = new Workspace({ desktop, interval: 30 });
  t.after(() => workspace.close());
  const concurrent = await Promise.all(Array.from({ length: 25 }, () => workspace.refresh()));
  assert.equal(desktop.calls.overview, 1, 'concurrent refreshes shared one discovery');
  assert.ok(concurrent.every(snapshot => snapshot === concurrent[0]));
  workspace.setVisible(true);
  await delay(400);
  workspace.setVisible(false);
  await delay(80);
  const afterHide = { overview: desktop.calls.overview, hits: fake.hits };
  assert.ok(afterHide.overview >= 4 && afterHide.overview <= 14, `visible polling ran ${afterHide.overview} times`);
  await delay(300);
  assert.deepEqual({ overview: desktop.calls.overview, hits: fake.hits }, afterHide, 'a hidden window kept discovering');
  assert.equal(workspace.timer, null);
  workspace.setVisible(true);
  await delay(100);
  assert.ok(desktop.calls.overview > afterHide.overview, 'showing the window resumes discovery');
  workspace.close();
  const closed = desktop.calls.overview;
  await delay(150);
  assert.equal(desktop.calls.overview, closed, 'a closed workspace left a timer behind');
  assert.equal(workspace.listeners.size, 0);
});

test('only the shell, inline data and opened dashboards are reachable', () => {
  const origins = new DashboardOrigins();
  origins.add('http://127.0.0.1:4000', 'i1:r1');
  const ok = url => allowRequest(url, { appRoot: '/app', origins });
  for (const url of ['file:///app/renderer/index.html', 'file:///app/.generated/dashboard/index.css', 'data:image/png;base64,AA', 'http://127.0.0.1:4000/api/status', 'ws://127.0.0.1:4000/ws'])
    assert.equal(ok(url), true, url);
  for (const url of ['file:///etc/passwd', 'file:///app/../etc/passwd', 'file:///application/x', 'http://127.0.0.1:4001/api/status', 'http://localhost:4000/',
    'https://127.0.0.1:4000/', 'http://user:pw@127.0.0.1:4000/', 'https://example.com/', 'https://api.github.com/repos', 'ftp://127.0.0.1:4000/', 'not a url'])
    assert.equal(ok(url), false, url);
  assert.equal(allowRequest('https://api.github.com/repos', { appRoot: '/app', origins, allowGitHub: true }), true);
  origins.add('http://127.0.0.1:4002', 'i2:r2');
  origins.retain(new Set(['i2:r2']));
  assert.equal(origins.size, 1);
  assert.equal(ok('http://127.0.0.1:4000/'), false, 'a finished run keeps its port');
});

test('native view shortcuts follow the platform and never fire during composition or repeat', () => {
  const key = (extra, platform = 'darwin') => shortcutAction({ type: 'keyDown', ...extra }, platform);
  assert.deepEqual(['KeyH', 'KeyJ', 'KeyK', 'KeyM'].map(code => key({ meta: true, code })), ['home', 'next', 'previous', 'model']);
  assert.equal(key({ meta: true, shift: true, code: 'KeyK' }), 'picker');
  assert.equal(key({ meta: true, shift: true, key: 'Enter' }), 'launch');
  assert.equal(key({ control: true, key: 'k' }), null, 'Control-K is an editing key on a Mac');
  assert.equal(key({ control: true, code: 'KeyK' }, 'linux'), 'previous');
  assert.equal(key({ meta: true, code: 'KeyK' }, 'linux'), null);
  assert.equal(key({ control: true, shift: true, key: 'Enter' }, 'win32'), 'launch');
  assert.equal(key({ alt: true, shift: true, code: 'KeyJ' }), null);
  assert.equal(key({ meta: true, alt: true, key: 'k' }), null);
  assert.equal(key({ meta: true, code: 'KeyJ', isAutoRepeat: true }), null);
  assert.equal(key({ meta: true, code: 'KeyJ', isComposing: true }), null);
  assert.equal(key({ type: 'keyUp', alt: true, code: 'KeyJ' }), null);
});

test('a hidden window polls only when asked to watch for notifications', async t => {
  const fake = await fakeRun(t, { stages: [{ id: 'a', status: 'awaiting_user_input', updated_at: 't1' }] });
  const desktop = desktopWith(() => overviewOf([row(fake)]));
  let watch = false;
  const workspace = new Workspace({ desktop, interval: 5, hiddenInterval: 10, watchHidden: () => watch });
  t.after(() => workspace.close());
  workspace.setVisible(false);
  await delay(60);
  assert.equal(desktop.calls.overview, 0, 'a hidden window without notifications does no discovery');
  watch = true;
  workspace.setVisible(false);
  await workspace.refresh();
  const before = desktop.calls.overview;
  await delay(80);
  assert.ok(desktop.calls.overview > before, 'a hidden window with notifications keeps discovering');
  assert.equal((await workspace.get()).attention[0].observedAt, 't1', 'the item carries its observed transition timestamp');
});

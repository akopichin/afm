import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import v8 from 'node:v8';
import vm from 'node:vm';
import { Costs, FETCH_LIMIT, costEntry, liveMs } from '../src/costs.js';
import { Workspace } from '../src/workspace.js';
import { runView } from '../src/runs.js';

// A garbage collection on demand, without the --expose-gc flag: undici drops a fetch's abort link to its body in one.
v8.setFlagsFromString('--expose-gc');
const collectGarbage = vm.runInNewContext('gc');
// The value of `promise`, or undefined once `ms` passed: a lost cancellation fails a test instead of hanging it.
const within = (promise, ms) => Promise.race([promise, new Promise(resolve => setTimeout(resolve, ms).unref())]);

const cost = (usd, tokens, extra = {}) => ({ display_cost: '$' + usd, coverage: 'full', estimated_cost_usd: usd, total_tokens: tokens, unpriced: 0, unmetered: 0, ...extra });
const T0 = Date.parse('2026-11-20T10:00:00Z');
const iso = ms => new Date(ms).toISOString();

// run_cost already contains the stages and the overhead: 1.00 = 0.60 + 0.30 + 0.10.
function statusBody(extra = {}) {
  return { run_id: 'r1', run_status: 'running', started_at: iso(T0), elapsed_accumulated_ms: 60000, elapsed_since: iso(T0 + 60000),
    idle_accumulated_ms: 5000, idle_since: null, accounting: { health: 'ok', has_data: true, show_money: true },
    run_cost: cost(1, 1000), run_overhead_cost: cost(0.1, 100),
    stages: [
      { id: 'a', name: 'Plan', status: 'done', cost: cost(0.6, 600), timing: { started: true, complete: true, work_ms: 10000 } },
      { id: 'b', status: 'running', cost: cost(0.3, 300), timing: { started: true, complete: true, work_ms: 20000, open: { kind: 'work', since: iso(T0 + 70000) } } },
      { id: 'c', status: 'pending', timing: { complete: true } }],
    capabilities: { usage_analytics: true }, analytics: { revision: 'rev1', mode: 'full' }, ...extra };
}

async function fakeRun(t, { runId = 'r1', instanceId = 'i1', status = statusBody() } = {}) {
  const state = { runId, instanceId, status, hits: { runtime: 0, status: 0, analytics: 0 }, models: [] };
  const server = http.createServer((req, res) => {
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/api/runtime') { state.hits.runtime++; res.end(JSON.stringify({ run_id: state.runId, instance_id: state.instanceId })); }
    else if (req.url === '/api/status') { state.hits.status++; res.end(JSON.stringify(state.status)); }
    else if (req.url.startsWith('/api/usage/analytics')) { state.hits.analytics++; res.end(JSON.stringify({ breakdown: { rows: state.models } })); }
    else { res.statusCode = 404; res.end('{}'); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  state.url = `http://127.0.0.1:${server.address().port}`;
  return state;
}

const row = (fake, extra = {}) => runView({ run_id: fake.runId, instance_id: fake.instanceId, run_name: 'flow', project_root: '/p', mode: 'dashboard',
  api_url: fake.url, ownership: 'attached', liveness: 'active', run_status: 'running', attention_required: false, actions: [], ...extra });
const overviewOf = (...projects) => ({ host: { state: 'connected', error: null }, projects, unassigned: [], candidates: [] });
const project = (id, runs) => ({ id, path: '/' + id, name: id, runs });
const desktopWith = get => ({ overview: async () => get() });

async function watchedWorkspace(t, get, { hidePrices = false, now = () => T0 + 100000 } = {}) {
  const costs = new Costs({ now, hidePrices });
  costs.setWatched(true);
  const workspace = new Workspace({ desktop: desktopWith(get), costs });
  workspace.setVisible(true);
  t.after(() => { workspace.close(); costs.close(); });
  await workspace.refresh();
  return { costs, workspace };
}

test('an entry reads tokens and money once from run_cost and keeps the clocks as anchors', () => {
  const entry = costEntry({ key: 'i1:r1', name: 'flow' }, 'p', statusBody());
  assert.equal(entry.tokens, 1000);
  assert.equal(entry.money.usd, 1, 'stage and overhead cost were added to the run total');
  assert.deepEqual(entry.stages.map(s => [s.id, s.usd]), [['a', 0.6], ['b', 0.3]]);
  assert.equal(entry.overhead.usd, 0.1);
  assert.equal(liveMs(entry.elapsed, T0 + 100000), 100000);
  assert.equal(liveMs(entry.idle, T0 + 100000), 5000, 'a closed idle anchor does not grow');
  // Stage work: 10 s + 20 s closed plus the open work interval of stage b.
  assert.equal(liveMs(entry.stageWork, T0 + 100000), 60000);
});

test('stage work is unknown while a started stage has an incomplete ledger', () => {
  const status = statusBody();
  status.stages[1].timing = { started: true, complete: false };
  assert.equal(costEntry({ key: 'k' }, null, status).stageWork, null);
});

test('hidden prices leave no money anywhere in the serialized payload', async t => {
  const fake = await fakeRun(t);
  const afmHidden = await fakeRun(t, { runId: 'r2', instanceId: 'i2', status: statusBody({ run_id: 'r2', accounting: { health: 'ok', has_data: true, show_money: false } }) });
  const off = await fakeRun(t, { runId: 'r3', instanceId: 'i3', status: (() => { const s = statusBody({ run_id: 'r3' }); delete s.run_cost; delete s.run_overhead_cost; delete s.accounting; return s; })() });
  const get = () => overviewOf(project('p', [row(fake), row(afmHidden), row(off)]));
  const { costs } = await watchedWorkspace(t, get);
  const prices = Object.fromEntries(costs.view().runs.map(r => [r.key, r.price]));
  assert.deepEqual(prices, { 'i1:r1': 'visible', 'i2:r2': 'afm-hidden', 'i3:r3': 'unavailable' });
  const byKey = Object.fromEntries(costs.view().runs.map(r => [r.key, r]));
  for (const key of ['i2:r2', 'i3:r3']) assert.doesNotMatch(JSON.stringify(byKey[key]), /usd|"money"/);
  assert.equal(byKey['i3:r3'].tokens, null, 'accounting off keeps time without tokens');
  assert.equal(byKey['i2:r2'].tokens, 1000, 'tokens stay when only money is hidden');
  costs.setHidePrices(true);
  const hidden = costs.view();
  assert.equal(hidden.hidePrices, true);
  assert.doesNotMatch(JSON.stringify(hidden), /usd|"money"/);
  assert.equal(hidden.runs[0].tokens, 1000);
  assert.equal(hidden.runs[0].price, 'desktop-hidden');
  costs.setHidePrices(false);
  assert.equal(costs.view().runs[0].money.usd, 1, 'Show prices restores what AFM allows');
});

test('one run listed by two projects, or twice, counts once', async t => {
  const fake = await fakeRun(t);
  const view = row(fake);
  const { costs } = await watchedWorkspace(t, () => overviewOf(project('outer', [view, view]), project('inner', [view])));
  assert.equal(costs.view().runs.length, 1);
  assert.equal(costs.view().runs[0].projectId, 'outer');
});

test('Costs adds no requests while it is not watched, and shares the status read with notices', async t => {
  const calm = await fakeRun(t);
  const flagged = await fakeRun(t, { runId: 'r2', instanceId: 'i2', status: statusBody({ run_id: 'r2' }) });
  const get = () => overviewOf(project('p', [row(calm), row(flagged, { attention_required: true })]));
  const costs = new Costs();
  const workspace = new Workspace({ desktop: desktopWith(get), costs });
  workspace.setVisible(true);
  t.after(() => { workspace.close(); costs.close(); });
  await workspace.refresh();
  assert.equal(calm.hits.status, 0, 'a run nobody asked about was read');
  assert.equal(flagged.hits.status, 1);
  costs.setWatched(true);
  await workspace.refresh();
  assert.deepEqual([calm.hits.status, flagged.hits.status], [1, 2], 'one read per run and cycle, however many consumers');
  assert.deepEqual([calm.hits.runtime, flagged.hits.runtime], [2, 3], 'one runtime check for the status and one for the analytics read');
  costs.setWatched(false);
  await workspace.refresh();
  assert.deepEqual([calm.hits.status, flagged.hits.status], [1, 3]);
});

test('a hidden window never reads costs', async t => {
  const fake = await fakeRun(t);
  const costs = new Costs();
  costs.setWatched(true);
  const workspace = new Workspace({ desktop: desktopWith(() => overviewOf(project('p', [row(fake)]))), costs });
  t.after(() => { workspace.close(); costs.close(); });
  await workspace.refresh();
  assert.equal(fake.hits.status, 0);
});

test('a failed read or a changed identity keeps the run, marks it stale and freezes its clocks', async t => {
  const fake = await fakeRun(t);
  let now = T0 + 100000;
  const view = row(fake);
  const { costs, workspace } = await watchedWorkspace(t, () => overviewOf(project('p', [view])), { now: () => now });
  const fresh = costs.view().runs[0];
  assert.equal(fresh.stale, false);
  assert.equal(fresh.staleSince, null);
  now += 30000;
  fake.runId = 'other';
  await workspace.refresh();
  const stale = costs.view().runs[0];
  assert.equal(stale.stale, true);
  assert.equal(stale.staleSince, T0 + 100000, 'the clock stops at the last good read');
  assert.equal(stale.tokens, 1000);
  fake.runId = 'r1';
  await workspace.refresh();
  assert.equal(costs.view().runs[0].stale, false);
});

test('an unchanged run produces no update, and a run that left the inventory is dropped', async t => {
  const fake = await fakeRun(t);
  let runs = [row(fake)];
  let seen = 0;
  const costs = new Costs({ now: () => T0 });
  costs.setWatched(true);
  costs.onChange(() => seen++);
  const workspace = new Workspace({ desktop: desktopWith(() => overviewOf(project('p', runs))), costs });
  workspace.setVisible(true);
  t.after(() => { workspace.close(); costs.close(); });
  await workspace.refresh();
  await workspace.refresh();
  assert.equal(seen, 1);
  runs = [];
  await workspace.refresh();
  assert.deepEqual(costs.view().runs, []);
});

test('model analytics are read only for a watched run and only when its revision changes', async t => {
  const fake = await fakeRun(t);
  fake.models = [
    { key: 'anthropic/opus', label: 'opus', agent: { id: 'stage:a' }, agent_time: { duration_ms: 5000 }, tokens: { counts: { total: 900 } }, money: { estimated_cost_usd: 0.9 } },
    { key: 'x', label: 'assistant', agent: { outside_run_total: true }, tokens: { counts: { total: 5 } } }];
  const { costs, workspace } = await watchedWorkspace(t, () => overviewOf(project('p', [row(fake)])));
  await workspace.refresh();
  assert.equal(fake.hits.analytics, 1);
  assert.deepEqual(costs.view().runs[0].models, [{ key: 'anthropic/opus', label: 'opus', timeMs: 5000, tokens: 900, usd: 0.9 }]);
  fake.status = statusBody({ analytics: { revision: 'rev2', mode: 'full' } });
  await workspace.refresh();
  assert.equal(fake.hits.analytics, 2);
  costs.setHidePrices(true);
  assert.doesNotMatch(JSON.stringify(costs.view()), /usd/);
});

test('a missing stage ledger makes stage work unknown, an explicitly unstarted stage counts zero', () => {
  const status = statusBody();
  delete status.stages[1].timing;
  assert.equal(costEntry({ key: 'k' }, null, status).stageWork, null);
  assert.deepEqual(costEntry({ key: 'k' }, null, statusBody({ stages: [{ id: 'x', timing: { started: false, complete: true } }] })).stageWork, { ms: 0, since: [] });
});

test('a host outage keeps every run, stops its clocks at the last good read and recovers', async t => {
  const fake = await fakeRun(t);
  const view = row(fake);
  let down = false;
  let now = T0 + 100000;
  const costs = new Costs({ now: () => now });
  costs.setWatched(true);
  const get = () => (down ? { host: { state: 'down', error: { code: 'host_unreachable' } }, projects: [], unassigned: [], candidates: [] } : overviewOf(project('p', [view])));
  const workspace = new Workspace({ desktop: desktopWith(get), costs });
  workspace.setVisible(true);
  t.after(() => { workspace.close(); costs.close(); });
  await workspace.refresh();
  down = true;
  now += 30000;
  await workspace.refresh();
  const [stale] = costs.view().runs;
  assert.equal(stale.stale, true);
  assert.equal(stale.staleSince, T0 + 100000);
  assert.equal(stale.tokens, 1000);
  down = false;
  now += 30000;
  await workspace.refresh();
  assert.equal(costs.view().runs[0].stale, false);
});

test('an offline or changed run keeps its last figures stale until it answers as the same run again', async t => {
  const fake = await fakeRun(t);
  let state = 'active';
  const get = () => overviewOf(project('p', [row(fake, state === 'offline' ? { liveness: 'unreachable' } : state === 'mismatch' ? { liveness: 'identity_mismatch' } : {})]));
  const { costs, workspace } = await watchedWorkspace(t, get);
  for (const next of ['offline', 'mismatch']) {
    state = next;
    await workspace.refresh();
    const [entry] = costs.view().runs;
    assert.equal(entry.stale, true, next);
    assert.equal(entry.tokens, 1000, next);
  }
  state = 'active';
  await workspace.refresh();
  assert.equal(costs.view().runs[0].stale, false);
});

test('a finished run leaves Costs, and an API-only core run contributes once', async t => {
  const dash = await fakeRun(t);
  const core = await fakeRun(t, { runId: 'r2', instanceId: 'i2', status: statusBody({ run_id: 'r2' }) });
  const done = await fakeRun(t, { runId: 'r3', instanceId: 'i3', status: statusBody({ run_id: 'r3' }) });
  const { costs } = await watchedWorkspace(t, () => overviewOf(project('p', [row(dash), row(core, { mode: 'core' }), row(done, { run_status: 'failed' })])));
  assert.deepEqual(costs.view().runs.map(r => r.key), ['i1:r1', 'i2:r2']);
  assert.equal(done.hits.status, 0);
});

test('analytics reads are bounded and refuse an endpoint that became another run', async t => {
  const fakes = [];
  for (let i = 0; i < 12; i++) fakes.push(await fakeRun(t, { runId: 'r' + i, instanceId: 'i' + i, status: statusBody({ run_id: 'r' + i }) }));
  let current = 0;
  let peak = 0;
  const request = async (url, options) => {
    const analytics = url.includes('/api/usage/analytics');
    if (analytics) { peak = Math.max(peak, ++current); await new Promise(resolve => setTimeout(resolve, 5)); }
    try { return await fetch(url, options); } finally { if (analytics) current--; }
  };
  const costs = new Costs({ request, now: () => T0 });
  costs.setWatched(true);
  const workspace = new Workspace({ desktop: desktopWith(() => overviewOf(project('p', fakes.map(f => row(f))))), costs, request });
  workspace.setVisible(true);
  t.after(() => { workspace.close(); costs.close(); });
  await workspace.refresh();
  assert.ok(peak > 0 && peak <= 6, 'peak analytics concurrency ' + peak);
  assert.equal(costs.view().runs.every(r => r.models !== null), true);
});

test('analytics of a replaced endpoint are rejected and never carry money into a price-hidden run', async t => {
  const fake = await fakeRun(t, { status: statusBody({ accounting: { health: 'ok', has_data: true, show_money: false } }) });
  fake.models = [{ key: 'm', label: 'm', tokens: { counts: { total: 1 } }, money: { estimated_cost_usd: 123.45 } }];
  const view = row(fake);
  // The endpoint answers as another instance by the time analytics are read.
  const request = (url, options) => {
    if (url.endsWith('/api/runtime')) return Promise.resolve(Response.json({ run_id: 'r1', instance_id: 'other' }));
    return fetch(url, options);
  };
  const costs = new Costs({ request, now: () => T0 });
  const rows = [];
  costs.setWatched(true);
  costs.setVisible(true);
  const inventory = Costs.inventoryOf([{ run: view, projectId: 'p' }]);
  await costs.observe(inventory, new Map([[view.key, statusBody({ accounting: { health: 'ok', has_data: true, show_money: false } })]]));
  assert.equal(costs.view().runs[0].models, null);
  // Same run, identity intact: money is still dropped because AFM hides prices.
  const plain = new Costs({ now: () => T0 });
  plain.setWatched(true);
  plain.setVisible(true);
  await plain.observe(inventory, new Map([[view.key, statusBody({ accounting: { health: 'ok', has_data: true, show_money: false } })]]));
  rows.push(...plain.view().runs[0].models);
  assert.doesNotMatch(JSON.stringify(rows), /usd|123/);
});

// What run i answers on /api/<route>; a flagged run waits for a plan approval.
function runAnswer(i, route, flagged = false) {
  if (route === 'runtime') return { run_id: 'r' + i, instance_id: 'i' + i };
  if (route === 'status') return statusBody({ run_id: 'r' + i, ...(flagged ? { stages: [{ id: 'plan', status: 'awaiting_approval' }] } : {}) });
  return { breakdown: { rows: [{ key: 'm', label: 'model', tokens: { counts: { total: 7 } } }] } };
}

// Runs answered in memory, for collection stopped mid-cycle. Every request is logged as "phase route run". The
// first FETCH_LIMIT requests at boundary `at` wait for release(); an abort-aware host gives up one whose signal aborts.
function memoryHost(count, { at, abortAware = false, flagged = 0 }) {
  const host = { runs: [], log: [], aborted: [], inflight: 0, peak: 0, flagged };
  for (let i = 0; i < count; i++) host.runs.push({ runId: 'r' + i, instanceId: 'i' + i, url: `http://127.0.0.1:${42000 + i}` });
  let held = 0;
  let full;
  host.reached = new Promise(resolve => { full = resolve; });
  const released = new Promise(resolve => { host.release = resolve; });
  const pass = (point, signal) => {
    if (point !== at || held === FETCH_LIMIT) return;
    if (++held === FETCH_LIMIT) full();
    if (!abortAware) return released;
    return new Promise((resolve, reject) => {
      released.then(resolve);
      signal.addEventListener('abort', () => { host.aborted.push(signal.reason.name); reject(signal.reason); }, { once: true });
    });
  };
  // `phase` is "status" for the Workspace's reads and "analytics" for the ones Costs makes itself.
  host.request = phase => async (url, { signal }) => {
    const i = host.runs.findIndex(run => run.url === new URL(url).origin);
    const route = new URL(url).pathname.split('/')[2];
    host.log.push(`${phase} ${route} ${i}`);
    host.peak = Math.max(host.peak, ++host.inflight);
    try {
      if (route === 'runtime') await pass(`${phase} runtime`, signal);
      return { ok: true, status: 200, json: async () => {
        try { if (route !== 'runtime') await pass(`${phase} body`, signal); return runAnswer(i, route, i < flagged); }
        finally { host.inflight--; }
      } };
    } catch (error) { host.inflight--; throw error; }
  };
  return host;
}

function memoryWorkspace(t, host) {
  const costs = new Costs({ request: host.request('analytics'), now: () => T0 });
  const runs = host.runs.map((run, i) => row(run, { attention_required: i < host.flagged }));
  const workspace = new Workspace({ desktop: desktopWith(() => overviewOf(project('p', runs))), request: host.request('status'), costs, interval: 60000 });
  t.after(() => { host.release(); workspace.close(); costs.close(); });
  return { costs, workspace };
}

// How collection stops, and how it is wanted again (none after a close: nothing reopens a closed collector).
const STOPS = {
  'unwatching': { stop: ({ costs }) => costs.setWatched(false), resume: ({ costs }) => costs.setWatched(true) },
  'hiding the window': { stop: ({ workspace }) => workspace.setVisible(false), resume: ({ workspace }) => workspace.setVisible(true) },
  'unwatching and watching again at once': { stop: ({ costs }) => { costs.setWatched(false); costs.setWatched(true); }, resume: () => {} },
  'closing the Workspace': { stop: ({ workspace }) => workspace.close() },
  'closing Costs': { stop: ({ costs }) => costs.close() },
};
const BOUNDARIES = ['status runtime', 'status body', 'analytics runtime', 'analytics body'];

// Starts a watched cycle in a visible window and counts the updates Costs reports.
function startCycle({ costs, workspace }) {
  const cycle = { updates: 0 };
  costs.onChange(() => cycle.updates++);
  costs.setWatched(true);
  workspace.setVisible(true);
  cycle.done = workspace.refresh();
  return cycle;
}

// After a stop no request starts, no late analytics is stored and nothing is reported. Fresh demand then collects
// every run again with one update, while a closed collector stays closed.
async function expectStopped(parts, host, cycle, { stoppedAt, resume }) {
  const { costs, workspace } = parts;
  assert.deepEqual(host.log.slice(stoppedAt), [], 'requests started after collection stopped');
  assert.equal(costs.view().runs.some(run => run.models), false, 'a late analytics answer was stored');
  assert.equal(cycle.updates, 0, 'a stopped collection reported an update');
  if (!resume) {
    costs.setWatched(true);
    workspace.setVisible(true);
    await workspace.refresh();
    assert.deepEqual(host.log.slice(stoppedAt), [], 'a closed collector read again');
    return;
  }
  resume(parts);
  await workspace.refresh();
  const recovered = costs.view().runs;
  assert.equal(recovered.length, host.runs.length);
  assert.ok(recovered.every(run => !run.stale && run.models?.length === 1), 'fresh demand collects every run');
  assert.equal(cycle.updates, 1);
}

for (const at of BOUNDARIES) {
  for (const [name, { stop, resume }] of Object.entries(STOPS)) {
    for (const abortAware of [false, true]) {
      test(`${name} at the ${at} boundary${abortAware ? ', abort-aware,' : ''} starts no request and changes nothing later`, async t => {
        const host = memoryHost(13, { at, abortAware });
        const parts = memoryWorkspace(t, host);
        const cycle = startCycle(parts);
        await host.reached;
        const stoppedAt = host.log.length;
        stop(parts);
        // Aborting is synchronous, so an abort-aware host has given up at once; one that ignores it answers late.
        if (abortAware) assert.deepEqual(host.aborted, Array(FETCH_LIMIT).fill('AbortError'), 'held requests were not aborted');
        host.release();
        await cycle.done;
        await expectStopped(parts, host, cycle, { stoppedAt, resume });
        assert.ok(host.peak <= FETCH_LIMIT, 'peak concurrency ' + host.peak);
      });
    }
  }
}

// Held at its runtime answer, the run with a notice still asks for its status afterwards; held at its body, it asks nothing more.
for (const [at, after] of [['status runtime', ['status status 0']], ['status body', []]]) {
  test(`stopping Costs at the ${at} boundary keeps the shared status read and the notice of a run that needs a person`, async t => {
    const host = memoryHost(13, { at, abortAware: true, flagged: 1 });
    const { costs, workspace } = memoryWorkspace(t, host);
    costs.setWatched(true);
    workspace.setVisible(true);
    const cycle = workspace.refresh();
    await host.reached;
    const stoppedAt = host.log.length;
    costs.setWatched(false);
    assert.equal(host.aborted.length, FETCH_LIMIT - 1, 'only the reads made for Costs alone were aborted');
    host.release();
    const snapshot = await cycle;
    assert.deepEqual(host.log.slice(stoppedAt), after, 'only the run with a notice went on reading');
    assert.deepEqual(snapshot.attention.map(item => [item.runKey, item.kind]), [['i0:r0', 'approval']]);
    assert.deepEqual(costs.view().runs, []);
  });
}

// Real runs on loopback. The first FETCH_LIMIT reads of /api/<route> send their headers and part of the JSON body,
// then wait; every other read answers in full. `reached` resolves once the client has those headers, `cut` with the
// time the client gave up the last waiting body.
async function stallingRuns(t, count, route) {
  const host = { runs: [], log: [] };
  let stalled = 0;
  let heard = 0;
  let cut = 0;
  let reached;
  let allCut;
  host.reached = new Promise(resolve => { reached = resolve; });
  host.cut = new Promise(resolve => { allCut = resolve; });
  const serve = i => (req, res) => {
    const asked = new URL(req.url, 'http://run').pathname.split('/')[2];
    host.log.push(`${asked} ${i}`);
    res.setHeader('Content-Type', 'application/json');
    const body = JSON.stringify(runAnswer(i, asked));
    if (asked !== route || stalled === FETCH_LIMIT) return res.end(body);
    stalled++;
    res.on('close', () => { if (++cut === FETCH_LIMIT) allCut(Date.now()); });
    res.write(body.slice(0, 10));
  };
  for (let i = 0; i < count; i++) {
    const server = http.createServer(serve(i));
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
    host.runs.push({ runId: 'r' + i, instanceId: 'i' + i, url: `http://127.0.0.1:${server.address().port}` });
  }
  host.request = async (url, options) => {
    const res = await fetch(url, options);
    if (new URL(url).pathname.split('/')[2] === route && ++heard === FETCH_LIMIT) reached();
    return res;
  };
  return host;
}

for (const [what, route] of [['status', 'status'], ['analytics', 'usage']]) {
  for (const [name, { stop, resume }] of Object.entries(STOPS)) {
    test(`${name} while real ${what} bodies arrive cancels them at once and changes nothing later`, async t => {
      const host = await stallingRuns(t, FETCH_LIMIT + 1, route);
      const costs = new Costs({ request: host.request, now: () => T0 });
      const workspace = new Workspace({ desktop: desktopWith(() => overviewOf(project('p', host.runs.map(run => row(run))))),
        request: host.request, costs, interval: 60000 });
      t.after(() => { workspace.close(); costs.close(); });
      const parts = { costs, workspace };
      const cycle = startCycle(parts);
      await host.reached;
      // Every reader takes its response first, so only the bodies are left to cancel, and fetch's own link to them is collected.
      await new Promise(resolve => setImmediate(resolve));
      collectGarbage();
      const stoppedAt = host.log.length;
      const stoppedTime = Date.now();
      stop(parts);
      assert.ok(await within(cycle.done.then(() => true), 1000), 'the cycle waited for the bodies after collection stopped');
      const cutAt = await within(host.cut, 1000);
      assert.ok(cutAt - stoppedTime < 1000, 'a body went on arriving after collection stopped');
      await expectStopped(parts, host, cycle, { stoppedAt, resume });
    });
  }
}

test('a body that stops arriving ends at its timeout, even after a garbage collection', async t => {
  const host = await stallingRuns(t, FETCH_LIMIT, 'status');
  const costs = new Costs({ request: host.request, now: () => T0 });
  const workspace = new Workspace({ desktop: desktopWith(() => overviewOf(project('p', host.runs.map(run => row(run))))),
    request: host.request, costs, interval: 60000 });
  t.after(() => { workspace.close(); costs.close(); });
  const cycle = startCycle({ costs, workspace });
  await host.reached;
  await new Promise(resolve => setImmediate(resolve));
  collectGarbage();
  assert.ok(await within(cycle.done.then(() => true), 6000), 'a refresh waited for a body past its timeout');
  assert.ok(await within(host.cut, 1000), 'the bodies were not cut at their timeout');
  assert.deepEqual(costs.view().runs, []);
});

test('unwatching aborts a real fetch in flight instead of waiting for its timeout', async t => {
  let requests = 0;
  let arrived;
  const first = new Promise(resolve => { arrived = resolve; });
  // An endpoint that never answers.
  const server = http.createServer(() => { requests++; arrived(); });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const run = { runId: 'r1', instanceId: 'i1', url: `http://127.0.0.1:${server.address().port}` };
  const costs = new Costs({ now: () => T0 });
  const workspace = new Workspace({ desktop: desktopWith(() => overviewOf(project('p', [row(run)]))), costs, interval: 60000 });
  t.after(() => { workspace.close(); costs.close(); });
  costs.setWatched(true);
  workspace.setVisible(true);
  const cycle = workspace.refresh();
  await first;
  const stoppedAt = Date.now();
  costs.setWatched(false);
  await cycle;
  assert.ok(Date.now() - stoppedAt < 1000, 'the cycle waited for the request timeout');
  assert.equal(requests, 1);
});

test('Costs records history for observed runs and drops stored money when prices get hidden', async t => {
  const fake = await fakeRun(t);
  const calls = [];
  const history = { record: async (entries, at, options) => calls.push({ keys: entries.map(e => e.key), at, options }), dropMoney: async () => calls.push('drop') };
  const costs = new Costs({ now: () => T0, history });
  costs.setWatched(true);
  costs.setVisible(true);
  const view = row(fake);
  await costs.observe(Costs.inventoryOf([{ run: view, projectId: 'p' }]), new Map([[view.key, statusBody()]]));
  assert.deepEqual(calls, [{ keys: ['i1:r1'], at: T0, options: { hidePrices: false } }]);
  costs.setHidePrices(true);
  assert.equal(calls.at(-1), 'drop');
  costs.setWatched(false);
  await costs.observe(Costs.inventoryOf([{ run: view, projectId: 'p' }]), new Map([[view.key, statusBody()]]));
  assert.equal(calls.length, 2, 'nothing is recorded once Costs is not wanted');
});

test('Costs.period reports growth from the history, hides money, and says no data instead of zero', async t => {
  const { mkdtemp, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { CostsHistory } = await import('../src/costs-history.js');
  const dir = await mkdtemp((await import('node:path')).join(tmpdir(), 'costs-period-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const fake = await fakeRun(t);
  const view = row(fake);
  let now = T0;
  const costs = new Costs({ now: () => now, history: new CostsHistory(dir) });
  costs.setWatched(true);
  costs.setVisible(true);
  const observe = async (tokens, usd) => costs.observe(Costs.inventoryOf([{ run: view, projectId: 'p' }]), new Map([[view.key, statusBody({ run_cost: cost(usd, tokens) })]]));
  await observe(1000, 1);
  now = T0 + 60000;
  await observe(4000, 2.5);
  now = T0 + 120000;
  await observe(9000, 4);
  const result = await costs.period(T0, T0 + 120000, 2);
  const run = result.runs[view.key];
  assert.equal(result.available, true);
  assert.equal(run.tokens.value, 8000);
  assert.equal(run.usd.value, 3);
  assert.deepEqual(run.buckets.tokens.map(slice => slice?.value), [3000, 5000]);
  assert.deepEqual(run.buckets.cost.map(slice => slice?.value), [1.5, 1.5]);
  // Stage work: 30 s closed plus the open stage since T0 + 70 s.
  assert.deepEqual(run.anchors, { cost: 4, work: 80000 }, 'the values the slices lead up to, from the same snapshots');
  assert.deepEqual((await costs.period(T0, T0 + 60000, 2)).runs[view.key].anchors, { cost: 2.5, work: 30000 }, 'a later snapshot is not used');
  const early = await costs.period(T0 - 7200000, T0 - 3600000, 1);
  assert.equal(early.runs[view.key], undefined, 'a run Desktop had not seen yet is not part of that moment');
  costs.setHidePrices(true);
  const hidden = await costs.period(T0, T0 + 120000, 2);
  assert.equal(hidden.runs[view.key].usd, null);
  assert.equal(hidden.runs[view.key].buckets.cost, null);
  assert.deepEqual(hidden.runs[view.key].anchors, { cost: null, work: 80000 });
  assert.doesNotMatch(JSON.stringify(hidden), /usd":\{/);
  costs.setHidePrices(false);
  now = T0 + 180000;
  await costs.observe(Costs.inventoryOf([{ run: view, projectId: 'p' }]), new Map([[view.key, statusBody({ accounting: { health: 'ok', has_data: true, show_money: false } })]]));
  assert.deepEqual((await costs.period(T0, now, 2)).runs[view.key].anchors, { cost: null, work: 140000 }, 'AFM hides the price of this run');
  assert.equal((await costs.period(T0, T0, 2)).available, false, 'an empty range asks for nothing');
  assert.equal((await new Costs().period(T0 - 1, T0 + 1, 2)).available, false, 'no history store');
});

test('Costs uses current counters for a run inside the window and preserves readings across an outage', async t => {
  const { mkdtemp, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { CostsHistory } = await import('../src/costs-history.js');
  const dir = await mkdtemp((await import('node:path')).join(tmpdir(), 'costs-window-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const history = new CostsHistory(dir);
  const view = row(await fakeRun(t));
  let now = T0;
  const costs = new Costs({ history, now: () => now });
  t.after(() => costs.close());
  costs.setWatched(true);
  costs.setVisible(true);
  const inventory = Costs.inventoryOf([{ run: view, projectId: 'p' }]);
  const observe = tokens => costs.observe(inventory, new Map([[view.key, statusBody({
    started_at: new Date(T0 - 60000).toISOString(), run_cost: cost(1, tokens),
  })]]));
  await observe(39019835);
  await history.markOutage([view.key], now);
  now += 60000;
  await observe(55500505);
  const { runs } = await costs.period(T0 - 3600000, now, 8);
  const run = runs[view.key];
  assert.equal(run.tokens.observed, false);
  assert.equal(run.meta.wholeRun, true);
  assert.equal(run.windowTotals.tokens, 55500505);
  assert.deepEqual(run.recorded.tokens, [
    { at: T0, value: 39019835, breakBefore: false },
    { at: now, value: 55500505, breakBefore: true },
  ]);
});

test('Costs.period as of a past moment: finished runs appear while followed, later ones do not, and the real reading time is separate', async t => {
  const { mkdtemp, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const path = await import('node:path');
  const { CostsHistory } = await import('../src/costs-history.js');
  const dir = await mkdtemp(path.join(tmpdir(), 'costs-asof-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const fake = await fakeRun(t);
  const view = row(fake);
  let now = T0;
  const history = new CostsHistory(dir);
  const costs = new Costs({ now: () => now, history });
  costs.setWatched(true);
  costs.setVisible(true);
  const observe = (tokens, usd) => costs.observe(Costs.inventoryOf([{ run: view, projectId: 'p' }]), new Map([[view.key, statusBody({ run_cost: cost(usd, tokens) })]]));
  await observe(1000, 1);
  now = T0 + 60000;
  await observe(4000, 2.5);
  // The run disappears from the inventory (finished); a later one starts.
  now = T0 + 3600000;
  await costs.observe(Costs.inventoryOf([]), new Map());
  const result = await costs.period(T0 + 58000, T0 + 59000, 0);
  const past = result.runs[view.key];
  assert.ok(past, 'the finished run was followed at that moment');
  assert.equal(past.meta.finished, true);
  assert.equal(past.asOf.tokens, 1000, 'the figure of the snapshot at or before the position');
  assert.equal(past.asOf.observedAt, T0, 'the real reading time, not the slider position');
  const later = await costs.period(T0 + 3000000, T0 + 3600000, 0);
  assert.equal(later.runs[view.key], undefined, 'a long time after the last snapshot a finished run is gone');
  const before = await costs.period(T0 - 7200000, T0 - 3600000, 0);
  assert.equal(before.runs[view.key], undefined);
  costs.setHidePrices(true);
  assert.equal((await costs.period(T0 + 58000, T0 + 59000, 0)).runs[view.key].asOf.usd, null);
});

test('active elapsed intervals extend from the clock anchor and stop at the last verified reading when offline', async t => {
  const { mkdtemp, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { CostsHistory } = await import('../src/costs-history.js');
  const dir = await mkdtemp((await import('node:path')).join(tmpdir(), 'costs-active-chart-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const run = row(await fakeRun(t));
  const minute = 60000;
  let now = T0 + 10 * minute;
  const costs = new Costs({ now: () => now, history: new CostsHistory(dir) });
  t.after(() => costs.close());
  costs.setWatched(true);
  costs.setVisible(true);
  const inventory = Costs.inventoryOf([{ run, projectId: 'p' }]);
  await costs.observe(inventory, new Map([[run.key, statusBody({ elapsed_accumulated_ms: 0, elapsed_since: iso(T0 + 12) })]]));
  const chart = () => costs.period(T0 - 10 * minute, now, 2, { intervalChart: true });
  const values = result => result.runs[run.key].buckets.time.map(point => point?.value ?? null);
  assert.deepEqual(values(await chart()), [null, 10 * minute - 12]);
  now += 10 * minute;
  const current = await chart();
  assert.equal(current.runs[run.key].elapsed.value, 20 * minute - 12);
  await costs.observe(inventory, new Map([[run.key, null]]));
  const offline = await chart();
  assert.equal(offline.runs[run.key].elapsed.value, 10 * minute - 12);
  assert.equal(offline.runs[run.key].buckets.time.at(-1).end, T0 + 10 * minute);
});

test('finished charts recover elapsed intervals and place token totals on recorded job completions', async t => {
  const { mkdtemp, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { CostsHistory } = await import('../src/costs-history.js');
  const dir = await mkdtemp((await import('node:path')).join(tmpdir(), 'costs-finished-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const run = row(await fakeRun(t));
  const unseen = { ...run, key: 'another:unseen', runId: 'unseen' };
  let now = T0;
  const reports = [];
  const costs = new Costs({ now: () => now, history: new CostsHistory(dir), readReport: async run => {
    reports.push(run.key);
    const job = (start, duration, tokens) => ({ started_at: iso(start), duration_ms: duration, status: 'ok', tokens: { counts: { total: tokens } } });
    return { run: { started_at: new Date(T0), ended_at: new Date(T0 + 120000), elapsed_ms: 119983, status: 'failed' },
      cost: { total: { tokens: { total: 9000 } } }, stage_time: { working_ms: 50000 },
      analytics: { jobs: [job(T0, 30000, 4000), job(T0 + 30000, 90000, run.key === unseen.key ? 1000 : 5000)] } };
  } });
  costs.setWatched(true);
  costs.setVisible(true);
  const observe = tokens => costs.observe(Costs.inventoryOf([{ run, projectId: 'p' }]), new Map([[run.key, statusBody({ run_cost: cost(2, tokens) })]]));
  await observe(1000);
  now += 60000;
  await observe(4000);
  now += 120000;
  costs.setFinished([run, unseen, { ...run, key: 'stale' }].map(item => ({
    run: { ...item, state: item.key === 'stale' ? 'offline' : 'finished', can: { report: true } }, projectId: 'p',
  })));
  await costs.observe([], new Map());
  assert.deepEqual(Object.keys((await costs.period(T0, now, 3)).runs), []);
  assert.deepEqual(reports, []);
  const options = { finished: true, growth: true };
  const { runs } = await costs.period(T0, now, 3, options);
  assert.deepEqual(Object.keys(runs), [run.key, unseen.key]);
  assert.equal(runs[run.key].tokens.value, 3000);
  assert.equal(runs[run.key].final.tokens, 9000);
  assert.equal(runs[unseen.key].final.tokens, 9000);
  assert.equal(runs[unseen.key].meta.wholeRun, true);
  assert.deepEqual(runs[unseen.key].buckets.tokens, [null, null, null]);
  const chart = await costs.period(T0, now, 3, { ...options, intervalChart: true });
  assert.deepEqual(chart.runs[unseen.key].buckets.time.map(slice => slice?.value ?? null), [59983, 60000, null]);
  assert.deepEqual(chart.runs[run.key].buckets.tokens.map(slice => slice?.value ?? null), [4000, 5000, null]);
  assert.deepEqual(chart.runs[unseen.key].buckets.tokens, [null, null, null], 'an incomplete job ledger cannot place the total');
  assert.equal(chart.runs[run.key].tokens.value, 9000);
  assert.deepEqual(Object.keys((await costs.period(T0, T0 + 60000, 0, { finished: true, rewind: true })).runs), []);
  assert.deepEqual(reports, [run.key, unseen.key], 'final reports are reused across ranges');
  costs.setHidePrices(true);
  assert.equal((await costs.period(T0, now, 3, options)).runs[run.key].asOf.usd, null);
});

test('history observation reports a gap at a moment nobody observed', async t => {
  const { mkdtemp, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const path = await import('node:path');
  const { CostsHistory, GAP_MS } = await import('../src/costs-history.js');
  const dir = await mkdtemp(path.join(tmpdir(), 'costs-gap-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const history = new CostsHistory(dir);
  const sample = (key, tokens) => ({ key, name: key, projectId: 'p', stale: false, startedAt: T0, elapsed: { ms: tokens, since: null }, idle: { ms: 0, since: null }, stageWork: null, tokens, money: null });
  await history.record([sample('i:r', 100)], T0);
  await history.record([sample('i:r', 900)], T0 + 2 * GAP_MS);
  assert.equal(history.observation('i:r', T0 + GAP_MS).gap, true);
  assert.equal(history.observation('i:r', T0 + GAP_MS).observedAt, T0);
  assert.equal(history.observation('i:r', T0 + 2 * GAP_MS).gap, false);
  assert.equal(history.observation('i:r', T0 - 1), null);
});

import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Workspace } from '../src/workspace.js';
import { runView } from '../src/runs.js';
import { WatchService } from '../src/watch/index.js';
import { Notifier } from '../src/notifier.js';

// A run's own HTTP API: identity, run status with its stages, and the dialog of a stage.
async function fakeRun(t, { runId = 'r1', instanceId = 'i1', stages = [], runStatus = 'running' } = {}) {
  // `hold` is a promise the server awaits before answering, so a slow run can be imitated exactly.
  const state = { runId, instanceId, stages, runStatus, fail: false, hold: null, hits: [], dialogs: {} };
  const server = http.createServer(async (req, res) => {
    state.hits.push(req.url);
    await state.hold;
    res.setHeader('Content-Type', 'application/json');
    if (state.fail) { res.statusCode = 500; res.end('{}'); return; }
    if (req.url === '/api/runtime') res.end(JSON.stringify({ run_id: state.runId, instance_id: state.instanceId, mode: 'dashboard', ready: true }));
    else if (req.url === '/api/status') res.end(JSON.stringify({ run_id: state.runId, run_status: state.runStatus, stages: state.stages }));
    else if (req.url.endsWith('/dialog')) res.end(JSON.stringify(state.dialogs[req.url.split('/')[3]] ?? []));
    else { res.statusCode = 404; res.end('{}'); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  state.url = `http://127.0.0.1:${server.address().port}`;
  return state;
}

// Inventory rows shaped like the host's: a discovered row has neither run_status nor attention_required.
const discovered = fake => ({ run_id: fake.runId, instance_id: 'i1', run_name: 'terminal', project_root: '/p', mode: 'dashboard',
  api_url: fake.url, ownership: 'discovered', liveness: 'active', actions: ['attach'] });
const core = fake => ({ run_id: fake.runId, instance_id: 'i1', run_name: 'core', project_root: '/p', mode: 'core',
  api_url: fake.url, ownership: 'attached', liveness: 'active', run_status: 'running', attention_required: true, actions: [] });
const attached = fake => ({ ...core(fake), run_name: 'dashboard', mode: 'dashboard' });

const question = (fake, stageId, id) => {
  fake.stages.push({ id: stageId, status: 'awaiting_user_input', updated_at: '2026-06-01T08:00:00Z' });
  fake.dialogs[stageId] = [{ id, phase: 'autonomous_execution', ts: '2026-06-01T08:00:30Z', question: 'Private prompt' }];
};

async function setup(t, rows) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-watch-inventory-'));
  const desktop = { overview: async () => ({ host: { state: 'connected', error: null },
    projects: [{ id: 'p', path: '/p', name: 'p', runs: rows().map(row => runView(row)) }], unassigned: [], candidates: [] }) };
  const results = [];
  const service = new WatchService({ home, onResult: (_task, result) => results.push(result) });
  const workspace = new Workspace({ desktop, watchActive: () => service.active });
  workspace.onPoll(snapshot => service.onSnapshot(snapshot));
  const banners = [];
  const notifier = new Notifier({ home, show: banner => banners.push(banner),
    settings: { enabled: true, kinds: { question: true, approval: true, decision: true, failed: true } } });
  t.after(async () => { workspace.close(); await service.flush(); await rm(home, { recursive: true, force: true }); });
  const poll = async () => { const snapshot = await workspace.refresh(); await notifier.onSnapshot(snapshot); return snapshot; };
  return { workspace, service, results, banners, poll };
}

test('a terminal-started run is read without attention_required: its question is a watch result, not a new banner', async t => {
  const terminal = await fakeRun(t, { runId: 'term' });
  const dashboard = await fakeRun(t, { runId: 'dash' });
  const { service, results, banners, poll } = await setup(t, () => [discovered(terminal), attached(dashboard)]);
  await poll();
  assert.deepEqual(terminal.hits, [], 'without a watch task the discovered run is not read');
  service.create({});
  question(terminal, 'ask', 'q1');
  question(dashboard, 'ask', 'q2');
  let snapshot = await poll();
  assert.deepEqual(results.map(r => [r.runId, r.kind, r.stageId]), [['dash', 'question', 'ask']], 'the first read of the discovered run is its baseline');
  assert.deepEqual(snapshot.attention.map(item => item.runKey), ['i1:dash'], 'the queue and the notifier still see only flagged runs');
  assert.deepEqual(banners.map(banner => banner.runKey), ['i1:dash'], 'no banner for a run that never produced one');
  question(terminal, 'next', 'q3');
  snapshot = await poll();
  assert.deepEqual(results.map(r => [r.runId, r.kind, r.stageId]), [['dash', 'question', 'ask'], ['term', 'question', 'next']]);
  assert.deepEqual(snapshot.attention.map(item => item.runKey), ['i1:dash']);
  assert.equal(JSON.stringify(snapshot).includes('Private prompt'), false);
  await poll();
  assert.equal(results.length, 2, 'an unchanged question is not new');
});

test('an API-only run with attention is read: a decision with an opaque round id becomes a result', async t => {
  const api = await fakeRun(t, { runId: 'core' });
  const { service, results, banners, poll } = await setup(t, () => [core(api)]);
  service.create({});
  await poll();
  api.stages.push({ id: 'gate', status: 'running', pre_completion_blocked: true, pre_completion: { round_id: 'round-7' }, updated_at: '2026-06-01T08:00:00Z' });
  await poll();
  assert.deepEqual(results.map(r => [r.kind, r.stageId]), [['decision', 'gate']]);
  assert.deepEqual(banners, []);
});

test('a discovered run that ends is reported from the same status read: finished and one failure, no second fetch', async t => {
  const done = await fakeRun(t, { runId: 'done' });
  const broken = await fakeRun(t, { runId: 'broken' });
  const { service, results, poll } = await setup(t, () => [discovered(done), discovered(broken)]);
  service.create({});
  await poll();
  done.runStatus = 'finished';
  broken.runStatus = 'failed';
  broken.stages.push({ id: 'build', status: 'failed', updated_at: '2026-06-01T08:01:00Z' });
  done.hits.length = 0;
  await poll();
  assert.deepEqual(done.hits, ['/api/runtime', '/api/status'], 'one identity check and one status read per poll');
  assert.deepEqual(results.map(r => [r.runId, r.kind]).sort(), [['broken', 'failed'], ['done', 'finished']]);
  await poll();
  assert.equal(results.length, 2);
});

test('a wrong runtime identity is rejected before the status read; a failed read is not an empty list', async t => {
  const impostor = await fakeRun(t, { runId: 'imp', instanceId: 'other' });
  const flaky = await fakeRun(t, { runId: 'flaky' });
  question(flaky, 'ask', 'q1');
  const { service, results, poll } = await setup(t, () => [discovered(impostor), discovered(flaky)]);
  const idle = await poll();
  assert.deepEqual(idle.observed.unavailable, ['i1:imp', 'i1:flaky'], 'runs the idle loop does not read are unknown, not empty');
  service.create({});
  flaky.fail = true;
  const failed = await poll();
  assert.deepEqual(impostor.hits, ['/api/runtime']);
  assert.deepEqual(failed.observed.unavailable, ['i1:imp', 'i1:flaky'], 'a run that never answered is unknown, not empty');
  flaky.fail = false;
  await poll();
  assert.deepEqual(results, [], 'neither the impostor nor the question pending before the first read is reported');
  question(flaky, 'next', 'q2');
  await poll();
  assert.deepEqual(results.map(r => r.stageId), ['next']);
});

test('without a watch task no discovered run is read: zero requests, and a slow one does not hold up the Home snapshot', async t => {
  const slow = await fakeRun(t, { runId: 'slow' });
  const { service, workspace, poll } = await setup(t, () => [discovered(slow)]);
  let release;
  slow.hold = new Promise(resolve => { release = resolve; });
  await poll();
  await poll();
  assert.deepEqual(slow.hits, [], 'two idle polls, no requests');
  service.create({});
  const pending = workspace.refresh();
  const raced = await Promise.race([pending, new Promise(resolve => setTimeout(() => resolve('held'), 50))]);
  assert.equal(raced, 'held', 'with a task the read really waits for the run');
  release();
  await pending;
  assert.deepEqual(slow.hits, ['/api/runtime', '/api/status']);
});

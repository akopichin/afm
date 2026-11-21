import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { WatchService } from '../src/watch/index.js';
import { MAX_RESULTS } from '../src/watch/store.js';
import { Workspace } from '../src/workspace.js';
import { eventually } from './support.js';

const NOW = Date.parse('2026-06-01T08:00:00Z');
const run = (runId, extra = {}) => ({ key: `i1:${runId}`, runId, instanceId: 'i1', name: `flow-${runId}`, state: 'running', ...extra });
// Request identities as fetchAttention builds them: opaque strings, never times.
const item = (r, stageId, kind = 'question', requestAt = JSON.stringify(['autonomous_execution', `q-${stageId}`, '2026-06-01T08:00:01Z'])) =>
  ({ id: `${r.key}:${stageId}`, runKey: r.key, stageId, stageName: stageId.toUpperCase(), kind, requestAt });
const snap = ({ projects = [{ id: 'p', runs: [] }], unassigned = [], attention = [], host = { state: 'connected', error: null } } = {}) =>
  ({ host, projects, unassigned, attention });

async function setup(t) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-watch-'));
  const made = [];
  t.after(async () => { await Promise.all(made.map(service => service.flush())); await rm(home, { recursive: true, force: true }); });
  const clock = { now: NOW };
  const results = [];
  const due = [];
  const make = () => { const service = new WatchService({ home, now: () => clock.now, onResult: (_task, result) => results.push(result), onSummaryDue: task => due.push(task.id) }); made.push(service); return service; };
  return { clock, results, due, make, service: make() };
}

test('a new task starts from what is already there; later events become results once', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }], attention: [item(a, 'ask')] }));
  const task = service.create({});
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }], attention: [item(a, 'ask')] }));
  assert.equal(results.length, 0, 'what was pending at creation is the baseline');
  const later = [item(a, 'ask'), item(a, 'review', 'approval')];
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }], attention: later }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }], attention: later }));
  assert.deepEqual(results.map(r => [r.kind, r.stageId]), [['approval', 'review']]);
  assert.equal(service.list()[0].unread, 1);
  assert.equal(service.list()[0].id, task.id);
});

test('a finished run and a new request in the same stage are separate results', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  service.create({});
  const projects = r => [{ id: 'p', runs: [r] }];
  service.onSnapshot(snap({ projects: projects(a) }));
  service.onSnapshot(snap({ projects: projects(a), attention: [item(a, 'ask')] }));
  service.onSnapshot(snap({ projects: projects(a), attention: [item(a, 'ask', 'question', JSON.stringify(['autonomous_execution', 'q-ask-2', '']))] }));
  service.onSnapshot(snap({ projects: projects(run('a', { state: 'finished', status: 'finished' })) }));
  assert.deepEqual(results.map(r => r.kind), ['question', 'question', 'finished']);
});

test('restart and host outage re-baseline silently: nothing from the gap is reported, later events are, once', async t => {
  const { service, make, results } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.create({});
  service.onSnapshot(snap({ projects }));
  service.onSnapshot(snap({ projects, attention: [item(a, 'ask')] }));
  await service.flush();
  assert.equal(results.length, 1);
  const restarted = make();
  await restarted.load();
  // The first poll after the restart shows the old request and one raised while Desktop was closed.
  restarted.onSnapshot(snap({ projects, attention: [item(a, 'ask'), item(a, 'closed')] }));
  assert.equal(results.length, 1, 'what happened while Desktop was closed is not reported');
  const later = [item(a, 'ask'), item(a, 'closed'), item(a, 'later')];
  restarted.onSnapshot(snap({ projects, attention: later }));
  restarted.onSnapshot(snap({ projects, attention: later }));
  assert.deepEqual(results.map(r => r.stageId), ['ask', 'later']);
  restarted.onSnapshot(snap({ projects, attention: [], host: { state: 'offline', error: { message: 'down' } } }));
  restarted.onSnapshot(snap({ projects, attention: [...later, item(a, 'outage')] }));
  assert.equal(results.length, 2, 'what happened while the host was down is not reported');
  restarted.onSnapshot(snap({ projects, attention: [...later, item(a, 'outage'), item(a, 'back')] }));
  assert.deepEqual(results.map(r => r.stageId), ['ask', 'later', 'back']);
  assert.equal(restarted.list()[0].results.length, 3, 'results survive the restart and the outage');
});

test('a re-baseline keeps the resolved history: a request that returns after a restart within the horizon is not new', async t => {
  const { service, make, results } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.create({});
  service.onSnapshot(snap({ projects }));
  service.onSnapshot(snap({ projects, attention: [item(a, 'ask')] }));
  service.onSnapshot(snap({ projects }));
  await service.flush();
  const restarted = make();
  await restarted.load();
  restarted.onSnapshot(snap({ projects }));
  restarted.onSnapshot(snap({ projects, attention: [item(a, 'ask')] }));
  assert.equal(results.length, 1);
});

test('an offline poll does not replace the connected snapshot a new task baselines from', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.onSnapshot(snap({ projects }));
  service.onSnapshot(snap({ projects, host: { state: 'offline', error: { message: 'down' } } }));
  service.create({});
  service.onSnapshot(snap({ projects, attention: [item(a, 'ask')] }));
  assert.deepEqual(results.map(r => r.stageId), ['ask'], 'raised after the creation: reported when the host is back');
});

const observed = (unavailable = [], notices = []) => ({ notices, status: {}, unavailable });

test('a run unreadable at the baseline is unknown, not empty: its first read seeds silently; an empty run that later asks is reported', async t => {
  const { service, results } = await setup(t);
  const x = run('x');
  const y = run('y');
  const projects = [{ id: 'p', runs: [x, y] }];
  service.onSnapshot({ ...snap({ projects }), observed: observed(['i1:x']) });
  service.create({});
  service.onSnapshot({ ...snap({ projects }), observed: observed(['i1:x']) });
  assert.deepEqual(results, []);
  // x answers at last and shows the question it had all along; y raises a new one.
  service.onSnapshot({ ...snap({ projects }), observed: observed([], [item(x, 'old'), item(y, 'fresh')]) });
  assert.deepEqual(results.map(r => [r.runKey, r.stageId]), [['i1:y', 'fresh']]);
  service.onSnapshot({ ...snap({ projects }), observed: observed([], [item(x, 'old'), item(x, 'new'), item(y, 'fresh')]) });
  assert.deepEqual(results.map(r => r.stageId), ['fresh', 'new']);
});

test('scope: a terminal-started run is watched, an exact watch ignores another instance, a project watch ignores other projects', async t => {
  const { service, results } = await setup(t);
  const external = run('ext');
  const sameIdOtherInstance = { ...run('ext'), key: 'i2:ext', instanceId: 'i2' };
  service.create({ title: 'exact', scope: { kind: 'run', runId: 'ext', instanceId: 'i1' } });
  service.create({ title: 'project q', scope: { kind: 'project', projectId: 'q' } });
  const base = { projects: [{ id: 'p', runs: [] }, { id: 'q', runs: [] }], unassigned: [external, sameIdOtherInstance] };
  service.onSnapshot(snap(base));
  service.onSnapshot(snap({ ...base, attention: [item(external, 'ask'), item(sameIdOtherInstance, 'ask')] }));
  assert.deepEqual(results.map(r => [r.runKey]), [['i1:ext']], 'only the exact instance of the externally started run');
  const inQ = run('inq');
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [run('inp')] }, { id: 'q', runs: [inQ] }], unassigned: [], attention: [item(inQ, 'ask')] }));
  assert.equal(results.filter(r => r.runKey === 'i1:inq').length, 1, 'one result: the project watch only');
});

test('event types filter results', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.create({ events: ['failed'] });
  service.onSnapshot(snap({ projects }));
  service.onSnapshot(snap({ projects, attention: [item(a, 'ask'), item(a, 'build', 'failed')] }));
  assert.deepEqual(results.map(r => r.kind), ['failed']);
});

test('paused and stopped tasks collect nothing and keep results; resume re-baselines, so the pause is not reported', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  const task = service.create({});
  service.onSnapshot(snap({ projects }));
  service.onSnapshot(snap({ projects, attention: [item(a, 'one')] }));
  service.pause(task.id);
  service.onSnapshot(snap({ projects, attention: [item(a, 'one'), item(a, 'two')] }));
  assert.equal(results.length, 1);
  service.resume(task.id);
  service.onSnapshot(snap({ projects, attention: [item(a, 'one'), item(a, 'two')] }));
  assert.deepEqual(results.map(r => r.stageId), ['one'], 'results are kept; the request that arose during the pause is not reported');
  service.onSnapshot(snap({ projects, attention: [item(a, 'one'), item(a, 'two'), item(a, 'three')] }));
  assert.deepEqual(results.map(r => r.stageId), ['one', 'three']);
  service.stop(task.id);
  service.onSnapshot(snap({ projects, attention: [item(a, 'one'), item(a, 'two'), item(a, 'three'), item(a, 'four')] }));
  assert.equal(results.length, 2);
  assert.equal(service.list()[0].results.length, 2);
});

test('an expired deadline completes the task, a due summary is signalled once; neither needs a snapshot change', async t => {
  const { service, clock, due } = await setup(t);
  const task = service.create({ deadline: '2026-06-01T09:00:00Z', summaryAt: '2026-06-01T08:50:00Z' });
  service.onSnapshot(snap());
  clock.now = Date.parse('2026-06-01T08:51:00Z');
  service.onSnapshot(snap());
  service.onSnapshot(snap());
  assert.deepEqual(due, [task.id]);
  assert.equal(service.active, true);
  clock.now = Date.parse('2026-06-01T09:00:01Z');
  service.onSnapshot(snap({ host: { state: 'offline', error: { message: 'down' } } }));
  assert.equal(service.list()[0].status, 'completed', 'the deadline is met even while the host is down');
  assert.equal(service.active, false);
});

test('burst: results and seen keys stay bounded and ordered', async t => {
  const { service } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.create({});
  service.onSnapshot(snap({ projects }));
  const many = n => Array.from({ length: n }, (_, i) => item(a, `s${i}`, 'question', `2026-06-01T08:00:${String(i % 60).padStart(2, '0')}Z#${i}`));
  service.onSnapshot(snap({ projects, attention: many(MAX_RESULTS + 300) }));
  const [task] = service.list();
  assert.equal(task.results.length, MAX_RESULTS);
  assert.ok(task.cursor.seen.length <= MAX_RESULTS + 300 && task.cursor.seen.length > 0);
  assert.equal(task.results.at(-1).stageId, `s${MAX_RESULTS + 299}`, 'the newest results are kept, in order');
  service.onSnapshot(snap({ projects }));
  assert.ok(service.list()[0].cursor.seen.length <= 500, 'resolved keys are evicted beyond the bound');
});

// ---- the one observation loop ----

const desktopOf = () => { const calls = { overview: 0 }; return { calls, overview: async () => { calls.overview++; return { host: { state: 'connected', error: null }, projects: [], unassigned: [], candidates: [] }; } }; };

test('a hidden window with an active task keeps exactly one loop; with nothing to watch there is no timer', async t => {
  const { service } = await setup(t);
  const desktop = desktopOf();
  const workspace = new Workspace({ desktop, interval: 5, hiddenInterval: 10, watchHidden: () => service.active });
  t.after(() => workspace.close());
  const polls = [];
  workspace.onPoll(snapshot => { polls.push(snapshot); service.onSnapshot(snapshot); });
  workspace.setVisible(false);
  assert.equal(workspace.timer, null, 'no task, no notifications: no timer');
  await delay(30);
  assert.equal(desktop.calls.overview, 0);
  const task = service.create({});
  workspace.wake();
  const timer = workspace.timer;
  assert.ok(timer, 'an active task starts the loop');
  workspace.wake();
  assert.equal(workspace.timer, timer, 'waking again does not add a second timer');
  await eventually(() => polls.length >= 2, { timeout: 3000, what: 'two hidden window polls' });
  assert.equal(polls.length, desktop.calls.overview, 'every poll reaches the observer, even when nothing changed');
  service.pause(task.id);
  await eventually(() => workspace.timer === null && !workspace.inflight, { timeout: 3000, what: 'the hidden window poll loop to go idle' });
  const settled = desktop.calls.overview;
  await delay(40);
  assert.equal(desktop.calls.overview, settled, 'a paused task lets the loop go idle');
  assert.equal(workspace.timer, null);
});

// ---- review round 1 ----

test('a request raised between creation and the first poll is reported; one pending at creation is not', async t => {
  const { service, results, clock } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.onSnapshot(snap({ projects }));
  service.create({});
  clock.now = Date.parse('2026-06-01T08:01:00Z');
  service.onSnapshot(snap({ projects, attention: [item(a, 'new')] }));
  assert.deepEqual(results.map(r => r.stageId), ['new']);
  // No connected snapshot at creation: the first connected poll is the baseline and reports nothing,
  // even when a request identity looks like a time after the creation.
  const other = await setup(t);
  other.service.onSnapshot(snap({ host: { state: 'offline', error: { message: 'down' } } }));
  other.service.create({});
  other.service.onSnapshot(snap({ projects, attention: [item(a, 'old'), item(a, 'new', 'question', '2026-06-01T08:00:30Z')] }));
  assert.deepEqual(other.results, []);
  other.service.onSnapshot(snap({ projects, attention: [item(a, 'old'), item(a, 'new', 'question', '2026-06-01T08:00:30Z'), item(a, 'later')] }));
  assert.deepEqual(other.results.map(r => r.stageId), ['later']);
});

test('a run failure is reported from the run outcome once; a starting launch is not a finished run', async t => {
  const { service, results } = await setup(t);
  service.create({});
  const running = run('a');
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [running] }] }));
  const starting = { key: 'i2:undefined', runId: undefined, instanceId: 'i2', state: 'finished', status: null };
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [running, starting] }] }));
  assert.equal(results.length, 0, 'no run id, no result');
  const failed = run('a', { state: 'finished', status: 'failed' });
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [failed, starting] }] }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [failed, starting] }] }));
  assert.deepEqual(results.map(r => r.kind), ['failed']);
});

test('a failed stage and the failed run it ends in are one result', async t => {
  const { service, results } = await setup(t);
  service.create({});
  const a = run('a');
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }] }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }], attention: [item(a, 'build', 'failed')] }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [run('a', { state: 'finished', status: 'failed' })] }] }));
  assert.deepEqual(results.map(r => r.kind), ['failed']);
});

test('a poll delivered at or after the deadline collects nothing, also after a restart', async t => {
  const { service, make, results, clock } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.onSnapshot(snap({ projects }));
  service.create({ deadline: '2026-06-01T09:00:00Z' });
  service.onSnapshot(snap({ projects }));
  await service.flush();
  clock.now = Date.parse('2026-06-01T10:00:00Z');
  const restarted = make();
  await restarted.load();
  // An identity that looks like a time before the deadline is still only an identity.
  restarted.onSnapshot(snap({ projects, attention: [item(a, 'before', 'question', '2026-06-01T08:30:00Z'), item(a, 'after')] }));
  assert.deepEqual(results, []);
  assert.equal(restarted.list()[0].status, 'completed');
});

test('the deadline is exact: a poll at the deadline collects nothing, the one before it collects', async t => {
  const { service, results, clock } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.onSnapshot(snap({ projects }));
  service.create({ deadline: '2026-06-01T09:00:00Z' });
  clock.now = Date.parse('2026-06-01T08:59:59.999Z');
  service.onSnapshot(snap({ projects, attention: [item(a, 'one', 'question', '2026-06-01T08:59:00Z')] }));
  clock.now = Date.parse('2026-06-01T09:00:00Z');
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [run('a', { state: 'finished', status: 'finished' })] }], attention: [item(a, 'one', 'question', '2026-06-01T08:59:00Z'), item(a, 'two', 'question', '2026-06-01T08:59:30Z')] }));
  assert.deepEqual(results.map(r => r.stageId ?? r.kind), ['one']);
  assert.equal(service.list()[0].status, 'completed');
});

test('a summary due before the deadline is signalled once when the first later poll is offline', async t => {
  const { service, clock, due } = await setup(t);
  const task = service.create({ deadline: '2026-06-01T09:00:00Z', summaryAt: '2026-06-01T08:30:00Z' });
  clock.now = Date.parse('2026-06-01T09:30:00Z');
  service.onSnapshot(snap({ host: { state: 'offline', error: { message: 'down' } } }));
  service.onSnapshot(snap());
  assert.deepEqual(due, [task.id]);
  assert.equal(service.list()[0].status, 'completed');
});

test('the seen set shrinks back after a burst resolves and stays bounded under many finished runs', async t => {
  const { service } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.onSnapshot(snap({ projects }));
  service.create({});
  service.onSnapshot(snap({ projects }));
  const many = Array.from({ length: 650 }, (_, i) => item(a, `s${i}`, 'question', `2026-06-01T08:00:01Z#${i}`));
  service.onSnapshot(snap({ projects, attention: many }));
  assert.equal(service.list()[0].cursor.seen.length, 650, 'pending requests are never forgotten');
  service.onSnapshot(snap({ projects }));
  assert.ok(service.list()[0].cursor.seen.length <= 200, 'resolved keys are pruned although nothing new arrived');
  for (let i = 0; i < 400; i++) service.onSnapshot(snap({ projects: [{ id: 'p', runs: [run(`done${i}`, { state: 'finished', status: 'finished' })] }] }));
  assert.ok(service.list()[0].cursor.seen.length <= 201, 'finished runs that left the inventory do not accumulate');
});

// ---- one failure per run ----

const failedRun = id => run(id, { state: 'finished', status: 'failed' });

test('a run failure has one identity: the run outcome first and the failed stage later are one result, also after a restart', async t => {
  const { service, make, results } = await setup(t);
  const a = run('a');
  service.create({});
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }] }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [failedRun('a')] }] }));
  await service.flush();
  const restarted = make();
  await restarted.load();
  // The endpoint recovers and reports the failed stage of the same run.
  restarted.onSnapshot(snap({ projects: [{ id: 'p', runs: [failedRun('a')] }], attention: [item(a, 'build', 'failed')] }));
  assert.deepEqual(results.map(r => r.kind), ['failed']);
});

test('two failed stages of one run in the same poll are one result; the stage stays result metadata', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  service.create({});
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }] }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }], attention: [item(a, 'build', 'failed'), item(a, 'test', 'failed')] }));
  assert.deepEqual(results.map(r => [r.kind, r.stageId]), [['failed', 'build']]);
});

test('a failure already there at the baseline is not reported when the run then ends failed', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a] }], attention: [item(a, 'build', 'failed')] }));
  service.create({});
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [failedRun('a')] }] }));
  assert.deepEqual(results, []);
});

test('the failure guard does not depend on kept results: 201 later results and a restart do not repeat it', async t => {
  const { service, make, results } = await setup(t);
  const a = run('a');
  const other = run('b');
  service.create({});
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a, other] }] }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a, other] }], attention: [item(a, 'build', 'failed')] }));
  const many = Array.from({ length: MAX_RESULTS + 1 }, (_, i) => item(other, `s${i}`));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [a, other] }], attention: [item(a, 'build', 'failed'), ...many] }));
  assert.equal(service.list()[0].results.some(r => r.kind === 'failed'), false, 'the failure result itself was evicted');
  await service.flush();
  const restarted = make();
  await restarted.load();
  restarted.onSnapshot(snap({ projects: [{ id: 'p', runs: [failedRun('a'), other] }], attention: many }));
  assert.equal(results.filter(r => r.kind === 'failed').length, 1);
});

// ---- bounded cursor: visible keys plus a fixed horizon of resolved ones ----

const RESOLVED_HORIZON = 200;
const finishedRows = (from, to) => Array.from({ length: to - from }, (_, i) => run(`t${from + i}`, { state: 'finished', status: 'finished' }));
const seenOf = service => service.list()[0].cursor.seen;

test('650 retained terminal rows: identical polls and a restart report nothing; the cursor follows the inventory', async t => {
  const { service, make, results } = await setup(t);
  const rows = finishedRows(0, 650);
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: rows }] }));
  service.create({});
  for (let i = 0; i < 3; i++) service.onSnapshot(snap({ projects: [{ id: 'p', runs: rows }] }));
  assert.deepEqual(results, []);
  assert.equal(seenOf(service).length, 650, 'keys still visible in the inventory are kept');
  await service.flush();
  const restarted = make();
  await restarted.load();
  restarted.onSnapshot(snap({ projects: [{ id: 'p', runs: rows }] }));
  assert.deepEqual(results, []);
  // 450 rows leave the inventory: at most the horizon of resolved keys stays.
  restarted.onSnapshot(snap({ projects: [{ id: 'p', runs: rows.slice(450) }] }));
  assert.ok(seenOf(restarted).length <= 200 + RESOLVED_HORIZON);
  // Fresh outcomes after the saturation are all reported once.
  const fresh = finishedRows(650, 700);
  restarted.onSnapshot(snap({ projects: [{ id: 'p', runs: [...rows.slice(450), ...fresh] }] }));
  restarted.onSnapshot(snap({ projects: [{ id: 'p', runs: [...rows.slice(450), ...fresh] }] }));
  assert.equal(results.length, 50);
  assert.equal(new Set(results.map(r => r.runKey)).size, 50, 'no duplicates');
  assert.ok(seenOf(restarted).length <= 250 + RESOLVED_HORIZON);
});

test('a key resolved most recently survives older resolutions: a row that disappears and returns within the horizon is not new', async t => {
  const { service, results } = await setup(t);
  const long = finishedRows(0, 1);
  const others = finishedRows(1, 301);
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [...long, ...others] }] }));
  service.create({});
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: long } ] }));
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: [] }] }));
  assert.ok(seenOf(service).length <= RESOLVED_HORIZON);
  service.onSnapshot(snap({ projects: [{ id: 'p', runs: long }] }));
  assert.deepEqual(results, [], 'the row that left last came back within the horizon');
  assert.ok(seenOf(service).length <= 1 + RESOLVED_HORIZON);
});

test('resolved questions follow the same horizon: within it a returning request is not new, beyond it the documented repeat happens', async t => {
  const { service, results } = await setup(t);
  const a = run('a');
  const projects = [{ id: 'p', runs: [a] }];
  service.onSnapshot(snap({ projects }));
  service.create({ events: ['question'] });
  const asks = (from, to) => Array.from({ length: to - from }, (_, i) => item(a, `q${from + i}`));
  service.onSnapshot(snap({ projects, attention: asks(0, 650) }));
  assert.equal(results.length, 650);
  service.onSnapshot(snap({ projects }));
  assert.ok(seenOf(service).length <= RESOLVED_HORIZON);
  service.onSnapshot(snap({ projects, attention: asks(600, 650) }));
  assert.equal(results.length, 650, 'requests resolved last are inside the horizon');
  service.onSnapshot(snap({ projects, attention: asks(650, 660) }));
  assert.equal(results.length, 660, 'new requests after saturation are reported');
  service.onSnapshot(snap({ projects, attention: asks(0, 1) }));
  assert.equal(results.length, 661, 'a request that returns after the horizon can be reported again (documented capacity)');
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { WatchService, normalizeTask } from '../src/watch/index.js';
import { MAX_RESULTS } from '../src/watch/store.js';

const NOW = Date.parse('2026-06-01T08:00:00Z');
async function setup(t, options = {}) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-watch-'));
  const made = [];
  // Queued saves must land before the directory goes away.
  t.after(async () => { await Promise.all(made.map(service => service.flush())); await rm(home, { recursive: true, force: true }); });
  const clock = { now: NOW };
  const make = () => { const service = new WatchService({ home, now: () => clock.now, ...options }); made.push(service); return service; };
  return { home, clock, make, service: make(), file: path.join(home, '.afm', 'desktop', 'watch-tasks.json') };
}

test('a task is validated, persisted apart from the chat and survives a restart', async t => {
  const { service, make, file } = await setup(t);
  const task = service.create({ title: 'Morning', scope: { kind: 'all' }, deadline: '2026-06-01T09:00:00Z', summaryAt: '2026-06-01T08:55:00Z' });
  await service.flush();
  assert.equal(JSON.parse(await readFile(file, 'utf8')).tasks[0].id, task.id);
  const restarted = make();
  await restarted.load();
  assert.deepEqual(restarted.list(), [task]);
  assert.equal(restarted.active, true);
});

test('invalid requests are rejected with a reason', () => {
  const bad = (request, pattern) => assert.throws(() => normalizeTask(request, NOW), pattern);
  bad({ scope: { kind: 'galaxy' } }, /Scope must be/);
  bad({ scope: { kind: 'project' } }, /needs a project/);
  bad({ scope: { kind: 'run', runId: 'r1' } }, /run id and instance id/);
  bad({ events: ['question', 'rm -rf'] }, /Unknown event type/);
  bad({ deadline: 'whenever' }, /must be a date/);
  bad({ deadline: '2026-05-01T00:00:00Z' }, /in the past/);
  bad({ deadline: '2026-06-01T09:00:00Z', summaryAt: '2026-06-01T10:00:00Z' }, /after the deadline/);
  assert.deepEqual(normalizeTask({}, NOW).events, ['question', 'approval', 'decision', 'failed', 'finished']);
});

test('pause, resume and stop change status but keep collected results', async t => {
  const { service } = await setup(t);
  const task = service.create({});
  service.store.get(task.id).results.push({ id: 'x' });
  assert.equal(service.pause(task.id).status, 'paused');
  assert.equal(service.active, false);
  assert.throws(() => service.stop('missing'), /no longer exists/);
  assert.equal(service.resume(task.id).status, 'active');
  assert.equal(service.stop(task.id).status, 'stopped');
  assert.throws(() => service.resume(task.id), /cannot become active/);
  assert.equal(service.list()[0].results.length, 1);
});

test('the active flag is announced so the observation loop can start or stop', async t => {
  const flags = [];
  const { service } = await setup(t, { onActiveChange: active => flags.push(active) });
  const task = service.create({});
  service.pause(task.id);
  service.resume(task.id);
  service.stop(task.id);
  assert.deepEqual(flags, [true, false, true, false]);
});

test('the store is bounded: finished tasks are evicted, running tasks never', async t => {
  const { service } = await setup(t);
  const first = service.create({});
  service.stop(first.id);
  for (let i = 0; i < 49; i++) service.create({});
  assert.equal(service.list().length, 50);
  service.create({});
  assert.equal(service.list().some(task => task.id === first.id), false, 'the stopped task made room');
  assert.throws(() => service.create({}), /At most 50 watch tasks/);
  assert.ok(MAX_RESULTS > 0);
});

test('a failed write is reported, kept and retried on the next poll', async t => {
  const errors = [];
  const { home, make, file } = await setup(t, { onError: error => errors.push(error) });
  const service = make();
  await mkdir(path.dirname(file), { recursive: true });
  await mkdir(file);
  const task = service.create({});
  await assert.rejects(service.flush());
  assert.equal(errors.length, 1);
  assert.equal(service.store.dirty, true);
  await rm(file, { recursive: true });
  service.onSnapshot({ host: { state: 'connected', error: null }, projects: [], unassigned: [], attention: [] });
  await service.flush();
  assert.equal(service.store.dirty, false);
  assert.equal(JSON.parse(await readFile(file, 'utf8')).tasks[0].id, task.id);
  assert.ok(home);
});

test('the window API names tasks by id only, hides the observer cursor, stores before it answers and announces every change', async t => {
  const { registerWatchIpc } = await import('../src/watch/ipc.js');
  const sent = [];
  const handlers = new Map();
  const { service, file } = await setup(t);
  const changes = [];
  service.store.onChange = () => changes.push(1);
  const publish = registerWatchIpc((name, handler) => handlers.set(name, handler), service, tasks => sent.push(tasks));
  const task = service.create({ title: 'Mine' });
  assert.equal('cursor' in handlers.get('watch:list')()[0], false, 'the cursor is private to the observer');
  const paused = await handlers.get('watch:pause')(task.id);
  assert.equal(paused.status, 'paused');
  assert.equal(JSON.parse(await readFile(file, 'utf8')).tasks[0].status, 'paused', 'on disk before the call returned');
  assert.equal((await handlers.get('watch:resume')({ toString: () => task.id })).status, 'active', 'the id is coerced to a string');
  await assert.rejects(handlers.get('watch:stop')('missing'), /no longer exists/);
  service.addReport(task.id, { id: 'p', at: '2026-06-01T08:00:00.000Z', title: 'T', markdown: 'x', source: 'fallback' });
  assert.equal((await handlers.get('watch:read')(task.id)).unread, 0);
  assert.ok(changes.length >= 4, 'every save is announced');
  publish();
  assert.equal(sent.at(-1)[0].reports.length, 1);
  await assert.rejects(handlers.get('watch:remove')(task.id), /Stop the task/);
  await handlers.get('watch:stop')(task.id);
  await handlers.get('watch:remove')(task.id);
  assert.deepEqual(JSON.parse(await readFile(file, 'utf8')).tasks, []);
  publish();
  assert.deepEqual(sent.at(-1), []);
});

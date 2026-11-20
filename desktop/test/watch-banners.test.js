import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Notifier } from '../src/notifier.js';
import { WatchService } from '../src/watch/index.js';
import { SummaryRunner } from '../src/watch/summary.js';
import { createWatchBanners } from '../src/watch/banners.js';

const ALL = { enabled: true, kinds: { question: true, approval: true, decision: true, failed: true } };
const run = (id, extra = {}) => ({ key: `i1:${id}`, runId: id, instanceId: 'i1', name: `flow-${id}`, projectRoot: '/work/app', state: 'running', ...extra });
const item = (r, stageId, kind = 'question', requestAt = `q-${stageId}`) => ({ id: `${r.key}:${stageId}`, runKey: r.key, stageId, stageName: stageId.toUpperCase(), kind, requestAt, text: 'SECRET QUESTION TEXT' });
const snap = (runs, attention = []) => ({ host: { state: 'connected', error: null }, projects: [{ id: 'p', name: 'app', runs }], unassigned: [], attention });

// Desktop's wiring of the observer, the notifier and the watch banners, with Electron replaced by a list.
async function setup(t, { visible = () => false } = {}) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-banners-'));
  const shown = [];
  const made = [];
  t.after(async () => { await Promise.all(made.map(service => service.flush())); await rm(home, { recursive: true, force: true }); });
  const make = () => {
    const banners = createWatchBanners({ show: banner => shown.push(banner), visible });
    const holder = {};
    const service = new WatchService({ home, onResult: banners.result, onSummaryDue: task => holder.summaries.run(task) });
    holder.summaries = new SummaryRunner({ watch: service, ask: async () => { throw Error('offline'); }, onReport: banners.report });
    const notifier = new Notifier({ home, show: banner => shown.push(banner), settings: ALL, visible, claimed: found => service.covers(found.kind, found.runKey) });
    made.push(service);
    const poll = async snapshot => { service.onSnapshot(snapshot); await notifier.onSnapshot(snapshot); await new Promise(resolve => setImmediate(resolve)); };
    return { service, poll };
  };
  return { shown, make, ...make() };
}

test('a hidden-window question and a failure give exactly one banner each, with fixed text and the right click target', async t => {
  const { service, poll, shown } = await setup(t);
  const a = run('a');
  await poll(snap([a]));
  service.create({ title: 'Night' });
  await poll(snap([a]));
  await poll(snap([a], [item(a, 'ask')]));
  await poll(snap([a], [item(a, 'ask')]));
  await poll(snap([run('a', { state: 'finished', status: 'failed' })], [item(a, 'build', 'failed')]));
  assert.deepEqual(shown.map(b => b.title), ['AFM needs your answer', 'An AFM stage failed']);
  assert.deepEqual(shown.map(b => [b.runKey, b.stageId]), [[a.key, 'ask'], [a.key, 'build']], 'a stage banner keeps the run and stage path');
  assert.equal(JSON.stringify(shown).includes('SECRET QUESTION TEXT'), false);
});

test('a finished run banner opens the task result; a paused task leaves the event to the notifier', async t => {
  const { service, poll, shown } = await setup(t);
  const a = run('a');
  await poll(snap([a]));
  const task = service.create({ title: 'Night' });
  await poll(snap([a]));
  await poll(snap([run('a', { state: 'finished', status: 'finished' })]));
  assert.equal(shown.length, 1);
  assert.deepEqual([shown[0].title, shown[0].runKey, shown[0].watchTaskId], ['An AFM run finished', undefined, task.id]);
  assert.match(shown[0].body, /flow-a\nThe run finished\./);
  service.pause(task.id);
  const b = run('b');
  await poll(snap([b]));
  await poll(snap([b], [item(b, 'ask')]));
  assert.deepEqual(shown.slice(1).map(x => x.title), ['AFM needs your answer'], 'one banner, from the notifier');
});

test('a run the user is looking at, and a burst, do not flood the desktop; restart replays nothing', async t => {
  const { service, poll, shown, make } = await setup(t, { visible: found => found.runKey === 'i1:seen' });
  const seen = run('seen');
  const many = Array.from({ length: 9 }, (_, i) => run('r' + i));
  await poll(snap([seen, ...many]));
  service.create({ title: 'All' });
  await poll(snap([seen, ...many]));
  await poll(snap([seen, ...many], [item(seen, 'ask')]));
  assert.equal(shown.length, 0, 'the focused run needs no banner');
  await poll(snap([seen, ...many], many.map(r => item(r, 'ask'))));
  assert.equal(shown.length, 6, 'five banners and one overflow line');
  assert.match(shown.at(-1).body, /4 more results/);
  await service.flush();
  const again = make();
  await again.service.load();
  const before = shown.length;
  await again.poll(snap([seen, ...many], many.map(r => item(r, 'ask'))));
  assert.equal(shown.length, before, 'the same pending requests are not announced after a restart');
});

test('a written summary raises one banner that opens the report', async t => {
  const { service, poll, shown } = await setup(t);
  await poll(snap([run('a')]));
  const task = service.create({ title: 'Morning', summaryAt: new Date(Date.now() + 1000).toISOString() });
  await poll(snap([run('a')]));
  const stored = service.store.get(task.id);
  stored.summaryAt = new Date(Date.now() - 1000).toISOString();
  await poll(snap([run('a')]));
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(shown.length, 1);
  assert.deepEqual([shown[0].title, shown[0].body, shown[0].watchTaskId, shown[0].itemId], ['AFM summary ready', 'Morning', task.id, service.list()[0].reports[0].id]);
});

test('overlapping watches announce one event once, with distinct events counted for the overflow', async t => {
  const { service, poll, shown } = await setup(t);
  const a = run('a');
  await poll(snap([a]));
  service.create({ title: 'All' });
  service.create({ title: 'Project', scope: { kind: 'project', projectId: 'p' } });
  service.create({ title: 'Exact', scope: { kind: 'run', runId: 'a', instanceId: 'i1' } });
  await poll(snap([a]));
  await poll(snap([a], [item(a, 'ask')]));
  assert.equal(shown.length, 1);
});

test('events a watch does not announce (resume, reconnect after an outage) stay with the notifier', async t => {
  const { service, poll, shown } = await setup(t);
  const a = run('a');
  const b = run('b');
  await poll(snap([a, b]));
  const task = service.create({ title: 'Night' });
  await poll(snap([a, b]));
  service.pause(task.id);
  service.resume(task.id);
  await poll(snap([a, b], [item(a, 'ask')]));
  assert.deepEqual(shown.map(x => x.title), ['AFM needs your answer'], 'the resume baseline is silent, the notifier speaks');
  await poll({ ...snap([a, b]), host: { state: 'connected', error: 'down' } });
  await poll(snap([a, b], [item(a, 'ask'), item(b, 'ask')]));
  assert.equal(shown.length, 2, 'the question raised during the outage is announced once');
});

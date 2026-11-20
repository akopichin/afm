import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Notifier } from '../src/notifier.js';
import { Prefs } from '../src/prefs.js';

const ALL = { enabled: true, kinds: { question: true, approval: true, decision: true, failed: false } };
const run = { key: 'i1:r1', runId: 'r1', name: 'dev-flow', projectRoot: '/work/app' };
const item = (stageId, kind = 'question', requestAt = '2026-01-01T00:00:00Z', runKey = run.key) =>
  ({ id: `${runKey}:${stageId}`, runKey, stageId, stageName: stageId.toUpperCase(), kind, requestAt, text: 'Answer question' });
const snapshot = (attention, host = { state: 'connected', error: null }) =>
  ({ host, projects: [{ id: 'p', name: 'app', runs: [run] }], unassigned: [], attention });

async function setup(t, options = {}) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-notifier-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const shown = [];
  const make = () => new Notifier({ home, show: banner => shown.push(banner), settings: ALL, ...options });
  return { home, shown, make, notifier: make() };
}

test('a request is announced once, with fixed text that never carries the question', async t => {
  const { notifier, shown } = await setup(t);
  await notifier.onSnapshot(snapshot([]));
  await notifier.onSnapshot(snapshot([item('ask')]));
  await notifier.onSnapshot(snapshot([item('ask')]));
  assert.deepEqual(shown, [{ title: 'AFM needs your answer', body: 'app / dev-flow\nStage ASK is waiting for an answer.', runKey: run.key, stageId: 'ask' }]);
});

test('requests pending at launch only seed the queue; a new request in the same stage is announced', async t => {
  const { notifier, shown } = await setup(t);
  await notifier.onSnapshot(snapshot([item('ask')]));
  assert.equal(shown.length, 0);
  await notifier.onSnapshot(snapshot([item('ask', 'question', '2026-01-02T00:00:00Z')]));
  assert.equal(shown.length, 1, 'a second question in the same stage is its own banner');
  await notifier.onSnapshot(snapshot([]));
  await notifier.onSnapshot(snapshot([item('ask', 'question', '2026-01-02T00:00:00Z')]));
  assert.equal(shown.length, 1, 'the same request returning is not new');
});

test('reconnects and restarts do not repeat a banner', async t => {
  const { notifier, shown, make } = await setup(t);
  await notifier.onSnapshot(snapshot([]));
  await notifier.onSnapshot(snapshot([item('ask', 'approval')]));
  await notifier.onSnapshot(snapshot([], { state: 'offline', error: { message: 'down' } }));
  await notifier.onSnapshot(snapshot([item('ask', 'approval')]));
  assert.equal(shown.length, 1);
  const restarted = make();
  await restarted.onSnapshot(snapshot([item('ask', 'approval')]));
  await restarted.onSnapshot(snapshot([item('ask', 'approval'), item('next', 'decision')]));
  assert.deepEqual(shown.map(b => b.stageId), ['ask', 'next']);
});

test('an unreachable host neither announces nor seeds', async t => {
  const { notifier, shown } = await setup(t);
  await notifier.onSnapshot(snapshot([item('ask')], { state: 'offline', error: { message: 'down' } }));
  await notifier.onSnapshot(snapshot([item('ask')]));
  assert.equal(shown.length, 0, 'the first real snapshot is the seed');
});

test('disabled notifications and switched-off categories stay quiet but are remembered', async t => {
  const { notifier, shown } = await setup(t);
  await notifier.onSnapshot(snapshot([]));
  await notifier.onSnapshot(snapshot([item('broken', 'failed')]));
  notifier.settings = { ...ALL, enabled: false };
  await notifier.onSnapshot(snapshot([item('broken', 'failed'), item('ask')]));
  notifier.settings = ALL;
  await notifier.onSnapshot(snapshot([item('broken', 'failed'), item('ask')]));
  assert.equal(shown.length, 0, 'turning banners on later does not flush old requests');
  notifier.settings = { enabled: true, kinds: { ...ALL.kinds, failed: true } };
  await notifier.onSnapshot(snapshot([item('crash', 'failed')]));
  assert.equal(shown[0].title, 'An AFM stage failed');
});

test('the run page already on screen gets no banner; another run does', async t => {
  const { notifier, shown } = await setup(t, { visible: found => found.runKey === run.key });
  await notifier.onSnapshot(snapshot([]));
  await notifier.onSnapshot(snapshot([item('here'), item('there', 'approval', 'x', 'i2:r2')]));
  assert.deepEqual(shown.map(b => b.runKey), ['i2:r2']);
});

test('the Dock dot follows the pending queue and a new batch requests one bounce', async t => {
  const dock = [];
  const { notifier } = await setup(t, { dock: {
    setPending: pending => dock.push(pending ? 'dot' : 'clear'),
    bounce: () => dock.push('bounce'),
  }});
  const pending = item('pending');
  const approval = item('approve', 'approval');
  const decision = item('decide', 'decision');
  await notifier.onSnapshot(snapshot([pending]));
  await notifier.onSnapshot(snapshot([pending, approval, decision]));
  await notifier.onSnapshot(snapshot([], { state: 'offline', error: { message: 'down' } }));
  await notifier.onSnapshot(snapshot([approval, decision]));
  await notifier.onSnapshot(snapshot([decision]));
  assert.deepEqual(dock, ['dot', 'bounce']);
  await notifier.onSnapshot(snapshot([]));
  assert.deepEqual(dock, ['dot', 'bounce', 'clear']);
});

test('the Dock respects request preferences and ignores failures as attention requests', async t => {
  const dock = [];
  const { notifier } = await setup(t, { dock: {
    setPending: pending => dock.push(pending ? 'dot' : 'clear'),
    bounce: () => dock.push('bounce'),
  }});
  const failed = item('failed', 'failed');
  const question = item('ask', 'question', null);
  const attention = [failed, question];
  const settings = { enabled: true, kinds: { ...ALL.kinds, failed: true } };
  notifier.setSettings(settings);
  await notifier.onSnapshot(snapshot([failed]));
  await notifier.onSnapshot(snapshot(attention));
  notifier.setSettings({ ...settings, kinds: { ...settings.kinds, question: false } }, attention);
  notifier.setSettings(settings, attention);
  notifier.setSettings({ ...settings, enabled: false }, attention);
  assert.deepEqual(dock, ['dot', 'clear', 'dot', 'clear']);
});

test('a burst is capped with one summary, and pending requests survive the history limit across a reconnect', async t => {
  const { notifier, shown } = await setup(t);
  const burst = Array.from({ length: 600 }, (_, i) => item('s' + i));
  await notifier.onSnapshot(snapshot([]));
  await notifier.onSnapshot(snapshot(burst));
  assert.equal(shown.length, 6);
  assert.equal(shown.at(-1).body, '595 more requests are waiting.');
  await notifier.onSnapshot(snapshot([], { state: 'offline', error: { message: 'down' } }));
  await notifier.onSnapshot(snapshot(burst));
  assert.equal(shown.length, 6, 'no pending request came back as new');
  await notifier.onSnapshot(snapshot([]));
  await notifier.onSnapshot(snapshot([item('later')]));
  assert.ok(notifier.seen.size <= 500, 'resolved history is bounded');
});

test('notification preferences default to questions, approvals and decisions and reject malformed values', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-prefs-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const prefs = new Prefs(home);
  assert.deepEqual((await prefs.get()).notifications, ALL);
  const off = { enabled: false, kinds: { ...ALL.kinds, failed: true } };
  assert.deepEqual((await prefs.set({ notifications: off })).notifications, off);
  const wide = { enabled: true, kinds: { ...ALL.kinds, extra: true } };
  assert.deepEqual((await prefs.set({ notifications: wide })).notifications, ALL, 'unknown switches are not stored');
  for (const bad of [true, { enabled: 'yes', kinds: ALL.kinds }, { enabled: true, kinds: { question: true } }])
    await assert.rejects(prefs.set({ notifications: bad }), /Invalid preference/);
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { RepeatingTasks } from '../src/watch/repeating.js';
import { taskView, WatchService } from '../src/watch/index.js';
import { sendTelegram } from '../src/watch/telegram.js';
import { eventually, tempHome } from './support.js';

const MINUTE = 60000;
const NOW = Date.parse('2026-11-21T10:00:00Z');
const settings = { provider: 'claude', model: 'haiku', reasoning: 'default', permission: 'read' };
const input = { prompt: 'Send the latest progress', title: 'Progress', everyMinutes: 10, delivery: 'telegram' };
const config = { scope: { kind: 'all' }, providerName: 'Claude Code', settings, chatId: '431' };

test('one repeating card claims each slot before execution, skips busy slots and keeps results after cancellation', async t => {
  const { home } = await tempHome(t);
  let now = NOW;
  const calls = [];
  const delivered = [];
  const service = new WatchService({ home, now: () => now });
  const { promise, resolve } = Promise.withResolvers();
  const tasks = new RepeatingTasks({ store: service.store, now: () => now,
    execute: async task => {
      const file = JSON.parse(await readFile(service.store.file, 'utf8'));
      assert.equal(file.tasks[0].inFlightAt, task.inFlightAt);
      calls.push(task.settings.model);
      return promise;
    },
    deliver: async (task, text) => delivered.push([task.chatId, text]) });
  t.after(() => tasks.close());
  const task = await tasks.create(input, config);
  assert.equal(taskView(task).chatId, undefined);
  assert.equal(task.nextAt, new Date(NOW + 10 * MINUTE).toISOString());

  now += 10 * MINUTE;
  await tasks.tick();
  await eventually(() => calls.length === 1, { what: 'the first scheduled request' });
  now += 10 * MINUTE;
  await tasks.tick();
  assert.deepEqual(calls, ['haiku']);
  resolve('Current report');
  await eventually(() => service.list()[0].reports.length, { what: 'stored report' });
  assert.deepEqual(delivered, [['431', 'Current report']]);
  assert.equal(service.list()[0].reports[0].deliveryStatus, 'sent');

  await tasks.setModel(task.id, { ...settings, provider: 'codex', model: 'gpt-model', permission: 'yolo' }, 'Codex');
  assert.equal(service.list()[0].settings.permission, 'read');
  service.stop(task.id);
  now += 10 * MINUTE;
  await tasks.tick();
  assert.deepEqual(calls, ['haiku']);
  assert.equal(service.list()[0].reports.length, 1);
});

test('a restart skips missed slots and marks an interrupted run without repeating it', async t => {
  const { home } = await tempHome(t);
  let now = NOW;
  const previous = new WatchService({ home, now: () => now });
  const scheduler = new RepeatingTasks({ store: previous.store, now: () => now });
  const { id } = await scheduler.create(input, config);
  await scheduler.close();
  previous.store.get(id).inFlightAt = new Date(NOW + 10 * MINUTE).toISOString();
  await previous.store.save();

  now += 25 * MINUTE;
  const service = new WatchService({ home, now: () => now });
  await service.load();
  const tasks = new RepeatingTasks({ store: service.store, now: () => now, execute: async () => { throw Error('Unexpected replay'); } });
  t.after(() => tasks.close());
  await tasks.load();
  await tasks.tick();
  const [task] = service.list();
  assert.equal(task.nextAt, new Date(NOW + 30 * MINUTE).toISOString());
  assert.equal(task.reports[0].status, 'interrupted');
  assert.equal(task.reports.length, 1);
});

test('Telegram posts to the bound chat and keeps its token out of errors', async () => {
  const config = { token: '123:example-secret', chatId: '431' };
  await sendTelegram(config, 'Report', async (url, options) => {
    assert.equal(new URL(url).origin, 'https://api.telegram.org');
    assert.deepEqual(JSON.parse(options.body), { chat_id: '431', text: 'Report' });
    assert.equal(options.redirect, 'error');
    return new Response(JSON.stringify({ ok: true }));
  });
  await assert.rejects(sendTelegram(config, 'Report', async url => { throw Error(url); }),
    { message: 'Telegram delivery could not be confirmed.' });
});

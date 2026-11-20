import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { buildInvocation } from '../src/assistant/modes.js';
import { WatchService } from '../src/watch/index.js';
import { fallbackReport, SummaryRunner } from '../src/watch/summary.js';
import { makeAssistant, providerHome, settled, stubContext } from './assistant-support.js';
import { eventually } from './support.js';

const NOW = Date.parse('2026-06-01T08:00:00Z');
// Project replies run in the project's folder, so it has to exist.
const makeContext = async home => {
  const folder = path.join(home, 'alpha');
  await mkdir(folder);
  return stubContext({
    runs: async () => [{ run_id: 'r1', instance_id: 'i1', run_name: 'build', project_root: folder }],
    project: async id => { if (id === 'alpha') return { id: 'alpha', path: folder, name: 'Alpha' }; throw Error('unknown'); } });
};

// A fake provider that calls the real tool bridge, and the real watch service behind it; nothing reaches a host or a paid provider.
async function world(t, vars = {}) {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'bridge', ...vars });
  const clock = { now: NOW };
  const service = new WatchService({ home: ctx.home, now: () => clock.now });
  t.after(() => service.flush());
  const { assistant, events } = makeAssistant(t, ctx, { context: await makeContext(ctx.home), watch: service.tools() });
  await assistant.configure({ provider: 'claude' });
  const ask = async (calls, scope = { kind: 'all' }) => {
    ctx.env.FAKE_CALLS = JSON.stringify(calls);
    events.length = 0;
    await assistant.send({ scope, text: 'go' });
    await settled(events);
    const text = events.filter(e => e.type === 'delta').map(e => e.text).join('');
    return { text, tools: text.match(/^tools: (.*)$/m)?.[1].split(',').filter(Boolean) ?? [],
      calls: [...text.matchAll(/^(desktop_watch_\w+):(ok|error):(.*)$/gm)].map(m => ({ name: m[1], status: m[2], body: m[3] })), events: [...events] };
  };
  const file = path.join(ctx.home, '.afm', 'desktop', 'watch-tasks.json');
  return { ctx, clock, service, assistant, events, ask, file };
}

test('a chat request creates a persisted watch and a card from Desktop with the right scope and times', async t => {
  const w = await world(t);
  const deadline = '2026-06-02T09:00:00.000Z';
  const summaryAt = '2026-06-02T08:30:00.000Z';
  const result = await w.ask([{ name: 'desktop_watch_create', args: { title: 'Overnight', events: ['question', 'failed'], deadline, summaryAt } }]);
  assert.deepEqual(result.tools, ['desktop_watch_create', 'desktop_watch_list', 'desktop_watch_pause', 'desktop_watch_resume', 'desktop_watch_stop']);
  assert.equal(result.calls[0].status, 'ok');
  const [task] = w.service.list();
  assert.deepEqual([task.title, task.scope.kind, task.events, task.deadline, task.summaryAt, task.status], ['Overnight', 'all', ['question', 'failed'], deadline, summaryAt, 'active']);
  const stored = JSON.parse(await readFile(w.file, 'utf8')).tasks;
  assert.equal(stored[0].id, task.id, 'it is on disk before the model is told');
  const card = result.events.find(event => event.type === 'watch_card');
  assert.deepEqual(card.card, { taskId: task.id, title: 'Overnight', scope: 'All projects', events: ['question', 'failed'], deadline, summaryAt });
  assert.equal(card.conversationId, 'main');
});

test('a request outside the conversation scope or with bad times is rejected, in the feed and to the model', async t => {
  const w = await world(t);
  const scoped = { projectId: 'alpha' };
  const refused = await w.ask([
    { name: 'desktop_watch_create', args: { scope: { all: true } } },
    { name: 'desktop_watch_create', args: { scope: { projectId: 'beta' } } },
    { name: 'desktop_watch_create', args: { scope: { runId: 'other', instanceId: 'i9' } } },
    { name: 'desktop_watch_create', args: { deadline: '2026-05-01T00:00:00Z' } },
    { name: 'desktop_watch_create', args: { events: ['bogus'] } },
    { name: 'desktop_watch_create', args: { deadline: '2026-06-02T09:00:00Z', summaryAt: '2026-06-02T10:00:00Z' } },
  ], scoped);
  assert.deepEqual(refused.calls.map(call => call.status), Array(6).fill('error'));
  assert.match(refused.calls[0].body, /outside this conversation's scope \(Alpha\)/);
  assert.match(refused.calls[1].body, /not registered/);
  assert.match(refused.calls[2].body, /no longer known/);
  assert.match(refused.calls[3].body, /already in the past/);
  assert.match(refused.calls[4].body, /Unknown event type/);
  assert.match(refused.calls[5].body, /after the deadline/);
  assert.equal(w.service.list().length, 0, 'nothing was created');
  assert.equal(refused.events.filter(event => event.type === 'tool_result' && event.error).length, 6, 'the feed shows each refusal');

  const own = await w.ask([{ name: 'desktop_watch_create', args: { title: 'Alpha watch' } },
    { name: 'desktop_watch_create', args: { scope: { runId: 'r1', instanceId: 'i1' } } }], scoped);
  assert.deepEqual(own.calls.map(call => call.status), ['ok', 'ok']);
  assert.deepEqual(w.service.list().map(task => [task.scope.kind, task.scope.projectId, task.scope.runId]), [['project', 'alpha', null], ['run', null, 'r1']]);
});

test('a conversation about one run can only watch that run', async t => {
  const w = await world(t);
  const scope = { runId: 'r1', instanceId: 'i1' };
  const result = await w.ask([{ name: 'desktop_watch_create', args: { scope: { projectId: 'alpha' } } }, { name: 'desktop_watch_create', args: {} }], scope);
  assert.deepEqual(result.calls.map(call => call.status), ['error', 'ok']);
  assert.deepEqual(w.service.list().map(task => [task.scope.kind, task.scope.runId, task.scope.instanceId]), [['run', 'r1', 'i1']]);
});

test('list, pause, resume and stop work from chat and keep the results', async t => {
  const w = await world(t);
  await w.ask([{ name: 'desktop_watch_create', args: { title: 'One' } }]);
  const [task] = w.service.list();
  w.service.store.get(task.id).results.push({ id: 'x', key: 'k', kind: 'failed' });
  const status = async name => (await w.ask([{ name, args: { id: task.id } }])).calls[0];
  assert.equal((await status('desktop_watch_pause')).status, 'ok');
  assert.equal(w.service.list()[0].status, 'paused');
  const listed = await w.ask([{ name: 'desktop_watch_list', args: {} }]);
  assert.equal(JSON.parse(listed.calls[0].body).tasks[0].status, 'paused');
  await status('desktop_watch_resume');
  assert.equal(w.service.list()[0].status, 'active');
  await status('desktop_watch_stop');
  assert.equal(w.service.list()[0].status, 'stopped');
  assert.equal(w.service.list()[0].results.length, 1, 'results survive');
  const missing = await w.ask([{ name: 'desktop_watch_pause', args: { id: 'nope' } }]);
  assert.match(missing.calls[0].body, /no longer exists/);
});

test('/clear and a new conversation leave watches and results alone', async t => {
  const w = await world(t);
  await w.ask([{ name: 'desktop_watch_create', args: { title: 'Keep me' } }]);
  w.service.store.get(w.service.list()[0].id).results.push({ id: 'x', key: 'k', kind: 'finished' });
  w.service.store.save();
  await w.assistant.clear('main');
  assert.deepEqual(await w.assistant.history('main'), []);
  assert.equal(w.service.list().length, 1);
  await w.service.flush();
  assert.equal(JSON.parse(await readFile(w.file, 'utf8')).tasks[0].results.length, 1);
});

test('list and management only reach watches inside the conversation scope', async t => {
  const w = await world(t);
  const make = scope => w.service.create({ title: JSON.stringify(scope), scope });
  const everything = make({ kind: 'all' });
  const alpha = make({ kind: 'project', projectId: 'alpha' });
  const beta = make({ kind: 'project', projectId: 'beta' });
  const mine = make({ kind: 'run', runId: 'r1', instanceId: 'i1' });
  const other = make({ kind: 'run', runId: 'r9', instanceId: 'i1' });
  const titles = async scope => JSON.parse((await w.ask([{ name: 'desktop_watch_list', args: {} }], scope)).calls[0].body).tasks.map(task => task.id).sort();
  assert.deepEqual(await titles({ kind: 'all' }), [alpha, beta, everything, mine, other].map(task => task.id).sort());
  assert.deepEqual(await titles({ projectId: 'alpha' }), [alpha, mine].map(task => task.id).sort(), 'a project sees its own project and its runs');
  assert.deepEqual(await titles({ runId: 'r1', instanceId: 'i1' }), [mine.id]);
  for (const task of [everything, beta, other]) {
    const refused = await w.ask([{ name: 'desktop_watch_stop', args: { id: task.id } }], { projectId: 'alpha' });
    assert.equal(refused.calls[0].status, 'error');
    assert.match(refused.calls[0].body, /no longer exists/);
  }
  assert.deepEqual(w.service.list().map(task => task.status), Array(5).fill('active'), 'nothing outside the scope was touched');
  assert.equal((await w.ask([{ name: 'desktop_watch_stop', args: { id: mine.id } }], { projectId: 'alpha' })).calls[0].status, 'ok');
});

test('a request that is stopped while its scope is being looked up changes nothing', async t => {
  const { runWatchTool } = await import('../src/assistant/watch-tools.js');
  const calls = [];
  const watch = { list: async () => [], create: async request => { calls.push(request); return { id: 'x', ...request, results: [] }; } };
  const run = authorized => runWatchTool({ watch, tool: 'desktop_watch_create', args: { scope: { runId: 'r1', instanceId: 'i1' } }, turnScope: { kind: 'all' },
    resolveScope: async input => ({ kind: 'run', ...input, projectRoot: '/p', name: 'build' }), belongsTo: () => true, scopeName: () => 'All', authorized });
  await assert.rejects(run(() => false), /was stopped/);
  assert.equal(calls.length, 0);
  await run(() => true);
  assert.equal(calls.length, 1);
});

test('a title never carries a credential or session id into the card or the store', async t => {
  const w = await world(t, { FAKE_SECRET: 'sk-title-secret-0001' });
  await w.ask([{ name: 'desktop_watch_create', args: { title: 'Watch sk-title-secret-0001' } }]);
  assert.equal(JSON.stringify(w.service.list()).includes('sk-title-secret-0001'), false);
});

test('reports do not use up the chat capacity, and are written without the provider tools', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'hang' });
  const { assistant, events } = makeAssistant(t, ctx, { context: await makeContext(ctx.home), maxActive: 1 });
  await assistant.configure({ provider: 'claude' });
  const pending = assistant.summarize('t-3', 'report').catch(() => 'cancelled');
  await eventually(async () => (await ctx.invocations()).length === 1, { what: 'the report provider started' });
  ctx.env.FAKE_SCENARIO = 'stream';
  await assistant.send({ scope: { kind: 'all' }, text: 'chat still works' });
  await eventually(() => events.find(event => event.conversationId === 'main' && event.type === 'done'), { what: 'the chat reply' });
  assistant.cancel('watch:t-3');
  assert.equal(await pending, 'cancelled');
  const reportCall = (await ctx.invocations())[0];
  assert.equal(reportCall.args.includes('--strict-mcp-config'), true, 'the reply cannot reach MCP servers the user configured');
  assert.equal(reportCall.args.includes('--mcp-config'), false);
  const at = reportCall.args.indexOf('--tools');
  assert.equal(at >= 0 && reportCall.args[at + 1] === '', true, 'native tools are switched off too');
});

test('without the host the watch tools still work; without a watch service they are not offered', async t => {
  const w = await world(t);
  const hostless = await w.ask([{ name: 'desktop_watch_list', args: {} }]);
  assert.equal(hostless.calls[0].status, 'ok');
  assert.match(hostless.events.find(event => event.type === 'notice').text, /run tools are off/);
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'bridge', FAKE_CALLS: '[]' });
  const plain = makeAssistant(t, ctx);
  await plain.assistant.configure({ provider: 'claude' });
  await plain.assistant.send({ scope: { kind: 'all' }, text: 'go' });
  await settled(plain.events);
  assert.equal(plain.events.some(event => event.type === 'notice' && /tools are off/.test(event.text)), true, 'no host and no watch: no bridge at all');
});

test('a report is written by a read-only reply in its own conversation, never touching the main chat', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'stream' });
  const { assistant, events } = makeAssistant(t, ctx, { context: await makeContext(ctx.home), watch: new WatchService({ home: ctx.home }).tools() });
  await assistant.configure({ provider: 'claude', permission: 'yolo' });
  const main = await assistant.history('main');
  const text = await assistant.summarize('t-1', 'Write the report');
  assert.equal(text, 'Hello world');
  assert.deepEqual(await assistant.history('main'), main);
  assert.deepEqual(await assistant.history('watch:t-1'), [], 'its transcript is dropped');
  assert.equal(events.some(event => event.conversationId === 'main'), false);
  const [call] = await ctx.invocations();
  assert.equal(call.args.includes('--mcp-config'), false, 'no tools are offered to the report');
  assert.equal(assistant.active.size, 0);
});

test('a report keeps working while the main chat is busy', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'hang' });
  const { assistant, events } = makeAssistant(t, ctx, { context: await makeContext(ctx.home) });
  await assistant.configure({ provider: 'claude' });
  await assistant.send({ scope: { kind: 'all' }, text: 'stay busy' });
  await eventually(() => events.some(event => event.type === 'start'), { what: 'main reply started' });
  ctx.env.FAKE_SCENARIO = 'stream';
  await assert.rejects(assistant.send({ scope: { kind: 'all' }, text: 'again' }), /still answering/);
  assert.equal(await assistant.summarize('t-2', 'report').catch(() => 'unavailable'), 'Hello world');
  assert.equal(assistant.active.has('main'), true, 'the main reply is untouched');
});

// ---- the scheduled report ----

const row = (runId, extra = {}) => ({ key: `i1:${runId}`, runId, instanceId: 'i1', name: `flow-${runId}`, state: 'running', ...extra });
const snapshot = (attention = []) => ({ host: { state: 'connected', error: null }, projects: [{ id: 'p', runs: [row('a')] }], unassigned: [], attention });

async function scheduled(t, ask) {
  const ctx = await providerHome(t);
  const clock = { now: NOW };
  const reports = [];
  const holder = {};
  const service = new WatchService({ home: ctx.home, now: () => clock.now, onSummaryDue: task => holder.runner.run(task).then(report => reports.push(report)) });
  holder.runner = new SummaryRunner({ watch: service, ask, now: () => clock.now });
  t.after(() => service.flush());
  return { service, clock, reports, ctx, holder };
}

test('a summary is produced offline from the collected events, and from a model when one answers', async t => {
  const asked = [];
  const w = await scheduled(t, async (id, prompt) => { asked.push(prompt); throw Error('no provider'); });
  w.service.onSnapshot(snapshot());
  w.service.create({ title: 'Morning', summaryAt: '2026-06-01T09:00:00Z' });
  w.service.onSnapshot(snapshot());
  w.service.onSnapshot(snapshot([{ id: 'x', runKey: 'i1:a', stageId: 'ask', stageName: 'ASK', kind: 'question', requestAt: 'q1' }]));
  w.clock.now = Date.parse('2026-06-01T09:00:01Z');
  w.service.onSnapshot(snapshot());
  await eventually(() => w.reports.length, { what: 'the report' });
  const [report] = w.reports;
  assert.equal(report.source, 'fallback');
  assert.match(report.markdown, /# Morning[\s\S]*1 question[\s\S]*"flow-a" asked a question \(stage "ASK"\)/);
  assert.equal(asked.length, 1);
  const [task] = w.service.list();
  assert.deepEqual([task.summaryDone, task.summaryDue, task.reports.length, task.unread], [true, false, 1, 2], 'the report is unread like a result');
  w.service.onSnapshot(snapshot());
  assert.equal(asked.length, 1, 'asked once');

  const m = await scheduled(t, async () => '## All quiet\n\nNothing needed you.');
  m.service.onSnapshot(snapshot());
  m.service.create({ title: 'Quiet', summaryAt: '2026-06-01T09:00:00Z' });
  m.clock.now = Date.parse('2026-06-01T09:00:01Z');
  m.service.onSnapshot(snapshot());
  await eventually(() => m.reports.length, { what: 'the model report' });
  assert.deepEqual([m.reports[0].source, m.reports[0].markdown], ['model', '## All quiet\n\nNothing needed you.']);
});

test('a summary that was due when Desktop quit is written after the restart, once', async t => {
  const calls = [];
  const w = await scheduled(t, async () => { calls.push(1); return 'Report'; });
  w.service.onSnapshot(snapshot());
  const task = w.service.create({ title: 'Late', summaryAt: '2026-06-01T09:00:00Z' });
  w.service.store.get(task.id).summaryDue = true;
  w.service.store.save();
  await w.service.flush();
  const again = new WatchService({ home: w.ctx.home, now: () => w.clock.now, onSummaryDue: item => w.holder.runner.run(item) });
  await again.load();
  await eventually(() => calls.length, { what: 'the report after restart' });
  assert.equal(calls.length, 1);
  assert.equal(fallbackReport({ title: 'T', results: [] }).includes('Nothing needed attention'), true);
});

test('a Codex report is refused so the plain report stands in, while a normal Codex reply is unchanged', () => {
  const provider = { kind: 'codex', name: 'Codex', permissions: ['ask', 'read', 'edit', 'yolo'], reasoning: [], capabilities: {} };
  const settings = { permission: 'read', model: '', reasoning: 'default' };
  assert.throws(() => buildInvocation({ provider, settings, bridge: null, isolated: true }), /cannot disable its tools for a report/);
  assert.deepEqual(buildInvocation({ provider, settings, bridge: null }).args, ['exec', '--json', '--sandbox', 'read-only', '-']);
});

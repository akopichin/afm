import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { buildInvocation } from '../src/assistant/modes.js';
import { taskView, WatchService } from '../src/watch/index.js';
import { fallbackReport, modelReport, reportPrompt, SummaryRunner } from '../src/watch/summary.js';
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
  let service;
  // Cleanup runs in the order it was added, so the last write finishes before the home below is removed.
  t.after(() => service?.flush());
  const ctx = await providerHome(t);
  const clock = { now: NOW };
  const reports = [];
  const banners = [];
  const holder = {};
  service = new WatchService({ home: ctx.home, now: () => clock.now, onSummaryDue: task => holder.runner.run(task).then(report => reports.push(report)) });
  holder.runner = new SummaryRunner({ watch: service, ask, now: () => clock.now, onReport: (stored, report) => banners.push(report) });
  return { service, clock, reports, banners, ctx, holder };
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
  assert.equal(fallbackReport({ title: 'T', results: [] }).markdown.includes('Nothing needed attention'), true);
});

test('a Codex report is refused so the plain report stands in, while a normal Codex reply is unchanged', () => {
  const provider = { kind: 'codex', name: 'Codex', permissions: ['ask', 'read', 'edit', 'yolo'], reasoning: [], capabilities: {} };
  const settings = { permission: 'read', model: '', reasoning: 'default' };
  assert.throws(() => buildInvocation({ provider, settings, bridge: null, isolated: true }), /cannot disable its tools for a report/);
  assert.deepEqual(buildInvocation({ provider, settings, bridge: null }).args, ['exec', '--json', '--sandbox', 'read-only', '-']);
});

// Re-creates the service on the same home, as a restart does.
const restarted = (w, runner) => new WatchService({ home: w.ctx.home, now: () => w.clock.now, onSummaryDue: item => runner.run(item) });

test('quitting while the provider is replying gives one plain report after the restart, with no second provider call', async t => {
  const calls = [];
  const w = await scheduled(t, () => { calls.push(1); return new Promise(() => {}); });
  w.service.onSnapshot(snapshot());
  const task = w.service.create({ title: 'Night', summaryAt: '2026-06-01T09:00:00Z' });
  w.service.onSnapshot(snapshot([{ id: 'x', runKey: 'i1:a', stageId: 'ask', stageName: 'ASK', kind: 'question', requestAt: 'q1' }]));
  w.clock.now = Date.parse('2026-06-01T09:00:01Z');
  w.service.onSnapshot(snapshot());
  await eventually(() => calls.length, { what: 'the provider call' });
  await w.service.flush();
  const planned = w.service.store.get(task.id).summaryId;
  assert.ok(planned);

  const reports = [];
  const holder = {};
  const again = restarted(w, { run: item => holder.runner.run(item).then(report => reports.push(report)) });
  holder.runner = new SummaryRunner({ watch: again, ask: async () => { calls.push(2); return 'second'; }, now: () => w.clock.now });
  await again.load();
  await eventually(() => reports.length, { what: 'the report after restart' });
  assert.deepEqual([reports[0].source, reports[0].id, calls], ['fallback', planned, [1]]);
  const [saved] = again.list();
  assert.deepEqual([saved.reports.length, saved.unread, saved.summaryDone, saved.summaryDue], [1, 2, true, false]);
  await again.load();
  assert.equal(again.list()[0].reports.length, 1, 'nothing is due after the report');
});

test('quitting before the provider starts still asks the provider once; quitting after the report writes nothing', async t => {
  const w = await scheduled(t, async () => 'unused');
  w.service.onSnapshot(snapshot());
  const task = w.service.create({ title: 'Early', summaryAt: '2026-06-01T09:00:00Z' });
  const stored = w.service.store.get(task.id);
  stored.summaryDue = true;
  w.service.store.save();
  await w.service.flush();
  const calls = [];
  const reports = [];
  const holder = {};
  const again = restarted(w, { run: item => holder.runner.run(item).then(report => reports.push(report)) });
  holder.runner = new SummaryRunner({ watch: again, ask: async () => { calls.push(1); return 'From the model'; }, now: () => w.clock.now });
  await again.load();
  await eventually(() => reports.length, { what: 'the report' });
  assert.deepEqual([reports[0].source, calls.length], ['model', 1]);
  await again.flush();
  const third = restarted(w, { run: () => assert.fail('nothing is due') });
  await third.load();
  assert.equal(third.list()[0].reports.length, 1);
});

test('a summary mark that cannot be stored writes the plain report without asking the provider; a repeated report id adds nothing', async t => {
  const calls = [];
  const w = await scheduled(t, async () => { calls.push(1); return 'model'; });
  w.service.onSnapshot(snapshot());
  const task = w.service.create({ title: 'Disk', summaryAt: '2026-06-01T09:00:00Z' });
  w.service.store.save = () => Promise.reject(new Error('disk full'));
  const report = await w.holder.runner.run(w.service.store.get(task.id));
  assert.deepEqual([report.source, calls.length], ['fallback', 0]);
  const before = w.service.list()[0];
  w.service.addReport(task.id, report);
  assert.deepEqual([w.service.list()[0].reports.length, w.service.list()[0].unread], [before.reports.length, before.unread]);
});

// A task whose summary time has passed, as the observer leaves it.
async function dueTask(w, title) {
  const task = w.service.create({ title });
  w.service.store.get(task.id).summaryDue = true;
  await w.service.store.save();
  return w.service.store.get(task.id);
}

// A restart on the same home that waits for every report it starts.
async function restart(w) {
  const calls = [];
  const jobs = [];
  const holder = {};
  const service = new WatchService({ home: w.ctx.home, now: () => w.clock.now, onSummaryDue: task => jobs.push(holder.runner.run(task)) });
  holder.runner = new SummaryRunner({ watch: service, ask: async id => { calls.push(id); return 'After restart'; }, now: () => w.clock.now });
  await service.load();
  await Promise.all(jobs);
  await service.flush();
  return { service, calls };
}

const onDisk = async w => JSON.parse(await readFile(w.service.store.file, 'utf8')).tasks;

// Holds the next save once it is on disk, before the caller learns it finished: the window between the started mark and the provider.
function holdNextSave(service) {
  const saved = Promise.withResolvers();
  const release = Promise.withResolvers();
  const save = service.store.save.bind(service.store);
  service.store.save = () => {
    service.store.save = save;
    return save().then(async () => { saved.resolve(); await release.promise; });
  };
  return { saved: saved.promise, release: release.resolve };
}

// A provider whose reply is held until the test answers it.
async function heldReply(t) {
  const calls = [];
  const asked = Promise.withResolvers();
  const reply = Promise.withResolvers();
  const w = await scheduled(t, async id => { calls.push(id); asked.resolve(); return reply.promise; });
  return { w, calls, asked: asked.promise, reply };
}

// Two summaries due together: the first reply is held, so the second one waits in the queue.
async function queuedPair(t) {
  const { w, calls, asked, reply } = await heldReply(t);
  const first = await dueTask(w, 'A');
  const second = await dueTask(w, 'B');
  const jobs = [w.holder.runner.run(first), w.holder.runner.run(second)];
  await asked;
  return { w, calls, first, second, jobs, reply };
}

test('stopping a task whose summary is queued writes nothing for it: no provider call, report or unread', async t => {
  const { w, calls, first, second, jobs, reply } = await queuedPair(t);
  w.service.stop(second.id);
  reply.resolve('Report');
  const [written, skipped] = await Promise.all(jobs);
  assert.deepEqual([written.source, skipped, calls], ['model', null, [first.id]]);
  const stopped = w.service.list().find(task => task.id === second.id);
  assert.deepEqual([stopped.reports.length, stopped.unread, stopped.summaryStarted ?? false, w.banners.length], [0, 0, false, 1]);
});

test('Quit stops queued summaries before their provider starts, waits for the running one, and the queued ones are asked after the restart', async t => {
  const { w, calls, first, second, jobs, reply } = await queuedPair(t);
  const late = await dueTask(w, 'C');
  const closed = w.holder.runner.close();
  // Refused at once instead of waiting behind the running report.
  assert.equal(await Promise.race([w.holder.runner.run(late), 'queued']), null, 'no summary is admitted after Quit began');
  reply.resolve('Report');
  await closed;
  assert.equal(w.service.store.get(first.id).summaryDone, true, 'Quit waits for the running report');
  const [written, skipped] = await Promise.all(jobs);
  assert.deepEqual([written.source, skipped, calls], ['model', null, [first.id]]);
  await w.service.flush();

  const again = await restart(w);
  assert.deepEqual(again.calls, [second.id, late.id]);
  assert.deepEqual(again.service.list().map(task => [task.reports.length, task.reports[0].source]), [[1, 'model'], [1, 'model'], [1, 'model']]);
});

test('quitting while the started mark is being saved takes the mark back, so the restart asks the provider for the same report', async t => {
  const calls = [];
  const w = await scheduled(t, async id => { calls.push(id); return 'unused'; });
  const task = await dueTask(w, 'Mark');
  const save = holdNextSave(w.service);
  const job = w.holder.runner.run(task);
  await save.saved;
  const planned = task.summaryId;
  assert.deepEqual((await onDisk(w)).map(item => [item.summaryStarted, item.summaryId]), [[true, planned]], 'the mark is stored before the provider');
  const closed = w.holder.runner.close();
  save.release();
  await closed;
  await w.service.flush();
  assert.deepEqual((await onDisk(w)).map(item => [item.summaryStarted, item.summaryId, item.summaryDue]), [[false, planned, true]], 'saved before Quit finishes');
  assert.deepEqual([await job, calls.length, w.banners.length], [null, 0, 0]);

  const again = await restart(w);
  const [saved] = again.service.list();
  assert.deepEqual([again.calls, saved.reports.length, saved.reports[0].source, saved.reports[0].id, saved.unread], [[task.id], 1, 'model', planned, 1]);
});

test('stopping a task while its started mark is being saved asks nothing and takes the mark back', async t => {
  const calls = [];
  const w = await scheduled(t, async id => { calls.push(id); return 'unused'; });
  const task = await dueTask(w, 'Stop');
  const save = holdNextSave(w.service);
  const job = w.holder.runner.run(task);
  await save.saved;
  w.service.stop(task.id);
  save.release();
  assert.equal(await job, null);
  await w.service.flush();
  const [stored] = await onDisk(w);
  assert.deepEqual([calls.length, stored.reports.length, stored.unread, stored.summaryStarted, w.banners.length], [0, 0, 0, false, 0]);
  const again = await restart(w);
  assert.deepEqual([again.calls.length, again.service.list()[0].status], [0, 'stopped']);
});

test('stopping a task during the provider reply drops the late report: no report, unread or banner', async t => {
  const { w, calls, asked, reply } = await heldReply(t);
  const task = await dueTask(w, 'Late');
  const job = w.holder.runner.run(task);
  await asked;
  w.service.stop(task.id);
  reply.resolve('Late reply');
  assert.equal(await job, null);
  await w.service.flush();
  const [stored] = await onDisk(w);
  assert.deepEqual([calls.length, stored.reports.length, stored.unread, w.banners.length], [1, 0, 0, 0]);
  assert.equal((await restart(w)).calls.length, 0);
});

test('quitting during the provider reply stores one plain report before Quit finishes; the restart asks nothing', async t => {
  const { w, calls, asked, reply } = await heldReply(t);
  const task = await dueTask(w, 'Cut');
  const job = w.holder.runner.run(task);
  await asked;
  const closed = w.holder.runner.close();
  reply.reject(Error('The reply was cancelled.'));
  await closed;
  await w.service.flush();
  const [stored] = await onDisk(w);
  assert.deepEqual([stored.reports.length, stored.reports[0].source, stored.unread, stored.summaryStarted, w.banners.length], [1, 'fallback', 1, true, 1]);
  assert.equal((await job).source, 'fallback');
  const again = await restart(w);
  assert.deepEqual([calls.length, again.calls.length, again.service.list()[0].reports.length], [1, 0, 1]);
});

test('Quit never takes back a mark left by an earlier start: the restart writes the plain report for it', async t => {
  const calls = [];
  const w = await scheduled(t, async id => { calls.push(id); return 'unused'; });
  const task = await dueTask(w, 'Earlier');
  task.summaryStarted = true;
  task.summaryId = 'planned-before';
  await w.service.store.save();
  const begun = Promise.withResolvers();
  const release = Promise.withResolvers();
  const begin = w.service.beginSummary.bind(w.service);
  w.service.beginSummary = async id => { const mayAsk = await begin(id); begun.resolve(); await release.promise; return mayAsk; };
  const job = w.holder.runner.run(task);
  await begun.promise;
  const closed = w.holder.runner.close();
  release.resolve();
  await closed;
  await w.service.flush();
  assert.deepEqual([await job, (await onDisk(w))[0].summaryStarted], [null, true]);
  const again = await restart(w);
  const [saved] = again.service.list();
  assert.deepEqual([calls.length, again.calls.length, saved.reports[0].source, saved.reports[0].id], [0, 0, 'fallback', 'planned-before']);
});

test('a mark that cannot be taken back stays on disk and Quit reports the failed save; the restart writes the plain report', async t => {
  const calls = [];
  const w = await scheduled(t, async id => { calls.push(id); return 'unused'; });
  const task = await dueTask(w, 'Disk');
  const save = holdNextSave(w.service);
  const job = w.holder.runner.run(task);
  await save.saved;
  const closed = w.holder.runner.close();
  const working = w.service.store.save;
  w.service.store.save = () => {
    w.service.store.pending = Promise.reject(Error('disk full'));
    w.service.store.pending.catch(() => {});
    return w.service.store.pending;
  };
  save.release();
  await closed;
  await assert.rejects(w.service.flush(), /disk full/);
  await assert.rejects(job, /disk full/);
  assert.deepEqual([(await onDisk(w))[0].summaryStarted, calls.length], [true, 0]);
  const again = await restart(w);
  assert.deepEqual([again.calls.length, again.service.list()[0].reports[0].source], [0, 'fallback']);
  // The disk works again, so the cleanup's flush passes.
  w.service.store.save = working;
  await w.service.store.save();
});

test('Quit as Desktop does it cancels the real provider reply, keeps the queued report for the restart and saves both', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'hang' });
  const { assistant } = makeAssistant(t, ctx);
  await assistant.configure({ provider: 'claude' });
  const service = new WatchService({ home: ctx.home, now: () => NOW });
  const banners = [];
  const runner = new SummaryRunner({ watch: service, ask: (id, prompt) => assistant.summarize(id, prompt), now: () => NOW, onReport: (stored, report) => banners.push(report) });
  const w = { service, ctx, clock: { now: NOW } };
  const first = await dueTask(w, 'A');
  const second = await dueTask(w, 'B');
  const jobs = [runner.run(first), runner.run(second)];
  await eventually(async () => (await ctx.invocations()).length === 1, { what: 'the provider process' });
  // The order of main.js before-quit: close the runner, cancel the Assistant together with the drain, then save.
  const closed = runner.close();
  await Promise.allSettled([assistant.close(), closed]);
  await service.flush();
  assert.deepEqual([assistant.active.size, (await ctx.invocations()).length, banners.length], [0, 1, 1]);
  assert.deepEqual((await onDisk(w)).map(task => [task.reports.length, task.summaryStarted ?? false]), [[1, true], [0, false]]);
  assert.deepEqual((await Promise.all(jobs)).map(report => report?.source ?? null), ['fallback', null]);

  const again = await restart(w);
  assert.deepEqual(again.calls, [second.id]);
  assert.deepEqual(again.service.list().map(task => [task.reports.length, task.reports[0].source, task.unread]), [[1, 'fallback', 1], [1, 'model', 1]]);
});

const resultOf = (extra = {}) => ({ id: 'r1', key: 'k1', kind: 'question', at: '2026-06-01T08:30:00.000Z', runKey: 'i1:a', runId: 'a', instanceId: 'i1',
  runName: 'flow-a', projectId: 'p', stageId: 'ask', stageName: 'ASK', ...extra });
const linksOf = markdown => [...markdown.matchAll(/\[([^\]\n]+)\]\((afm:\/\/[^)\s]+)\)/g)].map(match => match[2]);

test('report events link to the instance, run and stage; an event without a stage links the run', () => {
  const { markdown } = fallbackReport({ title: 'T', results: [resultOf(), resultOf({ id: 'r2', kind: 'finished', stageId: null, stageName: null })] });
  assert.deepEqual(linksOf(markdown), ['afm://run/i1/a/ask', 'afm://run/i1/a']);
  assert.match(markdown, /\[\"flow-a\" asked a question \(stage \"ASK\"\)\]\(afm:\/\/run\/i1\/a\/ask\)/);
});

test('names with markup cannot break or forge a link, and ids outside the plain alphabet stay text', () => {
  const hostile = resultOf({ runName: 'a](afm://run/evil/x) [b `c` *d*', stageName: 'x]\n[y' });
  const { markdown } = fallbackReport({ title: 'T', results: [hostile, resultOf({ id: 'r2', runId: 'a/../b' }), resultOf({ id: 'r3', instanceId: 'i 1' })] });
  assert.deepEqual(linksOf(markdown), ['afm://run/i1/a/ask'], 'only the first event is a link, and only to its own ids');
  assert.doesNotMatch(markdown, /\]\(afm:\/\/run\/evil/, 'the forged target is not a link target');
  assert.equal(markdown.split('\n').filter(line => line.startsWith('- ')).length, 3, 'a name cannot add lines');
});

test('a name with a link in an event whose ids are rejected stays inert text', () => {
  const { markdown } = fallbackReport({ title: 'T', results: [resultOf({ stageId: 'invalid stage', runName: '[Open another run](afm://run/i-other/other/ask)' })] });
  assert.deepEqual(linksOf(markdown), []);
  assert.doesNotMatch(markdown, /\]\(/);
});

test('a model report gets the same linked events from Desktop, and the model prompt carries no link', async t => {
  const task = { id: 'x', title: 'T', results: [resultOf()] };
  assert.equal(reportPrompt(task).includes('afm://'), false);
  const w = await scheduled(t, async () => '## All good');
  w.service.onSnapshot(snapshot());
  const created = w.service.create({ title: 'Linked', summaryAt: '2026-06-01T09:00:00Z' });
  w.service.store.get(created.id).results = [resultOf()];
  w.clock.now = Date.parse('2026-06-01T09:00:01Z');
  w.service.onSnapshot(snapshot());
  await eventually(() => w.reports.length, { what: 'the model report' });
  const [report] = w.reports;
  assert.equal(report.source, 'model');
  assert.match(report.markdown, /^## All good\n\n## Events\n\n- 2026-06-01T08:30:00.000Z - \[/);
  assert.deepEqual(linksOf(report.markdown), ['afm://run/i1/a/ask']);
});

test('a report keeps the text before its events unchanged and stores where the events start, in UTF-16 units', () => {
  const task = { title: 'T', results: [resultOf()] };
  for (const text of ['Intro\r```text\rWaiting', '```text\r\nWaiting\r\n```', '<details>\n<summary>😀 More</summary>\nWaiting\r', '']) {
    const { markdown, eventsOffset } = modelReport(task, text);
    assert.equal(markdown.slice(0, eventsOffset), text, 'nothing is added to close a block the model left open');
    assert.match(markdown.slice(eventsOffset), /^\n\n## Events\n\n- .*\]\(afm:\/\/run\/i1\/a\/ask\)$/);
  }
  assert.equal(modelReport(task, '😀').eventsOffset, 2);
  const fallback = fallbackReport({ title: 'Night\n```', results: [resultOf()] });
  assert.equal(fallback.markdown.slice(0, fallback.eventsOffset), '# Night\n```\n\nCollected 1 events: 1 question.', 'a title cannot reach the events either');
  assert.deepEqual(modelReport({ title: 'T', results: [] }, 'Nothing 😀'), { markdown: 'Nothing 😀', eventsOffset: 10 });
});

test('the events boundary of a model report is stored with it and reaches the window unchanged after a restart', async t => {
  const reply = '😀 Intro\r<details>\r<summary>More</summary>\r```text\rWaiting';
  const w = await scheduled(t, async () => reply);
  w.service.onSnapshot(snapshot());
  const created = w.service.create({ title: 'Boundary', summaryAt: '2026-06-01T09:00:00Z' });
  w.service.store.get(created.id).results = [resultOf()];
  w.clock.now = Date.parse('2026-06-01T09:00:01Z');
  w.service.onSnapshot(snapshot());
  await eventually(() => w.reports.length, { what: 'the model report' });
  const [report] = w.reports;
  assert.deepEqual([report.source, report.eventsOffset, report.markdown.slice(0, report.eventsOffset)], ['model', reply.length, reply]);
  await w.service.flush();
  const again = new WatchService({ home: w.ctx.home, now: () => w.clock.now });
  await again.load();
  assert.deepEqual(taskView(again.store.get(created.id)).reports, [report]);
});

test('an explicit repeating request requires approval and pins the creating model and scope', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'bridge', FAKE_CALLS: JSON.stringify([{
    name: 'desktop_task_schedule', args: { title: 'Progress', prompt: 'Read our run status', everyMinutes: 10, delivery: 'telegram' },
  }]) });
  const service = new WatchService({ home: ctx.home });
  const accepted = [];
  const { assistant, events } = makeAssistant(t, ctx, { context: await makeContext(ctx.home), watch: {
    ...service.tools(), schedule: async (input, config) => {
      accepted.push({ input, config });
      return { id: 'task-1', kind: 'repeating', title: input.title, scope: config.scope, everyMs: 600000, delivery: 'telegram', results: [], unread: 0 };
    },
  } });
  await assistant.configure({ provider: 'claude', model: 'haiku', permission: 'read' });
  await assistant.send({ text: 'Send me progress every 10 minutes', scope: { projectId: 'alpha' } });
  const approval = await eventually(() => events.find(event => event.type === 'approval'), { what: 'scheduling approval' });
  assert.match(approval.summary, /Telegram[\s\S]*Read our run status/);
  assert.deepEqual(accepted, []);
  assistant.approve(approval.approvalId, true);
  await settled(events);
  assert.equal(accepted[0].config.settings.model, 'haiku');
  assert.equal(accepted[0].config.scope.projectId, 'alpha');
  assert.equal(events.find(event => event.type === 'watch_card').card.kind, 'repeating');
});

test('a background run uses its pinned provider and leaves the interactive settings and transcript alone', async t => {
  const ctx = await providerHome(t);
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.configure({ provider: 'codex', model: 'other-model', permission: 'read' });
  const reply = await assistant.runRepeating({ id: 'task-1', prompt: 'Write one update', scope: { kind: 'all' },
    settings: { provider: 'claude', model: 'pinned-model', reasoning: 'default', permission: 'read' } });
  assert.equal(reply, 'Hello world');
  const [call] = await ctx.invocations();
  assert.equal(call.kind, 'claude');
  assert.ok(call.args.includes('pinned-model'));
  assert.match(call.stdin, /Write one update/);
  assert.ok(events.every(event => event.conversationId === 'task:task-1'));
  assert.deepEqual(await assistant.history(), []);
  assert.deepEqual(await assistant.history('task:task-1'), []);
  assert.equal((await assistant.state()).settings.model, 'other-model');
});

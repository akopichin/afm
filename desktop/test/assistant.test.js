import test from 'node:test';
import assert from 'node:assert/strict';
import { access, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { Assistant } from '../src/assistant/assistant.js';
import { makeAssistant, providerHome, settled, stubContext, until } from './assistant-support.js';
import { eventually } from './support.js';

const SECRET = 'sk-ant-secret-value-1234567890';

test('providers are found where a Finder-launched app would look, with capabilities and auth, and no token value', async t => {
  const ctx = await providerHome(t, { CLAUDE_CODE_OAUTH_TOKEN: SECRET });
  // The only PATH entry is empty: the CLIs live in a well-known directory under HOME.
  const { mkdir, rename } = await import('node:fs/promises');
  await mkdir(path.join(ctx.home, '.local'), { recursive: true });
  await rename(ctx.bin, path.join(ctx.home, '.local/bin'));
  const { assistant } = makeAssistant(t, { ...ctx, env: { ...ctx.env, PATH: '/nonexistent' } });
  assert.equal((await assistant.state()).scanned, false, 'state() alone never probes anything');
  const state = await assistant.scan();
  const claude = state.providers.find(p => p.id === 'claude'), codex = state.providers.find(p => p.id === 'codex');
  assert.equal(claude.found, true);
  assert.equal(claude.auth.state, 'ready');
  assert.equal(claude.auth.via, 'env');
  assert.deepEqual(claude.permissions, ['ask', 'read', 'edit', 'yolo']);
  assert.deepEqual(claude.reasoning, ['low', 'medium', 'high', 'xhigh', 'max']);
  assert.equal(codex.auth.via, 'cli-login');
  assert.equal(JSON.stringify(state).includes(SECRET), false, 'the token value never reaches the UI state');
});

test('a missing CLI and a missing login are reported with what to do', async t => {
  const ctx = await providerHome(t, { FAKE_AUTH: 'none' });
  const { rm } = await import('node:fs/promises');
  await rm(path.join(ctx.bin, 'codex'));
  const { assistant } = makeAssistant(t, ctx);
  const state = await assistant.scan();
  assert.equal(state.providers.find(p => p.id === 'codex').found, false);
  assert.match(state.providers.find(p => p.id === 'codex').auth.detail, /not found/i);
  assert.equal(state.providers.find(p => p.id === 'claude').auth.state, 'missing');
  assert.equal((await assistant.testProvider('claude')).ok, false);
  await assert.rejects(assistant.send({ text: 'hi' }), { code: 'missing_credentials' });
  assert.deepEqual(await ctx.invocations(), [], 'no provider process was started without credentials');
});

test('a reply streams to the window before the provider finishes, and is kept as history', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'stream', FAKE_PAUSE: '200' });
  const { assistant, events } = makeAssistant(t, ctx);
  const { turnId, effective } = await assistant.send({ text: 'secret question text' });
  assert.equal(effective.permission, 'ask');
  await settled(events);
  const deltas = events.filter(e => e.type === 'delta');
  assert.deepEqual(deltas.map(e => e.text), ['Hel', 'lo ', 'world']);
  assert.ok(deltas.at(-1).at - deltas[0].at >= 300, 'partial output arrived over time, not as one final block');
  assert.ok(deltas[0].at < events.find(e => e.type === 'done').at - 300, 'first text arrived while the turn was still running');
  assert.equal(events.find(e => e.type === 'done').turnId, turnId);
  const history = await assistant.history();
  assert.deepEqual(history.map(m => [m.role, m.text, m.status ?? null]), [['user', 'secret question text', null], ['assistant', 'Hello world', 'done']]);
  const [call] = await ctx.invocations();
  assert.equal(call.args.join(' ').includes('secret question text'), false, 'the prompt travels on stdin, not in argv');
  assert.match(call.stdin, /secret question text/);
});

test('Codex output is parsed the same way and later turns carry bounded history', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '10' });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.configure({ provider: 'codex' });
  await assistant.send({ text: 'first' });
  await until(events, 'done');
  events.length = 0;
  await assistant.send({ text: 'second' });
  await until(events, 'done');
  assert.equal((await assistant.history()).at(-1).text, 'Hel\n\nlo \n\nworld');
  const calls = await ctx.invocations();
  assert.match(calls[1].stdin, /User: first\n\nAssistant: Hel[\s\S]*User: second$/);
  assert.equal(calls[1].args.at(-1), '-');
});

test('permission and reasoning choices map to the flags this CLI supports, and unsupported ones are refused', async t => {
  const ctx = await providerHome(t);
  const { assistant, events } = makeAssistant(t, ctx);
  const argsFor = async (provider, change) => {
    await assistant.configure({ provider, ...change });
    events.length = 0;
    const before = (await ctx.invocations()).length;
    await assistant.send({ text: 'x' });
    await settled(events);
    return (await ctx.invocations())[before].args;
  };
  const claude = await argsFor('claude', { permission: 'read', reasoning: 'high', model: 'opus-test' });
  assert.deepEqual([claude[claude.indexOf('--permission-mode') + 1], claude[claude.indexOf('--effort') + 1], claude[claude.indexOf('--model') + 1]], ['plan', 'high', 'opus-test']);
  assert.ok(claude.includes('--include-partial-messages') && claude.includes('--output-format'));
  const ask = await argsFor('claude', { permission: 'ask', reasoning: 'default', model: '' });
  assert.equal(ask[ask.indexOf('--permission-mode') + 1], 'manual', 'this CLI names its default mode "manual"');
  assert.equal(ask.includes('--effort'), false);
  const edit = await argsFor('claude', { permission: 'edit' });
  assert.equal(edit[edit.indexOf('--permission-mode') + 1], 'acceptEdits');
  assert.ok((await argsFor('claude', { permission: 'yolo' })).includes('--dangerously-skip-permissions'));

  const codexJoin = async change => (await argsFor('codex', change)).join(' ');
  assert.match(await codexJoin({ permission: 'read' }), /--sandbox read-only/);
  assert.match(await codexJoin({ permission: 'ask' }), /--sandbox workspace-write -c approval_policy="on-request"/);
  const edit2 = await codexJoin({ permission: 'edit', reasoning: 'xhigh' });
  assert.match(edit2, /model_reasoning_effort="xhigh"/);
  assert.match(edit2, /--sandbox workspace-write -c approval_policy="never"/);
  assert.match(await codexJoin({ permission: 'yolo' }), /--dangerously-bypass-approvals-and-sandbox/);
});

test('an older CLI that cannot enforce a mode or reasoning level is refused instead of approximated', async t => {
  const ctx = await providerHome(t, { FAKE_CAPS: 'old' });
  const { assistant } = makeAssistant(t, ctx);
  const state = await assistant.scan();
  assert.equal(state.providers.find(p => p.id === 'codex').permissions.includes('yolo'), false);
  await assistant.configure({ provider: 'codex', permission: 'yolo' });
  await assert.rejects(assistant.send({ text: 'x' }), /cannot enforce "YOLO/);
  await assistant.configure({ provider: 'claude', permission: 'ask', reasoning: 'high' });
  await assert.rejects(assistant.send({ text: 'x' }), /does not support reasoning level "high"/);
  assert.deepEqual(await ctx.invocations(), []);
});

test('a CLI failure surfaces its exit code and stderr, with secrets removed', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'fail', FAKE_SECRET: SECRET, ANTHROPIC_API_KEY: SECRET });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.send({ text: 'x' });
  const failure = await until(events, 'error');
  assert.match(failure.message, /exited with code 3\. boom \[hidden\]/);
  assert.equal(failure.message.includes(SECRET), false);
  assert.equal((await assistant.history()).at(-1).status, 'error');
  assert.equal(JSON.stringify(await readFile(path.join(ctx.home, '.afm/desktop/assistant-history.json'), 'utf8')).includes(SECRET), false);
});

test('a provider that reports a login problem in its own result is shown that message', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'not-logged-in' });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.send({ text: 'x' });
  assert.match((await until(events, 'error')).message, /Not logged in/);
});

test('cancel stops the provider process, keeps the partial text and leaves the chat usable', async t => {
  const term = path.join((await providerHome(t)).home, 'term');
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'hang', FAKE_TERM_FILE: term });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.send({ text: 'long task' });
  await until(events, 'delta');
  assert.equal(assistant.cancel(), true);
  const cancelled = await until(events, 'cancelled');
  assert.equal(cancelled.reply.text, 'started');
  await eventually(() => access(term).then(() => true, () => false));
  assert.deepEqual((await assistant.history()).map(m => m.status ?? 'user'), ['user', 'cancelled']);
  assert.equal(assistant.cancel(), false, 'nothing left to cancel');
  events.length = 0;
  await assistant.configure({ provider: 'codex' });
  await assistant.send({ text: 'again' });
  await until(events, 'delta');
  assistant.cancel();
  await until(events, 'cancelled');
});

test('one reply per conversation and a bounded number overall', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'hang' });
  const { assistant, events } = makeAssistant(t, ctx, { maxActive: 1 });
  const first = assistant.send({ text: 'a' });
  await assert.rejects(assistant.send({ text: 'b' }), { code: 'busy' });
  await first;
  await assert.rejects(assistant.send({ conversationId: 'other', text: 'c' }), { code: 'too_many' });
  await until(events, 'delta');
  await assert.rejects(assistant.clear(), { code: 'busy' });
  assistant.cancel();
  await until(events, 'cancelled');
  await assistant.clear();
  assert.deepEqual(await assistant.history(), []);
});

test('the selected project or run is the working folder and the stated scope; unknown scopes fail closed', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '1' });
  const project = { id: 'proj', name: 'spider', path: path.join(ctx.home, 'spider') };
  const run = { run_id: 'r1', instance_id: 'i1', project_root: project.path, run_name: 'checkout' };
  const { assistant, events } = makeAssistant(t, ctx, { context: stubContext({ project: async id => { if (id !== 'proj') throw Error('x'); return project; }, runs: async () => [run] }) });
  const { mkdir } = await import('node:fs/promises');
  await mkdir(project.path);
  const started = await assistant.send({ scope: { projectId: 'proj' }, text: 'x' });
  assert.equal(started.scope, 'spider');
  await settled(events);
  await assistant.send({ scope: { runId: 'r1', instanceId: 'i1' }, text: 'y' });
  await eventually(() => events.filter(e => e.type === 'done').length === 2);
  const [first, second] = await ctx.invocations();
  assert.equal(first.cwd, await realpathOf(project.path));
  assert.match(first.args[first.args.indexOf('--append-system-prompt') + 1], /Scope: spider/);
  assert.match(second.args[second.args.indexOf('--append-system-prompt') + 1], /Scope: checkout/);
  await assert.rejects(assistant.send({ scope: { runId: 'r1', instanceId: 'other' }, text: 'z' }), { code: 'scope_unknown' });
  await assert.rejects(assistant.send({ scope: { projectId: 'nope' }, text: 'z' }), { code: 'scope_unknown' });
  assert.equal((await assistant.history()).at(-1).context, 'checkout');
});
const realpathOf = async p => (await import('node:fs/promises')).realpath(p);

test('history is bounded and settings survive a restart without any credential value', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '1', CLAUDE_CODE_OAUTH_TOKEN: SECRET });
  const first = makeAssistant(t, ctx);
  await first.assistant.configure({ provider: 'codex', model: 'gpt-test', reasoning: 'high', permission: 'edit' });
  await first.assistant.addProvider({ name: 'Mine', kind: 'claude', executable: path.join(ctx.bin, 'claude'), credential: { type: 'env', ref: 'MY_KEY' } });
  for (let i = 0; i < 33; i++) { await first.assistant.send({ text: 'msg ' + i }); await until(first.events.splice(0), 'done').catch(() => {}); await eventually(() => !first.assistant.active.size); }
  assert.equal((await first.assistant.history()).length, 60);
  const second = new Assistant({ home: ctx.home, env: ctx.env, context: stubContext(), bridge: {} });
  const state = await second.state();
  assert.deepEqual([state.settings.provider, state.settings.model, state.settings.reasoning, state.settings.permission], ['codex', 'gpt-test', 'high', 'edit']);
  assert.equal(state.providers.filter(p => !p.builtIn).length, 1);
  assert.equal((await readFile(path.join(ctx.home, '.afm/desktop/assistant.json'), 'utf8')).includes(SECRET), false);
  const transcript = (await ctx.invocations()).at(-1).stdin;
  assert.ok(transcript.length < 25000, 'the transcript sent to the provider stays within its budget');
});

test('provider setup rejects anything that is not a reference to a credential', async t => {
  const ctx = await providerHome(t);
  const { assistant } = makeAssistant(t, ctx);
  const bad = [
    { name: 'a', kind: 'claude', executable: 'claude' },
    { name: 'a', kind: 'claude', executable: '/bin/claude', credential: { type: 'env', ref: 'bad name' } },
    { name: 'a', kind: 'claude', executable: '/bin/claude', credential: { type: 'file', ref: 'relative' } },
    { name: 'a', kind: 'openai', endpoint: 'https://user:pw@example.com/v1', credential: { type: 'env', ref: 'K' } },
    { name: 'a', kind: 'openai', endpoint: 'http://example.com/v1', credential: { type: 'env', ref: 'K' } },
    { name: 'a', kind: 'openai', endpoint: 'https://example.com/v1', credential: { type: 'cli' } },
    { name: 'a', kind: 'shell', executable: '/bin/sh' },
  ];
  for (const input of bad) await assert.rejects(assistant.addProvider(input), /./, JSON.stringify(input));
  await assert.rejects(assistant.configure({ model: '--dangerously-skip-permissions' }), /Invalid assistant setting/);
  await assert.rejects(assistant.configure({ token: 'x' }), /Invalid assistant setting/);
  await assistant.addProvider({ name: 'Local', kind: 'openai', endpoint: 'http://127.0.0.1:11434/v1', credential: { type: 'env', ref: 'LOCAL_KEY' } });
  assert.equal((await assistant.state()).providers.length, 3);
});

test('a flood of provider deltas reaches the window in a few ordered batches', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'flood', FAKE_DELTAS: '1000' });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.send({ text: 'go' });
  await settled(events);
  const deltas = events.filter(e => e.type === 'delta');
  assert.ok(deltas.length < 100, `1000 provider deltas became ${deltas.length} messages`);
  const expected = Array.from({ length: 1000 }, (_, i) => `word${i} `).join('');
  assert.equal(deltas.map(e => e.text).join(''), expected);
  assert.equal(events.findLastIndex(e => e.type === 'delta') < events.findIndex(e => e.type === 'done'), true, 'text arrives before the end of the reply');
});

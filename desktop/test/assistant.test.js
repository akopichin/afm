import test from 'node:test';
import assert from 'node:assert/strict';
import { access, mkdir, open, readFile, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import path from 'node:path';
import { Assistant } from '../src/assistant/assistant.js';
import { desktopEnvironment, refreshEnvironment, stopEnvironmentImports } from '../src/environment.js';
import { Prefs } from '../src/prefs.js';
import { API_KEYS, makeAssistant, providerHome, SAMPLE_IMAGE, settled, stubContext, until } from './assistant-support.js';
import { eventually } from './support.js';

const SECRET = 'sk-ant-secret-value-1234567890';
const exec = promisify(execFile);

// Quit is terminal for the environment module, so each quit scenario gets a process of its own: this file is run
// again with only that test selected. Its output is shown when it fails.
const QUIT_SCENARIO = 'AFM_TEST_QUIT_SCENARIO';
function quitTest(name, body) {
  const selected = process.env[QUIT_SCENARIO];
  if (selected) {
    if (selected === name) test(name, body);
    return;
  }
  test(name, async () => {
    const pattern = '^' + name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$';
    // Without the runner's context variable the child reports plainly instead of in the runner's binary protocol.
    const { NODE_TEST_CONTEXT, ...env } = process.env;
    const { stdout } = await exec(process.execPath, ['--test-name-pattern=' + pattern, import.meta.filename],
      { env: { ...env, [QUIT_SCENARIO]: name }, timeout: 60000 })
      .catch(error => assert.fail(`${name} failed in its own process:\n${error.stdout}\n${error.stderr}`));
    assert.match(stdout, /^# pass 1$/m, 'the scenario did not run in its own process');
  });
}

const hasBash = () => access('/bin/bash', constants.X_OK).then(() => true, () => false);
const alive = pid => { try { process.kill(pid, 0); return true; } catch { return false; } };
// main.js lets Quit wait 6 s before the app exits.
const quitDeadline = () => new Promise(resolve => setTimeout(resolve, 6000, 'deadline').unref());

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

for (const kind of ['claude', 'codex']) {
  test(`a pasted image reaches the ${kind} CLI and the Desktop transcript stores only its count`, async t => {
    const ctx = await providerHome(t, { ...API_KEYS, FAKE_PAUSE: '5' });
    const { assistant, events } = makeAssistant(t, ctx);
    await assistant.configure({ provider: kind, permission: 'read' });
    await assert.rejects(assistant.send({ images: [{ ...SAMPLE_IMAGE, data: 'aGVsbG8=' }] }), /invalid/);
    await assistant.send({ images: [SAMPLE_IMAGE] });
    await settled(events);
    assert.equal(events.find(event => event.type === 'start').message.imageCount, 1);
    assert.equal((await assistant.history())[0].imageCount, 1);
    assert.equal(JSON.stringify(await assistant.history()).includes(SAMPLE_IMAGE.data), false);
    assert.equal(events.at(-1).type, 'done');
    const [call] = await ctx.invocations();
    assert.equal(call.args.some(arg => arg.includes(SAMPLE_IMAGE.data)), false);
    if (kind === 'claude') {
      const at = call.args.indexOf('--input-format');
      assert.ok(at >= 0);
      assert.equal(call.args[at + 1], 'stream-json');
      const [image, text] = JSON.parse(call.stdin).message.content;
      assert.deepEqual(image, { type: 'image', source: { type: 'base64', media_type: 'image/png', data: SAMPLE_IMAGE.data } });
      assert.match(text.text, /attached image/);
    } else {
      assert.deepEqual(call.images, [SAMPLE_IMAGE.data]);
      assert.match(call.stdin, /attached image/);
      const file = call.args.find(arg => arg.startsWith('--image=')).slice('--image='.length);
      await eventually(async () => await access(file).then(() => false, () => true), { what: 'temporary attachment removed' });
    }
  });
}

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

test('Codex output is parsed the same way and, without resume support, later turns carry bounded history', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '10', FAKE_NO_RESUME: '1' });
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

test('each reply reports where its time went, with the environment import apart from the provider and no text', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'stream' });
  const records = [];
  let reads = 0;
  const slow = async () => { reads++; await new Promise(resolve => setTimeout(resolve, 60)); return ctx.env; };
  const loginPath = async () => { await new Promise(resolve => setTimeout(resolve, 60)); return ''; };
  const { assistant, events } = makeAssistant(t, ctx, { env: slow, loginPath, onTiming: record => records.push(record) });
  await assistant.send({ text: 'secret question text' });
  await settled(events);
  assert.equal(reads, 1, 'detection and the provider process share one environment snapshot');
  assert.equal(records.length, 1);
  const [record] = records;
  assert.equal(record.status, 'done');
  assert.ok(record.environmentMs >= 110, 'the environment import and the login-shell PATH are both counted');
  assert.ok(record.receiptMs >= 110 && record.receiptMs <= record.providerReadyMs && record.providerReadyMs <= record.firstTextMs && record.firstTextMs <= record.totalMs);
  assert.doesNotMatch(JSON.stringify(record), /secret question text|Hello/);
});

test('a credential added to the environment is picked up by the next message without a manual rescan', async t => {
  const ctx = await providerHome(t, { FAKE_AUTH: 'none' });
  const added = {};
  const { assistant, events } = makeAssistant(t, ctx, { env: async () => ({ ...ctx.env, ...added }) });
  await assistant.scan();
  await assert.rejects(assistant.send({ text: 'hi' }), { code: 'missing_credentials' });
  added.ANTHROPIC_API_KEY = 'sk-ant-added-later-123456';
  await assistant.send({ text: 'hi' });
  await settled(events);
  assert.equal(events.at(-1).type, 'done');
});

test('a scan that was running when the environment was refreshed cannot bring back its stale detection', async t => {
  const ctx = await providerHome(t, { FAKE_AUTH: 'none' });
  const added = {};
  let release;
  let held = true;
  const env = async () => {
    const snapshot = { ...ctx.env, ...added };
    if (held) await new Promise(resolve => { release = resolve; });
    return snapshot;
  };
  const { assistant, events } = makeAssistant(t, ctx, { env });
  const scan = assistant.scan();
  await eventually(() => release, { what: 'the scan to read the environment' });
  held = false;
  added.ANTHROPIC_API_KEY = 'sk-ant-added-later-123456';
  assistant.resetDetections();
  release();
  await scan;
  assert.equal(assistant.detections.size, 0, 'the stale scan stored its result');
  await assistant.send({ text: 'hi' });
  await settled(events);
  assert.equal(events.at(-1).type, 'done');
});

test('closing while a reply is still preparing waits for it and leaves no approval socket open', async t => {
  const ctx = await providerHome(t);
  let gate, reading;
  const env = async () => { if (gate) { reading.resolve(); await gate.promise; } return ctx.env; };
  const host = stubContext({ endpoint: async () => ({ url: 'http://127.0.0.1:1', token: 'fake' }) });
  const { assistant, events } = makeAssistant(t, ctx, { env, context: host });
  await assistant.scan();
  gate = Promise.withResolvers();
  reading = Promise.withResolvers();
  const sending = assistant.send({ text: 'hi' });
  await reading.promise;
  let closed = false;
  const closing = assistant.close().then(() => { closed = true; });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(closed, false, 'close() returned while the reply was still preparing');
  gate.resolve();
  await sending;
  await closing;
  await settled(events);
  assert.equal(events.at(-1).type, 'cancelled');
  assert.equal(assistant.active.size, 0);
  assert.equal(assistant.approvals.address, null, 'an approval socket was opened after close()');
  assert.deepEqual(await ctx.invocations(), [], 'no provider process was started');
});

test('a reply whose preparation fails is settled, so closing does not wait for it forever', async t => {
  const ctx = await providerHome(t);
  const gate = Promise.withResolvers(), reading = Promise.withResolvers();
  const env = async () => { reading.resolve(); await gate.promise; throw Error('controlled environment failure'); };
  const { assistant } = makeAssistant(t, ctx, { env });
  const rejected = assert.rejects(assistant.send({ text: 'hi' }), /controlled environment failure/);
  await reading.promise;
  const closing = assistant.close();
  gate.resolve();
  await rejected;
  await closing;
  assert.equal(assistant.active.size, 0);
  assert.equal(assistant.approvals.address, null);
});

quitTest('quitting while a slow shell startup is still loading the environment leaves no shell running', async t => {
  if (!await hasBash()) return t.skip('Requires bash');
  const ctx = await providerHome(t);
  const prefs = new Prefs(ctx.home);
  await prefs.updateEnvironment({ shell: 'bash' });
  // About 4 s per run, and the redirected directory makes the import run twice: longer than the 6 s that Quit waits.
  const pids = path.join(ctx.home, 'pids');
  const redirected = path.join(ctx.home, 'zcfg');
  await mkdir(redirected);
  await writeFile(path.join(ctx.home, '.bashrc'), `echo $$ >> "${pids}"\nZDOTDIR="${redirected}"\nfor ((i = 0; i < 80; i++)); do /bin/sleep 0.05; done\n`);
  refreshEnvironment();
  const { assistant } = makeAssistant(t, ctx, { env: () => desktopEnvironment(prefs, ctx.home, ctx.env) });
  const failed = assert.rejects(assistant.send({ text: 'hi' }), /Desktop is quitting/);
  await eventually(() => readFile(pids, 'utf8').catch(() => ''), { what: 'the shell to start' });
  // The order of main.js's before-quit: the imports are stopped first, then the assistant is closed.
  stopEnvironmentImports();
  assert.equal(await Promise.race([assistant.close().then(() => 'closed'), quitDeadline()]), 'closed', 'Quit gave up waiting for the reply');
  const started = (await readFile(pids, 'utf8')).split('\n').filter(Boolean).map(Number);
  assert.equal(started.length, 1, 'the import ran its shell again after Quit');
  await eventually(() => !alive(started[0]), { timeout: 1000, what: 'the importing shell to be killed' });
  await failed;
  assert.equal(assistant.active.size, 0);
});

quitTest('quitting after the startup shell exited, while the environment file is still read, ends the reply without probing a provider', async t => {
  if (!await hasBash()) return t.skip('Requires bash');
  const ctx = await providerHome(t);
  const prefs = new Prefs(ctx.home);
  // A FIFO as the environment file: the import opens it to read only after its shell exited, and the test can open
  // it to write only once the import is reading. Writing to it lets the import go on.
  const file = path.join(ctx.home, 'gate.env');
  await exec('mkfifo', [file]);
  await prefs.updateEnvironment({ shell: 'bash', file });
  const { assistant, events } = makeAssistant(t, ctx, { env: () => desktopEnvironment(prefs, ctx.home, ctx.env) });
  const failed = assert.rejects(assistant.send({ text: 'hi' }), /Desktop is quitting/);
  const writer = await eventually(() => open(file, constants.O_WRONLY | constants.O_NONBLOCK).catch(() => null), { what: 'the import to read the environment file' });
  stopEnvironmentImports();
  const closing = Promise.race([assistant.close().then(() => 'closed'), quitDeadline()]);
  await writer.writeFile('FROM_FILE=1\n');
  await writer.close();
  assert.equal(await closing, 'closed', 'Quit gave up waiting for the reply');
  await failed;
  assert.equal(assistant.active.size, 0);
  assert.deepEqual(events.map(event => event.type), [], 'the reply went on after Quit');
  assert.deepEqual(await ctx.invocations(), [], 'a provider was probed after Quit');
});

// ---- provider sessions: resume and /clear ----

const stored = async ctx => JSON.parse(await readFile(path.join(ctx.home, '.afm', 'desktop', 'assistant-history.json'), 'utf8').catch(() => '{}'));
const turn = async (assistant, events, text, scope) => {
  const before = events.filter(e => ['done', 'error', 'cancelled'].includes(e.type)).length;
  await assistant.send({ text, scope });
  await eventually(() => events.filter(e => ['done', 'error', 'cancelled'].includes(e.type)).length > before, { what: 'the reply to end' });
  return events.findLast(e => ['done', 'error', 'cancelled'].includes(e.type));
};

for (const kind of ['claude', 'codex']) {
  test(`${kind}: the session id is kept with the chat, resumed after a restart, and the chat is not re-sent`, async t => {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_SESSION: 'sess-one', ...API_KEYS });
    const first = makeAssistant(t, ctx);
    await first.assistant.configure({ provider: kind });
    assert.equal((await first.assistant.state()).providers.find(p => p.id === kind).capabilities.resume, undefined, 'nothing is detected before a scan');
    await first.assistant.scan();
    await turn(first.assistant, first.events, 'one');
    assert.equal(first.events.some(e => JSON.stringify(e).includes('sess-one')), false, 'the session id never reaches the window');
    assert.equal((await stored(ctx)).sessions.main.sessionId, 'sess-one');
    await first.assistant.close();
    // A new Assistant, as after a restart: the visible chat and the session come from the same file.
    const second = makeAssistant(t, ctx);
    await second.assistant.configure({ provider: kind });
    assert.equal((await second.assistant.history()).length, 2);
    await turn(second.assistant, second.events, 'two');
    const calls = await ctx.invocations();
    const resume = kind === 'claude' ? ['--resume', 'sess-one'] : ['resume'];
    assert.deepEqual(calls[0].args.filter(arg => resume.includes(arg)), [], 'the first turn is a fresh session');
    assert.ok(resume.every(arg => calls[1].args.includes(arg)), `the second turn resumes: ${calls[1].args.join(' ')}`);
    assert.equal(calls[1].args.filter(arg => arg === 'sess-one').length, 1, 'the id appears once, as the provider flag value');
    const prompt = calls[1].stdin;
    if (kind === 'codex') assert.match(prompt, /^Backlog is built into AFM Desktop chat/);
    assert.equal(prompt.slice(prompt.startsWith('Backlog is built') ? prompt.indexOf('\n\n') + 2 : 0), 'User: two',
      'a resumed session is not given the transcript again');
    assert.equal(JSON.stringify(await second.assistant.state()).includes('sess-one'), false, 'the id is not part of the state shown to the window');
  });
}

test('a provider, scope or credential change does not reuse the session', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
  const projectA = path.join(ctx.home, 'a');
  const projectB = path.join(ctx.home, 'b');
  const projects = { a: { id: 'a', path: projectA, name: 'a' }, b: { id: 'b', path: projectB, name: 'b' } };
  const { mkdir } = await import('node:fs/promises');
  await mkdir(projectA); await mkdir(projectB);
  const context = stubContext({ project: async id => projects[id] });
  const { assistant, events } = makeAssistant(t, ctx, { context });
  await assistant.scan();
  const resumes = async () => (await ctx.invocations()).map(call => call.args.includes('--resume'));
  await turn(assistant, events, '1', { projectId: 'a' });
  await turn(assistant, events, '2', { projectId: 'a' });
  await turn(assistant, events, '3', { projectId: 'b' });
  assert.deepEqual(await resumes(), [false, true, false], 'another project is another session');
  await assistant.configure({ provider: 'codex' });
  await turn(assistant, events, '4', { projectId: 'b' });
  assert.equal((await ctx.invocations()).at(-1).args.includes('resume'), false, 'another provider does not take the Claude session');
  await assistant.configure({ provider: 'claude' });
  await turn(assistant, events, '5', { projectId: 'b' });
  assert.equal((await ctx.invocations()).at(-1).args.includes('--resume'), false, 'the stored session now belongs to Codex');
  const withToken = makeAssistant(t, ctx, { context, env: async () => ({ ...ctx.env, ANTHROPIC_API_KEY: 'sk-ant-changed-credential-123456' }) });
  await turn(withToken.assistant, withToken.events, '6', { projectId: 'b' });
  await turn(withToken.assistant, withToken.events, '7', { projectId: 'b' });
  const last = (await ctx.invocations()).slice(-2);
  assert.deepEqual(last.map(call => call.args.includes('--resume')), [false, true], 'a new login starts fresh, then resumes its own session');
});

test('an old CLI without resume support keeps sending the bounded transcript', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_NO_RESUME: '1' });
  const { assistant, events } = makeAssistant(t, ctx);
  await turn(assistant, events, 'one');
  await turn(assistant, events, 'two');
  const calls = await ctx.invocations();
  assert.equal(calls[1].args.includes('--resume'), false);
  assert.match(calls[1].stdin, /User: one\n\nAssistant: Hel[\s\S]*User: two$/);
});

for (const kind of ['claude', 'codex']) {
  test(`${kind}: an expired session falls back to the saved chat and the reply still succeeds`, async t => {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
    const { assistant, events } = makeAssistant(t, ctx);
    await assistant.configure({ provider: kind });
    await turn(assistant, events, 'one');
    ctx.env.FAKE_EXPIRED = '1';
    const end = await turn(assistant, events, 'two');
    assert.equal(end.type, 'done', end.message);
    const calls = await ctx.invocations();
    assert.equal(calls.length, 3, 'one fresh turn, the refused resume, the fallback');
    assert.match(calls[2].stdin, /User: one[\s\S]*User: two$/, 'the fallback carries the saved chat');
    assert.ok(events.some(e => e.type === 'notice' && /could not be resumed/.test(e.text)));
    const expired = kind === 'claude' ? calls[1].args[calls[1].args.indexOf('--resume') + 1] : calls[1].args.at(-2);
    const kept = (await stored(ctx)).sessions.main;
    assert.ok(kept.sessionId && kept.sessionId !== expired, 'the new session replaced the expired one');
    ctx.env.FAKE_EXPIRED = '0';
    await turn(assistant, events, 'three');
    assert.ok((await ctx.invocations()).at(-1).args.includes(kind === 'claude' ? '--resume' : 'resume'), 'the new session is resumed');
  });
}

test('a rejected session whose fallback fails too is forgotten and the error is shown', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  await turn(assistant, events, 'one');
  const id = (await stored(ctx)).sessions.main.sessionId;
  Object.assign(ctx.env, { FAKE_EXPIRED: '1', FAKE_SCENARIO: 'fail' });
  const end = await turn(assistant, events, 'two');
  assert.equal(end.type, 'error');
  assert.equal((await ctx.invocations()).length, 3, 'the rejected resume was retried once from the saved chat');
  assert.equal((await stored(ctx)).sessions?.main, undefined, `the refused session ${id} is forgotten`);
});

test('a failed resume that is not a rejected session is shown as it is: no second attempt, the session stays', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  await turn(assistant, events, 'one');
  const before = (await stored(ctx)).sessions.main;
  ctx.env.FAKE_SCENARIO = 'fail';
  const end = await turn(assistant, events, 'two');
  assert.equal(end.type, 'error');
  assert.equal((await ctx.invocations()).length, 2, 'nothing was replayed');
  assert.deepEqual((await stored(ctx)).sessions.main, before, 'a transient failure does not lose the session');
});

for (const kind of ['claude', 'codex']) {
  test(`${kind}: work that already ran is never replayed, even when the failure reads like a rejected session`, async t => {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
    const { assistant, events } = makeAssistant(t, ctx);
    await assistant.configure({ provider: kind, permission: kind === 'codex' ? 'edit' : 'edit' });
    await turn(assistant, events, 'one');
    const marker = path.join(ctx.home, 'marker');
    Object.assign(ctx.env, { FAKE_SCENARIO: 'native-fail', FAKE_MARKER: marker });
    const id = (await stored(ctx)).sessions.main.sessionId;
    const end = await turn(assistant, events, 'two');
    assert.equal(end.type, 'error');
    assert.equal((await ctx.invocations()).length, 2, 'no fallback attempt');
    assert.equal((await readFile(marker, 'utf8')).split('\n').filter(Boolean).length, 1, 'the side effect happened once');
    assert.match(end.message, /No conversation found with session ID: \[hidden\]/, 'the explanation stays, the id does not');
    assert.equal((JSON.stringify(events) + JSON.stringify(await assistant.history())).includes(id), false, 'the session id reached the window');
  });
}

test('rotating the credential behind the same variable starts a fresh session, also after a restart', async t => {
  for (const [kind, variable] of [['claude', 'ANTHROPIC_API_KEY'], ['codex', 'OPENAI_API_KEY']]) {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', [variable]: 'sk-first-credential-123456' });
    const first = makeAssistant(t, ctx);
    await first.assistant.configure({ provider: kind });
    await turn(first.assistant, first.events, 'one');
    await turn(first.assistant, first.events, 'two');
    const resumes = async () => (await ctx.invocations()).map(call => call.args.includes(kind === 'claude' ? '--resume' : 'resume'));
    assert.deepEqual(await resumes(), [false, true]);
    await first.assistant.close();
    const rotated = makeAssistant(t, ctx, { env: async () => ({ ...ctx.env, [variable]: 'sk-rotated-credential-654321' }) });
    await rotated.assistant.configure({ provider: kind });
    await turn(rotated.assistant, rotated.events, 'three');
    assert.deepEqual((await resumes()).at(-1), false, `${kind}: a new key is a new account`);
    await turn(rotated.assistant, rotated.events, 'four');
    assert.deepEqual((await resumes()).at(-1), true, `${kind}: the new session is resumed with the new key`);
    const file = await readFile(path.join(ctx.home, '.afm', 'desktop', 'assistant-history.json'), 'utf8');
    assert.equal(file.includes('first-credential') || file.includes('rotated-credential'), false, 'no key value is stored');
  }
});

test('Codex resume uses only the flags `exec resume` lists; settings it cannot honour start a fresh session instead', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_RESUME_STRICT: '1', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.configure({ provider: 'codex', permission: 'read' });
  await turn(assistant, events, 'one');
  await turn(assistant, events, 'two');
  let calls = await ctx.invocations();
  assert.equal((await ctx.invocations()).length, 2, 'the strict resume was accepted without a fallback');
  assert.ok(calls[1].args.includes('resume'));
  for (const flag of ['--skip-git-repo-check', '-m', '--dangerously-bypass-approvals-and-sandbox']) assert.equal(calls[1].args.includes(flag), false, `${flag} is not listed by exec resume`);
  assert.ok(calls[1].args.includes('--sandbox') && calls[1].args.includes('read-only'), 'the permission mode is still enforced');
  await assistant.configure({ model: 'gpt-test' });
  await turn(assistant, events, 'three');
  calls = await ctx.invocations();
  assert.equal(calls.at(-1).args.includes('resume'), false, 'a model choice cannot be passed to resume, so the session is not reused');
  assert.ok(calls.at(-1).args.includes('-m'));
  await assistant.configure({ permission: 'yolo', model: '' });
  await turn(assistant, events, 'four');
  assert.equal((await ctx.invocations()).at(-1).args.includes('resume'), false, 'full access needs a flag resume does not list');
});

test('a message sent while /clear is still being written is refused, and cannot bring the old session back', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  await turn(assistant, events, 'one');
  const clearing = assistant.clear();
  await assert.rejects(assistant.send({ text: 'racing' }), { code: 'busy' });
  await assert.rejects(assistant.clear(), { code: 'busy' });
  await clearing;
  await turn(assistant, events, 'after');
  assert.equal((await ctx.invocations()).at(-1).args.includes('--resume'), false);
  assert.deepEqual((await assistant.history()).map(m => m.text).slice(0, 1), ['after']);
});

test('what the session did not see is handed over when it is resumed', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  await turn(assistant, events, 'one');
  ctx.env.FAKE_SCENARIO = 'fail';
  await turn(assistant, events, 'missed question');
  ctx.env.FAKE_SCENARIO = 'stream';
  await turn(assistant, events, 'three');
  const call = (await ctx.invocations()).at(-1);
  assert.ok(call.args.includes('--resume'));
  assert.equal(call.stdin, 'User: missed question\n\nUser: three', 'the unseen message comes first; seen turns are not repeated');
});

test('clear discards the transcript and the session together, survives a restart and starts a fresh session', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
  const first = makeAssistant(t, ctx);
  await turn(first.assistant, first.events, 'one');
  assert.ok((await stored(ctx)).sessions.main);
  await first.assistant.clear();
  const afterClear = await stored(ctx);
  assert.deepEqual(afterClear.conversations.main, []);
  assert.equal(afterClear.sessions.main, undefined);
  await first.assistant.close();
  const second = makeAssistant(t, ctx);
  assert.deepEqual(await second.assistant.history(), []);
  await turn(second.assistant, second.events, 'again');
  const calls = await ctx.invocations();
  assert.equal(calls.at(-1).args.includes('--resume'), false, 'the next message starts a fresh session');
  assert.equal(calls.at(-1).stdin, 'User: again');
});

test('clear is refused while a reply runs, loses nothing, and a late reply cannot revive cleared messages', async t => {
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'hang' });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.send({ text: 'running' });
  await until(events, 'delta');
  await assert.rejects(assistant.clear(), { code: 'busy' });
  assert.equal((await assistant.history()).at(0).text, 'running');
  assistant.cancel();
  await settled(events);
  assert.equal((await assistant.history()).length, 2, 'the stopped reply is kept');
  await assistant.clear();
  assert.deepEqual(await assistant.history(), [], 'nothing from the finished reply comes back');
  await new Promise(resolve => setTimeout(resolve, 300));
  assert.deepEqual(await assistant.history(), []);
});

test('history files written before sessions existed still load, and session data never contains a credential', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', CLAUDE_CODE_OAUTH_TOKEN: SECRET });
  const { mkdir, writeFile } = await import('node:fs/promises');
  await mkdir(path.join(ctx.home, '.afm', 'desktop'), { recursive: true });
  await writeFile(path.join(ctx.home, '.afm', 'desktop', 'assistant-history.json'), JSON.stringify({ conversations: { main: [{ id: 'u', role: 'user', text: 'old chat' }] } }));
  const { assistant, events } = makeAssistant(t, ctx);
  assert.equal((await assistant.history())[0].text, 'old chat');
  await turn(assistant, events, 'new');
  const file = await readFile(path.join(ctx.home, '.afm', 'desktop', 'assistant-history.json'), 'utf8');
  assert.equal(file.includes(SECRET), false);
  assert.equal(JSON.parse(file).conversations.main.length, 3);
  assert.deepEqual(Object.keys(JSON.parse(file).sessions.main).sort(), ['credentialKey', 'model', 'providerId', 'scopeKey', 'sessionId', 'upTo']);
});

test('the parsers report the provider session id from real transcripts, and an id that could pass as a flag is ignored', async t => {
  const { ClaudeParser, CodexParser } = await import('../src/assistant/stream.js');
  const claude = new ClaudeParser();
  assert.deepEqual(claude.parse('{"type":"system","subtype":"init","session_id":"c-1"}'), [{ type: 'session', id: 'c-1' }]);
  assert.deepEqual(claude.parse('{"type":"result","subtype":"success","result":"ok","session_id":"c-1"}').map(e => e.type), ['result'], 'an unchanged id is reported once');
  assert.deepEqual(claude.parse('{"type":"result","subtype":"success","result":"ok","session_id":"c-2"}').map(e => e.type), ['session', 'result'], 'a changed id is reported again');
  assert.deepEqual(new CodexParser().parse('{"type":"thread.started","thread_id":"t-9"}'), [{ type: 'session', id: 't-9' }]);
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_SESSION: '--dangerously-skip-permissions' });
  const { assistant, events } = makeAssistant(t, ctx);
  await turn(assistant, events, 'one');
  await turn(assistant, events, 'two');
  assert.equal((await stored(ctx)).sessions?.main, undefined, 'a session id that is not a plain token is not kept');
  assert.equal((await ctx.invocations()).at(-1).args.includes('--resume'), false);
});

const resumeArg = kind => kind === 'claude' ? '--resume' : 'resume';
const historyFile = ctx => path.join(ctx.home, '.afm', 'desktop', 'assistant-history.json');

for (const kind of ['claude', 'codex']) {
  test(`${kind}: a native CLI login cannot be told from another account, so its sessions are never resumed or kept`, async t => {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5' });
    // A session kept under a key, then marked as if its credential were unknown: it must not carry over to a native login.
    const keyed = makeAssistant(t, ctx, { env: async () => ({ ...ctx.env, ...API_KEYS }) });
    await keyed.assistant.configure({ provider: kind });
    await turn(keyed.assistant, keyed.events, 'with a key');
    await keyed.assistant.close();
    const history = await stored(ctx);
    history.sessions.main.credentialKey = null;
    await writeFile(historyFile(ctx), JSON.stringify(history));
    const login = path.join(ctx.home, kind === 'claude' ? '.claude/.credentials.json' : '.codex/auth.json');
    await mkdir(path.dirname(login), { recursive: true });
    await writeFile(login, JSON.stringify({ account: 'A' }));
    const first = makeAssistant(t, ctx);
    await first.assistant.configure({ provider: kind });
    await first.assistant.scan();
    await turn(first.assistant, first.events, 'as A');
    assert.equal((await stored(ctx)).sessions?.main, undefined, 'the earlier session is forgotten');
    await writeFile(login, JSON.stringify({ account: 'B' }));
    await turn(first.assistant, first.events, 'as B, detection cached');
    await first.assistant.scan();
    await turn(first.assistant, first.events, 'as B, after a rescan');
    await first.assistant.close();
    const second = makeAssistant(t, ctx);
    await second.assistant.configure({ provider: kind });
    await turn(second.assistant, second.events, 'as B, after a restart');
    const calls = await ctx.invocations();
    assert.deepEqual(calls.map(call => call.args.includes(resumeArg(kind))), [false, false, false, false, false]);
    assert.match(calls.at(-1).stdin, /User: as A[\s\S]*User: as B, after a rescan[\s\S]*User: as B, after a restart$/, 'a fresh session gets the saved chat');
    assert.equal((await stored(ctx)).sessions?.main, undefined, 'no session is kept for a login Desktop cannot identify');
  });
}

test('rotating the key inside the same key file starts a fresh session, also after a restart', async t => {
  for (const kind of ['claude', 'codex']) {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5' });
    const keyFile = path.join(ctx.home, 'key');
    await writeFile(keyFile, 'sk-file-first-credential-123456\n', { mode: 0o600 });
    const first = makeAssistant(t, ctx);
    await first.assistant.addProvider({ name: 'Keyed', kind, executable: path.join(ctx.bin, kind), credential: { type: 'file', ref: keyFile } });
    await first.assistant.configure({ provider: (await first.assistant.state()).settings.providers.at(-1).id });
    const resumes = async () => (await ctx.invocations()).map(call => call.args.includes(resumeArg(kind)));
    await turn(first.assistant, first.events, 'one');
    await turn(first.assistant, first.events, 'two');
    assert.deepEqual(await resumes(), [false, true], `${kind}: a session under a key file is resumed`);
    await writeFile(keyFile, 'sk-file-rotated-credential-654321\n');
    await turn(first.assistant, first.events, 'three');
    assert.equal((await resumes()).at(-1), false, `${kind}: a new key in the same file is a new account`);
    await first.assistant.close();
    const second = makeAssistant(t, ctx);
    await turn(second.assistant, second.events, 'four');
    assert.equal((await resumes()).at(-1), true, `${kind}: the new session is resumed after a restart`);
    const file = await readFile(historyFile(ctx), 'utf8');
    assert.equal(file.includes('first-credential') || file.includes('rotated-credential'), false, 'no key value is stored');
  }
});

for (const kind of ['claude', 'codex']) {
  test(`${kind}: a session id in a tool call, its result, text, an error or stderr never reaches the window or the saved chat`, async t => {
    const id = 'sess-private-0001';
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_SESSION: id, ...API_KEYS });
    const { assistant, events } = makeAssistant(t, ctx);
    await assistant.configure({ provider: kind });
    await turn(assistant, events, 'one');
    ctx.env.FAKE_SCENARIO = 'leak-session';
    const failed = await turn(assistant, events, 'two');
    assert.match(failed.message, /failed in \[hidden\]$/);
    assert.equal(events.find(e => e.type === 'tool').tool.input.includes('[hidden]'), true, 'the tool input is shown without the id');
    assert.match(events.find(e => e.type === 'tool_result').text, /seen \[hidden\]/);
    ctx.env.FAKE_SCENARIO = 'stderr-session';
    assert.match((await turn(assistant, events, 'three')).message, /exited with code 3\. crashed in \[hidden\]/);
    assert.equal((JSON.stringify(events) + JSON.stringify(await assistant.history())).includes(id), false, 'the session id reached the window');
    const calls = await ctx.invocations();
    assert.deepEqual(calls.slice(1).map(call => call.args.filter(arg => arg === id).length), [1, 1], 'each resume still names the id once');
  });

  test(`${kind}: a short session id is hidden too, and text that only looked like its start is not lost`, async t => {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_SESSION: 's7', FAKE_SCENARIO: 'echo-session', ...API_KEYS });
    const { assistant, events } = makeAssistant(t, ctx);
    await assistant.configure({ provider: kind });
    const end = await turn(assistant, events, 'hello');
    const streamed = events.filter(e => e.type === 'delta').map(e => e.text).join('');
    assert.equal(streamed, 'Session: [hidden] ends s');
    assert.equal(end.reply.text, streamed);
    assert.equal((await assistant.history()).at(-1).text, streamed);
  });
}

test('a session id split across two deltas, further apart than the batching interval, never reaches the window', async t => {
  const ctx = await providerHome(t, { FAKE_SESSION: 'sess-split-private-0001', FAKE_SCENARIO: 'split-session', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  const end = await turn(assistant, events, 'hello');
  assert.deepEqual(events.filter(e => e.type === 'delta').map(e => e.text), ['Session: ', '[hidden] done'], 'the text before the id is not held back');
  assert.equal(end.reply.text, 'Session: [hidden] done');
  assert.equal((await assistant.history()).at(-1).text, 'Session: [hidden] done');
});

test('text held back in case it began a session id is kept when the reply is stopped', async t => {
  const ctx = await providerHome(t, { FAKE_SESSION: 's7', FAKE_SCENARIO: 'split-session', FAKE_HANG_AFTER_SPLIT: '1', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  await assistant.send({ text: 'hello' });
  await until(events, 'delta');
  await new Promise(resolve => setTimeout(resolve, 150));
  assistant.cancel();
  const end = await until(events, 'cancelled');
  assert.equal(end.reply.text, 'Session: s');
  assert.equal(events.filter(e => e.type === 'delta').map(e => e.text).join(''), 'Session: s');
});

// Codex sends whole messages and separates them, so an id can be split across deltas only by Claude.
for (const between of ['tool', 'approval']) {
  test(`a session id split around ${between === 'tool' ? 'a tool call' : 'a request for approval'} never reaches the window`, async t => {
    const id = 'sess-split-private-0001';
    const key = API_KEYS.ANTHROPIC_API_KEY;
    const ctx = await providerHome(t, { FAKE_SESSION: id, FAKE_SCENARIO: 'split-around', FAKE_BETWEEN: between, FAKE_SECRET: key, ...API_KEYS });
    const { assistant, events } = makeAssistant(t, ctx, { context: stubContext({ endpoint: async () => 'http://127.0.0.1:9' }) });
    assistant.onEvent(event => { if (event.type === 'approval') assistant.approve(event.approvalId, true); });
    const end = await turn(assistant, events, 'hello');
    const text = 'Session: [hidden]' + (between === 'tool' ? ' done' : ' approved');
    assert.equal(end.type, 'done', end.message);
    // The approval shows the tool's arguments, which must name neither the id nor the key.
    assert.deepEqual({ streamed: events.filter(e => e.type === 'delta').map(e => e.text).join(''), approval: events.find(e => e.type === 'approval')?.summary },
      { streamed: text, approval: between === 'approval' ? '{"note":"[hidden] [hidden]"}' : undefined });
    assert.equal(end.reply.text, text);
    const seen = JSON.stringify(events) + JSON.stringify(await assistant.history());
    assert.equal(seen.includes(id) || seen.includes(key), false, 'the session id or the key reached the window');
  });
}

test('text held back across a tool call is shown once the next text shows it is not a session id, or when the reply ends', async t => {
  const ctx = await providerHome(t, { FAKE_SESSION: 'sess-split-private-0001', FAKE_SCENARIO: 'split-around', FAKE_AFTER: 'lain text', ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  const streamed = () => events.filter(e => e.type === 'delta').map(e => e.text).join('');
  let end = await turn(assistant, events, 'continued');
  assert.equal(streamed(), 'Session: sess-split-plain text');
  assert.equal(end.reply.text, 'Session: sess-split-plain text');
  ctx.env.FAKE_AFTER = '';
  events.length = 0;
  end = await turn(assistant, events, 'ended');
  assert.equal(end.type, 'done', end.message);
  assert.equal(streamed(), 'Session: sess-split-p');
  assert.equal(end.reply.text, 'Session: sess-split-p');
});

for (const kind of ['claude', 'codex']) {
  test(`${kind}: a reply that is only the start of the session id is shown once and ends as a reply`, async t => {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_SESSION: 'abcdef', FAKE_SCENARIO: 'session-prefix', FAKE_PREFIX: '3', ...API_KEYS });
    const { assistant, events } = makeAssistant(t, ctx);
    await assistant.configure({ provider: kind });
    const end = await turn(assistant, events, 'hello');
    assert.equal(end.type, 'done', end.message);
    assert.deepEqual(events.filter(e => e.type === 'delta').map(e => e.text), ['abc']);
    assert.equal(end.reply.text, 'abc');
    assert.equal((await assistant.history()).at(-1).text, 'abc');
    assert.equal((await stored(ctx)).sessions.main.sessionId, 'abcdef', 'the session of a successful reply is kept');
  });
}

test('when a refused session falls back to a fresh one, neither id reaches the window', async t => {
  const [oldId, newId] = ['sess-old-private-0001', 'sess-new-private-0002'];
  const ctx = await providerHome(t, { FAKE_PAUSE: '5', FAKE_SESSION: oldId, ...API_KEYS });
  const { assistant, events } = makeAssistant(t, ctx);
  await turn(assistant, events, 'one');
  Object.assign(ctx.env, { FAKE_EXPIRED: '1', FAKE_SESSION: newId, FAKE_MENTION: oldId, FAKE_SCENARIO: 'leak-session' });
  const end = await turn(assistant, events, 'two');
  assert.equal((await ctx.invocations()).length, 3, 'one refused resume, one fallback');
  assert.match(end.message, /failed in \[hidden\] \[hidden\]$/);
  const seen = JSON.stringify(events) + JSON.stringify(await assistant.history());
  for (const id of [oldId, newId]) assert.equal(seen.includes(id), false, `${id} reached the window`);
});

test('a session is resumed only while everything it did not see fits the transcript budget', async t => {
  for (const kind of ['claude', 'codex']) {
    const ctx = await providerHome(t, { FAKE_PAUSE: '5', ...API_KEYS });
    const { assistant, events } = makeAssistant(t, ctx);
    await assistant.configure({ provider: kind });
    const seed = async messages => {
      const history = await stored(ctx);
      history.conversations.main.push(...messages);
      await writeFile(historyFile(ctx), JSON.stringify(history));
    };
    await turn(assistant, events, 'anchor');
    // Failed and empty replies are never handed over, so however large they are they do not count.
    await seed([{ id: 'u1', role: 'user', text: 'small unseen' }, { id: 'e1', role: 'assistant', status: 'error', text: 'x'.repeat(30000) }, { id: 'n1', role: 'assistant', text: '' }]);
    await turn(assistant, events, 'next');
    let call = (await ctx.invocations()).at(-1);
    assert.ok(call.args.includes(resumeArg(kind)), `${kind}: a catch-up that fits is resumed`);
    assert.equal(call.stdin.slice(kind === 'codex' ? call.stdin.indexOf('\n\n') + 2 : 0), 'User: small unseen\n\nUser: next');
    await seed(Array.from({ length: 24 }, (_, i) => ({ id: 'c' + i, role: i % 2 ? 'assistant' : 'user', text: `unseen-${i} ` + 'x'.repeat(1990) })));
    await turn(assistant, events, 'now');
    call = (await ctx.invocations()).at(-1);
    assert.equal(call.args.includes(resumeArg(kind)), false, `${kind}: a session that would miss unseen messages is not resumed`);
    assert.match(call.stdin, /unseen-23[\s\S]*User: now$/, 'the fresh session gets the bounded transcript');
    assert.ok(call.stdin.length < 25000);
  }
});

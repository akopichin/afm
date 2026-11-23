import { AssistantSettings } from '../src/assistant/settings.js';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { probeAcp, runAcp } from '../src/assistant/acp.js';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { makeAssistant, SAMPLE_IMAGE, settled, stubContext } from './assistant-support.js';

const AGENT = `
import { createInterface } from 'node:readline';
const send = packet => console.log(JSON.stringify({ jsonrpc: '2.0', ...packet }));
const settings = [
  { type: 'select', category: 'model', id: 'm', name: 'Model', currentValue: 'example', options: [{ value: 'example', name: 'Example' }, { value: 'other', name: 'Other' }] },
  { type: 'select', category: 'thought_level', id: 'r', name: 'Reasoning', currentValue: 'low', options: [{ value: 'low', name: 'Low' }, { value: 'high', name: 'High' }] },
  { type: 'select', category: 'mode', id: 'access', name: 'Access', currentValue: 'readonly', options: [{ value: 'readonly', name: 'Read only' }] },
];
let promptId;
const notify = update => send({ method: 'session/update', params: { sessionId: 'fake-session', update } });
const update = text => notify({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } });
for await (const line of createInterface({ input: process.stdin })) {
  const msg = JSON.parse(line);
  const result = result => send({ id: msg.id, result });
  if (msg.method === 'initialize') result({ protocolVersion: 1, agentInfo: { name: 'fake', version: '0.1' }, agentCapabilities: { promptCapabilities: { image: true } } });
  if (msg.method === 'session/new') result({ sessionId: 'fake-session', configOptions: settings });
  if (msg.method === 'session/set_config_option') {
    settings.find(option => option.id === msg.params.configId).currentValue = msg.params.value;
    if (settings[0].currentValue === 'other' && msg.params.configId === 'm')
      Object.assign(settings[1], { currentValue: 'max', options: [{ value: 'max', name: 'Max' }] });
    result({ configOptions: settings });
  }
  if (msg.method === 'session/prompt') {
    promptId = msg.id;
    if (settings[0].currentValue === 'other') {
      if (settings[1].currentValue !== 'max' || !msg.params.prompt[0].text.startsWith('Reply with OK'))
        send({ id: promptId, error: { code: -32602, message: 'Unexpected model test' } });
      else {
        if (!process.argv.includes('empty-reply')) update('OK');
        result({ stopReason: 'end_turn' });
      }
      continue;
    }
    notify({ sessionUpdate: 'subagent_update', sessionId: 'child-session-1', title: 'Reporter', state: { state: 'running' } });
    send({ method: 'session/update', params: { sessionId: 'child-session-1', update: {
      sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Check the child task.' } } } });
    for (const [sessionUpdate, status] of [['tool_call', 'in_progress'], ['tool_call_update', 'completed']]) {
      send({ method: 'session/update', params: { sessionId: 'child-session-1', update: {
        sessionUpdate, toolCallId: 'child-bash', title: 'Bash', rawInput: { command: 'pwd' }, status } } });
    }
    if (settings[1].currentValue !== 'high' || !msg.params.prompt.some(content => content.type === 'image')) {
      send({ id: promptId, error: { code: -32602, message: 'Missing image or reasoning' } });
      continue;
    }
    update('First ');
    update('update.');
    for (const toolCallId of ['a', 'b']) notify({ sessionUpdate: 'tool_call', toolCallId, title: toolCallId, status: 'in_progress' });
    notify({ sessionUpdate: 'tool_call_update', toolCallId: 'b', status: 'failed' });
    send({ id: 'permission', method: 'session/request_permission', params: { sessionId: 'child-session-1', toolCall: { toolCallId: 'command', title: 'Read project', kind: 'read' }, options: [
      { optionId: 'allow', name: 'Allow once', kind: 'allow_once' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
    ] } });
  }
  if (msg.id === 'permission' && msg.result?.outcome?.optionId === 'allow') {
    update(' Second update.');
    notify({ sessionUpdate: 'subagent_update', sessionId: 'child-session-1', state: { state: 'idle', stopReason: 'end_turn' } });
    notify({ sessionUpdate: 'tool_call_update', toolCallId: 'a', status: 'failed' });
    send({ id: promptId, result: { stopReason: 'end_turn' } });
  }
}
`;

test('ACP provider discovers controls, sends a selected model and image, and awaits approval', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'acp-test-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const fake = path.join(home, 'agent.mjs');
  await writeFile(fake, AGENT);
  const { assistant } = makeAssistant(t, { home, env: process.env });
  const id = 'test-acp';
  await assistant.settings.addProvider({ name: 'Test ACP', kind: 'acp', executable: process.execPath, args: [fake] }, id);
  const check = await assistant.testProvider(id);
  assert.equal(check.ok, true, check.auth.detail);
  assert.match(check.version, /ACP v1/);
  assert.equal(check.capabilities.image, true);
  assert.deepEqual((await assistant.state()).providers.find(item => item.id === id).reasoning, ['low', 'high']);
  await assistant.configure({ permission: 'yolo' });
  const { settings } = await assistant.configure({ provider: id, model: 'example', reasoning: 'high', acpMode: 'readonly' });
  assert.equal(settings.permission, 'ask');
  const now = Date.now();
  const clock = t.mock.method(Date, 'now', () => now);
  const outcome = Promise.withResolvers();
  assistant.onEvent(event => {
    if (['done', 'error', 'cancelled'].includes(event.type)) outcome.resolve(event);
    if (event.type !== 'approval') return;
    clock.mock.mockImplementation(() => now + 31000);
    assistant.approve(event.approvalId, true);
  });
  await assistant.send({ text: 'Read this image.', images: [SAMPLE_IMAGE] });
  const done = await outcome.promise;
  assert.equal(done.type, 'done', done.message);
  assert.equal(done.reply.text, 'First update. Second update.');
  const saved = (await assistant.history()).at(-1);
  assert.deepEqual(saved.delegates.map(({ name, status, reasoning }) => ({ name, status, reasoning })),
    [{ name: 'Reporter', status: 'done', reasoning: 'Check the child task.' }]);
  assert.doesNotMatch(saved.text, /Check the child task/);
  assert.deepEqual(saved.delegates[0].tools.map(({ name, status, input }) => ({ name, status, input })),
    [{ name: 'Bash', status: 'done', input: '{"command":"pwd"}' }]);
  assert.ok(!saved.tools.some(tool => tool.name === 'Bash'));
  assert.doesNotMatch(JSON.stringify(saved.delegates), /child-session-1/);
  assert.deepEqual(saved.tools.map(({ id, offset, order }) => ({ id, offset, order })), [
    { id: 'a', offset: 'First update.'.length, order: 1 },
    { id: 'b', offset: 'First update.'.length, order: 2 },
  ]);
  assert.deepEqual(saved.toolErrors, [
    { id: 'b', offset: 'First update.'.length, at: new Date(now).toISOString(), order: 3 },
    { id: 'a', offset: 'First update. Second update.'.length, at: new Date(now + 31000).toISOString(), order: 4 },
  ]);
  assert.deepEqual(saved.segments, [
    { offset: 0, at: new Date(now).toISOString() }, { offset: 13, at: new Date(now + 31000).toISOString() },
  ]);
});

test('a reply probe discovers ACP reasoning levels after changing the model', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'acp-test-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const fake = path.join(home, 'agent.mjs');
  await writeFile(fake, AGENT);
  const { assistant } = makeAssistant(t, { home, env: process.env });
  const id = 'model-test-acp';
  await assistant.settings.addProvider({ name: 'Test ACP', kind: 'acp', executable: process.execPath, args: [fake] }, id);
  await assistant.configure({ provider: id, model: 'other' });
  assert.deepEqual(await assistant.testModel(), { ok: true, detail: 'Test request succeeded.', levels: ['max'] });
  const empty = 'empty-reply-acp';
  await assistant.settings.addProvider({ name: 'Empty ACP', kind: 'acp',
    executable: process.execPath, args: [fake, 'empty-reply'] }, empty);
  await assistant.configure({ provider: empty, model: 'other' });
  assert.deepEqual(await assistant.testModel(), { ok: false, detail: 'The ACP agent returned no model response.', levels: ['max'] });
  assert.deepEqual(await assistant.history(), []);
});


test('remote ACP authenticates before opening a session and streams across the Docker bridge', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'remote-acp-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const fake = path.join(home, 'agent.mjs');
  const remoteAgent = AGENT.replace("if (msg.method === 'session/new') result", "if (msg.method === 'session/new' && msg.params.cwd !== process.cwd()) throw Error('Workspace mismatch');\n  if (msg.method === 'session/new') result")
    .replace('promptId = msg.id;', `promptId = msg.id;
      const text = msg.params.prompt.find(item => item.type === 'text')?.text;
      if (text.endsWith('Tell me how the backlog works.') &&
        (!text.includes('Scope: test-run') || !text.includes('/backlog list done') || !text.includes('this reply has no backlog write tool'))) {
        throw Error('Missing Desktop backlog guidance');
      }`);
  await writeFile(fake, "if (process.env.ACP_BRIDGE_TOKEN) process.exit(1);\n" + remoteAgent);
  const nested = path.join(await realpath(home), 'nested');
  await mkdir(nested);
  await symlink(path.dirname(await realpath(home)), path.join(home, 'outside'), 'dir');
  const bridge = fileURLToPath(new URL('../remote-acp/bridge.mjs', import.meta.url));
  const token = 'fixture-bridge-token';
  const child = spawn(process.execPath, [bridge, fake], {
    env: { ...process.env, ACP_LISTEN: '127.0.0.1', ACP_WORKSPACE: home, ACP_PORT: '0', ACP_AGENT: process.execPath, ACP_BRIDGE_TOKEN: token },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  const closed = once(child, 'close');
  t.after(async () => { child.kill(); await closed; });
  const [line] = await once(child.stdout, 'data', { signal: AbortSignal.timeout(5000) });
  const port = Number(line.toString().match(/port (\d+)/)?.[1]);
  assert(port);
  const provider = { endpoint: `ws://127.0.0.1:${port}/acp`, remoteCwd: home };
  await assert.rejects(probeAcp(provider, {}, 'wrong'));
  assert.equal((await probeAcp(provider, {}, token)).capabilities.image, true);
  const { assistant, events: chatEvents } = makeAssistant(t, { home, env: process.env }, { context: stubContext({
    runs: async () => [{ run_id: 'test-run', instance_id: 'instance', project_root: home }],
  }) });
  const id = 'test-remote';
  const credential = { type: 'file', ref: path.join(home, 'bridge-key') };
  await writeFile(credential.ref, token);
  await assistant.settings.addProvider({ ...provider, name: 'Remote', kind: 'acp', credential, remoteCwd: await realpath(home) }, id);
  await assistant.configure({ provider: id, model: 'example' });
  await assert.rejects(assistant.updateProviderWorkspace(id, path.join(home, 'outside')), /inside the ACP bridge workspace root/);
  assert.equal((await assistant.settings.get()).providers[0].remoteCwd, await realpath(home));
  const info = await assistant.updateProviderWorkspace(id, nested);
  assert.equal(info.providers.find(item => item.id === id).remoteCwd, nested);
  assert.deepEqual(info.settings.providers[0].credential, credential);
  assert.equal(info.settings.provider, id);
  assert.equal(info.settings.model, 'example');
  const events = [];
  const run = runAcp({ provider, token, cwd: home, env: {}, prompt: 'test', images: [SAMPLE_IMAGE],
    settings: { permission: 'ask', model: 'example', reasoning: 'high' },
    onEvent: event => events.push(event), approve: async () => true });
  assert.ifError((await run.done).error);
  assert.equal(events.filter(event => event.type === 'delta').map(event => event.text).join(''), 'First update. Second update.');
  await assistant.configure({ reasoning: 'high' });
  assistant.onEvent(event => { if (event.type === 'approval') assistant.approve(event.approvalId, true); });
  await assistant.send({ text: 'Tell me how the backlog works.', images: [SAMPLE_IMAGE], scope: { runId: 'test-run', instanceId: 'instance' } });
  const result = await settled(chatEvents);
  assert.equal(result.type, 'done', result.message);
});


test('switching the Codex transport keeps permissions and resets the old model selection', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'codex-transport-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const store = new AssistantSettings(home);
  await store.select({ provider: 'codex', model: 'cli-model', reasoning: 'medium', permission: 'yolo' });
  const settings = await store.select({ codexTransport: 'acp' });
  assert.equal(settings.model, '');
  assert.equal(settings.reasoning, 'default');
  assert.equal(settings.permission, 'yolo');
  assert.equal(settings.codexTransport, 'acp');
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { makeAssistant, SAMPLE_IMAGE } from './assistant-support.js';

const AGENT = `
import { createInterface } from 'node:readline';
const send = packet => console.log(JSON.stringify({ jsonrpc: '2.0', ...packet }));
const settings = [
  { type: 'select', category: 'model', id: 'm', name: 'Model', currentValue: 'example', options: [{ value: 'example', name: 'Example' }] },
  { type: 'select', category: 'thought_level', id: 'r', name: 'Reasoning', currentValue: 'low', options: [{ value: 'low', name: 'Low' }, { value: 'high', name: 'High' }] },
  { type: 'select', category: 'mode', id: 'access', name: 'Access', currentValue: 'readonly', options: [{ value: 'readonly', name: 'Read only' }] },
];
let promptId;
const update = text => send({ method: 'session/update', params: { sessionId: 'fake-session', update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } } } });
for await (const line of createInterface({ input: process.stdin })) {
  const msg = JSON.parse(line);
  const result = result => send({ id: msg.id, result });
  if (msg.method === 'initialize') result({ protocolVersion: 1, agentInfo: { name: 'fake', version: '0.1' }, agentCapabilities: { promptCapabilities: { image: true } } });
  if (msg.method === 'session/new') result({ sessionId: 'fake-session', configOptions: settings });
  if (msg.method === 'session/set_config_option') {
    settings.find(option => option.id === msg.params.configId).currentValue = msg.params.value;
    result({ configOptions: settings });
  }
  if (msg.method === 'session/prompt') {
    promptId = msg.id;
    if (settings[1].currentValue !== 'high' || !msg.params.prompt.some(content => content.type === 'image')) {
      send({ id: promptId, error: { code: -32602, message: 'Missing image or reasoning' } });
      continue;
    }
    update('First ');
    update('update.');
    send({ id: 'permission', method: 'session/request_permission', params: { sessionId: 'fake-session', toolCall: { toolCallId: 'command', title: 'Read project', kind: 'read' }, options: [
      { optionId: 'allow', name: 'Allow once', kind: 'allow_once' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
    ] } });
  }
  if (msg.id === 'permission' && msg.result?.outcome?.optionId === 'allow') {
    update(' Second update.');
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
  assert.deepEqual(saved.segments, [
    { offset: 0, at: new Date(now).toISOString() }, { offset: 13, at: new Date(now + 31000).toISOString() },
  ]);
});

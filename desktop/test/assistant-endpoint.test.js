import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { registerAssistantIpc } from '../src/assistant/ipc.js';
import { API_KEYS, makeAssistant, providerHome, SAMPLE_IMAGE, settled, until } from './assistant-support.js';
import { eventually } from './support.js';

const KEY = 'endpoint-key-abcdefgh';

// An OpenAI-compatible server that streams its reply slowly and records what it was sent.
async function endpoint(t, { status = 200, hang = false } = {}) {
  const seen = [];
  const server = createServer(async (req, res) => {
    if (req.url === '/v1/models') return res.end(JSON.stringify({ data: [] }));
    let body = '';
    for await (const chunk of req) body += chunk;
    seen.push({ url: req.url, auth: req.headers.authorization, body: JSON.parse(body || '{}') });
    if (status !== 200) { res.writeHead(status).end('no'); return; }
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    for (const part of ['One ', 'two ', 'three']) {
      res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: part } }] })}\n\n`);
      await delay(hang ? 5000 : 100);
    }
    res.end('data: [DONE]\n\n');
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); server.close(); });
  return { url: `http://127.0.0.1:${server.address().port}/v1`, seen };
}

async function withProvider(t, server, credential, vars = {}) {
  const ctx = await providerHome(t, vars);
  const made = makeAssistant(t, ctx);
  await made.assistant.addProvider({ name: 'Local model', kind: 'openai', endpoint: server.url, credential });
  const id = (await made.assistant.state()).providers.find(p => !p.builtIn).id;
  await made.assistant.configure({ provider: id, model: 'tiny-model' });
  return { ...made, ctx, id };
}

test('an endpoint provider streams its reply with a key read from the referenced variable, never from argv or settings', async t => {
  const server = await endpoint(t);
  const { assistant, events, ctx } = await withProvider(t, server, { type: 'env', ref: 'LOCAL_KEY' }, { LOCAL_KEY: KEY });
  await assistant.send({ text: 'hello', images: [SAMPLE_IMAGE] });
  await settled(events);
  assert.equal(events.filter(e => e.type === 'delta').map(e => e.text).join(''), 'One two three');
  assert.equal(server.seen[0].url, '/v1/chat/completions');
  assert.equal(server.seen[0].auth, 'Bearer ' + KEY);
  assert.equal(server.seen[0].body.model, 'tiny-model');
  assert.equal(server.seen[0].body.stream, true);
  assert.deepEqual(server.seen[0].body.messages.at(-1), { role: 'user', content: [
    { type: 'image_url', image_url: { url: `data:image/png;base64,${SAMPLE_IMAGE.data}` } },
    { type: 'text', text: 'hello' },
  ] });
  assert.equal((await readFile(path.join(ctx.home, '.afm/desktop/assistant.json'), 'utf8')).includes(KEY), false);
  assert.equal(JSON.stringify(await assistant.state()).includes(KEY), false);
  assert.equal((await assistant.state()).effective.flags, 'Chat only: no files and no AFM tools');
});

test('an endpoint key can come from a key file; a missing key stops before any request', async t => {
  const server = await endpoint(t);
  const keyFile = path.join((await providerHome(t)).home, 'key');
  await writeFile(keyFile, KEY + '\n');
  const first = await withProvider(t, server, { type: 'file', ref: keyFile });
  await first.assistant.send({ text: 'x' });
  await settled(first.events);
  assert.equal(server.seen.at(-1).auth, 'Bearer ' + KEY);

  const second = await withProvider(t, server, { type: 'env', ref: 'NOT_SET' });
  const before = server.seen.length;
  await assert.rejects(second.assistant.send({ text: 'x' }), { code: 'missing_credentials' });
  assert.equal(server.seen.length, before);
  assert.equal((await second.assistant.testProvider(second.id)).ok, false);
});

test('Deep Agent tests the selected effort without adding a chat message and sends it in actual replies', async t => {
  const requests = [];
  const model = 'dashscope/qwen3.8-max';
  const server = createServer(async (req, res) => {
    if (req.url === '/v1/models') return res.end(JSON.stringify({ data: [{ id: model }] }));
    if (req.url === '/model_group/info') return res.writeHead(404).end();
    if (req.url === '/model/info') return res.end(JSON.stringify({ data: [
      { model_name: model, model_info: { supported_reasoning_efforts: ['medium', 'xhigh'] } },
    ] }));
    let body = '';
    for await (const chunk of req) body += chunk;
    const request = JSON.parse(body);
    requests.push(request);
    if (request.reasoning_effort === 'medium') {
      res.writeHead(400, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ error: { message: 'LiteLLM does not support reasoning_effort' } }));
    }
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    const delta = { id: 'ok', object: 'chat.completion.chunk', created: 1, model,
      choices: [{ index: 0, delta: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }] };
    res.end(`data: ${JSON.stringify(delta)}\n\ndata: [DONE]\n\n`);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); server.close(); });
  const { assistant, events } = makeAssistant(t, await providerHome(t, { LOCAL_KEY: KEY }));
  const configured = await assistant.addProvider({
    name: 'Gateway', kind: 'openai-agent', endpoint: `http://127.0.0.1:${server.address().port}/v1`,
    credential: { type: 'env', ref: 'LOCAL_KEY' },
  });
  const provider = configured.providers.find(item => !item.builtIn);
  assert.deepEqual(provider.models[0].reasoning, ['medium', 'xhigh']);
  await assistant.configure({ provider: provider.id, model, reasoning: 'medium' });
  assert.match((await assistant.testModel()).detail, /rejected reasoning “medium”/);
  assert.equal((await assistant.testModel(false, { provider: provider.id, model, reasoning: 'xhigh' })).ok, true);
  assert.equal((await assistant.state()).settings.reasoning, 'medium');
  await assistant.configure({ reasoning: 'xhigh' });
  assert.equal((await assistant.testModel()).ok, true);
  assert.equal((await assistant.testModel()).ok, true);
  assert.deepEqual(requests.map(request => request.reasoning_effort), ['medium', 'xhigh']);
  assert.equal(requests[1].tools, undefined);
  assert.deepEqual(await assistant.history(), []);
  await assistant.send({ text: 'User message' });
  await settled(events);
  assert.equal(requests.at(-1).reasoning_effort, 'xhigh');
});

test('an endpoint failure and a cancelled stream are reported as such', async t => {
  const failing = await withProvider(t, await endpoint(t, { status: 500 }), { type: 'env', ref: 'K' }, { K: KEY });
  await failing.assistant.send({ text: 'x' });
  assert.match((await until(failing.events, 'error')).message, /answered 500/);

  const slow = await withProvider(t, await endpoint(t, { hang: true }), { type: 'env', ref: 'K' }, { K: KEY });
  await slow.assistant.send({ text: 'x' });
  await until(slow.events, 'delta');
  slow.assistant.cancel();
  assert.equal((await until(slow.events, 'cancelled')).reply.text, 'One ');
});

test('a turn answered by an endpoint in between is handed to the resumed CLI session, not lost', async t => {
  const server = await endpoint(t);
  const { assistant, events, ctx, id } = await withProvider(t, server, { type: 'env', ref: 'LOCAL_KEY' }, { LOCAL_KEY: KEY, FAKE_PAUSE: '5', ...API_KEYS });
  const turn = async text => { const n = events.filter(e => e.type === 'done').length; await assistant.send({ text }); await eventually(() => events.filter(e => e.type === 'done').length > n, { what: 'a reply' }); };
  await assistant.configure({ provider: 'claude', model: '' });
  await turn('first, with the CLI');
  await assistant.configure({ provider: id, model: 'tiny-model' });
  await turn('second, with the endpoint');
  await assistant.configure({ provider: 'claude', model: '' });
  await turn('third, back on the CLI');
  const call = (await ctx.invocations()).at(-1);
  assert.ok(call.args.includes('--resume'));
  assert.match(call.stdin, /User: second, with the endpoint\n\nAssistant: One two three\n\nUser: third, back on the CLI$/);
  assert.doesNotMatch(call.stdin, /first, with the CLI/, 'what the session already saw is not repeated');
});

test('the window reaches the assistant only through narrow IPC calls that ignore extra fields', async t => {
  const ctx = await providerHome(t, { FAKE_PAUSE: '1' });
  const { assistant } = makeAssistant(t, ctx);
  const handlers = new Map(), published = [];
  const off = registerAssistantIpc((name, fn) => handlers.set(name, fn), assistant, event => published.push(event.type));
  t.after(off);
  const call = (name, ...args) => handlers.get(name)(...args);
  assert.deepEqual([...handlers.keys()].sort(), ['assistant:add-provider', 'assistant:approve', 'assistant:backlog', 'assistant:backlog-plans', 'assistant:cancel', 'assistant:clear', 'assistant:configure', 'assistant:conversations',
    'assistant:follow-up', 'assistant:history', 'assistant:mcp-import', 'assistant:mcp-list', 'assistant:mcp-remove', 'assistant:mcp-save', 'assistant:mcp-test',
    'assistant:new-conversation', 'assistant:remove-provider', 'assistant:scan', 'assistant:send', 'assistant:state', 'assistant:test-model', 'assistant:test-provider', 'assistant:update-provider-workspace']);
  await assert.rejects(call('assistant:update-provider-workspace', 'claude', '/workspace'), /Remote ACP provider no longer exists/);
  await call('assistant:configure', { permission: 'read' });
  await assert.rejects(call('assistant:configure', { executable: '/bin/sh' }), /Invalid assistant setting/);
  await call('assistant:send', { text: 'hi', images: [SAMPLE_IMAGE], scope: { projectId: undefined, extra: '/etc' }, settings: { permission: 'yolo' } });
  await eventuallyDone(published);
  assert.equal((await call('assistant:history')).length, 2);
  assert.deepEqual((await call('assistant:history')).map(m => m.role), ['user', 'assistant']);
  assert.equal((await call('assistant:history'))[0].imageCount, 1);
  assert.equal((await call('assistant:state')).settings.permission, 'read');
});
const eventuallyDone = async published => { for (let i = 0; i < 100 && !published.includes('done'); i++) await delay(100); assert.ok(published.includes('done')); };

import assert from 'node:assert/strict';
import http from 'node:http';
import test from 'node:test';
import { endpointModels, runDeepAgent } from '../src/assistant/deep-agent.js';

test('OpenAI agent discovers models and streams a reply through a prefixed endpoint', async t => {
  const received = [];
  const server = http.createServer(async (request, response) => {
    received.push({ path: request.url, key: request.headers.authorization });
    if (request.url === '/gateway/v1/models') return response.end(JSON.stringify({ data: [{ id: 'test-model' }] }));
    let body = '';
    for await (const chunk of request) body += chunk;
    assert.equal(JSON.parse(body).model, 'test-model');
    response.setHeader('Content-Type', 'text/event-stream');
    const chunk = { id: 'test', object: 'chat.completion.chunk', created: 1, model: 'test-model',
      choices: [{ index: 0, delta: { role: 'assistant', content: 'Hello Desktop' }, finish_reason: 'stop' }] };
    response.end('data: ' + JSON.stringify(chunk) + '\n\ndata: [DONE]\n\n');
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.close());
  const provider = { kind: 'openai-agent', endpoint: `http://127.0.0.1:${server.address().port}/gateway/v1` };
  const apiKey = 'test-token';
  assert.deepEqual(await endpointModels(provider, apiKey), [{ id: 'test-model', label: 'test-model' }]);
  const items = [];
  const { done } = runDeepAgent({ provider, apiKey, modelId: 'test-model', systemPrompt: 'Reply to the user.',
    messages: [{ role: 'user', content: 'Hello' }], onEvent: event => items.push(event) });
  const result = await done;
  assert.ifError(result.error);
  assert.equal(items.filter(item => item.type === 'delta').map(item => item.text).join(''), 'Hello Desktop');
  assert.deepEqual(received.map(item => item.path), ['/gateway/v1/models', '/gateway/v1/chat/completions']);
  assert(received.every(item => item.key === 'Bearer test-token'));
});

test('Anthropic agent discovers models without duplicating v1', async t => {
  const requests = [];
  const server = http.createServer(async (request, response) => {
    requests.push({ path: request.url, key: request.headers['x-api-key'], version: request.headers['anthropic-version'] });
    response.end(JSON.stringify({ data: [{ id: 'test-claude', display_name: 'Test Claude' }] }));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.close());
  const provider = { kind: 'anthropic-agent', endpoint: `http://127.0.0.1:${server.address().port}/gateway/v1` };
  assert.deepEqual(await endpointModels(provider, 'test-token'), [{ id: 'test-claude', label: 'Test Claude' }]);
  assert.deepEqual(requests, [{ path: '/gateway/v1/models', key: 'test-token', version: '2023-06-01' }]);
});

test('Anthropic agent streams a reply through the Messages API', async t => {
  let observed;
  const server = http.createServer(async (request, response) => {
    let body = '';
    for await (const chunk of request) body += chunk;
    observed = { url: request.url, key: request.headers['x-api-key'], body: JSON.parse(body) };
    response.setHeader('content-type', 'text/event-stream');
    const usage = { input_tokens: 2, output_tokens: 0 };
    const events = [
      { type: 'message_start', message: { id: 'm', type: 'message', role: 'assistant', model: 'test-claude',
        content: [], stop_reason: null, stop_sequence: null, usage } },
      { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Hello Claude' } },
      { type: 'content_block_stop', index: 0 },
      { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 2 } },
      { type: 'message_stop' },
    ];
    response.end(events.map(event => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join(''));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.close());
  const provider = { kind: 'anthropic-agent', endpoint: `http://127.0.0.1:${server.address().port}/gateway/v1` };
  const items = [];
  const { done } = runDeepAgent({ provider, apiKey: 'test-token', modelId: 'test-claude',
    systemPrompt: 'Reply to the user.', messages: [{ role: 'user', content: 'Hello' }], onEvent: event => items.push(event) });
  assert.ifError((await done).error);
  assert.equal(items.filter(item => item.type === 'delta').map(item => item.text).join(''), 'Hello Claude');
  assert.equal(observed.url, '/gateway/v1/messages');
  assert.equal(observed.key, 'test-token');
  assert.equal(observed.body.model, 'test-claude');
});

test('OpenAI agent hands a tool call to the scoped bridge and streams the answer', async t => {
  const received = [];
  const server = http.createServer(async (request, response) => {
    let body = '';
    for await (const chunk of request) body += chunk;
    const parsed = JSON.parse(body);
    received.push(parsed);
    const ranTool = parsed.messages.some(item => item.role === 'tool');
    const delta = ranTool ? { role: 'assistant', content: 'The run is active.' } : { role: 'assistant', tool_calls: [
      { index: 0, id: 'run-list', type: 'function', function: { name: 'afm_runs_list', arguments: '{}' } },
    ] };
    const data = { id: 'r' + received.length, object: 'chat.completion.chunk', created: 1, model: 'test-model',
      choices: [{ index: 0, delta, finish_reason: ranTool ? 'stop' : 'tool_calls' }] };
    response.setHeader('content-type', 'text/event-stream');
    response.end(`data: ${JSON.stringify(data)}\n\ndata: [DONE]\n\n`);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.close());
  const script = `
    import readline from 'node:readline';
    for await (const line of readline.createInterface({input:process.stdin})) {
      const {id,method,params}=JSON.parse(line);
      if(id===undefined)continue;
      const result=method==='initialize'?{protocolVersion:params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:'fixture',version:'1'}}
        : method==='tools/list'?{tools:[{name:'afm_runs_list',description:'List runs',inputSchema:{type:'object',properties:{}}}]}
        : {content:[{type:'text',text:'One active run'}]};
      console.log(JSON.stringify({jsonrpc:'2.0',id,result}));
    }
  `;
  const provider = { kind: 'openai-agent', endpoint: `http://127.0.0.1:${server.address().port}/v1` };
  const events = [];
  const { done } = runDeepAgent({ provider, apiKey: 'test-token', modelId: 'test-model',
    systemPrompt: 'Use the run tool.', messages: [{ role: 'user', content: 'What is running?' }],
    bridge: { command: process.execPath, args: ['--input-type=module', '-e', script], env: {} }, onEvent: event => events.push(event) });
  assert.ifError((await done).error);
  assert.equal(events.filter(event => event.type === 'delta').map(event => event.text).join(''), 'The run is active.');
  const tool = events.find(event => event.type === 'tool' && event.name === 'afm_runs_list');
  assert(tool);
  assert.match(events.find(event => event.type === 'tool_result' && event.id === tool.id).text, /One active run/);
  assert.equal(received.length, 2);
});

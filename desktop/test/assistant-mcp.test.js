import { createServer } from 'node:http';
import { randomUUID } from 'node:crypto';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { once } from 'node:events';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import assert from 'node:assert/strict';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { McpServers } from '../src/assistant/mcp.js';
import { makeAssistant, settled, stubContext } from './assistant-support.js';

async function setup(t) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-mcp-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const values = new Map();
  const secrets = { set: async (id, value) => values.set(id, value), get: async id => values.get(id), remove: async id => values.delete(id) };
  const project = async id => {
    if (id !== 'project') throw Error('Unknown project');
    return { id, path: home };
  };
  return { home, values, secrets, project, registry: new McpServers({ home, secrets, project }) };
}

test('MCP registry scopes worktree runs and keeps credentials outside settings and IPC', async t => {
  const { home, values, registry } = await setup(t);
  const token = 'private-token';
  let [global] = await registry.save({ name: 'global', transport: 'http', url: 'https://example.com/mcp', token });
  await registry.save({ name: 'project-server', transport: 'stdio', command: process.execPath, projectId: 'project',
    env: [{ name: 'MCP_AUTH', ref: 'PROJECT_TOKEN' }], args: ['one argument'] });
  assert.equal(global.hasToken, true);
  assert.equal(JSON.stringify(await registry.list()).includes(token), false);
  assert.equal((await readFile(path.join(home, '.afm/desktop/mcp.json'), 'utf8')).includes(token), false);
  assert.deepEqual((await registry.forScope({ kind: 'all' }, {})).configs.map(item => item.name), ['global']);
  const run = { kind: 'run', projectRoot: path.join(home, '.worktrees', 'branch') };
  const resolved = await registry.forScope(run, { PROJECT_TOKEN: 'derived-project-secret' });
  assert.deepEqual(resolved.configs.map(item => item.name), ['global', 'project-server']);
  assert.deepEqual(resolved.configs[0].headers, [{ name: 'Authorization', value: 'Bearer ' + token }]);
  assert.deepEqual(resolved.configs[1].env, [{ name: 'MCP_AUTH', value: 'derived-project-secret' }]);
  assert.match((await registry.forScope(run, {})).notices[0], /PROJECT_TOKEN is missing/);
  assert.equal((await registry.forScope({ kind: 'run', projectRoot: home + '-other' }, {})).configs.length, 1);
  await assert.rejects(registry.save({ ...global, name: 'AFM' }), /reserved/);
  await assert.rejects(registry.save({ ...global, url: 'https://example.org/mcp' }), /Enter the token again/);
  [global] = (await registry.save({ ...global, token: 'rotated-token' })).filter(item => item.id === global.id);
  assert.deepEqual([...values.values()], ['rotated-token']);
  await registry.remove(global.id);
  assert.equal(values.size, 0);
});

test('MCP connection check authenticates and checks the protocol for both transports', async t => {
  const { home, registry } = await setup(t);
  const token = 'http-fixture-token';
  const mcp = new Server({ name: 'fixture', version: '1' });
  const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: randomUUID, enableJsonResponse: true });
  await mcp.connect(transport);
  const server = createServer((request, response) => {
    if (request.headers.authorization !== 'Bearer ' + token) {
      response.writeHead(401).end('Rejected credentials');
      return;
    }
    transport.handleRequest(request, response).catch(() => response.end());
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(async () => { await mcp.close(); await new Promise(resolve => server.close(resolve)); });
  const [http] = await registry.save({ name: 'http', transport: 'http', url: `http://127.0.0.1:${server.address().port}/mcp`, token });
  assert.equal((await registry.test(http.id, {}))[0].status.state, 'connected');
  await registry.save({ ...http, token: 'wrong-token' });
  assert.match((await registry.test(http.id, {}))[0].status.detail, /rejected authentication/i);
  const script = path.join(home, 'stdio.mjs');
  await writeFile(script, `
    import {createInterface} from 'node:readline';
    if (process.env.MCP_TOKEN !== 'stdio-fixture-token') process.exit(1);
    for await (const line of createInterface({ input: process.stdin })) {
      const {id,method,params} = JSON.parse(line);
      if (method === 'initialize') console.log(JSON.stringify({jsonrpc:'2.0',id,result:{
        protocolVersion:params.protocolVersion,capabilities:{},serverInfo:{name:'stdio',version:'1'}
      }}));
    }`);
  const stdio = (await registry.save({ name: 'stdio', transport: 'stdio', command: process.execPath, args: [script],
    tokenEnv: 'MCP_TOKEN', token: 'stdio-fixture-token' })).find(item => item.name === 'stdio');
  assert.equal((await registry.test(stdio.id, {})).find(item => item.id === stdio.id).status.state, 'connected');
});

test('OpenCode import keeps flags and arguments and resolves environment and file references', async t => {
  const { home, registry } = await setup(t);
  const file = path.join(home, 'key');
  await writeFile(file, 'file-fixture-token\n');
  const text = `{
    "provider": {"ignored": true},
    "mcp": {
      "server": {
        "type": "local",
        "command": [${JSON.stringify(path.basename(process.execPath))}, "-e", "console.log('test')"],
        "environment": {
          "PERSONAL_ACCESS_TOKEN": "{YOUR_GITLAB_TOKEN}", # supplied separately
          "SERVICE_PASSWORD": "{file:~/key}",
          "READ_ONLY": "true"
        }
      },
      "disabled": {"enabled": false}
    }
  }`;
  const [server] = await registry.importOpenCode(text, 'project');
  assert.equal(server.projectId, 'project');
  await assert.rejects(registry.importOpenCode(text, 'project'), /already exists/);
  await assert.rejects(registry.importOpenCode(text.replace('"server"', '"other"').replace('{YOUR_GITLAB_TOKEN}', 'unsafe-token')), /Use a file or Desktop environment variable/);
  const scope = { kind: 'project', projectId: 'project' };
  const { configs, secrets, hasFileCredentials } = await registry.forScope(scope, { PATH: path.dirname(process.execPath), PERSONAL_ACCESS_TOKEN: 'env-fixture-token' });
  assert.deepEqual(configs, [{ name: 'server', command: process.execPath, args: ['-e', "console.log('test')"], env: [
    { name: 'PERSONAL_ACCESS_TOKEN', value: 'env-fixture-token' },
    { name: 'SERVICE_PASSWORD', value: 'file-fixture-token' },
    { name: 'READ_ONLY', value: 'true' },
  ] }]);
  assert.deepEqual(secrets, ['env-fixture-token', 'file-fixture-token']);
  assert.equal(hasFileCredentials, true);
  await registry.save({ name: 'balian-local', transport: 'http', url: 'http://127.0.0.1:8080/mcp', tokenFile: '~/key' });
  const [http] = (await registry.forScope({ kind: 'all' }, {})).configs;
  assert.deepEqual(http.headers, [{ name: 'Authorization', value: 'Bearer file-fixture-token' }]);
  assert.equal((await readFile(path.join(home, '.afm/desktop/mcp.json'), 'utf8')).includes('file-fixture-token'), false);
});

test('Assistant sends scoped MCP with the AFM bridge and checks live HTTP capabilities', async t => {
  const { home, secrets, project } = await setup(t);
  const agent = path.join(home, 'agent.mjs');
  await writeFile(agent, `
    import { createInterface } from 'node:readline';
    const send = packet => console.log(JSON.stringify({jsonrpc:'2.0', ...packet}));
    let servers;
    for await (const line of createInterface({ input: process.stdin })) {
      const {id,method,params} = JSON.parse(line);
      if (method === 'initialize') send({id,result:{protocolVersion:1,agentCapabilities:{
        mcpCapabilities:{http:process.argv[2]==='http'}
      }}});
      if (method === 'session/new') {
        servers = params.mcpServers;
        send({id,result:{sessionId:'fixture'}});
      }
      if (method === 'session/prompt') {
        const text = servers.map(server => server.name + (server.headers?.[0]?.value ? ' ' + server.headers[0].value : '')).join(', ');
        send({method:'session/update',params:{sessionId:'fixture',update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text}}}});
        send({id,result:{stopReason:'end_turn'}});
      }
    }
  `);
  const { assistant, events } = makeAssistant(t, { home, env: process.env }, {
    secrets, context: stubContext({ project, endpoint: async () => 'http://localhost:12345' }),
  });
  for (const id of ['plain', 'http']) {
    await assistant.settings.addProvider({ kind: 'acp', name: id, executable: process.execPath, args: [agent, id] }, id);
  }
  await assistant.mcp.save({ name: 'Local', transport: 'stdio', command: process.execPath, projectId: 'project' });
  await assistant.mcp.save({ name: 'Remote', transport: 'http', url: 'https://example.com/mcp', token: 'should-stay-private' });
  await assistant.configure({ provider: 'plain' });
  await assistant.send({ text: 'List server names', scope: { projectId: 'project' } });
  const first = await settled(events);
  assert.equal(first.reply.text, 'afm, Local');
  assert(events.some(event => event.type === 'notice' && /does not support HTTP/.test(event.text)));
  events.length = 0;
  await assistant.configure({ provider: 'http' });
  await assistant.send({ text: 'List server names' });
  const second = await settled(events);
  assert.equal(second.reply.text, 'afm, Remote Bearer [hidden]');
  assert.equal(JSON.stringify(events).includes('should-stay-private'), false);
  assert.equal(JSON.stringify(await assistant.history()).includes('should-stay-private'), false);
});

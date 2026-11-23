import { readFile } from 'node:fs/promises';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';
import { openMcp } from './mcp-client.js';

const { config, cwd, env, secrets } = JSON.parse(await readFile(process.argv[2], 'utf8'));
const server = new Server({ name: config.name, version: '1' }, { capabilities: { tools: {} } });
const controller = new AbortController();
const ordered = [...new Set(secrets)].sort((a, b) => b.length - a.length);
let connection;

const sanitize = value => JSON.parse(JSON.stringify(value, (_key, item) => typeof item === 'string'
  ? ordered.reduce((text, secret) => text.replaceAll(secret, '[hidden]'), item) : item));

async function relay(request, { signal }, invoke) {
  try {
    connection ??= openMcp(config, { cwd, env, signal: controller.signal });
    const client = await connection.ready;
    signal.throwIfAborted();
    return sanitize(await invoke(client, request.params, signal));
  } catch (error) {
    throw Error(sanitize(error.message));
  }
}

server.setRequestHandler(ListToolsRequestSchema, (request, extra) =>
  relay(request, extra, (client, params, signal) => client.listTools(params, { signal })));
server.setRequestHandler(CallToolRequestSchema, (request, extra) =>
  relay(request, extra, (client, params, signal) => client.callTool(params, undefined, { signal, timeout: 601_000 })));
process.stdin.once('end', () => {
  controller.abort();
  void server.close();
  void connection?.close();
});
await server.connect(new StdioServerTransport());

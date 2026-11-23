import { openMcp } from './mcp-client.js';

export function deepMcpTools(Tool, { servers, cwd, env, approve, redact }, signal) {
  const connections = new Map();
  const byName = new Map(servers.map(server => [server.name, server]));
  const server = { type: 'string', enum: [...byName.keys()] };
  const connect = async name => {
    signal.throwIfAborted();
    if (!byName.has(name)) throw Error('Choose a configured MCP server.');
    if (!connections.has(name)) {
      const connection = openMcp(byName.get(name), { cwd, env, signal });
      connections.set(name, connection);
      connection.ready.catch(() => connections.delete(name));
    }
    return connections.get(name).ready;
  };
  const tool = (name, description, properties, required, run) => new Tool({
    name, description, schema: { type: 'object', properties: { server, ...properties }, required: ['server', ...required] },
    func: async input => {
      try { return redact(JSON.stringify(await run(await connect(input.server), input))); }
      catch (error) {
        signal.throwIfAborted();
        return redact(JSON.stringify({ isError: true, error: error.message }));
      }
    },
  });
  const toolName = { type: 'string', description: 'The tool name returned by mcp_list_tools.' };
  return {
    tools: [
      tool('mcp_list_tools', 'Connect to a configured MCP server when needed and list its tools and input schemas. Use nextCursor to request another page.',
        { cursor: { type: 'string' } }, [], (client, { cursor }) => client.listTools(cursor ? { cursor } : undefined, { signal })),
      tool('mcp_call_tool', 'Call a tool from mcp_list_tools on its MCP server with the arguments in its input schema.',
        { name: toolName, arguments: { type: 'object', additionalProperties: true } }, ['name'], async (client, input) => {
          if (!await approve(input.server, input.name, input.arguments)) return { isError: true, error: 'The user declined this MCP call.' };
          signal.throwIfAborted();
          return client.callTool({ name: input.name, arguments: input.arguments }, undefined, { signal, timeout: 601_000 });
        }),
    ],
    close: () => Promise.all([...connections.values()].map(connection => connection.close())),
  };
}

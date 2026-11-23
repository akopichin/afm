import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

export function openMcp(config, { cwd, env, signal }) {
  const http = config.type === 'http';
  const timeout = AbortSignal.timeout(http ? 8000 : 30000);
  const handshake = signal ? AbortSignal.any([signal, timeout]) : timeout;
  const transport = http
    ? new StreamableHTTPClientTransport(new URL(config.url), {
      requestInit: { headers: Object.fromEntries(config.headers.map(({ name, value }) => [name, value])), redirect: 'error' },
      fetch: (url, init = {}) => {
        if (init.method !== 'DELETE') return fetch(url, { ...init, redirect: 'error' });
        const deadline = AbortSignal.timeout(1500);
        return fetch(url, { ...init, redirect: 'error', signal: init.signal ? AbortSignal.any([init.signal, deadline]) : deadline });
      },
    })
    : new StdioClientTransport({ command: config.command, args: config.args, cwd, stderr: 'ignore',
      env: { ...(env.PATH ? { PATH: env.PATH } : {}), ...Object.fromEntries(config.env.map(({ name, value }) => [name, value])) } });
  const client = new Client({ name: 'afm-desktop', version: '1' });
  let closing;
  const close = () => (closing ??= (async () => {
    if (http && transport.sessionId) await transport.terminateSession().catch(() => {});
    await client.close().catch(() => {});
  })());
  const ready = client.connect(transport, { signal: handshake }).then(() => client, async error => {
    await close();
    signal?.throwIfAborted();
    const message = timeout.aborted ? 'The MCP handshake timed out.'
      : [401, 403].includes(error.code) ? 'The server rejected authentication.'
        : 'The MCP handshake failed. Check the server settings and make sure it is reachable.';
    throw Error(message);
  });
  return { ready, close };
}

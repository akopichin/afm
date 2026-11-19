import { readFile } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

export function localURL(value) {
  const url = new URL(value);
  if (url.protocol !== 'http:' || !['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
      || url.username || url.password || !url.port) throw Error('Expected a local AFM endpoint.');
  return url;
}

export class Host {
  constructor(home, binary) {
    this.file = path.join(home, '.afm', 'serve', 'host.json');
    this.binary = binary;
  }
  async connect() {
    if (!this.connection) this.connection = this.open().catch(error => { this.connection = null; throw error; });
    return this.connection;
  }
  async open() {
    let child, failure;
    for (let attempt = 0; attempt < 35; attempt++) {
      try {
        const info = JSON.parse(await readFile(this.file, 'utf8'));
        const url = localURL(info.endpoint);
        if (url.pathname !== '/mcp') throw Error('Unexpected AFM host endpoint.');
        const client = new Client({ name: 'afm-desktop', version: '0.0.1' });
        const transport = new StreamableHTTPClientTransport(url, { requestInit: {redirect:'error'} });
        try {
          await client.connect(transport, { timeout: 3000 });
          client.onclose = () => { this.connection = null; };
          return client;
        } catch (error) { await transport.close(); throw error; }
      } catch (error) { failure = error; }
      if (!child) {
        child = spawn(await this.binary(), ['serve', '--port', '0'], { detached: true, stdio: 'ignore', windowsHide: true });
        child.on('error', error => { failure = error; });
        child.unref();
      }
      await delay(150);
    }
    throw Error('Cannot connect to afm serve: ' + failure.message);
  }
  async tool(name, args = {}) {
    const client = await this.connect();
    const result = await client.callTool({ name, arguments: args }, undefined, { timeout: 15000 });
    if (result.isError) throw Error(result.content?.[0]?.text || name + ' failed');
    return result.structuredContent ?? JSON.parse(result.content[0].text);
  }
  async runs() { return (await this.tool('afm_runs_list')).runs; }
}

export async function dashboardURL(run, request = fetch) {
  if (!run || run.mode !== 'dashboard' || !['active','active_orphaned'].includes(run.liveness)
      || !['attached','managed'].includes(run.ownership) || !run.instance_id || !run.run_id) throw Error('Dashboard is unavailable.');
  const origin = localURL(run.api_url).origin;
  const res = await request(origin + '/api/runtime', { redirect:'error', signal:AbortSignal.timeout(3000) });
  if (!res.ok) throw Error('Dashboard is unavailable.');
  const runtime = await res.json();
  if (runtime.run_id !== run.run_id || runtime.instance_id !== run.instance_id || runtime.mode !== 'dashboard' || !runtime.ready)
    throw Error('Run changed. Refresh the project.');
  return origin;
}

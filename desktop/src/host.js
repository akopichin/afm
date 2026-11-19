import { mkdirSync, openSync, closeSync, readFileSync, statSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { desktopDir, readJSON, writeJSON } from './files.js';

export function localURL(value) {
  const url = new URL(value);
  if (url.protocol !== 'http:' || !['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
      || url.username || url.password || !url.port) throw Error('Expected a local AFM endpoint.');
  return url;
}

// Tools Desktop depends on. A host without them is a different or too old program.
export const REQUIRED_TOOLS = ['afm_runs_list', 'afm_run_start', 'afm_run_attach', 'afm_run_cancel', 'afm_run_release', 'afm_run_report'];
// Calls that are safe to repeat after a lost connection: reads, and starts guarded by request_id.
const REPEATABLE = new Set(['afm_runs_list', 'afm_run_report', 'afm_run_start']);

export class HostError extends Error {
  constructor(code, message) { super(message); this.code = code; }
}

function toolFailure(name, text) {
  const match = /^([a-z_]+): (.*)$/s.exec(text || '');
  return new HostError(match ? match[1] : 'tool_failed', match ? match[2] : text || name + ' failed');
}

// Connection to the user's single `afm serve`. Desktop reuses a running host and starts the bundled
// one otherwise. The host it starts keeps running after the app quits, because runs it manages are
// only reachable through it; the owner file records which host is ours so controls stay honest.
export class Host {
  constructor(home, binary, { attempts = 35, interval = 150, env = process.env } = {}) {
    this.env = env;
    this.home = home;
    this.file = path.join(home, '.afm', 'serve', 'host.json');
    this.ownerFile = path.join(desktopDir(home), 'host-owner.json');
    this.logFile = path.join(desktopDir(home), 'host.log');
    this.binary = binary;
    this.attempts = attempts;
    this.interval = interval;
    this.state = { state: 'offline', reason: null };
    this.listeners = new Set();
  }

  onState(listener) { this.listeners.add(listener); return () => this.listeners.delete(listener); }
  #set(state) { this.state = state; for (const listener of this.listeners) listener(state); }

  connect() {
    if (!this.connection) {
      this.#set({ state: 'connecting', reason: null });
      this.connection = this.open().catch(error => {
        this.connection = null;
        this.#set({ state: error.code === 'incompatible_host' ? 'incompatible' : 'offline', reason: error.message });
        throw error;
      });
    }
    return this.connection;
  }

  async #hostInfo() {
    try { return JSON.parse(await readFile(this.file, 'utf8')); } catch { return null; }
  }

  async open() {
    let child;
    let failure = Error('AFM host did not start.');
    for (let attempt = 0; attempt < this.attempts; attempt++) {
      const info = await this.#hostInfo();
      if (info) {
        try { return await this.#dial(info); }
        catch (error) { if (error.code === 'incompatible_host') throw error; failure = error; }
      }
      if (!child) child = this.#startChild();
      if (child.failed) failure = child.failed;
      await delay(this.interval);
    }
    throw new HostError('host_unreachable', 'Cannot connect to afm serve: ' + failure.message);
  }

  async #dial(info) {
    const url = localURL(info.endpoint);
    if (url.pathname !== '/mcp') throw new HostError('incompatible_host', 'Unexpected AFM host endpoint.');
    const client = new Client({ name: 'afm-desktop', version: '0.0.1' });
    const transport = new StreamableHTTPClientTransport(url, { requestInit: { redirect: 'error' } });
    try {
      await client.connect(transport, { timeout: 3000 });
      const server = client.getServerVersion();
      // The endpoint speaks MCP, so anything wrong from here on means it is not the AFM host we need.
      const tools = await client.listTools().then(list => new Set(list.tools.map(tool => tool.name)), () => new Set());
      const missing = REQUIRED_TOOLS.filter(name => !tools.has(name));
      if (server?.name !== 'afm-serve' || missing.length) {
        throw new HostError('incompatible_host', `The service on this port is not a compatible AFM host${server?.name ? ' (' + server.name + ')' : ''}.`);
      }
      client.onclose = () => { this.connection = null; this.#set({ state: 'offline', reason: 'Connection to AFM host lost.' }); };
      this.#set({ state: 'connected', reason: null, endpoint: info.endpoint, pid: info.pid, startedAt: info.started_at,
        version: server.version, ownership: await this.#ownership(info) });
      return client;
    } catch (error) { await transport.close().catch(() => {}); throw error; }
  }

  async #ownership(info) {
    const owner = await readJSON(this.ownerFile, null);
    if (this.spawnedPid === info.pid) return 'desktop';
    return owner?.pid === info.pid && owner.startedAt === info.started_at ? 'desktop' : 'external';
  }

  // Spawns the bundled host with its stderr in Desktop's own log. The child outlives this process.
  #startChild() {
    const child = {};
    const spawnHost = async () => {
      const [binary, env] = await Promise.all([this.binary(), typeof this.env === 'function' ? this.env() : this.env]);
      mkdirSync(path.dirname(this.logFile), { recursive: true, mode: 0o700 });
      const log = openSync(this.logFile, 'a', 0o600);
      try {
        const process_ = spawn(binary, ['serve', '--port', '0'], { detached: true, stdio: ['ignore', log, log], windowsHide: true, env });
        process_.on('error', error => { child.failed = error; });
        process_.on('exit', code => { if (code) child.failed = Error(`afm serve exited with ${code}: ${this.#logTail()}`); });
        child.pid = this.spawnedPid = process_.pid;
        process_.unref();
        this.#recordOwner(process_.pid);
      } finally { closeSync(log); }
    };
    spawnHost().catch(error => { child.failed = error; });
    return child;
  }

  #logTail() {
    try { return readFileSync(this.logFile, 'utf8').trim().split('\n').slice(-2).join(' '); } catch { return ''; }
  }

  // The new host writes host.json with its own pid a moment later; the owner file claims that pid.
  #recordOwner(pid) {
    const claim = async () => {
      for (let i = 0; i < this.attempts; i++) {
        const info = await this.#hostInfo();
        if (info?.pid === pid) return writeJSON(this.ownerFile, { pid, startedAt: info.started_at });
        await delay(this.interval);
      }
    };
    claim().catch(() => {});
  }

  async tool(name, args = {}) {
    for (let attempt = 0; ; attempt++) {
      const client = await this.connect();
      let result;
      try { result = await client.callTool({ name, arguments: args }, undefined, { timeout: 15000 }); }
      catch (error) {
        if (attempt > 0 || !REPEATABLE.has(name) || error.code === -32602) throw error;
        this.connection = null;
        await client.close().catch(() => {});
        continue;
      }
      if (result.isError) throw toolFailure(name, result.content?.[0]?.text);
      return result.structuredContent ?? JSON.parse(result.content[0].text);
    }
  }

  async runs() { return (await this.tool('afm_runs_list')).runs; }

  // True when serve.yaml changed after the host read it: the host loads it only at startup.
  configStale(serveYamlFile) {
    try { return statSync(serveYamlFile).mtimeMs > Date.parse(this.state.startedAt); } catch { return false; }
  }

  // Stops the host process and starts a fresh one. Runs keep running. A host Desktop did not start is
  // restarted only on explicit request, because other tools may be using it.
  async restart({ confirmExternal = false } = {}) {
    await this.connect();
    if (this.state.ownership !== 'desktop' && !confirmExternal) {
      throw new HostError('external_host', 'This AFM host was not started by Desktop. Confirm to restart it; running runs are not affected.');
    }
    const { pid, endpoint } = this.state;
    const info = await this.#hostInfo();
    if (info?.pid !== pid || info.endpoint !== endpoint) throw new HostError('host_changed', 'The AFM host changed; refresh and try again.');
    const client = await this.connection;
    this.connection = null;
    await client.close().catch(() => {});
    process.kill(pid, 'SIGTERM');
    for (let i = 0; i < 100; i++) {
      if (!(await this.#hostInfo()) || !alive(pid)) break;
      await delay(50);
    }
    if (alive(pid)) throw new HostError('restart_failed', 'The AFM host did not stop.');
    await this.connect();
    return this.state;
  }

  async close() {
    const pending = this.connection;
    this.connection = null;
    if (pending) await (await pending.catch(() => null))?.close().catch(() => {});
    this.#set({ state: 'offline', reason: 'Closed.' });
  }
}

function alive(pid) {
  try { process.kill(pid, 0); return true; } catch { return false; }
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

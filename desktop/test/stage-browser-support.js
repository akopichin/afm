import { EventEmitter } from 'node:events';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, rm } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { randomBytes } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import { StageBrowsers } from '../src/stage-browser/broker.js';
import { pipeFramer } from '../src/stage-browser/wire.js';
import { eventually } from './support.js';

// A page that records what reaches it and answers like a tiny Chrome tab. No browser is involved.
export class FakePage extends EventEmitter {
  constructor(url) {
    super();
    this.id = randomBytes(8).toString('hex').toUpperCase();
    this.url = url;
    this.title = 'Page ' + this.id.slice(0, 4);
    this.received = [];
    this.closed = false;
  }
  info() { return { url: this.url, title: this.title }; }
  async send(method, params, sessionId) {
    this.received.push({ method, params, sessionId });
    if (method === 'Page.navigate') { this.url = params.url; this.emit('changed'); return { frameId: 'F' }; }
    if (method === 'Runtime.evaluate') return { result: { type: 'string', value: `${this.id}:${params.expression}` } };
    if (method === 'Page.crash') throw Object.assign(Error('Target crashed'), { code: -32000 });
    return {};
  }
  close() { if (!this.closed) { this.closed = true; this.emit('closed'); } }
}

export class FakeBackend {
  constructor() { this.pages = []; this.shown = []; this.hidden = 0; this.disposed = false; }
  async createPage(url) { const page = new FakePage(url); this.pages.push(page); return page; }
  show(page, bounds) { this.shown.push([page?.id, bounds]); }
  hide() { this.hidden++; }
  dispose() { this.disposed = true; for (const page of this.pages) page.close(); }
}

// StageBrowsers with fake pages, a disposable HOME and the real companion launched the way AFM launches Chrome.
export async function stageFixture(t, options = {}) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'stagebrowser-'));
  const backends = new Map();
  const inspected = [];
  const browsers = new StageBrowsers({
    home, graceMs: 150, helloMs: 400, product: 'Chrome/Fixture', ...options,
    launcher: { node: process.execPath, script: new URL('../src/stage-browser/companion-main.js', import.meta.url).pathname, mcp: options.mcp },
    createBackend: entry => { const backend = new FakeBackend(); backends.set(entry.key, backend); return backend; },
    inspect: async (entry, runs) => { inspected.push(entry.key); return options.inspect?.(entry, runs) ?? null; },
  });
  const wrapper = await browsers.start();
  const profiles = path.join(os.tmpdir(), 'afm-browser');
  await mkdir(profiles, { recursive: true });
  const made = [];
  const children = new Set();
  t.after(async () => {
    for (const child of children) child.kill('SIGKILL');
    await browsers.close();
    for (const dir of made) await rm(dir, { recursive: true, force: true });
    await rm(home, { recursive: true, force: true });
  });
  const profile = async stage => {
    const dir = path.join(profiles, `b-${randomBytes(4).toString('hex')}__${stage}`);
    await mkdir(dir, { recursive: true });
    made.push(dir);
    return dir;
  };
  // What `afm` does when a stage first calls a browser tool: starts the executable with the pipe on fds 3 and 4.
  const launch = (dir, extra = {}) => {
    const child = spawn(wrapper, ['--remote-debugging-pipe', '--user-data-dir=' + dir, '--no-first-run', 'about:blank'],
      { stdio: ['ignore', 'ignore', 'pipe', 'pipe', 'pipe'], env: { ...process.env, ...extra.env } });
    children.add(child);
    const client = cdpClient(child);
    return client;
  };
  return { home, browsers, backends, inspected, wrapper, profile, launch };
}

// The client side of AFM's pipe: NUL-terminated JSON out on fd 3, in on fd 4.
function cdpClient(child) {
  const framer = pipeFramer();
  const client = { child, events: [], pending: new Map(), nextId: 0, stderr: '', exited: new Promise(resolve => child.on('exit', (code, signal) => resolve({ code, signal }))) };
  child.stdio[2].on('data', chunk => { client.stderr += chunk; });
  child.stdio[4].setEncoding('utf8');
  child.stdio[4].on('data', chunk => {
    for (const text of framer.push(chunk)) {
      const message = JSON.parse(text);
      if (message.id !== undefined && client.pending.has(message.id)) { client.pending.get(message.id)(message); client.pending.delete(message.id); }
      else client.events.push(message);
    }
  });
  client.call = (method, params = {}, sessionId) => new Promise(resolve => {
    const id = ++client.nextId;
    client.pending.set(id, resolve);
    child.stdio[3].write(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }) + '\0');
  });
  client.ok = async (...args) => { const answer = await client.call(...args); if (answer.error) throw Error(answer.error.message); return answer.result; };
  client.event = (method, since = 0) => eventually(() => client.events.slice(since).find(item => item.method === method), { timeout: 5000, what: method });
  return client;
}

// A raw companion handshake, for refusals the real companion would never send.
export async function rawHello(browsers, hello, { token = browsers.token } = {}) {
  const socket = net.connect(browsers.socketPath);
  await new Promise((resolve, reject) => { socket.once('connect', resolve); socket.once('error', reject); });
  socket.write(JSON.stringify({ hello: { protocol: 1, token, ...hello } }) + '\n');
  const answer = await new Promise(resolve => { let text = ''; socket.on('data', chunk => { text += chunk; if (text.includes('\n')) resolve(JSON.parse(text)); }); socket.on('close', () => resolve(text ? JSON.parse(text) : { ready: false, error: 'closed' })); });
  return { answer, socket };
}

export { delay };

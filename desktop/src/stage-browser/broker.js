import { EventEmitter } from 'node:events';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { chmod, mkdtemp, rm } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { StringDecoder } from 'node:string_decoder';
import { desktopDir, writeJSON } from '../files.js';
import { PROTOCOL } from './companion.js';
import { StageRouter, stageURL } from './cdp-router.js';
import { writeWrapper, writeMcpWrapper } from './install.js';
import { lineFramer, MAX_MESSAGE } from './wire.js';

// AFM names a stage browser's profile directory <epoch>__<stage>; the epoch is random per run process.
const PROFILE = /^(b-[0-9a-f]{8})__([A-Za-z0-9_.-]+)$/;

export const brokerFile = home => path.join(desktopDir(home), 'stage-browser.json');

// Stage browsers of this app: a private socket the companion executables (AFM's "Chrome") connect to, one
// page-backed router per stage, and the lifetime rules. Nothing here is reachable from the network, from the
// shell page or from another stage.
export class StageBrowsers extends EventEmitter {
  // createBackend(entry) -> { createPage(url), show(page, bounds), hide(), dispose() }
  // inspect(entry, runs) -> { runKey, stageId, ended } | null, how a stage is tied to its run.
  constructor({ home, createBackend, inspect, launcher, graceMs = 20000, helloMs = 5000, maxStages = 8, product }) {
    super();
    Object.assign(this, { home, createBackend, inspect, launcher, graceMs, helloMs, maxStages, product });
    this.token = randomBytes(24).toString('hex');
    this.entries = new Map();
    this.reconciling = null;
  }

  // Idempotent: the wrapper executable and broker file exist, and the socket accepts companions.
  start() { return (this.starting ??= this.#listen()); }

  async #listen() {
    this.dir = await mkdtemp(path.join(os.tmpdir(), 'afmd-'));
    await chmod(this.dir, 0o700);
    this.socketPath = path.join(this.dir, 's');
    this.server = net.createServer(socket => this.#accept(socket));
    await new Promise((resolve, reject) => { this.server.once('error', reject); this.server.listen(this.socketPath, resolve); });
    this.wrapper = await writeWrapper(this.home, this.launcher, brokerFile(this.home));
    if (this.launcher.mcp) this.mcpWrapper = await writeMcpWrapper(this.home, this.launcher.mcp);
    await writeJSON(brokerFile(this.home), { protocol: PROTOCOL, socket: this.socketPath, token: this.token, pid: process.pid });
    return this.wrapper;
  }

  // The environment that makes runs of the host Desktop starts use this broker for their browser stages.
  async hostEnv(basePath = process.env.PATH) {
    const chrome = await this.start();
    // The shipped chrome-devtools-mcp goes first on PATH so AFM finds it without any external install.
    const searchPath = this.mcpWrapper ? { PATH: [path.dirname(this.mcpWrapper), basePath].filter(Boolean).join(path.delimiter) } : {};
    return { AFM_CHROME_EXECUTABLE: chrome, ...searchPath };
  }

  #accept(socket) {
    const frames = lineFramer(), decoder = new StringDecoder('utf8');
    const timer = setTimeout(() => socket.destroy(), this.helloMs);
    let entry = null, hello = false;
    socket.on('error', () => socket.destroy());
    socket.on('data', async chunk => {
      let lines;
      try { lines = frames.push(decoder.write(chunk)); } catch { return socket.destroy(); }
      for (const line of lines) {
        if (entry) { entry.router.handle(line); continue; }
        if (hello) return;
        hello = true;
        clearTimeout(timer);
        try { entry = await this.#hello(socket, line); }
        catch (error) { socket.end(JSON.stringify({ ready: false, error: String(error.message) }) + '\n'); return; }
        socket.write('{"ready":true}\n');
      }
    });
    socket.on('close', () => {
      clearTimeout(timer);
      if (entry?.socket === socket) this.#disconnected(entry);
    });
  }

  async #hello(socket, line) {
    let request;
    try { request = JSON.parse(line).hello; } catch { throw Error('Malformed hello.'); }
    if (!request || request.protocol !== PROTOCOL) throw Error('Incompatible companion protocol.');
    const given = Buffer.from(String(request.token)), own = Buffer.from(this.token);
    if (given.length !== own.length || !timingSafeEqual(given, own)) throw Error('Unauthorized.');
    const profile = String(request.profile ?? '');
    const match = PROFILE.exec(path.basename(profile));
    if (!match || path.basename(path.dirname(profile)) !== 'afm-browser' || !existsSync(profile)) throw Error('Not an AFM stage browser profile.');
    if (!Number.isSafeInteger(request.pid) || request.pid < 2 || typeof request.cwd !== 'string' || !path.isAbsolute(request.cwd)) throw Error('Missing process identity.');

    const url = stageURL(request.url);
    const key = match[0];
    let entry = this.entries.get(key);
    if (entry) {
      // A relaunched browser of the same stage and process gets its pages back; any other process never does.
      if (entry.pid !== request.pid) throw Error('This stage browser belongs to another process.');
      clearTimeout(entry.graceTimer);
      entry.socket?.destroy();
      entry.router.resetClient();
    } else {
      if (this.entries.size >= this.maxStages) throw Error('Too many stage browsers are open.');
      entry = this.#create(key, match, request);
      this.entries.set(key, entry);
    }
    entry.socket = socket;
    if (!entry.router.activePage) {
      try { await entry.router.open(url); }
      catch (error) { entry.socket = null; this.release(entry); throw error; }
    }
    this.emit('change');
    return entry;
  }

  #create(key, match, request) {
    const entry = { key, handle: randomBytes(12).toString('hex'), epoch: match[1], stagePart: match[2], pid: request.pid, cwd: request.cwd,
      socket: null, runKey: null, stageId: null, graceTimer: null };
    entry.backend = this.createBackend(entry);
    entry.router = new StageRouter({
      createPage: url => entry.backend.createPage(url), product: this.product,
      send: message => {
        const { socket } = entry;
        if (!socket || socket.destroyed) return;
        if (socket.writableLength > MAX_MESSAGE) return socket.destroy();
        socket.write(JSON.stringify(message) + '\n');
      },
      onClose: () => this.release(entry),
    });
    return entry;
  }

  // The companion went away: AFM may relaunch it for the same stage soon, so the pages wait a short while.
  #disconnected(entry) {
    entry.socket = null;
    entry.graceTimer = setTimeout(() => this.release(entry), this.graceMs);
    entry.graceTimer.unref?.();
  }

  release(entry) {
    if (this.entries.get(entry.key) !== entry) return;
    this.entries.delete(entry.key);
    clearTimeout(entry.graceTimer);
    entry.router.dispose();
    entry.backend.dispose();
    entry.socket?.end();
    this.emit('change');
  }

  // Stage browsers that are tied to a run; the page and its view stay while the user looks elsewhere.
  list() {
    return [...this.entries.values()].filter(entry => entry.runKey).map(entry => {
      const page = entry.router.activePage?.info() ?? { url: '', title: '' };
      return { runKey: entry.runKey, stageId: entry.stageId, handle: entry.handle, url: page.url, title: page.title, pages: entry.router.pages.size };
    });
  }

  // Ties new stages to their runs and releases the ones whose stage or run has ended. Idle cost is zero.
  reconcile(runs) {
    if (!this.entries.size) return Promise.resolve();
    this.reconciling ??= Promise.all([...this.entries.values()].map(async entry => {
      let found = null;
      try { found = await this.inspect(entry, runs); } catch { return; }
      if (this.entries.get(entry.key) !== entry || !found) return;
      if (found.ended) return this.release(entry);
      if (!entry.runKey) { entry.runKey = found.runKey; entry.stageId = found.stageId; this.emit('change'); }
    })).finally(() => { this.reconciling = null; });
    return this.reconciling;
  }

  // A new window needs the existing pages added to it again.
  reattach() { for (const entry of this.entries.values()) entry.backend.reattach?.(); }

  // Shows the stage's active page at the bounds, or hides everything. Unknown handles are ignored.
  place(handle, bounds) {
    for (const entry of this.entries.values()) {
      if (entry.handle === handle && bounds) entry.backend.show(entry.router.activePage, bounds);
      else entry.backend.hide();
    }
  }

  // Gives the keyboard to the stage's page, so the Live tab can be used without a mouse.
  focus(handle) {
    for (const entry of this.entries.values()) if (entry.handle === handle) entry.backend.focus?.(entry.router.activePage);
  }

  async close() {
    for (const entry of [...this.entries.values()]) this.release(entry);
    await this.starting?.catch(() => {});
    this.server?.close();
    if (this.dir) await rm(this.dir, { recursive: true, force: true });
    await rm(brokerFile(this.home), { force: true });
  }
}

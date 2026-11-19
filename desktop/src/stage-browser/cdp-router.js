import { randomBytes } from 'node:crypto';

const CONTEXT = 'AFMDESKTOPSTAGECONTEXT';
const OPENABLE = new Set(['http:', 'https:']);
// Domains and methods that act on the whole browser, not on a page; a page session never needs them.
const BROWSER_WIDE = /^(Browser|Storage|SystemInfo)\./;
const PAGE_SESSION_TARGET_METHODS = new Set(['Target.setAutoAttach']);
const newId = () => randomBytes(16).toString('hex').toUpperCase();

class CdpError extends Error {
  constructor(message, code = -32000) { super(message); this.code = code; }
}

// Only web pages may be opened; the shell, files and Chrome's own pages are not part of a stage.
export function stageURL(value) {
  if (value === undefined || value === '' || value === 'about:blank') return 'about:blank';
  let url;
  try { url = new URL(value); } catch { throw new CdpError('Invalid URL.', -32602); }
  if (!OPENABLE.has(url.protocol)) throw new CdpError('Only http and https pages can be opened in a stage browser.');
  return url.href;
}

// Speaks the browser endpoint of the Chrome DevTools protocol to one stage's client (AFM's chrome-devtools-mcp)
// on top of pages the app owns. The client can only reach targets of this stage: a session id outside this
// router is refused, and browser-wide commands that could reach beyond its pages are not implemented.
//
// A page is an EventEmitter with: id, info() -> {url, title}, send(method, params, sessionId) -> Promise,
// close(), and the events 'event' (method, params, childSessionId), 'changed', 'popup' (url), 'closed'.
export class StageRouter {
  constructor({ createPage, send, product = 'Chrome/AFM-Desktop', onClose = () => {} }) {
    Object.assign(this, { createPage, emit: send, product, onClose });
    this.pages = new Map();
    this.sessions = new Map();
    this.children = new Map();
    this.active = null;
    this.discover = false;
    this.autoAttach = false;
    this.disposed = false;
    this.generation = 0;
  }

  async open(url = 'about:blank') {
    const page = await this.createPage(stageURL(url));
    if (this.disposed) { page.close(); throw new CdpError('The stage browser is closed.'); }
    this.pages.set(page.id, page);
    this.active = page.id;
    page.on('event', (method, params, child) => this.#pageEvent(page, method, params, child));
    page.on('changed', () => this.#announce('Target.targetInfoChanged', page));
    page.on('popup', url => this.open(url).catch(() => {}));
    page.on('closed', () => this.#removed(page));
    if (this.discover) this.#announce('Target.targetCreated', page);
    if (this.autoAttach) this.#attach(page);
    return page;
  }

  info(page) {
    const { url, title } = page.info();
    const attached = [...this.sessions.values()].some(owner => owner === page);
    return { targetId: page.id, type: 'page', title, url, attached, canAccessOpener: false, browserContextId: CONTEXT };
  }

  get activePage() { return this.pages.get(this.active) ?? null; }

  // A new client of the same stage (AFM relaunched its browser) starts clean; the pages stay.
  resetClient() {
    this.generation++;
    this.sessions.clear();
    this.children.clear();
    this.discover = false;
    this.autoAttach = false;
  }

  dispose() {
    this.disposed = true;
    this.generation++;
    for (const page of [...this.pages.values()]) { this.pages.delete(page.id); page.close(); }
    this.sessions.clear();
    this.children.clear();
    this.active = null;
  }

  async handle(text) {
    let message;
    try { message = JSON.parse(text); } catch { return; }
    if (!message || typeof message !== 'object' || message.id === undefined) return;
    const { id, sessionId } = message;
    // An answer that finishes after the client was replaced belongs to the old client and is dropped.
    const generation = this.generation;
    const reply = body => { if (generation === this.generation) this.emit({ id, ...(sessionId ? { sessionId } : {}), ...body }); };
    try { reply({ result: (await this.#dispatch(message)) ?? {} }); }
    catch (error) { reply({ error: { code: error.code ?? -32000, message: String(error.message || error) } }); }
  }

  #dispatch({ method, params = {}, sessionId }) {
    if (sessionId) return this.#forward(sessionId, method, params);
    const handler = this.#commands[method];
    if (!handler) throw new CdpError(`'${method}' is not available in the AFM Desktop stage browser.`, -32601);
    return handler.call(this, params);
  }

  #forward(sessionId, method, params) {
    if (BROWSER_WIDE.test(method) || (method.startsWith('Target.') && !PAGE_SESSION_TARGET_METHODS.has(method))) {
      throw new CdpError(`'${method}' is not available on a page session in the AFM Desktop stage browser.`, -32601);
    }
    const page = this.sessions.get(sessionId);
    if (page) return page.send(method, params);
    const owner = this.children.get(sessionId);
    if (owner) return owner.send(method, params, sessionId);
    throw new CdpError('Session with given id not found.');
  }

  #target(params) {
    const page = this.pages.get(params.targetId);
    if (!page) throw new CdpError('No target with given id found.');
    return page;
  }

  #attach(page) {
    const sessionId = newId();
    this.sessions.set(sessionId, page);
    this.emit({ method: 'Target.attachedToTarget', params: { sessionId, targetInfo: { ...this.info(page), attached: true }, waitingForDebugger: false } });
    return sessionId;
  }

  #announce(method, page) {
    if (this.discover) this.emit({ method, params: { targetInfo: this.info(page) } });
  }

  #pageEvent(page, method, params, child) {
    if (child) {
      if (this.children.get(child) === page) this.emit({ method, params, sessionId: child });
      return;
    }
    if (method === 'Target.attachedToTarget' && params?.sessionId) this.children.set(params.sessionId, page);
    if (method === 'Target.detachedFromTarget') this.children.delete(params?.sessionId);
    for (const [sessionId, owner] of this.sessions) if (owner === page) this.emit({ method, params, sessionId });
  }

  #removed(page) {
    if (!this.pages.delete(page.id)) return;
    for (const [sessionId, owner] of this.sessions) {
      if (owner !== page) continue;
      this.sessions.delete(sessionId);
      this.emit({ method: 'Target.detachedFromTarget', params: { sessionId, targetId: page.id } });
    }
    for (const [child, owner] of this.children) if (owner === page) this.children.delete(child);
    if (this.discover) this.emit({ method: 'Target.targetDestroyed', params: { targetId: page.id } });
    if (this.active === page.id) this.active = [...this.pages.keys()].at(-1) ?? null;
  }

  #commands = {
    'Browser.getVersion': () => ({ protocolVersion: '1.3', product: this.product, revision: 'afm-desktop', userAgent: this.product, jsVersion: '' }),
    'Browser.close': () => { setImmediate(() => this.onClose()); return {}; },
    'Browser.getWindowForTarget': params => { this.#target(params); return { windowId: 1, bounds: { left: 0, top: 0, width: 1280, height: 800, windowState: 'normal' } }; },
    'Browser.setWindowBounds': () => ({}),
    'Browser.setDownloadBehavior': () => ({}),
    'Target.getBrowserContexts': () => ({ browserContextIds: [] }),
    'Target.setDiscoverTargets': params => {
      this.discover = params.discover !== false;
      if (this.discover) for (const page of this.pages.values()) this.#announce('Target.targetCreated', page);
      return {};
    },
    'Target.setAutoAttach': params => {
      this.autoAttach = params.autoAttach !== false;
      if (this.autoAttach) for (const page of this.pages.values()) if (![...this.sessions.values()].includes(page)) this.#attach(page);
      return {};
    },
    'Target.getTargets': () => ({ targetInfos: [...this.pages.values()].map(page => this.info(page)) }),
    'Target.getTargetInfo': params => ({ targetInfo: this.info(this.#target(params)) }),
    'Target.attachToTarget': params => ({ sessionId: this.#attach(this.#target(params)) }),
    'Target.detachFromTarget': params => {
      const page = this.sessions.get(params.sessionId);
      if (!page) throw new CdpError('Session with given id not found.');
      this.sessions.delete(params.sessionId);
      this.emit({ method: 'Target.detachedFromTarget', params: { sessionId: params.sessionId, targetId: page.id } });
      return {};
    },
    'Target.activateTarget': params => { this.active = this.#target(params).id; return {}; },
    'Target.createTarget': async params => {
      if (params.browserContextId && params.browserContextId !== CONTEXT) throw new CdpError('Unknown browser context.');
      return { targetId: (await this.open(params.url)).id };
    },
    'Target.closeTarget': params => {
      const page = this.#target(params);
      page.close();
      this.#removed(page);
      return { success: true };
    },
  };
}

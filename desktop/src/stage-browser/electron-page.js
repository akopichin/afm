import { EventEmitter } from 'node:events';
import { randomBytes } from 'node:crypto';
import { shortcutAction } from '../shortcuts.js';

const WEB_SCHEMES = /^(https?|wss?|data|blob):/;
const DEFAULT_BOUNDS = { x: 0, y: 0, width: 1280, height: 800 };

const LOOPBACK = new Set(['127.0.0.1', 'localhost', '[::1]']);

// Origins as AFM's servers use them: every loopback spelling of one port is the same server.
export function afmOriginKey(rawUrl) {
  try {
    const url = new URL(rawUrl);
    const protocol = { 'ws:': 'http:', 'wss:': 'https:' }[url.protocol] ?? url.protocol;
    const host = LOOPBACK.has(url.hostname) ? 'loopback' : url.hostname;
    return `${protocol}//${host}:${url.port || (protocol === 'https:' ? 443 : 80)}`;
  } catch { return null; }
}

// Web pages only, and never an AFM server (host MCP endpoint, run dashboards and APIs): a stage page must not
// reach the controls of AFM, the user's other projects or other stages.
export function stageRequestAllowed(rawUrl, blockedOrigins = []) {
  if (rawUrl.startsWith('about:')) return true;
  if (!WEB_SCHEMES.test(rawUrl)) return false;
  const key = afmOriginKey(rawUrl);
  return key !== null && !blockedOrigins.some(origin => afmOriginKey(origin) === key);
}

// A page of a stage browser: a WebContentsView in the stage's own in-memory partition, driven through its
// DevTools debugger. The view has no preload and no access to the shell, the dashboards or other stages.
class ElectronPage extends EventEmitter {
  constructor(view) {
    super();
    this.view = view;
    this.id = randomBytes(16).toString('hex').toUpperCase();
    const contents = view.webContents;
    this.closing = false;
    contents.debugger.attach('1.3');
    contents.debugger.on('message', (_event, method, params, sessionId) => this.emit('event', method, params, sessionId));
    contents.debugger.on('detach', () => this.#gone());
    contents.on('did-navigate', () => this.emit('changed'));
    contents.on('did-navigate-in-page', () => this.emit('changed'));
    contents.on('page-title-updated', () => this.emit('changed'));
    contents.on('destroyed', () => this.#gone());
    contents.setWindowOpenHandler(({ url }) => { this.emit('popup', url); return { action: 'deny' }; });
  }

  info() {
    const contents = this.view.webContents;
    return contents.isDestroyed() ? { url: '', title: '' } : { url: contents.getURL(), title: contents.getTitle() };
  }

  send(method, params, sessionId) {
    const contents = this.view.webContents;
    if (contents.isDestroyed()) return Promise.reject(Error('The page is closed.'));
    return contents.debugger.sendCommand(method, params, sessionId);
  }

  #gone() {
    if (!this.closing) this.close();
  }

  close() {
    if (this.closing) return;
    this.closing = true;
    const contents = this.view.webContents;
    this.view.setVisible(false);
    if (!contents.isDestroyed()) {
      try { contents.debugger.detach(); } catch { /* Already detached. */ }
      contents.close();
    }
    this.emit('closed');
  }
}

// The pages of one stage share one partition, so cookies and storage are the stage's own and are dropped with it.
// `blocked()` lists the origins of AFM servers known right now.
export function electronBackend({ electron, window, shortcut, blocked = () => [] }) {
  const partition = 'afm-stage-' + randomBytes(12).toString('hex');
  const pages = new Set();
  let session = null;
  const prepareSession = () => {
    if (session) return session;
    session = electron.session.fromPartition(partition);
    session.setPermissionRequestHandler((_contents, _permission, reply) => reply(false));
    session.on('will-download', event => event.preventDefault());
    // Stage pages are the web: never the app's files, Chrome's own pages or devtools.
    session.webRequest.onBeforeRequest((details, reply) => reply({ cancel: !stageRequestAllowed(details.url, blocked()) }));
    return session;
  };
  const attach = page => {
    const parent = window();
    if (parent && !page.view.webContents.isDestroyed()) parent.contentView.addChildView(page.view);
  };
  return {
    async createPage(url) {
      prepareSession();
      const view = new electron.WebContentsView({ webPreferences: {
        sandbox: true, nodeIntegration: false, contextIsolation: true, partition, backgroundThrottling: false,
      } });
      view.setBounds(DEFAULT_BOUNDS);
      view.setVisible(false);
      const page = new ElectronPage(view);
      pages.add(page);
      page.on('closed', () => { pages.delete(page); window()?.contentView.removeChildView(view); });
      view.webContents.on('before-input-event', (event, input) => {
        const action = shortcutAction(input);
        if (!action) return;
        event.preventDefault();
        if (typeof action !== 'number') window()?.webContents.focus();
        shortcut(action);
      });
      attach(page);
      // A fresh WebContentsView has an empty URL, which the pinned MCP excludes from its page list.
      try { await view.webContents.loadURL('about:blank'); }
      catch (error) { page.close(); throw error; }
      if (url !== 'about:blank') view.webContents.loadURL(url).catch(() => {});
      return page;
    },
    show(active, bounds) {
      for (const page of pages) page.view.setVisible(page === active);
      if (!active) return;
      const parent = window();
      if (!parent) return;
      const [width, height] = parent.getContentSize();
      const x = Math.max(0, Math.min(width, bounds.x)), y = Math.max(0, Math.min(height, bounds.y));
      active.view.setBounds({ x, y, width: Math.min(width - x, bounds.width), height: Math.min(height - y, bounds.height) });
    },
    focus(page) { if (page && !page.view.webContents.isDestroyed()) page.view.webContents.focus(); },
    hide() { for (const page of pages) page.view.setVisible(false); },
    // Windows come and go (macOS re-opens one); the pages must be re-added to the new window.
    reattach() { for (const page of pages) attach(page); },
    dispose() {
      for (const page of [...pages]) page.close();
      session?.clearStorageData().catch(() => {});
    },
  };
}

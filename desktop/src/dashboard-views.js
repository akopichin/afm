import { WebContentsView, session } from 'electron';
import { dashboardShortcutMessage } from './shortcuts.js';

// AFM serves its dashboard with frame-ancestors 'none'. Each verified run gets a top-level guest
// in the project window, and only that run's HTTP and WebSocket origin is available to the guest.
export class DashboardViews {
  constructor(window, preload) {
    this.window = window;
    this.preload = preload;
    this.pages = new Map();
    this.origins = new Map();
    this.session = session.fromPartition('afm-dashboards');
    this.session.setPermissionRequestHandler((_contents, _permission, reply) => reply(false));
    this.session.webRequest.onBeforeRequest((details, reply) => {
      const url = new URL(details.url);
      const origin = this.origins.get(details.webContentsId);
      const local = url.protocol === 'data:' || url.protocol === 'blob:' && url.origin === origin;
      if (url.protocol === 'ws:') url.protocol = 'http:';
      reply({ cancel: !origin || !(local || url.origin === origin) });
    });
  }

  async open(key, origin) {
    const previous = this.pages.get(key);
    if (previous?.origin === origin) return previous.loaded;
    this.drop(key);
    if (this.pages.size >= 8) this.drop(this.pages.keys().next().value);
    const view = new WebContentsView({ webPreferences: {
      session: this.session, preload: this.preload, sandbox: true, nodeIntegration: false, contextIsolation: true
    }});
    const wc = view.webContents;
    view.setVisible(false);
    this.window.contentView.addChildView(view);
    this.origins.set(wc.id, origin);
    wc.setWindowOpenHandler(() => ({action: 'deny'}));
    const guard = event => { if (new URL(event.url).origin !== origin) event.preventDefault(); };
    wc.on('will-frame-navigate', guard);
    wc.on('will-redirect', guard);
    wc.on('before-input-event', (event, input) => {
      if (key !== this.active) return;
      const message = dashboardShortcutMessage(input);
      if (!message) return;
      event.preventDefault();
      this.window.webContents.focus();
      this.window.webContents.send('dashboard:event', key, message);
    });
    wc.ipc.on('dashboard:event', (event, message) => {
      if (event.senderFrame !== wc.mainFrame || key !== this.active) return;
      this.window.webContents.send('dashboard:event', key, message);
    });
    const page = {origin, view, loaded: wc.loadURL(origin)};
    this.pages.set(key, page);
    try { await page.loaded; } catch (error) {
      if (this.pages.get(key) === page) this.drop(key);
      throw error;
    }
  }

  place(key, bounds) {
    if (this.active !== key) this.pages.get(this.active)?.view.setVisible(false);
    const page = this.pages.get(key);
    this.active = page && bounds ? key : null;
    if (!page) return;
    if (!bounds) { page.view.setVisible(false); return; }
    const [width, height] = this.window.getContentSize();
    const x = Math.min(bounds.x, width), y = Math.min(bounds.y, height);
    page.view.setBounds({x, y, width: Math.min(bounds.width, width - x), height: Math.min(bounds.height, height - y)});
    page.view.setVisible(true);
    // Revisiting a page keeps it in the bounded cache.
    this.pages.delete(key);
    this.pages.set(key, page);
  }

  command(key, action) {
    const page = this.pages.get(key);
    if (!page) return;
    if (action === 'focus') {
      if (key === this.active) page.view.webContents.focus();
    } else {
      page.view.webContents.send('dashboard:command', action);
    }
  }

  drop(key) {
    const page = this.pages.get(key);
    if (!page) return;
    if (key === this.active) this.active = null;
    this.origins.delete(page.view.webContents.id);
    this.pages.delete(key);
    this.window.contentView.removeChildView(page.view);
    page.view.webContents.close();
  }

  close() { for (const key of this.pages.keys()) this.drop(key); }
}

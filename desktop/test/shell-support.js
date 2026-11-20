import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
import { JSDOM, VirtualConsole } from 'jsdom';

const files = new URL('../renderer/', import.meta.url);

// jsdom cannot move native focus between windows; every other page error is collected and fails the test.
function pageErrors() {
  const virtualConsole = new VirtualConsole(), errors = [];
  virtualConsole.on('jsdomError', error => { if (!/Not implemented/.test(error.message)) errors.push(error.detail ?? error); });
  return { virtualConsole, errors };
}

export const tick = (ms = 10) => new Promise(resolve => setTimeout(resolve, ms));

// The real shell page and scripts in jsdom. Demo mode loads the approved sample app; production
// mode also loads the code that follows the main process snapshot, driven by the given nativeAFM.
export async function loadShell(t, { demo = true, platform = 'MacIntel', native = {} } = {}) {
  const { virtualConsole, errors } = pageErrors();
  const dom = new JSDOM(await readFile(new URL('index.html', files), 'utf8'), { url: 'http://desktop.test/' + (demo ? '?demo=1' : ''), runScripts: 'dangerously', pretendToBeVisual: true, virtualConsole });
  t.after(() => { dom.window.close(); assert.deepEqual(errors, [], 'the page threw while running'); });
  const w = dom.window;
  Object.defineProperty(w.navigator, 'platform', { value: platform, configurable: true });
  w.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  w.nativeAFM = { version: async () => '0.0.1', models: async () => [], prefs: async () => ({ gitMode: 'current' }), sources: async () => [], assistant: fakeAssistant(), liveLayout: async () => {}, onLiveShortcut: () => {}, ...native };
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new w.Event('close')); };
  const scripts = demo ? ['store', 'desktop', 'library', 'controls', 'attention'] : ['store', 'desktop', 'library', 'assistant', 'controls', 'attention', 'inventory', 'native'];
  // Classic scripts in page order, so a name used before its script has loaded fails here as it would in a browser.
  const addScript = text => { const script = w.document.createElement('script'); script.textContent = text; w.document.body.append(script); };
  for (const name of scripts) addScript(await readFile(new URL(name + '.js', files), 'utf8'));
  addScript('window.shell={projects,openProject,openLauncher,chooseWorkflow:index=>{choice=index;updateWorkflows()}};');
  const d = w.document;
  const get = id => d.getElementById(id);
  const key = (code, keyName, mods = {}, target = d.body) => {
    const event = new w.KeyboardEvent('keydown', { code, key: keyName, bubbles: true, cancelable: true, ...mods });
    target.dispatchEvent(event);
    return event;
  };
  return { w, d, get, key };
}

// What Command (Mac) or Control (elsewhere) looks like to the page.
export const primaryMods = platform => platform.startsWith('Mac') ? { metaKey: true } : { ctrlKey: true };

// A snapshot like the main process sends.
export const runRow = (id, extra = {}) => ({ key: `i-${id}:${id}`, runId: id, instanceId: 'i-' + id, name: 'flow-' + id, projectRoot: '/work/' + id,
  mode: 'dashboard', apiUrl: 'http://127.0.0.1:9', ownership: 'managed', origin: 'desktop', state: 'running', attention: false,
  can: { open: true, cancel: true, release: false, attach: false, report: true }, ...extra });
export const projectRow = (id, runs) => ({ id, name: id, path: '/work/' + id, runs });
export const notice = (runKey, stageId, seq, kind = 'approval') => ({ id: `${runKey}:${stageId}`, runKey, stageId, stageName: stageId.toUpperCase(), kind,
  text: kind === 'approval' ? 'Approve plan' : 'Answer question', seq });
export const snapshotOf = (projects, attention = [], extra = {}) => ({ host: { state: 'connected', error: null }, projects, unassigned: [], candidates: [], attention, ...extra });

// A nativeAFM stub that follows snapshots the test publishes and counts every request.
export function fakeNative(initial) {
  const native = { snapshot: initial, listeners: new Set(), calls: { dashboard: 0, settled: 0, cancel: [] }, opened: [] };
  Object.assign(native, {
    workspace: async () => native.snapshot,
    refreshWorkspace: async () => native.snapshot,
    onWorkspace: callback => { native.listeners.add(callback); return () => native.listeners.delete(callback); },
    dashboardLayout: async () => {},
    dropDashboard: async () => {},
    dashboardCommand: async (key, message) => {
      native.dashboardMessages ??= new Map();
      if (!native.dashboardMessages.has(key)) native.dashboardMessages.set(key, []);
      native.dashboardMessages.get(key).push(message);
    },
    onDashboardEvent: listener => { native.emitDashboard = listener; },
    dashboard: async (id, instance, external) => {
      native.calls.dashboard++;
      try { if (external) native.opened.push([id, instance]); return `http://127.0.0.1:${4000 + (Number(String(id).replace(/\D/g, '')) || 0)}`; }
      finally { native.calls.settled++; }
    },
    cancel: async (id, instance) => { native.calls.cancel.push([id, instance]); },
    publish: snapshot => { native.snapshot = snapshot; for (const callback of native.listeners) callback(snapshot); },
  });
  return native;
}

export const providerInfo = (id, extra = {}) => ({ id, name: id === 'claude' ? 'Claude Code' : 'Codex', kind: id, builtIn: true, credential: { type: 'cli' }, found: true,
  auth: { state: 'ready', via: 'cli-login', detail: 'Existing login' }, permissions: ['ask', 'read', 'edit', 'yolo'], reasoning: ['low', 'medium', 'high'],
  modes: { ask: `--${id}-ask`, read: `--${id}-read`, edit: `--${id}-edit`, yolo: `--${id}-yolo` }, ...extra });

// The assistant API of the main process, recording calls; tests push events with emit().
// Objects built in the page belong to another realm; a JSON copy compares by value.
const plain = value => JSON.parse(JSON.stringify(value ?? null));
export function fakeAssistant() {
  const fake = { listeners: new Set(), saved: [], calls: { scan: 0, send: [], cancel: 0, approve: [], configure: [], add: [], test: [], remove: [] },
    info: { settings: { provider: 'claude', model: '', reasoning: 'default', permission: 'ask', providers: [] }, scanned: false, providers: [providerInfo('claude'), providerInfo('codex')] } };
  Object.assign(fake, {
    state: async () => fake.info,
    scan: async () => { fake.calls.scan++; fake.info = { ...fake.info, scanned: true }; return fake.info; },
    configure: async changes => { changes = plain(changes); fake.calls.configure.push(changes); fake.info = { ...fake.info, settings: { ...fake.info.settings, ...changes } }; return fake.info; },
    addProvider: async input => { fake.calls.add.push(plain(input)); return fake.info; },
    removeProvider: async id => { fake.calls.remove.push(id); return fake.info; },
    testProvider: async id => { fake.calls.test.push(id); return { id, found: true, ok: true, version: '1.0', auth: { state: 'ready', detail: 'ok' } }; },
    send: async request => { fake.calls.send.push(plain(request)); if (fake.rejectSend) throw Error(fake.rejectSend); return { turnId: 't' + fake.calls.send.length }; },
    cancel: async () => { fake.calls.cancel++; return true; },
    approve: async (id, approved) => { fake.calls.approve.push([id, approved]); return true; },
    history: async () => fake.saved,
    onEvent: callback => { fake.listeners.add(callback); return () => fake.listeners.delete(callback); },
    emit: event => { for (const callback of fake.listeners) callback(event); },
  });
  return fake;
}

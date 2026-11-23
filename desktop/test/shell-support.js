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
export async function loadShell(t, { demo = true, platform = 'MacIntel', native = {}, windowControls = false } = {}) {
  const { virtualConsole, errors } = pageErrors();
  const dom = new JSDOM(await readFile(new URL('index.html', files), 'utf8'), { url: 'http://desktop.test/' + (demo ? '?demo=1' : ''), runScripts: 'dangerously', pretendToBeVisual: true, virtualConsole });
  t.after(() => { dom.window.close(); assert.deepEqual(errors, [], 'the page threw while running'); });
  const w = dom.window;
  Object.defineProperty(w.navigator, 'platform', { value: platform, configurable: true });
  if (windowControls) w.navigator.windowControlsOverlay = Object.assign(new w.EventTarget(), { visible: true });
  w.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  w.nativeAFM = { version: async () => '0.0.1', models: async () => [], prefs: async () => ({ gitMode: 'current' }), sources: async () => [], assistant: fakeAssistant(), watch: fakeWatch(), liveLayout: async () => {}, focusShell: async () => {}, onLiveShortcut: () => {}, onNoticeOpen: () => {}, onWatchOpen: () => {}, reportSelection: async () => {}, onSSHKeyPrompt: () => {}, onSSHKeyClosed: () => {}, ...native };
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new w.Event('close')); };
  const scripts = demo ? ['store', 'run-report', 'project-path', 'run-changes', 'desktop', 'library', 'tasks', 'report', 'mcp', 'controls', 'attention', 'costs'] : ['store', 'run-report', 'project-path', 'run-changes', 'desktop', 'library', 'assistant', 'tasks', 'report', 'mcp', 'controls', 'attention', 'inventory', 'costs', 'native'];
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
  const native = { snapshot: initial, listeners: new Set(), calls: { dashboard: 0, settled: 0, cancel: [] }, opened: [], selections: [], prefChanges: [],
    notifications: { enabled: true, kinds: { question: true, approval: true, decision: true, failed: false } } };
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
    onNoticeOpen: listener => { native.openNotice = listener; },
    onWatchOpen: listener => { native.openWatch = listener; },
    reportSelection: async runKey => { native.selections.push(runKey); },
    prefs: async () => ({ gitMode: 'current', notifications: native.notifications }),
    setPrefs: async changes => { Object.assign(native, changes); native.prefChanges.push(changes); return { gitMode: 'current', notifications: native.notifications }; },
    dashboard: async (id, instance, external) => {
      native.calls.dashboard++;
      try { if (external) native.opened.push([id, instance]); return `http://127.0.0.1:${4000 + (Number(String(id).replace(/\D/g, '')) || 0)}`; }
      finally { native.calls.settled++; }
    },
    cancel: async (id, instance) => { native.calls.cancel.push([id, instance]); },
    costsView: { hidePrices: false, runs: [] }, costsWatches: [], costsListeners: new Set(),
    periodCalls: [], periodResult: null,
    costsPeriod: async (...args) => { native.periodCalls.push(args); return (await native.periodResult?.(...args)) ?? { available: false, runs: {}, series: null }; },
    costs: async () => native.costsView,
    watchCosts: async watched => { native.costsWatches.push(watched); },
    onCosts: callback => { native.costsListeners.add(callback); return () => native.costsListeners.delete(callback); },
    publishCosts: view => { native.costsView = view; for (const callback of native.costsListeners) callback(view); },
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
  const fake = { listeners: new Set(), saved: [], calls: { scan: 0, send: [], followUp: [], cancel: 0, approve: [], configure: [], add: [], test: [], remove: [], clear: 0 },
    info: { settings: { provider: 'claude', model: '', reasoning: 'default', permission: 'ask', providers: [] }, scanned: false, providers: [providerInfo('claude'), providerInfo('codex')] } };
  Object.assign(fake, {
    state: async () => fake.info,
    scan: async () => { fake.calls.scan++; fake.info = { ...fake.info, scanned: true }; return fake.info; },
    configure: async changes => { changes = plain(changes); fake.calls.configure.push(changes); fake.info = { ...fake.info, settings: { ...fake.info.settings, ...changes } }; return fake.info; },
    addProvider: async input => { fake.calls.add.push(plain(input)); return fake.info; },
    removeProvider: async id => { fake.calls.remove.push(id); return fake.info; },
    testProvider: async id => { fake.calls.test.push(id); return { id, found: true, ok: true, version: '1.0', auth: { state: 'ready', detail: 'ok' } }; },
    testModel: async () => ({ ok: true, detail: 'Test request succeeded.' }),
    send: async request => { fake.calls.send.push(plain(request)); if (fake.rejectSend) throw Error(fake.rejectSend); return { turnId: 't' + fake.calls.send.length }; },
    followUp: async (id, queued) => { fake.calls.followUp.push([id, queued]); },
    cancel: async () => { fake.calls.cancel++; return true; },
    approve: async (id, approved) => { fake.calls.approve.push([id, approved]); return true; },
    history: async () => fake.saved,
    clear: async () => { fake.calls.clear++; if (fake.rejectClear) throw Error(fake.rejectClear); fake.saved = []; },
    onEvent: callback => { fake.listeners.add(callback); return () => fake.listeners.delete(callback); },
    emit: event => { for (const callback of fake.listeners) callback(event); },
  });
  return fake;
}

// The watch API of the main process: a list the test controls, the calls it receives and pushed changes.
export function fakeWatch(tasks = []) {
  const fake = { tasks, listeners: new Set(), calls: { pause: [], resume: [], stop: [], remove: [], markRead: [] } };
  const change = (name, apply) => async id => {
    fake.calls[name].push(id);
    const task = fake.tasks.find(item => item.id === id);
    if (!task) throw Error('That watch task no longer exists.');
    apply(task);
    fake.emit();
    return JSON.parse(JSON.stringify(task));
  };
  Object.assign(fake, {
    list: async () => JSON.parse(JSON.stringify(fake.tasks)),
    pause: change('pause', task => { task.status = 'paused'; }),
    resume: change('resume', task => { task.status = 'active'; }),
    stop: change('stop', task => { task.status = 'stopped'; }),
    remove: async id => { fake.calls.remove.push(id); fake.tasks = fake.tasks.filter(task => task.id !== id); fake.emit(); },
    markRead: change('markRead', task => { task.unread = 0; }),
    onChange: callback => { fake.listeners.add(callback); return () => fake.listeners.delete(callback); },
    emit: () => { for (const callback of fake.listeners) callback(JSON.parse(JSON.stringify(fake.tasks))); },
  });
  return fake;
}

export const watchTask = (id, extra = {}) => ({ id, title: 'Overnight watch', status: 'active', scope: { kind: 'all', projectId: null, runId: null, instanceId: null },
  events: ['question', 'failed'], deadline: null, summaryAt: null, createdAt: '2026-06-01T08:00:00.000Z', results: [], reports: [], unread: 0,
  summaryDue: false, summaryDone: false, ...extra });

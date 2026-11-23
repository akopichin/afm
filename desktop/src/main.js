import { desktopDir, writeAtomic } from './files.js';
import { app, BrowserWindow, clipboard, dialog, ipcMain, Menu, Notification, shell, session, safeStorage, WebContentsView } from 'electron';
import { randomUUID } from 'node:crypto';
import path from 'node:path';
import os from 'node:os';
import { realpath, readFile, rm } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { desktopEnvironment, refreshEnvironment, stopEnvironmentImports } from './environment.js';
import { PipelineOptions } from './pipeline-options.js';
import { Desktop } from './desktop.js';
import { LibraryLinks } from './library-links.js';
import { LiveView } from './live-view.js';
import { afmBinary } from './binary.js';
import { DashboardViews } from './dashboard-views.js';
import { Costs } from './costs.js';
import { CostsHistory } from './costs-history.js';
import { Workspace } from './workspace.js';
import { Notifier } from './notifier.js';
import { WatchService } from './watch/index.js';
import { SummaryRunner } from './watch/summary.js';
import { registerWatchIpc } from './watch/ipc.js';
import { createWatchBanners } from './watch/banners.js';
import { RepeatingTasks } from './watch/repeating.js';
import { sendTelegram, telegramSettings } from './watch/telegram.js';
import { DashboardOrigins, allowRequest } from './protocol.js';
import { Assistant } from './assistant/assistant.js';
import { loginShellPath } from './assistant/detect.js';
import { registerAssistantIpc } from './assistant/ipc.js';
import { StageBrowsers } from './stage-browser/broker.js';
import { createInspector } from './stage-browser/bind.js';
import { electronBackend } from './stage-browser/electron-page.js';
import { unlockSSHKey } from './ssh-key.js';

const demo = process.argv.includes('--demo');
if (app.isPackaged && !demo && !app.requestSingleInstanceLock()) app.exit(0);

const libraryLinks = new LibraryLinks(input => desktop.sources.add(input));
let readyForLinks = false;
function receiveLibraryLink(url) {
  if (demo || !libraryLinks.receive(url) || !readyForLinks) return;
  showWindow().then(() => window?.webContents.send('library:link', libraryLinks.current())).catch(console.error);
}
app.on('open-url', (event, url) => { event.preventDefault(); receiveLibraryLink(url); });
app.on('second-instance', (_event, argv) => {
  argv.forEach(receiveLibraryLink);
  if (readyForLinks) showWindow().catch(console.error);
});
process.argv.forEach(receiveLibraryLink);

const home = os.homedir();
const root = app.getAppPath();
// Files excluded from the asar archive live next to it.
const unpacked = file => file.replace(`app.asar${path.sep}`, `app.asar.unpacked${path.sep}`);
let window;
let dashboardViews;
// Stage browsers: runs started by the host Desktop starts open their browser stage inside the app.
const stageBrowsers = new StageBrowsers({
  home, inspect: createInspector(), product: 'Chrome/' + process.versions.chrome,
  launcher: {
    node: process.execPath, script: path.join(root, 'src/stage-browser/companion-main.js'), env: { ELECTRON_RUN_AS_NODE: '1' },
    // Loose files, not the asar archive: the pinned chrome-devtools-mcp is started like any Node program.
    mcp: { node: process.execPath, script: unpacked(path.join(root, 'node_modules/chrome-devtools-mcp/build/src/bin/chrome-devtools-mcp.js')), env: { ELECTRON_RUN_AS_NODE: '1', CHROME_DEVTOOLS_MCP_NO_UPDATE_CHECKS: '1', CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS: '1' } },
  },
  createBackend: () => electronBackend({ electron: { WebContentsView, session }, window: () => window, shortcut: action => window?.webContents.send('live:shortcut', action), blocked: afmOrigins }),
});
// Origins of AFM's own servers: the host endpoint and every run's API and dashboard.
function afmOrigins() {
  const runs = [...(workspace.snapshot?.projects ?? []).flatMap(project => project.runs), ...(workspace.snapshot?.unassigned ?? [])];
  return [desktop.host.state.endpoint, ...runs.map(run => run.apiUrl)].filter(Boolean);
}
const environment = () => desktopEnvironment(desktop.prefs, home);
const unlockSSH = process.platform === 'win32' ? undefined : chooseSSHKey;
const desktop = new Desktop({home, binary: () => afmBinary(app.isPackaged ? process.resourcesPath : undefined), environment,
  unlockSSH,
  hostEnv: async () => {
    const env = await environment();
    if (!(await desktop.prefs.get()).stageBrowser) return env;
    return {...env, ...await stageBrowsers.hostEnv(env.PATH)};
  }});
const pipelineOptions = new PipelineOptions({home, environment, unlockSSH, projects: desktop.projects, sources: desktop.sources,
  serveConfig: desktop.serveConfig, binary: () => afmBinary(app.isPackaged ? process.resourcesPath : undefined)});
const entry = pathToFileURL(path.join(root, 'renderer/index.html')).href;
const origins = new DashboardOrigins();
// The run page the renderer shows right now (null on Live or Home); a banner for it would only repeat what is on screen.
let shownRun = null;
// A banner that is not referenced can be garbage-collected before the user sees or clicks it.
const banners = new Set();
const notifier = new Notifier({ home, show: showBanner, settings: {enabled: false, kinds: {}},
  visible: item => Boolean(window?.isFocused() && shownRun === item.runKey),
  dock: app.dock && !demo ? {
    setPending: pending => app.dock.setIcon(path.join(root, 'assets', pending ? 'afm-desktop-attention.png' : 'afm-desktop.png')),
    bounce: () => { if (!window?.isFocused()) app.dock.bounce('informational'); },
  } : undefined,
  claimed: (item, snapshot) => watchTasks.covers(item.kind, item.runKey) });
const costs = new Costs({ history: new CostsHistory(desktop.home), readReport: run => desktop.runs.report(run.runId, run.instanceId) });
// Background watch tasks keep the one hidden-window poll running while any of them is active.
let publishWatch;
const watchBanners = createWatchBanners({ show: banner => showBanner(banner), visible: result => Boolean(window?.isFocused() && shownRun === result.runKey) });
const watchTasks = new WatchService({ home, onActiveChange: () => { workspace.wake(); repeatingTasks.wake(); }, onChange: () => publishWatch?.(), onResult: (task, result) => watchBanners.result(task, result), onSummaryDue: task => summaries.run(task).catch(error => console.error('Watch report:', error)), onError: error => console.error('Watch tasks were not saved:', error) });
const workspace = new Workspace({ desktop, stageBrowsers, costs, watchHidden: () => notifier.enabled || watchTasks.active, watchActive: () => watchTasks.active });
watchTasks.load().then(async () => { workspace.wake(); await repeatingTasks.load(); }).catch(error => console.error('Watch tasks:', error));
desktop.prefs.get().then(prefs => costs.setHidePrices(prefs.hidePrices)).catch(() => {});
const loadNotificationSettings = () => desktop.prefs.get().then(prefs => notifier.setSettings(prefs.notifications, workspace.snapshot?.attention));
// A stage browser appearing or going away changes the Live tabs at once, not at the next poll.
stageBrowsers.on('change', () => workspace.refresh().catch(() => {}));
// A Finder-launched app lacks the login shell's PATH; it is read once, when the assistant first needs it.
let loginPath;
const assistantKeyFile = id => path.join(desktopDir(home), 'assistant', 'secrets', id + '.bin');
const assistant = new Assistant({
  migrateHistory: true,
  codexAdapter: { runtime: process.execPath, script: app.isPackaged
    ? path.join(process.resourcesPath, 'codex-acp.mjs')
    : path.join(root, 'node_modules', '@agentclientprotocol', 'codex-acp', 'dist', 'index.js') },
  secrets: {
    set: (id, token) => {
      if (!safeStorage.isEncryptionAvailable() || safeStorage.getSelectedStorageBackend?.() === 'basic_text')
        throw Error('Secure storage is unavailable. Choose Environment variable or Key file.');
      return writeAtomic(assistantKeyFile(id), safeStorage.encryptString(token));
    },
    get: async id => safeStorage.decryptString(await readFile(assistantKeyFile(id))),
    remove: id => rm(assistantKeyFile(id), { force: true }),
  },
  home, env: environment, watch: { ...watchTasks.tools(), schedule: createRepeatingTask }, loginPath: () => (loginPath ??= loginShellPath(process.env)),
  context: {
    endpoint: async () => { await desktop.host.connect(); return desktop.host.state.endpoint; },
    runs: () => desktop.host.runs(),
    project: id => desktop.projects.get(id),
  },
  bridge: { command: process.execPath, script: path.join(root, 'src/assistant/bridge.js'), env: { ELECTRON_RUN_AS_NODE: '1' } },
});

// The scheduled report asks the assistant for a read-only reply and falls back to the plain collected facts.
const summaries = new SummaryRunner({ watch: watchTasks, ask: (id, prompt) => assistant.summarize(id, prompt), onReport: (task, report) => watchBanners.report(task, report) });
const repeatingTasks = new RepeatingTasks({ store: watchTasks.store, execute: task => assistant.runRepeating(task),
  deliver: async (task, text) => {
    const config = await telegramSettings(home, await environment());
    if (config.chatId !== task.chatId) throw Error('The configured Telegram chat changed. Create a task for the new chat.');
    await sendTelegram(config, text);
  },
  onError: error => console.error('Repeating task:', error),
});
async function createRepeatingTask(input, config) {
  if (input.delivery === 'telegram') {
    const { chatId } = await telegramSettings(home, await environment());
    config = { ...config, chatId };
  }
  return repeatingTasks.create(input, config);
}

function handle(name, handler) {
  ipcMain.handle(name, (event, ...args) => {
    if (event.sender !== window?.webContents || event.senderFrame !== event.sender.mainFrame
        || event.senderFrame.url.split(/[?#]/)[0] !== entry) throw Error('Unknown desktop caller.');
    return handler(...args);
  });
}
let sshPrompt;
let unlockingSSH = false;
function askSSHKey(key) {
  const owner = window;
  if (!owner || owner.isDestroyed() || sshPrompt) return null;
  return new Promise(resolve => {
    const id = randomUUID();
    const finish = value => {
      clearTimeout(timer);
      owner.removeListener('closed', cancel);
      sshPrompt = undefined;
      if (!owner.isDestroyed()) owner.webContents.send('ssh:closed', id);
      resolve(value);
    };
    const cancel = () => finish(null);
    const timer = setTimeout(cancel, 75000);
    owner.once('closed', cancel);
    sshPrompt = {id, finish};
    owner.webContents.send('ssh:prompt', {id, key: path.basename(key)});
  });
}
async function chooseSSHKey({ env }) {
  if (!window) return false;
  if (unlockingSSH) throw Error('An SSH key unlock is already in progress.');
  unlockingSSH = true;
  try {
    const picked = await dialog.showOpenDialog(window, {
      title: 'Choose your private SSH key',
      defaultPath: path.join(home, '.ssh'),
      properties: ['openFile', 'showHiddenFiles'],
    });
    if (picked.canceled || !picked.filePaths[0]) return false;
    return await unlockSSHKey({key:picked.filePaths[0], env, ask:askSSHKey, launcher:{
      node:process.execPath, script:path.join(root,'src/ssh-askpass-main.js'), env:{ELECTRON_RUN_AS_NODE:'1'},
    }});
  } finally { unlockingSSH = false; }
}
handle('ssh:reply', (id, answer) => {
  if (sshPrompt?.id !== id) return;
  sshPrompt.finish(typeof answer === 'string' && answer.length <= 4096 ? answer : null);
});
async function pickFolder() {
  const pick = await dialog.showOpenDialog(window, {properties:['openDirectory','createDirectory']});
  return pick.canceled ? null : realpath(pick.filePaths[0]);
}
async function projectSummary(project) {
  const details = await desktop.projectDetails(project.id);
  return {...project, git: details.git.git, flows: details.flows.map(f=>({name:f.name,kind:'Flow',path:f.file,policy:f.policy}))};
}
handle('projects:pick', async () => {
  const folder = await pickFolder();
  return folder ? projectSummary(await desktop.projects.add(folder)) : null;
});
handle('projects:overview', () => desktop.overview());
handle('projects:copy-path', projectPath => {
  if (typeof projectPath !== 'string' || !projectPath) throw Error('Invalid project path.');
  clipboard.writeText(projectPath);
});
handle('app:version', () => app.getVersion());
handle('workspace:get', () => workspace.get());
handle('workspace:refresh', () => workspace.refresh());
handle('costs:get', () => costs.view());
handle('costs:period', (from, to, buckets, options) => costs.period(Number(from), Number(to), Math.min(48, Math.max(0, Number(buckets) || 0)), options ?? {}));
// Watching starts the extra status reads at once; the renderer stops it when Home and Costs are both off screen.
handle('costs:watch', watched => { costs.setWatched(watched); if (costs.watched) workspace.refresh().catch(() => {}); });
// A folder only a run knows about has no Home entry yet; its details are readable because the host listed it.
handle('projects:details', async (id, folder) => {
  try { return await desktop.projectDetails(id); }
  catch (error) {
    const known = (await workspace.get()).unassigned.some(run => run.projectRoot === folder);
    if (!known) throw error;
    return desktop.folderDetails({ id, path: folder });
  }
});
handle('projects:add', async folder => projectSummary(await desktop.projects.add(await realpath(expandHome(String(folder))))));
handle('projects:remove', async id => desktop.projects.remove(id, {active:(await desktop.runs.forProject(id)).some(r=>r.state==='running')}));
handle('sources:list', () => desktop.sources.list());
handle('sources:add', location => desktop.sources.add({location}));
handle('library:link', () => libraryLinks.current());
handle('library:confirm-link', id => libraryLinks.confirm(String(id)));
handle('library:dismiss-link', id => libraryLinks.dismiss(String(id)));
handle('sources:pick', async () => { const folder = await pickFolder(); return folder ? desktop.sources.add({location: folder}) : null; });
handle('sources:remove', id => desktop.sources.remove(id));
handle('sources:refresh', id => desktop.sources.refresh(id));
handle('prefs:get', () => desktop.prefs.view());
handle('prefs:environment', async changes => {
  const prefs = await desktop.prefs.updateEnvironment(changes);
  assistant.resetDetections();
  return prefs;
});
// Imports again now, so a broken shell file is reported where the user pressed Refresh.
handle('prefs:environment-refresh', async () => {
  if (quitting) throw Error('Desktop is quitting, so the environment was not refreshed.');
  refreshEnvironment();
  assistant.resetDetections();
  await environment();
  return true;
});
handle('prefs:set', async changes => { const prefs = await desktop.prefs.set(changes); costs.setHidePrices(prefs.hidePrices); await loadNotificationSettings(); return desktop.prefs.view(); });
handle('notify:selection', runKey => { shownRun = typeof runKey === 'string' ? runKey : null; });
handle('host:status', () => desktop.host.state);
handle('host:restart', confirm => desktop.host.restart({confirmExternal: confirm === true}));
handle('runs:list', async () => desktop.host.runs());
handle('runs:project', id => desktop.runs.forProject(id));
// The same verified origin serves the embedded dashboard and the user's own browser.
handle('runs:open', async (id,instanceId,external) => {
  const origin=await desktop.runs.open(id,instanceId);
  if (external) await shell.openExternal(origin);
  else {
    origins.add(origin, `${instanceId}:${id}`);
    await dashboardViews.open(`${instanceId}:${id}`, origin);
  }
  return origin;
});
handle('dashboard:layout', (key, bounds) => {
  if (bounds && !['x','y','width','height'].every(k => Number.isSafeInteger(bounds[k]) && bounds[k] >= 0)) throw Error('Invalid dashboard bounds.');
  dashboardViews.place(key, bounds);
});
handle('dashboard:command', (key, message) => dashboardViews.command(key, message));
handle('dashboard:drop', key => dashboardViews.drop(key));
handle('navigation:focus-shell', () => window.webContents.focus());
handle('pipeline-options:get', (id, projectId) => pipelineOptions.get(id, projectId));
handle('pipeline-options:save', input => pipelineOptions.save(input));
handle('launch:preview', request => desktop.runs.preview(request));
handle('runs:start', async request => {
  // The first renderer sent a folder and a worktree name; accept it by registering the folder as a project.
  const projectId = request.projectId ?? (await desktop.projects.add(request.folder)).id;
  const explicit = request.explicit ?? (request.worktree ? 'worktree' : undefined);
  const worktree = request.worktree ?? undefined;
  try {
    if (request.kind === 'Pipeline' && !request.confirmHostRestart) {
      const options = await pipelineOptions.get(request.file, projectId, {forLaunch: true});
      if (options.error || options.issues.length) return {status: 'pipeline_options', options};
    }
    const launch = await desktop.runs.start({...request, projectId, explicit, worktree});
    return await desktop.runs.waitForLaunch(launch);
  }
  // Error codes do not survive IPC, so a host Desktop did not start is reported as data the UI can confirm.
  catch (error) { if (error.code === 'external_host') return {status: 'external_host', message: error.message}; throw error; }
});
handle('runs:launch-log', async id => {
  if (typeof id !== 'string' || !/^[a-f0-9]{64}$/.test(id)) throw Error('Invalid launch ID.');
  const error = await shell.openPath(path.join(home, '.afm', 'serve', 'logs', id + '.log'));
  if (error) throw Error('Could not open the AFM launch log.');
});
handle('runs:cancel', async (id, instanceId) => {
  instanceId ??= (await desktop.host.runs()).find(row=>row.run_id===id)?.instance_id;
  return desktop.runs.cancel(id, instanceId);
});
handle('runs:resume', async (id, instanceId, confirmHostRestart) => {
  try { return await desktop.runs.resume(id, instanceId, confirmHostRestart === true); }
  catch (error) { if (error.code === 'external_host') return {status:'external_host', message:error.message}; throw error; }
});
handle('runs:delete', (id, instanceId) => desktop.runs.delete(id, instanceId, async ({worktree, shared}) => {
  const workspace = worktree ? '\n\nGit will remove this worktree if it is clean and remove its branch if merged:\n'+worktree
    : shared ? '\n\nThe worktree is shared with another run and will be kept.' : '';
  const result = await dialog.showMessageBox(window, {
    type:'warning', message:'Delete this run?',
    detail:'This permanently removes the saved run log, state, and artifacts.'+workspace,
    buttons:['Cancel','Delete run'], defaultId:0, cancelId:0, noLink:true,
  });
  return result.response === 1;
}));
handle('runs:release', (id, instanceId) => desktop.runs.release(id, instanceId));
handle('runs:report', (id, instanceId) => desktop.runs.report(id, instanceId));
handle('runs:changes', (id, instanceId, mode) => desktop.runs.changes(id, instanceId, mode));
handle('runs:change-patch', (token, file) => desktop.runs.changePatch(token, file));
handle('runs:attach', endpoint => desktop.runs.attach(endpoint));
registerAssistantIpc(handle, assistant, event => window?.webContents.send('assistant:event', event));
publishWatch = registerWatchIpc(handle, watchTasks, tasks => window?.webContents.send('watch:changed', tasks));
handle('watch:schedule', async input => {
  const { title, prompt, everyMinutes, deadline, delivery } = input;
  await createRepeatingTask({ title, prompt, everyMinutes, deadline, delivery }, await assistant.prepareRepeating(input.scope ?? {}));
});
handle('watch:set-model', async (id, input) => {
  const { settings, providerName } = await assistant.prepareRepeating(null, { provider: input.provider, model: input.model, reasoning: 'default' });
  await repeatingTasks.setModel(String(id), settings, providerName);
});
let sampleView;
handle('live:focus', async stage => {
  if(!demo){ if(typeof stage==='string') stageBrowsers.focus(stage); return; }
  sampleView?.view.webContents.focus();
});
handle('live:layout', async (bounds, stage) => {
  if(!demo){
    if(bounds!==null&&!(['x','y','width','height'].every(key=>Number.isSafeInteger(bounds?.[key])&&bounds[key]>=0)))throw Error('Invalid tab bounds.');
    if(typeof stage==='string')stageBrowsers.place(stage,bounds);
    return;
  }
  if(!bounds){sampleView?.place(null);return}
  if(!['x','y','width','height'].every(key=>Number.isSafeInteger(bounds[key])&&bounds[key]>=0))throw Error('Invalid tab bounds.');
  if(!sampleView){
    sampleView=new LiveView(window,path.join(root,'renderer/checkout.html'),action=>window.webContents.send('live:shortcut',action));
  }
  sampleView.place(bounds);
  await sampleView.ready;
});
// The broker is cheap but not needed on the first-paint path, and it is off when the user turned stage browsers off.
function startStageBrowsers(){
  if(demo)return;
  desktop.prefs.get().then(prefs=>prefs.stageBrowser?stageBrowsers.start():null).catch(error=>console.error('Stage browser broker:',error));
}
function expandHome(folder){return folder==='~'||folder.startsWith('~/')?path.join(home,folder.slice(1)):folder}
function pushWorkspace(snapshot){
  origins.retain(new Set([...snapshot.projects.flatMap(p=>p.runs),...snapshot.unassigned].map(run=>run.key)));
  window?.webContents.send('workspace:update',snapshot);
}
workspace.onChange(pushWorkspace);
costs.onChange(view => window?.webContents.send('costs:update', view));
workspace.onChange(snapshot => notifier.onSnapshot(snapshot).catch(error => console.error('Notifications:', error)));
workspace.onPoll(snapshot => watchTasks.onSnapshot(snapshot));
// The renderer registers its banner listener while loading, so every opener waits for the same load.
let windowLoaded = Promise.resolve();
// Only the newest banner click navigates when several arrive while the window is still loading.
let latestTarget = null;
async function showWindow() {
  if (!window) { windowLoaded = openWindow(); await windowLoaded; stageBrowsers.reattach(); return; }
  await windowLoaded;
  if (!window) return;
  if (window.isMinimized()) window.restore();
  window.show();
  window.focus();
}
function showBanner({ title, body, runKey, stageId, watchTaskId, itemId }) {
  if (!Notification.isSupported()) return;
  const banner = new Notification({ title, body, icon: path.join(root, 'assets/afm-desktop.png'), silent: false });
  banners.add(banner);
  banner.on('close', () => banners.delete(banner));
  banner.on('failed', (_event, error) => { banners.delete(banner); console.error('Notification failed:', error); });
  banner.on('click', () => {
    // A stage request keeps the run and stage path; a watch result or summary opens in Tasks. The overflow banner only brings Desktop forward.
    const target = runKey ? { channel: 'notice:open', payload: { runKey, stageId } } : watchTaskId ? { channel: 'watch:open', payload: { taskId: watchTaskId, itemId } } : null;
    latestTarget = target;
    showWindow().then(() => { if (target && latestTarget === target) window?.webContents.send(target.channel, target.payload); }).catch(console.error);
  });
  banner.show();
}
function openWindow(){
  const frame = process.platform === 'darwin' ? {
    titleBarStyle: 'hidden', titleBarOverlay: true, transparent: true,
    vibrancy: 'titlebar', visualEffectState: 'active', backgroundColor: '#00000000',
  } : { backgroundColor: '#0b111b' };
  window=new BrowserWindow({width:1350,height:870,minWidth:480,minHeight:420,...frame,icon:path.join(root,'assets/afm-desktop.png'),
    webPreferences:{sandbox:true,contextIsolation:true,nodeIntegration:false,preload:path.join(root,'src/preload.cjs')}});
  dashboardViews = new DashboardViews(window, path.join(root, 'src/dashboard-keys.cjs'), path.join(root, 'renderer/focus.css'));
  window.webContents.setWindowOpenHandler(()=>({action:'deny'}));
  window.webContents.on('will-navigate',event=>event.preventDefault());
  function guard(event){
    const url=new URL(event.url);
    if(event.isMainFrame || !(origins.has(url.origin) || demo && url.href.startsWith(pathToFileURL(path.join(root,'.generated/run.html')).href+'?'))) event.preventDefault();
  }
  window.webContents.on('will-frame-navigate',guard);
  window.webContents.on('will-redirect',guard);
  window.on('close',()=>{dashboardViews.close();sampleView?.close();sampleView=null;stageBrowsers.place(null,null)});
  window.on('closed',()=>{window=null;costs.setWatched(false);workspace.setVisible(false)});
  // The sample app never talks to the real host.
  const watch=visible=>{if(!demo)workspace.setVisible(visible)};
  for(const [event,visible] of [['show',true],['restore',true],['hide',false],['minimize',false]])
    window.on(event,()=>watch(visible));
  watch(true);
  return window.loadURL(entry+(demo?'?demo=1':''));
}
app.whenReady().then(async () => {
  if (process.platform === 'darwin') {
    // Keep the native menu actions available while leaving Command-H and Command-M to Desktop.
    const macMenu = Menu.getApplicationMenu().items.map(menu => ({
      label: menu.label,
      submenu: menu.submenu.items.map(item => {
        if (item.role === 'hide') return { role: 'hide', accelerator: 'Command+Shift+H' };
        if (item.role === 'minimize') return { role: 'minimize', accelerator: 'Command+Shift+M' };
        return item;
      }),
    }));
    Menu.setApplicationMenu(Menu.buildFromTemplate(macMenu));
  }
  session.defaultSession.setPermissionRequestHandler((_contents, _permission, reply) => reply(false));
  session.defaultSession.webRequest.onBeforeRequest((details, reply) =>
    reply({ cancel: !allowRequest(details.url, { appRoot: path.resolve(root), origins, allowGitHub: demo }) }));
  loadNotificationSettings().catch(error => console.error('Notification settings:', error));
  windowLoaded = openWindow();
  readyForLinks = true;
  await windowLoaded;
  startStageBrowsers();
  if (process.argv.includes('--desktop-library-link-check')) {
    for (const label of ['cold', 'warm']) {
      const repository = 'https://example.invalid/' + label;
      const deadline = Date.now() + 12000;
      let found = false;
      while (!found && Date.now() < deadline) {
        found = await window.webContents.executeJavaScript(`(() => {
          const panel = document.querySelector('#settings[open] .library-link');
          return !!panel?.querySelector('#library-link-confirm') && panel.textContent.includes(${JSON.stringify(repository)});
        })()`);
        if (!found) await new Promise(resolve => setTimeout(resolve, 50));
      }
      if (!found) throw Error('Packaged Desktop did not review the ' + label + ' Library link.');
      console.log('Library link ready: ' + label);
    }
    app.quit();
    return;
  }
  if (process.argv.includes('--desktop-launch-check')) {
    const ready = await window.webContents.executeJavaScript(`(async () => {
      if (!document.querySelector('#settings[open] #finish-setup')) return false;
      document.querySelector('#finish-setup').click();
      document.querySelector('#assistant-toggle').click();
      return document.activeElement.id === 'chat-input' && Array.isArray(await window.nativeAFM.runs());
    })()`);
    if (!ready) throw Error('Packaged Desktop failed its first-run check.');
    const versionLabel = await window.webContents.executeJavaScript(`(() => {
      const label = document.getElementById('desktop-version');
      return label && !label.hidden && label.textContent;
    })()`);
    if (versionLabel !== 'v' + app.getVersion()) throw Error('Packaged Desktop did not display its version.');
    if (process.platform === 'darwin') {
      const titlebar = await window.webContents.executeJavaScript(`(async () => {
        const bar = document.getElementById('window-titlebar');
        const controls = navigator.windowControlsOverlay;
        // The overlay geometry can arrive after the renderer has loaded.
        const deadline = Date.now() + 3000;
        do {
          if (controls?.visible && controls.getTitlebarAreaRect().x > 0 && !bar.hidden
            && bar.getBoundingClientRect().height >= 30
            && getComputedStyle(document.body).backgroundColor === 'rgba(0, 0, 0, 0)'
            && getComputedStyle(document.querySelector('.app-body')).backgroundColor !== 'rgba(0, 0, 0, 0)') return true;
          await new Promise(resolve => setTimeout(resolve, 50));
        } while (Date.now() < deadline);
        return false;
      })()`);
      if (!titlebar) throw Error('Packaged Desktop failed its macOS title bar check.');
    }
    const probe = app.commandLine.getSwitchValue('desktop-check-run');
    if (probe) {
      const deadline = Date.now() + 12000;
      let rendered = false;
      while (!rendered && Date.now() < deadline) {
        if (!dashboardViews.active) await window.webContents.executeJavaScript(`(() => {
          const project = projects.find(p => p.runs.some(r => r.rawId === ${JSON.stringify(probe)}));
          if (project) openProject(project, project.runs.find(r => r.rawId === ${JSON.stringify(probe)}));
        })()`);
        const page = dashboardViews.pages.get(dashboardViews.active);
        if (page) rendered = await page.view.webContents.executeJavaScript("!!document.querySelector('[data-stage-id=visible-stage] .stage-row')");
        if (!rendered) await new Promise(resolve => setTimeout(resolve, 120));
      }
      if (!rendered) throw Error('The real AFM stage did not render in the packaged dashboard.');
      const page = dashboardViews.pages.get(dashboardViews.active);
      await window.webContents.executeJavaScript(`window.desktopCheckShortcuts = [];
        nativeAFM.onDashboardEvent((_key, message) => {
          if (message?.navigate || message === 'switch-project') window.desktopCheckShortcuts.push(message.navigate ?? message);
        });
        if (!document.getElementById('assistant').hidden) document.getElementById('close-chat').click();`);
      const shortcuts = [
        [{code:'KeyJ', meta:true}, 'next'], [{code:'KeyK', meta:true}, 'previous'], [{code:'KeyA', alt:true}, 'assistant'],
        [{code:'KeyM', meta:true}, 'model', 'quick-model'],
        [{code:'KeyK', meta:true, shift:true}, 'switch-project', 'picker'], [{key:',', meta:true}, 'settings', 'settings'], [{code:'KeyH', meta:true}, 'home'],
      ];
      const dispatch = input => {
        let prevented = false;
        page.view.webContents.emit('before-input-event', {preventDefault: () => { prevented = true; }}, {type:'keyDown', ...input});
        return prevented;
      };
      const waitFor = async (check, error) => {
        const until = Date.now() + 2500;
        while (Date.now() < until) {
          if (await check()) return;
          await new Promise(resolve => setTimeout(resolve, 35));
        }
        throw Error(error);
      };
      if (dispatch({code:'KeyZ', key:'z'})) throw Error('Desktop intercepted an ordinary key.');
      for (const [input, action, modal] of shortcuts) {
        await waitFor(() => dashboardViews.pages.get(dashboardViews.active) === page, 'AFM did not return after closing a Desktop overlay.');
        if (!dispatch(input)) throw Error(`Desktop did not intercept ${action}.`);
        await waitFor(() => window.webContents.executeJavaScript(`desktopCheckShortcuts.includes(${JSON.stringify(action)})`), `Desktop missed ${action} inside AFM.`);
        if (modal) {
          await waitFor(() => window.webContents.executeJavaScript(`document.getElementById(${JSON.stringify(modal)}).open`), `Desktop did not open ${modal}.`);
          await window.webContents.executeJavaScript(`document.getElementById(${JSON.stringify(modal)}).close()`);
        }
      }
      await waitFor(() => window.webContents.executeJavaScript(`location.hash === '' && !document.getElementById('assistant').hidden`),
        'Desktop did not open the assistant and return Home from AFM.');
    }
    console.log('AFM Desktop window and host ready');
    app.quit();
  }
}).catch(error => {
  console.error(error);
  app.exit(1);
});
// Clicking the Dock icon brings a hidden or minimized window back and refreshes at once, so nothing shown is stale.
app.on('activate',()=>{if(app.isReady())showWindow().then(()=>workspace.refresh()).catch(console.error)});
// Quit waits for assistant providers to stop (SIGTERM, then SIGKILL after the grace period), at most a few seconds.
// Shells still importing the environment are killed at once; their retries could outlast that deadline.
let quitting=false;
app.on('before-quit',event=>{
  if(quitting)return;
  event.preventDefault();quitting=true;
  stopEnvironmentImports();const repeatingClosed=repeatingTasks.close();const summariesClosed=summaries.close();stageBrowsers.close();workspace.close();costs.close();
  // Cancelling the Assistant ends a running report; watch tasks are saved only after that report stored its last change.
  const watchSaved=Promise.allSettled([assistant.close(),summariesClosed,repeatingClosed]).then(()=>watchTasks.flush()).catch(error=>console.error('Watch tasks were not saved on Quit:',error));
  const settled=Promise.allSettled([watchSaved,desktop.close()]);
  Promise.race([settled,new Promise(resolve=>setTimeout(resolve,6000))]).finally(()=>app.quit());
});
app.on('window-all-closed',()=>{if(process.platform!=='darwin')app.quit()});

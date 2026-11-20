import { app, BrowserWindow, dialog, ipcMain, shell, session, WebContentsView } from 'electron';
import path from 'node:path';
import os from 'node:os';
import { realpath } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { desktopEnvironment } from './environment.js';
import { PipelineOptions } from './pipeline-options.js';
import { Desktop } from './desktop.js';
import { LiveView } from './live-view.js';
import { afmBinary } from './binary.js';
import { DashboardViews } from './dashboard-views.js';
import { Workspace } from './workspace.js';
import { DashboardOrigins, allowRequest } from './protocol.js';
import { Assistant } from './assistant/assistant.js';
import { loginShellPath } from './assistant/detect.js';
import { registerAssistantIpc } from './assistant/ipc.js';
import { StageBrowsers } from './stage-browser/broker.js';
import { createInspector } from './stage-browser/bind.js';
import { electronBackend } from './stage-browser/electron-page.js';

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
const desktop = new Desktop({home, binary: () => afmBinary(app.isPackaged ? process.resourcesPath : undefined),
  hostEnv: async () => ({...await environment(), ...((await desktop.prefs.get()).stageBrowser ? await stageBrowsers.hostEnv() : {})})});
const environment = () => desktopEnvironment(desktop.prefs, home);
const pipelineOptions = new PipelineOptions({home, environment, projects: desktop.projects, sources: desktop.sources,
  serveConfig: desktop.serveConfig, binary: () => afmBinary(app.isPackaged ? process.resourcesPath : undefined)});
const entry = pathToFileURL(path.join(root, 'renderer/index.html')).href;
const demo = process.argv.includes('--demo');
const origins = new DashboardOrigins();
const workspace = new Workspace({ desktop, stageBrowsers });
// A stage browser appearing or going away changes the Live tabs at once, not at the next poll.
stageBrowsers.on('change', () => workspace.refresh().catch(() => {}));
// A Finder-launched app lacks the login shell's PATH; it is read once, when the assistant first needs it.
let loginPath;
const assistant = new Assistant({
  home, env: environment, loginPath: () => (loginPath ??= loginShellPath(process.env)),
  context: {
    endpoint: async () => { await desktop.host.connect(); return desktop.host.state.endpoint; },
    runs: () => desktop.host.runs(),
    project: id => desktop.projects.get(id),
  },
  bridge: { command: process.execPath, script: path.join(root, 'src/assistant/bridge.js'), env: { ELECTRON_RUN_AS_NODE: '1' } },
});

function handle(name, handler) {
  ipcMain.handle(name, (event, ...args) => {
    if (event.sender !== window?.webContents || event.senderFrame !== event.sender.mainFrame
        || event.senderFrame.url.split(/[?#]/)[0] !== entry) throw Error('Unknown desktop caller.');
    return handler(...args);
  });
}
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
handle('workspace:get', () => workspace.get());
handle('workspace:refresh', () => workspace.refresh());
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
handle('sources:pick', async () => { const folder = await pickFolder(); return folder ? desktop.sources.add({location: folder}) : null; });
handle('sources:remove', id => desktop.sources.remove(id));
handle('sources:refresh', id => desktop.sources.refresh(id));
handle('prefs:get', () => desktop.prefs.view());
handle('prefs:environment', async changes => {
  const prefs = await desktop.prefs.updateEnvironment(changes);
  assistant.detections.clear();
  return prefs;
});
handle('prefs:set', async changes => { await desktop.prefs.set(changes); return desktop.prefs.view(); });
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
handle('runs:release', (id, instanceId) => desktop.runs.release(id, instanceId));
handle('runs:attach', endpoint => desktop.runs.attach(endpoint));
registerAssistantIpc(handle, assistant, event => window?.webContents.send('assistant:event', event));
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
function openWindow(){
  window=new BrowserWindow({width:1350,height:870,minWidth:480,minHeight:420,backgroundColor:'#0b111b',
    webPreferences:{sandbox:true,contextIsolation:true,nodeIntegration:false,preload:path.join(root,'src/preload.cjs')}});
  dashboardViews = new DashboardViews(window, path.join(root, 'src/dashboard-keys.cjs'));
  window.webContents.setWindowOpenHandler(()=>({action:'deny'}));
  window.webContents.on('will-navigate',event=>event.preventDefault());
  function guard(event){
    const url=new URL(event.url);
    if(event.isMainFrame || !(origins.has(url.origin) || demo && url.href.startsWith(pathToFileURL(path.join(root,'.generated/run.html')).href+'?'))) event.preventDefault();
  }
  window.webContents.on('will-frame-navigate',guard);
  window.webContents.on('will-redirect',guard);
  window.on('close',()=>{dashboardViews.close();sampleView?.close();sampleView=null;stageBrowsers.place(null,null)});
  window.on('closed',()=>{window=null;workspace.setVisible(false)});
  // The sample app never talks to the real host.
  const watch=visible=>{if(!demo)workspace.setVisible(visible)};
  for(const [event,visible] of [['show',true],['restore',true],['hide',false],['minimize',false]])
    window.on(event,()=>watch(visible));
  watch(true);
  return window.loadURL(entry+(demo?'?demo=1':''));
}
app.whenReady().then(async () => {
  session.defaultSession.setPermissionRequestHandler((_contents, _permission, reply) => reply(false));
  session.defaultSession.webRequest.onBeforeRequest((details, reply) =>
    reply({ cancel: !allowRequest(details.url, { appRoot: path.resolve(root), origins, allowGitHub: demo }) }));
  await openWindow();
  startStageBrowsers();
  if (process.argv.includes('--desktop-launch-check')) {
    const ready = await window.webContents.executeJavaScript(`(async () => {
      if (!document.querySelector('#settings[open] #finish-setup')) return false;
      document.querySelector('#finish-setup').click();
      document.querySelector('#assistant-toggle').click();
      return document.activeElement.id === 'chat-input' && Array.isArray(await window.nativeAFM.runs());
    })()`);
    if (!ready) throw Error('Packaged Desktop failed its first-run check.');
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
    }
    console.log('AFM Desktop window and host ready');
    app.quit();
  }
}).catch(error => {
  console.error(error);
  app.exit(1);
});
app.on('activate',()=>{if(app.isReady()&&!window)openWindow().then(()=>stageBrowsers.reattach())});
// Quit waits for assistant providers to stop (SIGTERM, then SIGKILL after the grace period), at most a few seconds.
let quitting=false;
app.on('before-quit',event=>{
  if(quitting)return;
  event.preventDefault();quitting=true;
  stageBrowsers.close();workspace.close();
  const settled=Promise.allSettled([assistant.close(),desktop.close()]);
  Promise.race([settled,new Promise(resolve=>setTimeout(resolve,6000))]).finally(()=>app.quit());
});
app.on('window-all-closed',()=>{if(process.platform!=='darwin')app.quit()});

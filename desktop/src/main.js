import { app, BrowserWindow, dialog, ipcMain, shell, session } from 'electron';
import path from 'node:path';
import os from 'node:os';
import { access, readdir, readFile, realpath } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { parse } from 'yaml';
import { Host, dashboardURL } from './host.js';
import { LiveView } from './live-view.js';
import { afmBinary } from './binary.js';

const home = os.homedir();
const host = new Host(home, () => afmBinary(app.isPackaged ? process.resourcesPath : undefined));
const root = app.getAppPath();
const entry = pathToFileURL(path.join(root, 'renderer/index.html')).href;
const demo = process.argv.includes('--demo');
const tabOrigins = new Set();
let window;
let inventory = [];

function handle(name, handler) {
  ipcMain.handle(name, (event, ...args) => {
    if (event.sender !== window?.webContents || event.senderFrame !== event.sender.mainFrame
        || event.senderFrame.url.split(/[?#]/)[0] !== entry) throw Error('Unknown desktop caller.');
    return handler(...args);
  });
}
async function lookup(runId, instanceId) {
  return (await host.runs()).find(row => row.run_id === runId && row.instance_id === instanceId);
}
async function serveConfig() {
  try { return parse(await readFile(path.join(home,'.afm','serve.yaml'),'utf8')) || {}; }
  catch (error) { if (error.code === 'ENOENT') return {}; throw error; }
}
handle('projects:pick', async () => {
  const pick = await dialog.showOpenDialog(window, {properties:['openDirectory']});
  if (pick.canceled) return null;
  const folder = await realpath(pick.filePaths[0]);
  let flows = [];
  try { flows = (await readdir(path.join(folder,'.afm','flows'),{withFileTypes:true}))
    .filter(f => f.isFile() && /\.ya?ml$/.test(f.name)).map(f => ({name:f.name,kind:'Flow',path:f.name})); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  const git = await access(path.join(folder,'.git')).then(()=>true,()=>false);
  return {path:folder,git,flows};
});
handle('runs:list', async () => { inventory=await host.runs();return inventory; });
handle('runs:open', async (id,instanceId,external) => {
  const origin=await dashboardURL(await lookup(id,instanceId));
  tabOrigins.add(origin);
  if (external) await shell.openExternal(origin);
  return origin;
});
handle('runs:start', async ({folder,kind,file,requestId,worktree}) => {
  const config=await serveConfig();
  const project=config.projects?.find(p=>path.resolve(p.path)===folder);
  if(!project)throw Error('This project is not registered in the active afm serve allowlist.');
  return host.tool('afm_run_start',{project:project.id,mode:'dashboard',request_id:requestId,
    ...(kind==='Pipeline'?{pipeline:file}:{flow:file}),...(worktree?{worktree}:{})});
});
handle('runs:cancel', (id) => host.tool('afm_run_cancel',{run_id:id}));
handle('models:list', async () => {
  const models=[];
  for(const [id,name] of [['claude','Claude Code'],['codex','Codex'],['gemini','Gemini CLI'],['ollama','Ollama']]){
    const found=await Promise.all((process.env.PATH||'').split(path.delimiter).filter(Boolean).map(dir=>
      access(path.join(dir,id+(process.platform==='win32'?'.exe':''))).then(()=>true,()=>false)));
    if(found.includes(true))models.push({id,name,model:'CLI default',auth:id==='ollama'?'Local endpoint':'Existing CLI login'});
  }
  return models;
});
let sampleView;
handle('live:layout', async (bounds) => {
  if(!demo)return;
  if(!bounds){sampleView?.place(null);return}
  if(!['x','y','width','height'].every(key=>Number.isSafeInteger(bounds[key])&&bounds[key]>=0))throw Error('Invalid tab bounds.');
  if(!sampleView){
    sampleView=new LiveView(window,path.join(root,'renderer/checkout.html'),action=>window.webContents.send('live:shortcut',action));
  }
  sampleView.place(bounds);
  await sampleView.ready;
});
function openWindow(){
  window=new BrowserWindow({width:1350,height:870,minWidth:480,minHeight:420,backgroundColor:'#0b111b',
    webPreferences:{sandbox:true,contextIsolation:true,nodeIntegration:false,preload:path.join(root,'src/preload.cjs')}});
  window.webContents.setWindowOpenHandler(()=>({action:'deny'}));
  window.webContents.on('will-navigate',event=>event.preventDefault());
  function guard(event){
    const url=new URL(event.url);
    if(event.isMainFrame || !(tabOrigins.has(url.origin) || demo && url.href.startsWith(pathToFileURL(path.join(root,'.generated/run.html')).href+'?'))) event.preventDefault();
  }
  window.webContents.on('will-frame-navigate',guard);
  window.webContents.on('will-redirect',guard);
  window.on('close',()=>{sampleView?.close();sampleView=null});
  window.on('closed',()=>{window=null});
  return window.loadURL(entry+(demo?'?demo=1':''));
}
app.whenReady().then(async () => {
  session.defaultSession.setPermissionRequestHandler((_contents, _permission, reply) => reply(false));
  session.defaultSession.registerPreloadScript({
    type: 'frame', filePath: path.join(root, 'src/dashboard-keys.cjs'),
    matches: ['http://127.0.0.1/*', 'http://localhost/*']
  });
  await openWindow();
  if (process.argv.includes('--desktop-launch-check')) {
    const ready = await window.webContents.executeJavaScript(`(async () => {
      if (!document.querySelector('#settings[open] #finish-setup')) return false;
      document.querySelector('#finish-setup').click();
      document.querySelector('#assistant-toggle').click();
      return document.activeElement.id === 'chat-input' && Array.isArray(await window.nativeAFM.runs());
    })()`);
    if (!ready) throw Error('Packaged Desktop failed its first-run check.');
    console.log('AFM Desktop window and host ready');
    app.quit();
  }
}).catch(error => {
  console.error(error);
  app.exit(1);
});
app.on('activate',()=>{if(app.isReady()&&!window)openWindow()});
app.on('window-all-closed',()=>{if(process.platform!=='darwin')app.quit()});

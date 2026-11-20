const {contextBridge,ipcRenderer}=require('electron');
if(process.isMainFrame)contextBridge.exposeInMainWorld('nativeAFM',{
  dashboardLayout: (key,bounds) => ipcRenderer.invoke('dashboard:layout',key,bounds),
  dashboardCommand: (key,message) => ipcRenderer.invoke('dashboard:command',key,message),
  dropDashboard: key => ipcRenderer.invoke('dashboard:drop',key),
  onDashboardEvent: callback => ipcRenderer.on('dashboard:event',(_event,key,message)=>callback(key,message)),
  focusLive:stage=>ipcRenderer.invoke('live:focus',stage),
  liveLayout:(bounds,stage)=>ipcRenderer.invoke('live:layout',bounds,stage),
  onLiveShortcut:callback=>ipcRenderer.on('live:shortcut',(_event,action)=>callback(action)),
  pickProject:()=>ipcRenderer.invoke('projects:pick'),
  runs:()=>ipcRenderer.invoke('runs:list'),
  dashboard:(id,instance,external=false)=>ipcRenderer.invoke('runs:open',id,instance,external),
  openLaunchLog:id=>ipcRenderer.invoke('runs:launch-log',id),
  pipelineOptions:(id,projectId)=>ipcRenderer.invoke('pipeline-options:get',id,projectId),
  savePipelineOptions:input=>ipcRenderer.invoke('pipeline-options:save',input),
  start:args=>ipcRenderer.invoke('runs:start',args),
  cancel:(id,instance)=>ipcRenderer.invoke('runs:cancel',id,instance),
  release:(id,instance)=>ipcRenderer.invoke('runs:release',id,instance),
  attach:endpoint=>ipcRenderer.invoke('runs:attach',endpoint),
  version:()=>ipcRenderer.invoke('app:version'),
  workspace:()=>ipcRenderer.invoke('workspace:get'),
  refreshWorkspace:()=>ipcRenderer.invoke('workspace:refresh'),
  onWorkspace:callback=>{
    const listener=(_event,snapshot)=>callback(snapshot);
    ipcRenderer.on('workspace:update',listener);
    return ()=>ipcRenderer.removeListener('workspace:update',listener);
  },
  projectDetails:(id,folder)=>ipcRenderer.invoke('projects:details',id,folder),
  addProject:folder=>ipcRenderer.invoke('projects:add',folder),
  overview:()=>ipcRenderer.invoke('projects:overview'),
  removeProject:id=>ipcRenderer.invoke('projects:remove',id),
  projectRuns:id=>ipcRenderer.invoke('runs:project',id),
  preview:request=>ipcRenderer.invoke('launch:preview',request),
  sources:()=>ipcRenderer.invoke('sources:list'),
  addSource:location=>ipcRenderer.invoke('sources:add',location),
  pickSource:()=>ipcRenderer.invoke('sources:pick'),
  removeSource:id=>ipcRenderer.invoke('sources:remove',id),
  refreshSource:id=>ipcRenderer.invoke('sources:refresh',id),
  onNoticeOpen:callback=>ipcRenderer.on('notice:open',(_event,target)=>callback(target)),
  reportSelection:runKey=>ipcRenderer.invoke('notify:selection',runKey),
  prefs:()=>ipcRenderer.invoke('prefs:get'),
  updateEnvironment:changes=>ipcRenderer.invoke('prefs:environment', changes),
  setPrefs:changes=>ipcRenderer.invoke('prefs:set',changes),
  assistant:{
    state:()=>ipcRenderer.invoke('assistant:state'),
    scan:()=>ipcRenderer.invoke('assistant:scan'),
    configure:changes=>ipcRenderer.invoke('assistant:configure',changes),
    addProvider:input=>ipcRenderer.invoke('assistant:add-provider',input),
    removeProvider:id=>ipcRenderer.invoke('assistant:remove-provider',id),
    testProvider:id=>ipcRenderer.invoke('assistant:test-provider',id),
    send:request=>ipcRenderer.invoke('assistant:send',request),
    cancel:()=>ipcRenderer.invoke('assistant:cancel'),
    approve:(id,approved)=>ipcRenderer.invoke('assistant:approve',id,approved),
    history:()=>ipcRenderer.invoke('assistant:history'),
    clear:()=>ipcRenderer.invoke('assistant:clear'),
    onEvent:callback=>{
      const listener=(_event,event)=>callback(event);
      ipcRenderer.on('assistant:event',listener);
      return ()=>ipcRenderer.removeListener('assistant:event',listener);
    }
  },
  hostStatus:()=>ipcRenderer.invoke('host:status'),
  restartHost:confirm=>ipcRenderer.invoke('host:restart',confirm)
});

const {contextBridge,ipcRenderer}=require('electron');
if(process.isMainFrame)contextBridge.exposeInMainWorld('nativeAFM',{
  dashboardLayout: (key,bounds) => ipcRenderer.invoke('dashboard:layout',key,bounds),
  dashboardCommand: (key,message) => ipcRenderer.invoke('dashboard:command',key,message),
  dropDashboard: key => ipcRenderer.invoke('dashboard:drop',key),
  focusShell: () => ipcRenderer.invoke('navigation:focus-shell'),
  onDashboardEvent: callback => ipcRenderer.on('dashboard:event',(_event,key,message)=>callback(key,message)),
  focusLive:stage=>ipcRenderer.invoke('live:focus',stage),
  liveLayout:(bounds,stage)=>ipcRenderer.invoke('live:layout',bounds,stage),
  onLiveShortcut:callback=>ipcRenderer.on('live:shortcut',(_event,action)=>callback(action)),
  pickProject:()=>ipcRenderer.invoke('projects:pick'),
  copyProjectPath:path=>ipcRenderer.invoke('projects:copy-path',path),
  runs:()=>ipcRenderer.invoke('runs:list'),
  dashboard:(id,instance,external=false)=>ipcRenderer.invoke('runs:open',id,instance,external),
  openLaunchLog:id=>ipcRenderer.invoke('runs:launch-log',id),
  pipelineOptions:(id,projectId)=>ipcRenderer.invoke('pipeline-options:get',id,projectId),
  savePipelineOptions:input=>ipcRenderer.invoke('pipeline-options:save',input),
  start:args=>ipcRenderer.invoke('runs:start',args),
  cancel:(id,instance)=>ipcRenderer.invoke('runs:cancel',id,instance),
  resume:(id,instance,confirm=false)=>ipcRenderer.invoke('runs:resume',id,instance,confirm),
  deleteRun:(id,instance)=>ipcRenderer.invoke('runs:delete',id,instance),
  release:(id,instance)=>ipcRenderer.invoke('runs:release',id,instance),
  runReport:(id,instance)=>ipcRenderer.invoke('runs:report',id,instance),
  runChanges:(id,instance,mode)=>ipcRenderer.invoke('runs:changes',id,instance,mode),
  runChangePatch:(token,file)=>ipcRenderer.invoke('runs:change-patch',token,file),
  attach:endpoint=>ipcRenderer.invoke('runs:attach',endpoint),
  version:()=>ipcRenderer.invoke('app:version'),
  workspace:()=>ipcRenderer.invoke('workspace:get'),
  refreshWorkspace:()=>ipcRenderer.invoke('workspace:refresh'),
  onWorkspace:callback=>{
    const listener=(_event,snapshot)=>callback(snapshot);
    ipcRenderer.on('workspace:update',listener);
    return ()=>ipcRenderer.removeListener('workspace:update',listener);
  },
  costs:()=>ipcRenderer.invoke('costs:get'),
  costsPeriod:(from,to,buckets,options)=>ipcRenderer.invoke('costs:period',from,to,buckets,options),
  watchCosts:watched=>ipcRenderer.invoke('costs:watch',watched),
  onCosts:callback=>{
    const listener=(_event,view)=>callback(view);
    ipcRenderer.on('costs:update',listener);
    return ()=>ipcRenderer.removeListener('costs:update',listener);
  },
  projectDetails:(id,folder)=>ipcRenderer.invoke('projects:details',id,folder),
  addProject:folder=>ipcRenderer.invoke('projects:add',folder),
  overview:()=>ipcRenderer.invoke('projects:overview'),
  removeProject:id=>ipcRenderer.invoke('projects:remove',id),
  projectRuns:id=>ipcRenderer.invoke('runs:project',id),
  preview:request=>ipcRenderer.invoke('launch:preview',request),
  sources:()=>ipcRenderer.invoke('sources:list'),
  onSSHKeyPrompt:callback=>ipcRenderer.on('ssh:prompt',(_event,prompt)=>callback(prompt)),
  onSSHKeyClosed:callback=>ipcRenderer.on('ssh:closed',(_event,id)=>callback(id)),
  answerSSHKey:(id,answer)=>ipcRenderer.invoke('ssh:reply',id,answer),
  addSource:location=>ipcRenderer.invoke('sources:add',location),
  libraryLink:()=>ipcRenderer.invoke('library:link'),
  confirmLibraryLink:id=>ipcRenderer.invoke('library:confirm-link',id),
  dismissLibraryLink:id=>ipcRenderer.invoke('library:dismiss-link',id),
  onLibraryLink:callback=>{
    const listener=(_event,link)=>callback(link);
    ipcRenderer.on('library:link',listener);
    return ()=>ipcRenderer.removeListener('library:link',listener);
  },
  pickSource:()=>ipcRenderer.invoke('sources:pick'),
  removeSource:id=>ipcRenderer.invoke('sources:remove',id),
  refreshSource:id=>ipcRenderer.invoke('sources:refresh',id),
  onNoticeOpen:callback=>ipcRenderer.on('notice:open',(_event,target)=>callback(target)),
  onWatchOpen:callback=>ipcRenderer.on('watch:open',(_event,target)=>callback(target)),
  reportSelection:runKey=>ipcRenderer.invoke('notify:selection',runKey),
  prefs:()=>ipcRenderer.invoke('prefs:get'),
  updateEnvironment:changes=>ipcRenderer.invoke('prefs:environment', changes),
  refreshEnvironment:()=>ipcRenderer.invoke('prefs:environment-refresh'),
  setPrefs:changes=>ipcRenderer.invoke('prefs:set',changes),
  assistant:{
    state:()=>ipcRenderer.invoke('assistant:state'),
    scan:()=>ipcRenderer.invoke('assistant:scan'),
    discoverProvider:id=>ipcRenderer.invoke('assistant:discover-provider',id),
    configure:changes=>ipcRenderer.invoke('assistant:configure',changes),
    addProvider:input=>ipcRenderer.invoke('assistant:add-provider',input),
    removeProvider:id=>ipcRenderer.invoke('assistant:remove-provider',id),
    updateProviderWorkspace:(id,cwd)=>ipcRenderer.invoke('assistant:update-provider-workspace',id,cwd),
    testProvider:id=>ipcRenderer.invoke('assistant:test-provider',id),
    testModel:(force,draft)=>ipcRenderer.invoke('assistant:test-model',force,draft),
    mcpList:()=>ipcRenderer.invoke('assistant:mcp-list'),
    mcpSave:input=>ipcRenderer.invoke('assistant:mcp-save',input),
    mcpImport:(text,projectId)=>ipcRenderer.invoke('assistant:mcp-import',text,projectId),
    mcpRemove:id=>ipcRenderer.invoke('assistant:mcp-remove',id),
    mcpTest:id=>ipcRenderer.invoke('assistant:mcp-test',id),
    send:request=>ipcRenderer.invoke('assistant:send',request),
    followUp:(turnId,queued)=>ipcRenderer.invoke('assistant:follow-up',turnId,queued),
    cancel:id=>ipcRenderer.invoke('assistant:cancel',id),
    approve:(id,approved)=>ipcRenderer.invoke('assistant:approve',id,approved),
    backlogPlans:(scope,completed)=>ipcRenderer.invoke('assistant:backlog-plans',scope,completed),
    backlog:(id,text,scope)=>ipcRenderer.invoke('assistant:backlog',id,text,scope),
    conversations:()=>ipcRenderer.invoke('assistant:conversations'),
    newConversation:()=>ipcRenderer.invoke('assistant:new-conversation'),
    history:id=>ipcRenderer.invoke('assistant:history',id),
    clear:id=>ipcRenderer.invoke('assistant:clear',id),
    onEvent:callback=>{
      const listener=(_event,event)=>callback(event);
      ipcRenderer.on('assistant:event',listener);
      return ()=>ipcRenderer.removeListener('assistant:event',listener);
    }
  },
  watch:{
    list:()=>ipcRenderer.invoke('watch:list'),
    schedule:input=>ipcRenderer.invoke('watch:schedule',input),
    setModel:(id,input)=>ipcRenderer.invoke('watch:set-model',id,input),
    pause:id=>ipcRenderer.invoke('watch:pause',id),
    resume:id=>ipcRenderer.invoke('watch:resume',id),
    stop:id=>ipcRenderer.invoke('watch:stop',id),
    remove:id=>ipcRenderer.invoke('watch:remove',id),
    markRead:id=>ipcRenderer.invoke('watch:read',id),
    onChange:callback=>{
      const listener=(_event,tasks)=>callback(tasks);
      ipcRenderer.on('watch:changed',listener);
      return ()=>ipcRenderer.removeListener('watch:changed',listener);
    }
  },
  hostStatus:()=>ipcRenderer.invoke('host:status'),
  restartHost:confirm=>ipcRenderer.invoke('host:restart',confirm)
});

const {contextBridge,ipcRenderer}=require('electron');
if(process.isMainFrame)contextBridge.exposeInMainWorld('nativeAFM',{
  liveLayout:bounds=>ipcRenderer.invoke('live:layout',bounds),
  onLiveShortcut:callback=>ipcRenderer.on('live:shortcut',(_event,action)=>callback(action)),
  pickProject:()=>ipcRenderer.invoke('projects:pick'),
  models:()=>ipcRenderer.invoke('models:list'),
  runs:()=>ipcRenderer.invoke('runs:list'),
  dashboard:(id,instance,external=false)=>ipcRenderer.invoke('runs:open',id,instance,external),
  start:args=>ipcRenderer.invoke('runs:start',args),
  cancel:id=>ipcRenderer.invoke('runs:cancel',id)
});

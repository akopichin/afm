'use strict';
function showNativeError(error) {
  const bar=$('native-error');
  bar.textContent=String(error?.message||error); bar.hidden=false;
  if($('launch').open)$('git-hint').textContent=bar.textContent;
}
// One snapshot from the main process carries projects, runs and notices; nothing here polls the host.
// A snapshot that changes nothing the page shows touches nothing.
let attentionSignature='[]';
let inventoryAvailable=false;
let lastSnapshot=null;
function applyWorkspace(snapshot){
  lastSnapshot=snapshot;
  const {changed,reordered}=reconcileInventory(projects,snapshot);
  const attention=JSON.stringify(snapshot.attention),noticesChanged=attention!==attentionSignature;
  attentionSignature=attention;attentionItems=snapshot.attention;
  const problem=snapshot.host.error?.message||(snapshot.host.state==='incompatible'?snapshot.host.reason:null);
  inventoryAvailable=snapshot.host.state==='connected'&&!snapshot.host.error;
  if(problem)showNativeError(problem);else if(!$('native-error').hidden)$('native-error').hidden=true;
  dashboards.retain(new Set(projects.flatMap(p=>p.runs.map(r=>r.id))));
  if(inventoryAvailable&&pendingStage&&!notices().some(item=>item.id===pendingStage.id))pendingStage=null;
  const {p}=selection();
  // Home lists every project; a project page only depends on its own runs.
  const stale=p?changed.has(p.id):changed.size>0||reordered;
  if(stale){const saved=rememberFocus();render();restoreFocus(saved)}
  else if(noticesChanged)paintQueue();
  openPendingNotice();
}
// A re-render replaces the screen's elements; a keyboard user keeps their place on the same control.
function rememberFocus(){
  const el=document.activeElement;
  if(!el||!$('screen').contains(el))return null;
  return {id:el.id,tag:el.tagName,data:JSON.stringify(el.dataset),text:el.textContent};
}
function restoreFocus(saved){
  if(!saved||$('screen').contains(document.activeElement))return;
  const same=el=>JSON.stringify(el.dataset)===saved.data&&(saved.data!=='{}'||el.textContent===saved.text);
  (saved.id?document.getElementById(saved.id):[...$('screen').querySelectorAll(saved.tag)].find(same))?.focus();
}
function refreshNative(){
  if(isDemo)return Promise.resolve();
  return nativeAFM.refreshWorkspace().then(applyWorkspace).catch(showNativeError);
}
document.addEventListener('click',event=>{
  if(!isDemo&&event.target.closest('a.shell-link')){
    event.preventDefault();
    const {r}=selection();
    if(r?.can.open)nativeAFM.dashboard(r.rawId,r.instance,true).catch(showNativeError);
  }
});
if(!isDemo){
  const keyDialog=$('ssh-key-dialog'), keyField=$('ssh-key-answer');
  let keyPrompt;
  function closeKeyPrompt(answer, send = true){
    const current=keyPrompt;
    keyPrompt=null;
    keyField.value='';
    if(keyDialog.open)keyDialog.close();
    if(current&&send)nativeAFM.answerSSHKey(current,answer).catch(() => {});
  }
  nativeAFM.onSSHKeyPrompt(({id,key})=>{
    keyPrompt=id;
    $('ssh-key-name').textContent=key;
    keyField.value='';
    keyDialog.showModal();
    keyField.focus();
  });
  nativeAFM.onSSHKeyClosed(id=>{if(id===keyPrompt)closeKeyPrompt(null,false)});
  keyDialog.addEventListener('cancel',e=>{e.preventDefault();closeKeyPrompt(null)});
  keyDialog.addEventListener('close',()=>{if(keyPrompt&&!keyDialog.open)closeKeyPrompt(null)});
  $('ssh-key-cancel').onclick=()=>closeKeyPrompt(null);
  $('ssh-key-form').onsubmit=e=>{e.preventDefault();closeKeyPrompt(keyField.value)};
  nativeAFM.onDashboardEvent((key,message)=>{
    const frame=[...dashboards.values()].find(page=>page.dataset.runKey===key);
    if(frame)document.dispatchEvent(new CustomEvent('native-dashboard-message',{detail:{frame,message}}));
  });
  const unsubscribe=nativeAFM.onWorkspace(applyWorkspace);
  addEventListener('pagehide',unsubscribe);
  nativeAFM.version().then(version=>{
    const label=$('desktop-version');
    label.textContent='v'+version;
    label.hidden=false;
  }).catch(showNativeError);
  nativeAFM.workspace().then(applyWorkspace).catch(showNativeError);
  startAssistant();
  startTasks();
  // Main skips a banner for the run page already on screen; it only needs the run's key, and only when it changes.
  let reportedRun;
  const reportSelection=()=>{
    const {r,tab}=selection();
    const runKey=r&&tab!=='live'?r.key:null;
    if(runKey===reportedRun)return;
    reportedRun=runKey;
    nativeAFM.reportSelection(runKey).catch(showNativeError);
  };
  addEventListener('hashchange',reportSelection);
  document.addEventListener('workspace-change',reportSelection);
  reportSelection();
  addEventListener('focus',refreshNative);
  document.addEventListener('visibilitychange',()=>{if(!document.hidden)refreshNative()});
}
// Both modes place a native view over the Live tab. A stage browser names its view; the demo has one sample.
{
  let tab, lastBounds, lastStage, lastDashboard, dashboardKey;
  function positionDashboard(overlaysClear){
    if(isDemo)return;
    const host=$('dashboard-host');
    const page=host.hidden ? null : host.querySelector('[data-ready]:not([hidden])');
    const key=page?.dataset.runKey;
    const box=page && overlaysClear ? host.getBoundingClientRect() : null;
    const bounds=box ? {x:Math.max(0,Math.round(box.x)),y:Math.max(0,Math.round(box.y)),width:Math.round(box.width),height:Math.round(box.height)} : null;
    const signature=JSON.stringify([key,bounds]);
    if(signature===lastDashboard)return;
    lastDashboard=signature;
    const changed=dashboardKey!==(bounds?key:null);
    dashboardKey=bounds?key:null;
    nativeAFM.dashboardLayout(dashboardKey,bounds).then(()=>{
      if(changed && bounds && dashboardKey===key){
        dashboardMessage(page,'ready');
        if(document.activeElement===page)dashboardMessage(page,'focus');
      }
    }).catch(showNativeError);
  }
  function layout(){
    const next=$('native-live-tab');
    if(next!==tab){
      if(tab)resizer.unobserve(tab);
      tab=next;if(tab)resizer.observe(tab);
    }
    const overlaysClear=!document.querySelector('dialog[open]') && $('notice-list').hidden && !document.body.classList.contains('sizing-assistant')
      && !(innerWidth<=800&&!$('assistant').hidden);
    positionDashboard(overlaysClear);
    const rect=tab && overlaysClear && tab.getBoundingClientRect();
    const bounds=rect?{x:Math.round(rect.x),y:Math.round(rect.y),width:Math.round(rect.width),height:Math.round(rect.height)}:null;
    const stage=tab?.dataset.stageBrowser;
    const signature=JSON.stringify([bounds,stage]);
    if(signature===lastBounds)return;
    lastBounds=signature;
    // Leaving a stage's tab hides that stage's view even though the next tab names nothing.
    const target=stage??lastStage;
    if(!isDemo&&!target)return;
    lastStage=stage;
    nativeAFM.liveLayout(bounds,target).catch(showNativeError);
  }
  const resizer=new ResizeObserver(layout);
  if(!isDemo)resizer.observe($('dashboard-host'));
  new MutationObserver(layout).observe(document.body,{subtree:true,childList:true,attributes:true,attributeFilter:['open','hidden','data-ready']});
  // A native view ignores pointer-events: none, so it steps aside while the assistant grip is dragged.
  new MutationObserver(layout).observe(document.body,{attributes:true,attributeFilter:['class']});
  nativeAFM.onLiveShortcut(action=>{
    if(action==='launch')openLauncher(selection().p);
    else if(action==='picker')showPicker();
    else navigateApp(action);
  });
}

'use strict';
function showNativeError(error) {
  const bar=$('native-error');
  bar.textContent=String(error?.message||error); bar.hidden=false;
  if($('launch').open)$('git-hint').textContent=bar.textContent;
}
// One snapshot from the main process carries projects, runs and notices; nothing here polls the host.
// A snapshot that changes nothing the page shows touches nothing.
let attentionSignature='[]';
function applyWorkspace(snapshot){
  const {changed,reordered}=reconcileInventory(projects,snapshot);
  const attention=JSON.stringify(snapshot.attention),noticesChanged=attention!==attentionSignature;
  attentionSignature=attention;attentionItems=snapshot.attention;
  const problem=snapshot.host.error?.message||(snapshot.host.state==='incompatible'?snapshot.host.reason:null);
  if(problem)showNativeError(problem);else if(!$('native-error').hidden)$('native-error').hidden=true;
  dashboards.retain(new Set(projects.flatMap(p=>p.runs.map(r=>r.id))));
  if(pendingStage&&!notices().some(item=>item.id===pendingStage.id))pendingStage=null;
  const {p}=selection();
  // Home lists every project; a project page only depends on its own runs.
  const stale=p?changed.has(p.id):changed.size>0||reordered;
  if(stale){const saved=rememberFocus();render();restoreFocus(saved)}
  else if(noticesChanged)paintQueue();
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
  const unsubscribe=nativeAFM.onWorkspace(applyWorkspace);
  addEventListener('pagehide',unsubscribe);
  nativeAFM.workspace().then(applyWorkspace).catch(showNativeError);
  startAssistant();
  addEventListener('focus',refreshNative);
  document.addEventListener('visibilitychange',()=>{if(!document.hidden)refreshNative()});
}
// Both modes place a native view over the Live tab. A stage browser names its view; the demo has one sample.
{
  let tab, lastBounds, lastStage;
  function layout(){
    const next=$('native-live-tab');
    if(next!==tab){
      if(tab)resizer.unobserve(tab);
      tab=next;if(tab)resizer.observe(tab);
    }
    const visible=tab && !document.querySelector('dialog[open]') && $('notice-list').hidden && !document.body.classList.contains('sizing-assistant')
      && !(innerWidth<=800&&!$('assistant').hidden);
    const rect=visible && tab.getBoundingClientRect();
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
  new MutationObserver(layout).observe(document.body,{subtree:true,childList:true,attributes:true,attributeFilter:['open','hidden']});
  // A native view ignores pointer-events: none, so it steps aside while the assistant grip is dragged.
  new MutationObserver(layout).observe(document.body,{attributes:true,attributeFilter:['class']});
  nativeAFM.onLiveShortcut(action=>{
    if(action==='launch')openLauncher(selection().p);
    else if(action==='picker')showPicker();
    else navigateApp(action);
  });
}

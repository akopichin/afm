'use strict';
function showNativeError(error) {
  const bar=$('native-error');
  bar.textContent=String(error?.message||error); bar.hidden=false;
  if($('launch').open)$('git-hint').textContent=bar.textContent;
}
let listing=false;
async function refreshNative(){
  if(isDemo||listing||document.hidden)return;
  listing=true;
  try{
    const rows=await nativeAFM.runs();
    const result=reconcileInventory(projects,rows,uid);
    $('native-error').hidden=true;
    if(result.added)save();
    if(result.changed.size){
      const {p}=selection();
      if(!p||result.changed.has(p.id))render();
      else paintQueue();
    }
  }catch(error){showNativeError(error)}finally{listing=false}
}
document.addEventListener('click',event=>{
  if(!isDemo&&event.target.closest('a.shell-link')){
    event.preventDefault();
    const {r}=selection();
    nativeAFM.dashboard(r.rawId,r.instance,true).catch(showNativeError);
  }
});
if(!isDemo){refreshNative();setInterval(refreshNative,5000);document.addEventListener('visibilitychange',refreshNative)}
if(isDemo){
  let tab, lastBounds;
  function layout(){
    const next=$('native-live-tab');
    if(next!==tab){
      if(tab)resizer.unobserve(tab);
      tab=next;if(tab)resizer.observe(tab);
    }
    const visible=tab && !document.querySelector('dialog[open]') && $('notice-list').hidden
      && !(innerWidth<=800&&!$('assistant').hidden);
    const rect=visible && tab.getBoundingClientRect();
    const bounds=rect?{x:Math.round(rect.x),y:Math.round(rect.y),width:Math.round(rect.width),height:Math.round(rect.height)}:null;
    const signature=JSON.stringify(bounds);
    if(signature!==lastBounds){lastBounds=signature;nativeAFM.liveLayout(bounds).catch(showNativeError)}
  }
  const resizer=new ResizeObserver(layout);
  new MutationObserver(layout).observe(document.body,{subtree:true,childList:true,attributes:true,attributeFilter:['open','hidden']});
  nativeAFM.onLiveShortcut(action=>{
    if(action==='launch')openLauncher(selection().p);
    else if(action==='picker')showPicker();
    else navigateApp(action);
  });
}

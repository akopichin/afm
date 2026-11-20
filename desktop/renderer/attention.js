'use strict';
const altKey=/Mac/i.test(navigator.platform)?'⌥':'Alt+';
$('assistant-toggle').title='AFM Assistant · '+altKey+'A';
$('home').title='All projects · '+altKey+'H';
let cursorNotice=null;
let focusedFrame=null;
let pendingStage=null;
// Notices of the real app come from running stages; the demo derives them from its sample runs.
function nativeNotices(){
  const byKey=new Map(projects.flatMap(p=>p.runs.map(r=>[r.key,{p,r}])));
  return attentionItems.flatMap(item=>{
    const target=byKey.get(item.runKey);
    return target?[{id:item.id,...target,at:item.seq,text:item.text,stage:item.stageId,stageName:item.stageName}]:[];
  });
}
function notices(){
  if(!isDemo)return nativeNotices();
  return activeRuns().flatMap(({p,r})=>{
    const base={p,r,at:r.noticeAt||0};
    if(r.state==='review')return [{...base,id:r.id+':review-plan',text:'Approve plan',stage:'review-plan',stageName:'review-plan'}];
    if(r.state==='question')return [{...base,id:r.id+':delivery',text:'Answer question',stage:'delivery',stageName:'delivery'}];
    if(r.live&&r.liveNotice)return [{...base,id:r.id+':live',text:'Live browser ready',stage:''}];
    return [];
  }).sort((a,b)=>a.at-b.at||a.id.localeCompare(b.id));
}
dashboards.isWaiting=id=>notices().some(item=>item.r.id===id);
function closeQueue(){
  $('notice-list').hidden=true;
  $('all-notices').setAttribute('aria-expanded','false');
}
function paintQueue(){
  const items=notices();
  $('attention').hidden=!items.length;
  if(!items.length)closeQueue();
  $('notice-count').textContent=items.length+'';
  $('next-notice').title='Next notification · '+altKey+'J';
  $('notice-list').innerHTML=`<div class="queue-title"><strong>Notifications</strong><kbd>${altKey}J / K</kbd></div>`+items.map(({id,p,r,text,stageName})=>`<button data-notice="${esc(id)}"><span><strong>${esc(p.name)}</strong> / ${esc(r.flow)}</span><small>${esc(text)}${stageName?' · '+esc(stageName):''}</small></button>`).join('');
}
function closeFocus(){
  dashboardMessage(focusedFrame,'exit-stage-focus');
  focusedFrame=null;
  document.body.classList.remove('desktop-focus');
}
function deliverStage(){
  const {r,tab}=selection();
  if(!pendingStage||pendingStage.r.id!==r?.id||tab==='live')return;
  dashboardMessage(dashboards.get(r.id),{type:'open-stage',stage:pendingStage.stage});
}
function jumpTo(item){
  if(!item)return;
  closeQueue();closeFocus();cursorNotice={id:item.id,at:item.at};
  pendingStage=item.stage?item:null;
  if(!item.stage){item.r.liveNotice=false;save()}
  openProject(item.p,item.r,item.stage?'run':'live');
  setTimeout(deliverStage,0);
  paintQueue();
}
// A clicked system banner opens its exact stage; a request that is already resolved opens the run only.
// A queued click needs a current inventory to distinguish a missing run from a lost connection.
let pendingNotice=null;
function openNoticeTarget(target){pendingNotice=target;openPendingNotice()}
function openPendingNotice(){
  if(!pendingNotice||!inventoryAvailable)return;
  const {runKey,stageId}=pendingNotice;
  const target=projects.flatMap(p=>p.runs.map(r=>({p,r}))).find(({r})=>r.key===runKey);
  pendingNotice=null;
  if(!target)return;
  // The user chose this request explicitly, so it takes priority over an open dialog.
  for(const dialog of document.querySelectorAll('dialog[open]'))dialog.close();
  const item=notices().find(n=>n.r.key===runKey&&n.stage===stageId);
  if(item){jumpTo(item);return}
  closeQueue();closeFocus();openProject(target.p,target.r,'run');
}
if(!isDemo)nativeAFM.onNoticeOpen(openNoticeTarget);
function stepNotice(direction){
  const items=notices();
  if(!items.length)return;
  let target;
  if(cursorNotice){
    const later=item=>item.at-cursorNotice.at||item.id.localeCompare(cursorNotice.id);
    target=direction>0?items.find(item=>later(item)>0):items.findLast(item=>later(item)<0);
  }
  jumpTo(target||(direction>0?items[0]:items.at(-1)));
}
function navigateApp(command){
  if(document.querySelector('dialog[open]'))return;
  if(command==='assistant'){
    const open=$('assistant').hidden||Boolean(focusedFrame);
    closeQueue();closeFocus();toggleChat(open);return;
  }
  if(command==='settings'){closeQueue();closeFocus();openSettings();return}
  if(command==='model'){showQuickModel();return}
  if(command==='live'){
    // The native Live view is outside the page's tab order; this opens the run's Live tab if needed and hands it the keyboard.
    closeFocus();
    const focusLive=()=>{
      const tab=$('native-live-tab');
      if(tab&&window.nativeAFM?.focusLive)nativeAFM.focusLive(tab.dataset.stageBrowser||null).catch(()=>{});
    };
    const active=document.querySelector('.tab.active[data-run]');
    const opener=active&&!$('native-live-tab')?document.querySelector(`.tab[data-view="live"][data-run="${active.dataset.run}"]`):null;
    if(opener){opener.click();setTimeout(focusLive,60)}else focusLive();
    return;
  }
  if(command==='home'){
    cursorNotice=null;pendingStage=null;closeFocus();closeQueue();location.hash='';
    $('home').focus();return;
  }
  stepNotice(command==='next'?1:-1);
}
$('next-notice').onclick=()=>stepNotice(1);
$('all-notices').onclick=()=>{
  const open=$('notice-list').hidden;
  $('notice-list').hidden=!open;$('all-notices').setAttribute('aria-expanded',String(open));
  if(open)$('notice-list').querySelector('button')?.focus();
};
$('notice-list').onclick=e=>{const id=e.target.closest('button')?.dataset.notice;if(id)jumpTo(notices().find(item=>item.id===id))};
document.addEventListener('pointerdown',e=>{if(!$('attention').contains(e.target))closeQueue()});
document.addEventListener('keydown',e=>{
  if(e.defaultPrevented||e.isComposing||e.repeat||document.querySelector('dialog[open]'))return;
  if(e.key==='Escape'&&!$('notice-list').hidden){closeQueue();$('all-notices').focus();return}
  if(e.altKey&&!e.shiftKey&&!e.ctrlKey&&!e.metaKey){
    const command={KeyH:'home',KeyJ:'next',KeyK:'previous',KeyM:'model',KeyA:'assistant',KeyL:'live'}[e.code];
    if(command){e.preventDefault();navigateApp(command)}return;
  }
  if(e.target.closest('input,textarea,select,[contenteditable="true"]')||e.ctrlKey||e.metaKey||e.shiftKey)return;
  const {r,tab}=selection(),frame=dashboards.get(r?.id);
  if(frame&&!frame.hidden&&tab!=='live'&&(e.code==='KeyF'||(focusedFrame&&e.key==='Escape'))){
    e.preventDefault();dashboardMessage(frame,e.key==='Escape'?'exit-stage-focus':'toggle-stage-focus');
  }
});
function resolveNotice(msg){
  const run=projects.flatMap(p=>p.runs).find(r=>r.id===msg.runId);
  if(!run||!['review','question'].includes(run.state))return;
  run.state='running';
  if(typeof msg.answer==='string')run.answer=msg.answer;
  if(pendingStage?.r===run)pendingStage=null;
  save();paintQueue();drawChat();if(!selection().p)renderHome();
}
window.addEventListener('storage',e=>{
  if(e.key==='afm.desktop.reply'&&e.newValue){
    const msg=JSON.parse(e.newValue);resolveNotice(msg);
    if(selection().r?.id===msg.runId)render();
  }
});
onDashboardMessage((frame,msg)=>{
  if(msg?.type==='notice-resolved'&&dashboards.get(msg.runId)===frame){resolveNotice(msg);return}
  const {r,tab}=selection();
  if(!r||tab==='live'||frame!==dashboards.get(r.id)||frame.hidden)return;
  if(msg?.type==='stage-focus'){
    focusedFrame=msg.active?frame:null;
    document.body.classList.toggle('desktop-focus',Boolean(focusedFrame));
    if(focusedFrame)frame.focus();
  }
  if(['home','next','previous','model','assistant','live'].includes(msg?.navigate))navigateApp(msg.navigate);
  if(msg==='stage-ready')deliverStage();
  if(msg==='stage-opened')pendingStage=null;
});
document.addEventListener('workspace-change',()=>{
  const {p,r,tab}=selection();
  if(focusedFrame&&(focusedFrame!==dashboards.get(r?.id)||tab==='live'))closeFocus();
  if(!p){cursorNotice=null;pendingStage=null}
  paintQueue();
  setTimeout(deliverStage,0);
});
paintQueue();

'use strict';
const $ = id => document.getElementById(id);
const esc = value => String(value).replace(/[&<>"']/g, c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const label = r => ({running:'Running',review:'Needs approval',question:'Needs reply',stopped:'Stopped'})[r.state];
const unavailable = r => ({finished:({interrupted:'This run was interrupted.',failed:'This run failed.'})[r.status]||'This run has finished.',offline:'This run is offline. Desktop reconnects when it answers again.',
  mismatch:'This address now belongs to a different run. Refresh the project.','api-only':'Core run · API only'})[r.hostState]||'This run is not available.';
const active = r => ['running','review','question'].includes(r.state);
const activeRuns = () => projects.flatMap(p=>p.runs.filter(active).map(r=>({p,r})));
const remembered = new Map();
const isMac = /Mac/i.test(navigator.platform);
if (isMac && navigator.windowControlsOverlay) {
  const overlay = navigator.windowControlsOverlay;
  const updateTitlebar = () => { $('window-titlebar').hidden = !overlay.visible; };
  overlay.addEventListener('geometrychange', updateTitlebar);
  updateTitlebar();
}
// The accepted shortcut modifier is Command on Mac and Control elsewhere; the other one keeps its text-editing meaning.
const primary = e => isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
const modKey = isMac ? '⌘' : 'Ctrl';
const ariaModKey = isMac ? 'Meta' : 'Control';
const MAX_DASHBOARDS = 8;
// Pages are created when first shown; over the limit the oldest one not waiting for a person is closed.
class DashboardPages extends Map {
  // Set by the notice code once it has loaded.
  isWaiting=()=>false;
  touch(key){const frame=super.get(key);if(frame){super.delete(key);super.set(key,frame)}return frame}
  set(key,frame){
    super.set(key,frame);
    while(this.size>MAX_DASHBOARDS){
      const victim=[...this.keys()].find(id=>id!==key&&!this.isWaiting(id))??[...this.keys()].find(id=>id!==key);
      if(victim===undefined)break;
      this.drop(victim);
    }
    return this;
  }
  drop(key){
    const frame=this.get(key);
    if(frame?.dataset.runKey)nativeAFM.dropDashboard(frame.dataset.runKey).catch(showNativeError);
    frame?.remove();this.delete(key);
  }
  retain(live){for(const key of [...this.keys()])if(!live.has(key))this.drop(key)}
}
const dashboards = new DashboardPages();
function dashboardMessage(frame, message) {
  if (!frame) return;
  if (frame.dataset.runKey) nativeAFM.dashboardCommand(frame.dataset.runKey, message).catch(showNativeError);
  else frame.contentWindow?.postMessage(message, '*');
}
function onDashboardMessage(callback) {
  window.addEventListener('message', event => {
    if (!event.source) return;
    const frame = [...dashboards.values()].find(item => item.contentWindow === event.source);
    if (frame) callback(frame, event.data);
  });
  document.addEventListener('native-dashboard-message', event => callback(event.detail.frame, event.detail.message));
}

// A dialog returns focus to where it came from, including a dashboard page, or to the shell if that is gone.
const showDialog = HTMLDialogElement.prototype.showModal;
HTMLDialogElement.prototype.showModal = function(){
  const origin = document.activeElement;
  this.addEventListener('close',()=>{
    const visible=[...dashboards.values()].find(frame=>!frame.hidden);
    const target=origin?.isConnected&&!origin.hidden&&origin!==document.body&&!origin.closest('[hidden]')?origin:visible??$('quick');
    target.focus();
    if(target.tagName==='IFRAME')target.contentWindow?.focus();
  },{once:true});
  return showDialog.call(this);
};
function selection(){
  const [id,runId,tab] = location.hash.slice(1).split('/');
  const p=projects.find(item=>item.id===id);
  const r=p?.runs.find(run=>run.id===runId)||p?.runs.at(-1);
  return {p,r,tab:tab||'run'};
}
let focusDashboard=false;
const resumingRuns=new Set();
const deletingRuns=new Set();
function openProject(p,r,tab){
  focusDashboard=true;
  const last=remembered.get(p.id);
  const run=r||p.runs.find(item=>item.id===last?.id)||p.runs.at(-1);
  const view=tab||(run?.id===last?.id?last?.tab:'run');
  location.hash=[p.id, run?.id, run?.live&&view==='live'?'live':'run'].filter(Boolean).join('/');
}
function artwork(index){
  const paths = [
    'M-10 64H60L88 36H142L166 60H218L246 32H310 M88 36L112 12H175L199 36H246',
    'M-10 56H58L82 32H146L172 58H232L260 30H312 M82 32L108 58H172 M146 32L168 10H214L234 30H260',
    'M-10 56H50L78 28H142L170 56H220L248 28H312 M78 28L106 56H170 M142 28L160 10H194L212 28H248'
  ];
  const coords = [[[60,64],[88,36],[142,36],[199,36],[218,60],[246,32]],[[58,56],[82,32],[146,32],[172,58],[214,10],[260,30]],[[50,56],[78,28],[142,28],[170,56],[220,56],[248,28]]];
  const i=index%3;
  return `<div class="art"><svg aria-hidden="true" viewBox="0 0 300 80" preserveAspectRatio="xMidYMid slice"><path d="${paths[i]}" stroke="currentColor" stroke-width="1.5" fill="none" opacity=".7"/>${coords[i].map(([x,y],n)=>`<circle cx="${x}" cy="${y}" r="${n===2?9:4}" fill="var(--surface-raised)" stroke="currentColor"/><circle cx="${x}" cy="${y}" r="${n===2?3.5:1.5}" fill="currentColor"/>`).join('')}</svg></div>`;
}
// The stage a run waits on, shown after the project name.
function stageLabel(r){
  if(isDemo)return ' / '+(r.state==='review'?'review-plan':r.state==='question'?'delivery':r.live?'live-run':'implement');
  const stage=attentionItems.find(item=>item.runKey===r.key)?.stageName;
  return stage?' / '+esc(stage):'';
}
const pinnedProjectIds=new Set(Array.isArray(state.pinnedProjects)?state.pinnedProjects:[]);
const pinIcon='<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M16 3 21 8 17.5 9.4 14 13l.5 3-2 2-6.5-6.5 2-2 3 .5 3.6-3.5L16 3ZM9 15l-6 6"/></svg>';
function rememberProjectPins(){
  state.pinnedProjects=[...pinnedProjectIds];
  save();
}
function projectTile(p,index,compact=false){
  const id=esc(p.id),name=esc(p.name),pinned=pinnedProjectIds.has(p.id);
  const scope=compact?'pinned':'recent',pinLabel=`${pinned?'Unpin':'Pin'} ${name}`;
  const running=p.runs.filter(active).length;
  return `<article class="project-tile tile${compact?' compact-tile':''}" style="--tone:${p.tone}">
    ${artwork(index)}${compact?'':`<div class="art project-cost-art" data-cost-project="${id}" hidden></div>`}
    <div class="tile-body"><h3><button type="button" class="open-tile" data-project="${id}" data-tile="${scope}">${name}</button></h3>
      <button type="button" class="path project-path" data-project-path="${id}" data-tile="${scope}" aria-controls="project-path-panel" aria-expanded="false">${esc(p.path)}</button></div>
    <div class="cardfooter"><i class="dot"></i>${running?running+' active run'+(running===1?'':'s'):'No active runs'}</div>
    <button type="button" class="pin-project icon" data-pin-project="${id}" data-tile="${scope}" aria-label="${pinLabel}" title="${pinLabel}" aria-pressed="${pinned}">${pinIcon}</button>
    ${compact?'':`<button class="tile-cost-link action" data-cost-link="${id}" aria-label="Costs for ${name}" hidden>Costs ↗</button><button class="forget-project icon" data-forget="${id}" aria-label="Remove ${name} from recent projects" title="Remove from recent projects">×</button>`}
  </article>`;
}
function renderHome(){
  closeProjectPath();
  const runs=activeRuns();
  const pinned=projects.map((project,index)=>({project,index})).filter(({project})=>pinnedProjectIds.has(project.id));
  $('screen').innerHTML=`<div class="home"><div class="heading"><div><h1>Projects</h1><p class="summary">${projects.length} recent projects · ${runs.length} active runs</p></div><div class="heading-actions"><button class="action" data-folder>Open folder…</button><button class="action primary" data-start>＋ New run</button></div></div>
  ${pinned.length?`<section aria-labelledby="pinned-projects-title"><div class="section-title"><h2 id="pinned-projects-title">Pinned projects</h2></div><div class="tiles pinned-tiles">${pinned.map(({project,index})=>projectTile(project,index,true)).join('')}</div></section>`:''}
  <section aria-labelledby="recent-projects-title"><div class="section-title"><h2 id="recent-projects-title">Recent projects</h2></div><div class="tiles">${projects.map((p,i)=>projectTile(p,i)).join('')}</div></section>
  <section><div class="section-title"><h2>Active runs</h2><span class="total">${runs.length}</span></div><div class="active-list">${runs.map(({p,r})=>`<button class="row" data-project="${p.id}" data-run="${r.id}" style="--tone:${p.tone}"><span class="r-icon">${r.source==='Terminal'?'›_':'▷'}</span><span class="r-name"><strong>${esc(r.flow)}</strong><small>${esc(p.name)}${stageLabel(r)}</small></span><span class="origin">${r.source}</span><span class="mini-state"><i class="dot"></i>${label(r)}</span><small class="row-cost" data-cost-run-key="${esc(r.key||'')}"></small><span class="chev">→</span></button>`).join('')||'<p class="empty">No active runs</p>'}</div></section></div>`;
  wireProjectPaths($('screen'));
  if(typeof paintHomeCosts==='function')paintHomeCosts();
}
function runURL(p,r){
  return '../.generated/run.html?'+new URLSearchParams({flow:r.flow,project:p.name,state:r.state,source:r.source,id:r.id,answer:r.answer||'',scenario:r.scenario||'',workspace:r.workspace||'',agent:r.agent||"codex",kind:r.kind||"",live:r.live?"yes":"no"});
}
function runTabStatus(run){
  if(run.hostState==='offline')return ['inactive','Offline'];
  if(run.hostState==='mismatch')return ['inactive','Run changed'];
  if(!active(run)&&run.hostState!=='api-only'){
    return ['inactive',({failed:'Failed',interrupted:'Interrupted',finished:'Finished'})[run.status]||'Finished'];
  }
  if(run.needsAttention||['review','question'].includes(run.state))return ['waiting','Needs attention'];
  return ['running','Running'];
}
function render(){
  closeProjectPath();
  const {p,r,tab}=selection();
  const wantFocus=focusDashboard;focusDashboard=false;
  document.dispatchEvent(new Event("workspace-change"));
  const onCosts=location.hash==='#costs';
  document.title=`${onCosts?'Costs':p?p.name:'Projects'} · AFM Desktop`;
  $('costs-nav').setAttribute('aria-current',onCosts?'page':'false');
  $('window-title').textContent=document.title;
  $('crumb').innerHTML=p?`<div class="crumb">/ <button data-switch>${esc(p.name)} ⌄</button></div>`:'';
  for(const frame of dashboards.values()) frame.hidden=true;
  const stage = $('dashboard-host');
  stage.hidden=true;
  if(onCosts){renderCosts();return}
  if(!p){renderHome();return}
  const liveTab=r?.live&&tab==='live';
  if(r)remembered.set(p.id,{id:r.id,tab:liveTab?'live':'run'});
  const tabs=p.runs.map((run,index)=>{
    const [status,statusLabel]=runTabStatus(run);
    const title=p.runs.length>1&&index<9?`${statusLabel} · ${modKey} ${index+1}`:statusLabel;
    const shortcut=p.runs.length>1&&index<9
      ?` aria-keyshortcuts="${ariaModKey}+${index+1}"`:'';
    return `<button class="tab ${r===run&&!liveTab?'active':''}" data-project="${p.id}" data-run="${run.id}" data-run-state="${status}" aria-label="${esc(run.flow)} · ${statusLabel}" title="${title}"${shortcut}><i class="dot" aria-hidden="true"></i>${esc(run.flow)}</button>${run.live?`<button class="tab ${r===run&&liveTab?'active':''}" data-project="${p.id}" data-run="${run.id}" data-view="live">◉ Live · ${esc(run.liveStage?.stageId||'live-run')}</button>`:''}`;
  }).join('');
  const continuing=resumingRuns.has(r?.key);
  const deleting=deletingRuns.has(r?.key);
  const deleteButton=r?.can?.delete
    ?`<button class="action" data-delete-run ${deleting||continuing?'disabled':''}>${deleting?'Deleting…':'Delete run…'}</button>`:'';
  const resumeButton=r?.can?.resume||continuing
    ?`<button class="action" data-resume ${continuing?'disabled aria-busy="true"':deleting?'disabled':''}>${continuing?'Resuming…':'Continue run'}</button>`:'';
  $('screen').innerHTML=`<div class="workspace"><nav class="tabs" aria-label="Project tabs"><div class="run-tabs">${tabs||'<span class="tab">Overview</span>'}</div><div class="spacer"></div>${r&&(isDemo||r.can.open)?`<a class="shell-link" target="_blank" rel="noopener" href="${isDemo?esc(runURL(p,r)):'#'}">Open in browser ↗</a>`:''}${r&&active(r)&&(isDemo?r.source==='Desktop':r.can.cancel)?'<button class="action" data-stop>Stop run</button>':''}${resumeButton}${deleteButton}${r?'<button class="action primary" data-start>＋ New run</button>':''}</nav><section id="content" class="view"></section></div>`;
  const area=$('content');
  if(!r){area.innerHTML=`<div class="project-idle"><h1>${esc(p.name)}</h1><p class="path">${esc(p.path)}</p><h2>No runs yet</h2><p class="muted">Choose a flow or pipeline to start in this folder.</p><button class="action primary" data-start>＋ New run</button></div>`;return}
  if(liveTab){
    const stage=r.liveStage;
    area.innerHTML=stage
      ?`<div class="livebar"><i class="dot"></i><code>${esc(stage.stageId)}</code><small>${esc(stage.title||stage.url||'Stage browser')}</small></div><div id="native-live-tab" class="view" data-stage-browser="${esc(stage.handle)}"></div>`
      :'<div class="livebar"><i class="dot"></i><code>Checkout</code><small>Demo browser</small></div><div id="native-live-tab" class="view"></div>';
    return
  }
  if(!isDemo&&!r.can.open){
    if(r.hostState==='finished'&&r.can.report)area.append(finishedRunReport(r));
    else area.innerHTML=`<p class="empty">${esc(unavailable(r))}</p>`;
    return
  }
  let frame=dashboards.touch(r.id);
  if(!frame){
    frame=document.createElement(isDemo ? 'iframe' : 'div');
    if (!isDemo) {
      frame.dataset.runKey=r.key;
      frame.tabIndex=0;
      frame.addEventListener('focus',()=>dashboardMessage(frame,'focus'));
    }
    stage.append(frame);
    dashboards.set(r.id,frame);
  }
  if(!isDemo){
    if(frame.dataset.hostState!==r.hostState){delete frame.dataset.requested;frame.dataset.hostState=r.hostState}
    if(!frame.dataset.requested){
      frame.dataset.requested='yes';
      frame.title=r.flow+' dashboard';
      window.nativeAFM.dashboard(r.rawId,r.instance).then(() => {
        if (!frame.isConnected) return nativeAFM.dropDashboard(r.key);
        frame.dataset.ready='yes';
      }).catch(error=>{
        delete frame.dataset.requested;showNativeError(error);
      });
    }
  }else if(frame.getAttribute('src')!==runURL(p,r)){
    frame.title=r.flow+' dashboard';
    frame.src=runURL(p,r);
  }
  frame.hidden=false;
  stage.hidden=false;
  area.hidden=true;
  // Keyboard commands such as F go to the page the user just opened.
  if(wantFocus){frame.focus();frame.contentWindow?.focus()}
}
function showPicker(){
  if(document.querySelector('dialog[open]'))return;
  $('query').value='';$('picker').showModal();refreshResults();$('query').focus();
}
let cursor=0;
function refreshResults(){
  const q=$('query').value.toLowerCase();
  const options=projects.filter(p=>[p.name,p.path,...p.runs.map(r=>r.flow)].some(s=>s?.toLowerCase().includes(q)));
  cursor=Math.max(0,Math.min(cursor,options.length-1));
  $('results').innerHTML=options.map((p,i)=>`<button class="result ${i===cursor?'current':''}" data-project="${p.id}"><strong>${esc(p.name)}</strong><small>${esc(p.path)}</small></button>`).join('')||'<p class="empty">No matching projects</p>';
}
$('query').oninput=()=>{cursor=0;refreshResults()};
$('query').onkeydown=e=>{
  if(['ArrowUp','ArrowDown'].includes(e.key)){e.preventDefault();cursor+=(e.key==='ArrowUp'?-1:1);refreshResults();$('results').querySelector('.current')?.scrollIntoView({block:'nearest'})}
  if(e.key==='Enter')$('results').querySelector('.current')?.click();
};
$('quick').onclick=showPicker;
$('home').onclick=()=>{location.hash=''};
$('quick').querySelector('kbd').textContent=`${modKey} Shift K`;
$('quick').setAttribute('aria-keyshortcuts',`${ariaModKey}+Shift+K`);
document.addEventListener('keydown',e=>{if(!e.repeat&&!e.isComposing&&primary(e)&&e.shiftKey&&!e.altKey&&e.code==='KeyK'){e.preventDefault();if($('picker').open)$('picker').close();else showPicker()}});
onDashboardMessage((frame,message)=>{
  const {p,r}=selection();
  if(!r||frame!==dashboards.get(r.id)||frame.hidden)return;
  if(message==='switch-project')showPicker();
  if(message==='new-run')openLauncher(p);

});
async function continueRun(project, run) {
  if (resumingRuns.has(run.key)) return;
  const originalTab = location.hash;
  resumingRuns.add(run.key);
  render();
  try {
    let result = await nativeAFM.resume(run.rawId, run.instance);
    if (result.status === 'external_host') {
      if (!confirm(result.message+'\n\nRestart that host and continue the run?')) return;
      result = await nativeAFM.resume(run.rawId, run.instance, true);
    }
    await refreshNative();
    if (result.message) { showNativeError(result.message); return; }
    const current = projects.find(p => p.id === project.id);
    const resumed = current?.runs.find(r => r.rawId === result.run_id && r.instance === result.instance_id);
    if (location.hash === originalTab && resumed) openProject(current, resumed);
  } catch (error) { showNativeError(error); }
  finally {
    resumingRuns.delete(run.key);
    if (selection().r?.key === run.key) render();
  }
}
async function deleteRun(project, run) {
  if (deletingRuns.has(run.key)) return;
  const originalTab = location.hash;
  deletingRuns.add(run.key);
  render();
  try {
    const result = await nativeAFM.deleteRun(run.rawId, run.instance);
    if (!result.deleted) return;
    if (remembered.get(project.id)?.id === run.id) remembered.delete(project.id);
    await refreshNative();
    if (location.hash === originalTab) {
      const current = projects.find(p => p.id === project.id);
      if (current) openProject(current);
      else location.hash = '';
    }
  } catch (error) { showNativeError(error); }
  finally {
    deletingRuns.delete(run.key);
    if (location.hash === originalTab && selection().r?.key === run.key) render();
  }
}
document.addEventListener('click',e=>{
  if(e.target.closest('.project-path-panel'))return;
  const el=e.target.closest('button')??e.target.closest('.project-tile')?.querySelector('.open-tile');if(!el)return;
  const {p,r}=selection();
  if(el.dataset.pinProject){
    const id=el.dataset.pinProject;
    if(!pinnedProjectIds.delete(id))pinnedProjectIds.add(id);
    rememberProjectPins();
    const tile=el.dataset.tile;
    renderHome();
    const buttons=[...$('screen').querySelectorAll('[data-pin-project]')].filter(button=>button.dataset.pinProject===id);
    (buttons.find(button=>button.dataset.tile===tile)??buttons[0])?.focus({preventScroll:true});
    return;
  }
  if(el.dataset.forget){
    const id=el.dataset.forget;
    const forgetPin=()=>{if(pinnedProjectIds.delete(id))rememberProjectPins()};
    if(isDemo){const i=projects.findIndex(item=>item.id===id);if(i>=0)projects.splice(i,1);forgetPin();save();render()}
    else nativeAFM.removeProject(id).then(()=>{forgetPin();return refreshNative()}).catch(showNativeError);
    return;
  }
  if(el.hasAttribute('data-switch'))showPicker();
  if(el.dataset.project){
    if($('picker').open)$('picker').close();
    const project=projects.find(item=>item.id===el.dataset.project);
    openProject(project,project.runs.find(run=>run.id===el.dataset.run),el.dataset.run?(el.dataset.view||'run'):undefined);
  }
  if(el.hasAttribute('data-folder'))$('open-project').showModal();
  if(el.hasAttribute('data-start'))openLauncher(p);
  if(el.hasAttribute('data-resume')&&r?.can?.resume){continueRun(p,r);return}
  if(el.hasAttribute('data-delete-run')&&r?.can?.delete){deleteRun(p,r);return}
  if(el.hasAttribute('data-stop')&&!isDemo){window.nativeAFM.cancel(r.rawId,r.instance).then(refreshNative).catch(showNativeError);return}
  if(el.hasAttribute('data-stop')&&r.source==='Desktop'){r.state='stopped';r.live=false;save();openProject(p,r,'run');render()}
  if(el.dataset.delivery){r.delivery=el.dataset.delivery;save();render()}
});
window.addEventListener('hashchange',render);
render();

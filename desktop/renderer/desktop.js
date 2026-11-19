'use strict';
const $ = id => document.getElementById(id);
const esc = value => String(value).replace(/[&<>"']/g, c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const label = r => ({running:'Running',review:'Needs approval',question:'Needs reply',stopped:'Stopped'})[r.state];
const unavailable = r => ({finished:'This run has finished.',offline:'This run is offline. Desktop reconnects when it answers again.',
  mismatch:'This address now belongs to a different run. Refresh the project.','api-only':'Core run · API only'})[r.hostState]||'This run is not available.';
const active = r => ['running','review','question'].includes(r.state);
const activeRuns = () => projects.flatMap(p=>p.runs.filter(active).map(r=>({p,r})));
const remembered = new Map();
const isMac = /Mac/i.test(navigator.platform);
// The accepted shortcut modifier is Command on Mac and Control elsewhere; the other one keeps its text-editing meaning.
const primary = e => isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
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
  drop(key){this.get(key)?.remove();this.delete(key)}
  retain(live){for(const key of [...this.keys()])if(!live.has(key))this.drop(key)}
}
const dashboards = new DashboardPages();
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
function renderHome(){
  const runs=activeRuns();
  $('screen').innerHTML=`<div class="home"><div class="heading"><div><h1>Projects</h1><p class="summary">${projects.length} recent projects · ${runs.length} active runs</p></div><div class="heading-actions"><button class="action" data-folder>Open folder…</button><button class="action primary" data-start>＋ New run</button></div></div>
  <section><div class="section-title"><h2>Recent projects</h2></div><div class="tiles">${projects.map((p,i)=>`<button class="tile" data-project="${p.id}" style="--tone:${p.tone}">${artwork(i)}<div class="tile-body"><h3>${esc(p.name)}</h3><p class="path">${esc(p.path)}</p></div><div class="cardfooter"><i class="dot"></i>${p.runs.some(active)?p.runs.filter(active).length+' active run'+(p.runs.filter(active).length===1?'':'s'):'No active runs'}</div></button>`).join('')}</div></section>
  <section><div class="section-title"><h2>Active runs</h2><span class="total">${runs.length}</span></div><div class="active-list">${runs.map(({p,r})=>`<button class="row" data-project="${p.id}" data-run="${r.id}" style="--tone:${p.tone}"><span class="r-icon">${r.source==='Terminal'?'›_':'▷'}</span><span class="r-name"><strong>${esc(r.flow)}</strong><small>${esc(p.name)}${stageLabel(r)}</small></span><span class="origin">${r.source}</span><span class="mini-state"><i class="dot"></i>${label(r)}</span><span class="chev">→</span></button>`).join('')||'<p class="empty">No active runs</p>'}</div></section></div>`;
}
function runURL(p,r){
  return '../.generated/run.html?'+new URLSearchParams({flow:r.flow,project:p.name,state:r.state,source:r.source,id:r.id,answer:r.answer||'',scenario:r.scenario||'',workspace:r.workspace||'',agent:r.agent||"codex",kind:r.kind||"",live:r.live?"yes":"no"});
}
function render(){
  const {p,r,tab}=selection();
  const wantFocus=focusDashboard;focusDashboard=false;
  document.dispatchEvent(new Event("workspace-change"));
  document.title=`${p?p.name:'Projects'} · AFM Desktop`;
  $('crumb').innerHTML=p?`<div class="crumb">/ <button data-switch>${esc(p.name)} ⌄</button></div>`:'';
  for(const frame of dashboards.values()) frame.hidden=true;
  const stage = $('dashboard-host');
  stage.hidden=true;
  if(!p){renderHome();return}
  const liveTab=r?.live&&tab==='live';
  if(r)remembered.set(p.id,{id:r.id,tab:liveTab?'live':'run'});
  const tabs=p.runs.map(run=>`<button class="tab ${r===run&&!liveTab?'active':''}" data-project="${p.id}" data-run="${run.id}">${esc(run.flow)}</button>${run.live?`<button class="tab ${r===run&&liveTab?'active':''}" data-project="${p.id}" data-run="${run.id}" data-view="live">◉ Live · ${esc(run.liveStage?.stageId||'live-run')}</button>`:''}`).join('');
  $('screen').innerHTML=`<div class="workspace"><nav class="tabs" aria-label="Project tabs"><div class="run-tabs">${tabs||'<span class="tab">Overview</span>'}</div><div class="spacer"></div>${r&&(isDemo||r.can.open)?`<a class="shell-link" target="_blank" rel="noopener" href="${isDemo?esc(runURL(p,r)):'#'}">Open in browser ↗</a>`:''}${r&&active(r)&&(isDemo?r.source==='Desktop':r.can.cancel)?'<button class="action" data-stop>Stop run</button>':''}${r?'<button class="action primary" data-start>＋ New run</button>':''}</nav><section id="content" class="view"></section></div>`;
  const area=$('content');
  if(!r){area.innerHTML=`<div class="project-idle"><h1>${esc(p.name)}</h1><p class="path">${esc(p.path)}</p><h2>No runs yet</h2><p class="muted">Choose a flow or pipeline to start in this folder.</p><button class="action primary" data-start>＋ New run</button></div>`;return}
  if(liveTab){
    const stage=r.liveStage;
    area.innerHTML=stage
      ?`<div class="livebar"><i class="dot"></i><code>${esc(stage.stageId)}</code><small>${esc(stage.title||stage.url||'Stage browser')}</small></div><div id="native-live-tab" class="view" data-stage-browser="${esc(stage.handle)}"></div>`
      :'<div class="livebar"><i class="dot"></i><code>Checkout</code><small>Demo browser</small></div><div id="native-live-tab" class="view"></div>';
    return
  }
  if(!isDemo&&!r.can.open){area.innerHTML=`<p class="empty">${esc(unavailable(r))}</p>`;return}
  let frame=dashboards.touch(r.id);
  if(!frame){
    frame=document.createElement('iframe');
    stage.append(frame);
    dashboards.set(r.id,frame);
  }
  if(!isDemo){
    if(frame.dataset.hostState!==r.hostState){delete frame.dataset.requested;frame.dataset.hostState=r.hostState}
    if(!frame.dataset.requested){
      frame.dataset.requested='yes';
      frame.title=r.flow+' dashboard';
      frame.setAttribute('sandbox','allow-scripts allow-same-origin allow-forms');
      window.nativeAFM.dashboard(r.rawId,r.instance).then(url=>{if(frame.getAttribute('src')!==url)frame.src=url}).catch(error=>{
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
document.querySelector('kbd').textContent=/Mac/i.test(navigator.platform)?'⌘ K':'Ctrl K';
document.addEventListener('keydown',e=>{if(!e.repeat&&!e.isComposing&&primary(e)&&!e.shiftKey&&!e.altKey&&e.key.toLowerCase()==='k'){e.preventDefault();if($('picker').open)$('picker').close();else showPicker()}});
window.addEventListener('message',e=>{
  const {p,r}=selection();
  if(!r||e.source!==dashboards.get(r.id)?.contentWindow)return;
  if(e.data==='switch-project')showPicker();
  if(e.data==='new-run')openLauncher(p);

});
document.addEventListener('click',e=>{
  const el=e.target.closest('button');if(!el)return;
  const {p,r}=selection();
  if(el.hasAttribute('data-switch'))showPicker();
  if(el.dataset.project){
    if($('picker').open)$('picker').close();
    const project=projects.find(item=>item.id===el.dataset.project);
    openProject(project,project.runs.find(run=>run.id===el.dataset.run),el.dataset.run?(el.dataset.view||'run'):undefined);
  }
  if(el.hasAttribute('data-folder'))$('open-project').showModal();
  if(el.hasAttribute('data-start'))openLauncher(p);
  if(el.hasAttribute('data-stop')&&!isDemo){window.nativeAFM.cancel(r.rawId,r.instance).then(refreshNative).catch(showNativeError);return}
  if(el.hasAttribute('data-stop')&&r.source==='Desktop'){r.state='stopped';r.live=false;save();openProject(p,r,'run');render()}
  if(el.dataset.delivery){r.delivery=el.dataset.delivery;save();render()}
});
window.addEventListener('hashchange',render);
render();

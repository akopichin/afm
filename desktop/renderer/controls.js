'use strict';
const modKey=/Mac/i.test(navigator.platform)?'⌘':'Ctrl';
const gitModes={worktree:'New worktree',branch:'New branch',current:'Current checkout'};
const option=(v,t,selected)=>`<option value="${esc(v)}"${selected?' selected':''}>${esc(t)}</option>`;
const gitOptions=selected=>Object.entries(gitModes).map(([v,t])=>option(v,t,v===selected)).join('');
const profile=()=>state.models.find(m=>m.id===state.profile)||state.models[0];
let choice=0, matches=[], settingsTab='models', onboarding=false;
const permissionLabels={ask:'Ask before actions',read:'Read only',edit:'Allow workspace edits',yolo:'YOLO · Full access'};
function permissionDetail(m){
  const mode=m.permission||'ask';
  if(m.id==='claude')return {ask:'--permission-mode default',read:'--permission-mode plan',edit:'--permission-mode acceptEdits',yolo:'--dangerously-skip-permissions'}[mode];
  if(m.id==='codex')return {ask:'--sandbox workspace-write · --ask-for-approval on-request',read:'--sandbox read-only',edit:'--sandbox workspace-write · --ask-for-approval never',yolo:'--dangerously-bypass-approvals-and-sandbox'}[mode];
  return 'Desktop tool permissions';
}
function reasoningOptions(m){
  const reasoning=m.id==='claude'?['low','medium','high','xhigh','max']:m.id==='codex'?['minimal','low','medium','high','xhigh']:['low','medium','high'];
  return option('default','Provider default',!m.reasoning||m.reasoning==='default')+reasoning.map(v=>option(v,v,v===m.reasoning)).join('');
}
function profileFields(m){
  return `<div class="profile-fields"><label>Model<input id="profile-model" value="${esc(m.model==='CLI default'?'':m.model)}" placeholder="CLI default" autocomplete="off"></label>
    <label>Reasoning<select id="profile-effort">${reasoningOptions(m)}</select></label>
    <label>Permissions<select id="profile-permissions">${Object.entries(permissionLabels).map(([k,v])=>option(k,v,k===(m.permission||'ask'))).join('')}</select></label></div>
    <p class="hint profile-mode" id="permission-detail">${esc(permissionDetail(m))}</p>`;
}
function wireProfileFields(){
  const m=profile();
  $('profile-model').oninput=e=>{
    m.model=e.target.value.trim()||'CLI default';save();drawChat();
    document.querySelector('#settings-body .workflow.chosen small').textContent=m.model+' · '+m.auth;
  };
  $('profile-effort').onchange=e=>{m.reasoning=e.target.value;save();drawChat()};
  $('profile-permissions').onchange=e=>{
    m.permission=e.target.value;save();drawChat();
    $('permission-detail').textContent=permissionDetail(m);
  };
}
let modelScan='', sourceMessage='', remoteBusy=false;
function openLauncher(p,query=''){
  if(document.querySelector('dialog[open]'))return;
  if(!projects.length){$('open-project').showModal();return}
  $('launch-project').innerHTML=projects.map(item=>option(item.id,item.name,item.id===p?.id)).join('');
  $('workflow-search').value=query;
  $('launch-git').innerHTML=option('auto','Automatic',true)+gitOptions('');
  $('launch-hint').textContent='↑ ↓ to choose · ↵ to start';
  choice=0;updateWorkflows();
  $('launch').showModal();$('workflow-search').focus();
}
function targetProject(){return projects.find(item=>item.id===$('launch-project').value)}
function workspace(){
  const p=targetProject(),item=matches[choice],manual=$('launch-git').value;
  if(!p.git)return {mode:'folder',name:'Current folder',from:'Project has no Git repository'};
  if(manual!=='auto')return {mode:manual,name:gitModes[manual],from:'This run'};
  const mode=item?.git||state.gitDefault;
  return {mode,name:gitModes[mode],from:item?.git?'Flow setting':'Desktop default'};
}
function updateGit(){
  $('launch-git').disabled=!targetProject()?.git;
  const w=workspace();
  $('git-hint').textContent=w.from+' · '+w.name;
}
function updateWorkflows(){
  const p=targetProject();
  const sources=[{name:'This project',items:p?.flows||[]},...state.sources];
  const query=$('workflow-search').value.toLowerCase().trim();
  matches=sources.flatMap(s=>s.items.map(item=>({...item,source:s.name}))).filter(item=>[item.name,item.kind,item.source,item.path].join(' ').toLowerCase().includes(query));
  choice=Math.max(0,Math.min(choice,matches.length-1));
  $('workflows').innerHTML=matches.map((item,i)=>`<button class="workflow ${i===choice?'chosen':''}" aria-pressed="${i===choice}" data-choice="${i}" title="${esc(item.path)}"><span class="glyph">${item.kind==='Flow'?'⑂':'▦'}</span><span><strong>${esc(item.name)}</strong><small>${esc(item.source)} · ${esc(item.kind)}</small></span><span class="tick">${i===choice?'✓':''}</span></button>`).join('')||'<p class="empty">No matching workflows. Add a source in Settings › Library.</p>';
  $('launch-submit').disabled=!matches.length;updateGit();
}
function startRun(){
  const p=targetProject(),item=matches[choice];
  if(!p||!item)return;
  const ws=workspace(),id=uid();
  if(!isDemo){
    if(ws.mode==='branch'){showNativeError(Error('Branch-only launch is pending. Choose current checkout or worktree.'));return}
    $('launch-submit').disabled=true;
    window.nativeAFM.start({requestId:id,folder:p.path,kind:item.kind,file:item.path,worktree:ws.mode==='worktree'?'desktop-'+id.slice(0,8):null})
      .then(()=>{$('launch').close();openProject(p);refreshNative()}).catch(showNativeError).finally(()=>{$('launch-submit').disabled=false});
    return;
  }
  const branch=(item.name.toLowerCase().replace(/[^a-z0-9-]+/g,'-').slice(0,32)||'run')+'-'+id.slice(0,5);
  const r={id,flow:item.name,kind:item.kind,workflowPath:item.path,library:item.source,source:'Desktop',state:'running',
    live:item.name==='feature-delivery',
    workspace:ws.name+(['branch','worktree'].includes(ws.mode)?' · '+branch:''), workspaceSource:ws.from};
  if(item.name==='code-review'){r.state='review';r.scenario='review'}
  r.noticeAt=Date.now();r.liveNotice=r.live;
  p.runs.push(r);save();$('launch').close();openProject(p,r,'run');
}
$('launch-submit').onclick=startRun;
$('launch-project').onchange=()=>{choice=0;updateWorkflows()};
$('launch-git').onchange=updateGit;
$('workflow-search').oninput=()=>{choice=0;updateWorkflows()};
$('workflow-search').onkeydown=e=>{
  if(e.key==='Enter'){e.preventDefault();startRun();return}
  if(e.key==='ArrowDown'||e.key==='ArrowUp'){
    e.preventDefault();choice+=e.key==='ArrowDown'?1:-1;updateWorkflows();
    $('workflows').querySelector('.chosen')?.scrollIntoView({block:'nearest'});
  }
};
$('workflows').onclick=e=>{
  const row=e.target.closest('[data-choice]');
  if(row){choice=Number(row.dataset.choice);updateWorkflows()}
};
function openSettings(first=false){
  onboarding=first;settingsTab='models';sourceMessage='';drawSettings();
  $('settings').showModal();
  if(first)scanModels();
}
function drawSettings(){
  $('settings-title').textContent=onboarding?'Set up AFM Desktop':'Settings';
  const tabs=onboarding?'':`<nav class="segments">${[['models','Models'],['library','Library'],['runs','Runs']].map(([v,t])=>`<button data-panel="${v}" aria-pressed="${v===settingsTab}">${t}</button>`).join('')}</nav>`;
  const body=$('settings-body');
  if(settingsTab==='models'){
    body.innerHTML=tabs+`<p class="muted">${onboarding?'Choose the model for your desktop assistant.':'Default assistant model'}</p>
      <div class="choices">${state.models.map(m=>`<button class="workflow ${m.id===state.profile?'chosen':''}" data-profile="${esc(m.id)}" aria-pressed="${m.id===state.profile}"><span class="glyph">${m.id==='claude'?'✳':'›_'}</span><span><strong>${esc(m.name)}</strong><small>${esc(m.model)} · ${esc(m.auth)}</small></span><span class="tick">${m.id===state.profile?'✓':''}</span></button>`).join('')}</div>
      ${profileFields(profile())}<div class="links"><button id="scan-models">Scan again</button><button id="add-model">＋ Other model</button></div><p class="hint" role="status">${esc(modelScan)}</p>
      <form id="model-form" hidden><label>Provider or CLI<input name="name" required placeholder="Ollama, Gemini CLI…"></label><label>Model or deployment<input name="model" required placeholder="Model ID"></label><label>Credential source<select name="auth"><option>Existing CLI login</option><option>Environment variable</option><option>Config file</option><option>Local endpoint</option></select></label><label id="reference-label" hidden><span>Source</span><input name="reference"></label><div class="actions"><button class="primary">Add model</button></div></form>
      ${onboarding?'<div class="actions"><button class="primary" id="finish-setup">Continue to projects →</button></div>':''}`;
    wireProfileFields();
    $('scan-models').onclick=scanModels;
    $('add-model').onclick=()=>{const f=$('model-form');f.hidden=!f.hidden;if(!f.hidden)f.elements.name.focus()};
    $('model-form').elements.auth.onchange=e=>{
      const val=e.target.value,input=$('model-form').elements.reference;
      $('reference-label').hidden=val==='Existing CLI login';
      input.required=!$('reference-label').hidden;
      input.value='';input.placeholder=val==='Environment variable'?'MODEL_API_KEY':val==='Config file'?'~/.config/provider/config.json':'http://localhost:11434';
      input.type=val==='Local endpoint'?'url':'text';
      if(val==='Environment variable')input.pattern='[A-Za-z_][A-Za-z0-9_]*';else input.removeAttribute('pattern');
      $('reference-label').querySelector('span').textContent=val==='Environment variable'?'Environment variable name':val==='Config file'?'Config file path':'Endpoint URL';
    };
    $('model-form').onsubmit=e=>{
      e.preventDefault();const f=new FormData(e.target);
      const name=f.get('name').trim(),model=f.get('model').trim();if(!name||!model)return;
      const m={id:uid(),name,model,auth:[f.get('auth'),f.get('reference')].filter(Boolean).join(' · ')};
      state.models.push(m);state.profile=m.id;save();drawSettings();drawChat();
    };
    if(onboarding)$('finish-setup').onclick=()=>$('settings').close();
  }else if(settingsTab==='library'){
    body.innerHTML=tabs+`<p class="muted">Flows and pipelines available in every project.</p><div class="source-list">${state.sources.map(s=>`<div><span><strong>${esc(s.name)}</strong><small>${s.items.length} workflows</small></span>${s.id!=='starter'?`<button class="icon" data-remove="${esc(s.id)}" aria-label="Remove ${esc(s.name)}">×</button>`:''}</div>`).join('')}</div>
      <div class="links"><button id="add-folder">＋ Local folder</button><button id="add-repo">＋ GitHub repository</button></div>
      <form id="repo-form" hidden><label>Public GitHub repository<input name="repo" required placeholder="owner/repository"></label><div class="actions"><button class="primary">Connect repository</button></div></form><p class="hint" role="status">${esc(sourceMessage)}</p>`;
    $('add-folder').onclick=()=>importFolder('source');
    $('add-repo').onclick=()=>{$('repo-form').hidden=false;$('repo-form').elements.repo.focus()};
    $('repo-form').onsubmit=async e=>{
      e.preventDefault();if(remoteBusy)return;remoteBusy=true;
      const name=e.target.elements.repo.value;
      sourceMessage='Reading workflow manifests…';drawSettings();
      try{addSource(await gitHubSource(name))}catch(err){sourceMessage=err.message}
      finally{remoteBusy=false;if(settingsTab==='library')drawSettings()}
    };
  }else{
    body.innerHTML=tabs+`<label>Default workspace for Git projects<select id="default-git">${gitOptions(state.gitDefault)}</select></label>
    <p class="muted">A flow’s workspace preference takes precedence. You can change it for an individual run in the launcher.</p>
    <div class="setting-row"><span>All projects</span><kbd>Alt H</kbd></div>
    <div class="setting-row"><span>Next / previous notification</span><kbd>Alt J / K</kbd></div>
    <div class="setting-row"><span>Stage focus</span><kbd>F · Esc</kbd></div>
    <div class="setting-row"><span>Open / close assistant</span><kbd>Alt A</kbd></div>
    <div class="setting-row"><span>Assistant model</span><kbd>Alt M</kbd></div>
    <div class="setting-row"><span>Quick launch</span><kbd>${modKey} Shift Enter</kbd></div>`;
    $('default-git').onchange=e=>{state.gitDefault=e.target.value;save()};
  }
}
function addSource(source){
  if(!source.items.length)throw Error('No flows or pipeline manifests found in this source.');
  const previous=state.sources.findIndex(s=>s.name===source.name&&s.id!=='starter');
  if(previous>=0)state.sources[previous]=source;else state.sources.push(source);
  sourceMessage=source.items.length+' workflows available in New run.';save();
}
async function scanModels(){
  const status= document.querySelector('#settings-body [role="status"]');
  modelScan='Scanning command-line agents…';if(status)status.textContent=modelScan;
  let detected=[];
  if(window.nativeAFM){
    detected=await window.nativeAFM.models();
    for(const model of detected)if(!state.models.some(m=>m.id===model.id))state.models.push(model);
    modelScan=detected.length?'Found on this machine: '+detected.map(m=>m.name).join(', ')+'.':'No agent CLI found on PATH.';
    save();
  }else if(location.protocol!=='file:'){
    try{
      const res=await fetch('preview-api/models',{signal:AbortSignal.timeout(2400)});
      if(res.ok){const result=await res.json();detected=result.models;
        for(const m of detected){const old=state.models.find(v=>v.id===m.id);if(!old)state.models.push(m)}
        save();
        modelScan=detected.length?'Found on this machine: '+detected.map(m=>m.name).join(', ')+'.':'No supported CLIs found on PATH. Add a model manually.';
      }else throw Error();
    }catch{modelScan='Preview profiles. Use the local preview server for CLI detection.'}
  }else{modelScan='Preview profiles. Use the local preview server for CLI detection.'}
  if($('settings').open&&settingsTab==='models'){
    const note=document.querySelector('#settings-body [role="status"]');
    if(note)note.textContent=modelScan;
    if($('model-form')?.hidden&&!$('settings-body').querySelector('input:focus,select:focus')&&detected.some(m=>!document.querySelector('[data-profile="'+CSS.escape(m.id)+'"]')))drawSettings();
  }
}
async function importFolder(kind){
  if(!window.showDirectoryPicker){$(kind==='project'?'folder':'source-folder').click();return}
  try{
    const handle=await showDirectoryPicker({mode:'read'});
    if(kind==='project'){await projectFromFolder(handle);return}
    addSource({id:uid(),name:handle.name,items:await catalog(await collectFiles(handle))});drawSettings();
  }catch(err){
    if(err.name==='AbortError')return;
    if(err.name==='SecurityError'){ $(kind==='project'?'folder':'source-folder').click();return }
    showFolderError(kind,err);
  }
}
function showFolderError(kind,err){
  if(kind==='project')$('open-project').querySelector('.muted').textContent=err.message;
  else{sourceMessage=err.message;drawSettings()}
}
for(const [id,kind] of [['folder','project'],['source-folder','source']]){
  $(id).onchange=async e=>{
    const list=Array.from(e.target.files);e.target.value='';if(!list.length)return;
    const name=list[0].webkitRelativePath.split('/')[0];
    try{
      const files=inputFiles(list);
      if(kind==='source'){addSource({id:uid(),name,items:await catalog(files)});drawSettings()}
      else addProject(name,list.some(f=>/^[^/]+\/\.git(?:\/|$)/.test(f.webkitRelativePath)),await catalog(new Map([...files].filter(([path])=>path.startsWith('.afm/flows/')))));
    }catch(err){showFolderError(kind,err)}
  };
}
$('browse-project').onclick=async()=>{
  try{const folder=await window.nativeAFM.pickProject();if(folder)addProject(folder.path,folder.git,folder.flows)}
  catch(error){showFolderError('project',error)}
};
$('path-form').onsubmit=e=>{
  e.preventDefault();const path=$('project-path').value.trim();
  if(path){addProject(path,$('path-git').checked);e.target.reset()}
};
$('settings-toggle').onclick=()=>openSettings();
$('settings').addEventListener('close',()=>{
  if(onboarding){state.setup=true;save();onboarding=false}
});
document.addEventListener('click',e=>{
  const el=e.target.closest('button');if(!el)return;
  if(el.hasAttribute('data-close'))el.closest('dialog')?.close();
  if(el.dataset.panel){settingsTab=el.dataset.panel;drawSettings()}
  if(el.dataset.profile){state.profile=el.dataset.profile;save();drawSettings();drawChat()}
  if(el.dataset.remove){state.sources=state.sources.filter(s=>s.id!==el.dataset.remove);save();drawSettings()}
});
document.addEventListener('keydown',e=>{
  if((e.ctrlKey||e.metaKey)&&e.shiftKey&&e.key==='Enter'){e.preventDefault();openLauncher(selection().p)}
});
function drawChat(){
  const {p,r}=selection(),m=profile();
  $('chat-context').textContent=p?`${p.name}${r?' / '+r.flow:''}`:'All projects';
  $('chat-model').textContent=[m.name,m.model,m.reasoning&&m.reasoning!=='default'?m.reasoning:null,permissionLabels[m.permission||'ask'],'Demo chat'].filter(Boolean).join(' · ');
  $('messages').innerHTML=state.chat.length?state.chat.map(msg=>`<div class="bubble ${msg.role==='user'?'mine':''}">${msg.context?`<small>${esc(msg.context)}</small>`:''}<p>${esc(msg.text)}</p>${(msg.links||[]).map(link=>`<button class="inline-link" data-project="${esc(link.project)}" data-run="${esc(link.run)}">${esc(link.name)} ↗</button>`).join('')}</div>`).join(''):'<div class="chat-empty"><span>✧</span><h3>What’s running?</h3><p>Explore your projects or launch a workflow.</p></div>';
  $('messages').scrollTop=$('messages').scrollHeight;
}
function toggleChat(open){
  $('assistant').hidden=!open;
  $('assistant-toggle').setAttribute('aria-expanded',String(open));
  if(open){drawChat();$('chat-input').focus()}else $('assistant-toggle').focus();
}
function sendMessage(text){
  if(!text.trim())return;
  const {p,r}=selection(),context=p?[p.name,r?.flow].filter(Boolean).join(' / '):'All projects';
  state.chat.push({role:'user',text,context});$('chat-input').value='';
  const reply={role:'assistant',text:''};
  const mentioned=[...projects].sort((a,b)=>b.name.length-a.name.length).find(item=>text.toLowerCase().includes(item.name.toLowerCase()))||p;
  if(!isDemo){reply.text='Desktop provider transport is pending in the integration plan.'}
  else if(/start|launch|запуст|запуск/i.test(text)){
    const workflow=state.sources.flatMap(s=>s.items).find(item=>text.includes(item.name));
    if(profile().permission==='read'){
      reply.text='This assistant has read-only access. You can start the run yourself with New run.';
    }else if(profile().permission==='yolo'&&mentioned&&workflow){
      openLauncher(mentioned,workflow.name);startRun();
      reply.text='Started '+workflow.name+' in '+mentioned.name+'.';
    }else{
      reply.text='Choose the project and workflow in New run.';
      openLauncher(mentioned,workflow?.name);
    }
  }else if(/run|active|attention|approval|статус|ран|работ|ожида|запущ/i.test(text)){
    const attention=/attention|approval|ожида/i.test(text);
    const explicit=projects.find(item=>text.toLowerCase().includes(item.path.toLowerCase()))||[...projects].sort((a,b)=>b.name.length-a.name.length).find(item=>text.toLowerCase().includes(item.name.toLowerCase()));
    const runs=activeRuns().filter(({p,r})=>(!attention||['review','question'].includes(r.state))&&(!explicit||p===explicit));
    reply.text=runs.length?runs.map(({p,r})=>`${p.name} / ${r.flow} — ${label(r)}`).join('\n'):attention?'No runs need approval.':'There are no active runs.';
    reply.links=runs.map(({p,r})=>({project:p.id,run:r.id,name:r.flow}));
  }else if(/flow|pipeline|флоу|пайплайн/i.test(text)){
    reply.text=state.sources.flatMap(s=>s.items.map(item=>`${item.name} · ${item.kind}`)).join('\n');
  }else{
    reply.text='I can list active runs, show approvals, find workflows, or open the launcher for a project.';
  }
  state.chat.push(reply);save();drawChat();
}
$('assistant-toggle').onclick=()=>toggleChat($('assistant').hidden);
$('close-chat').onclick=()=>toggleChat(false);
$('chat-form').onsubmit=e=>{e.preventDefault();sendMessage($('chat-input').value)};
$('chat-input').onkeydown=e=>{if(e.key==='Enter'&&!e.shiftKey&&!e.isComposing){e.preventDefault();sendMessage(e.target.value)}};
document.querySelector('.suggestions').onclick=e=>{if(e.target.dataset.prompt)sendMessage(e.target.dataset.prompt)};
document.addEventListener('workspace-change',drawChat);
$('launch-hint').title=`${modKey} Shift Enter`;
document.querySelectorAll('[data-start]').forEach(el=>el.title=`New run (${modKey} Shift Enter)`);
function showQuickModel(){
  if(document.querySelector('dialog[open]'))return;
  $('quick-agent').innerHTML=state.models.map(m=>option(m.id,m.name+(m.model==='CLI default'?'':' · '+m.model),m.id===state.profile)).join('');
  fillQuickModel();$('quick-model').showModal();$('quick-model-id').select();
}
function fillQuickModel(){
  const m=state.models.find(item=>item.id===$('quick-agent').value);
  $('quick-model-id').value=m.model==='CLI default'?'':m.model;
  $('quick-effort').innerHTML=reasoningOptions(m);
}
$('quick-agent').onchange=fillQuickModel;
$('quick-model-form').onsubmit=e=>{
  e.preventDefault();
  const m=state.models.find(item=>item.id===$('quick-agent').value);
  m.model=$('quick-model-id').value.trim()||'CLI default';
  m.reasoning=$('quick-effort').value;
  state.profile=m.id;save();drawChat();$('quick-model').close();
};
$('chat-model').onclick=()=>openSettings();
$('quick-model-key').onclick=showQuickModel;
$('quick-model-key').textContent=/Mac/i.test(navigator.platform)?'⌥M':'Alt+M';
if(!state.setup)openSettings(true);

const grip=$('assistant-resizer');
let preferredWidth=Number(state.assistantWidth)||355;
function applyAssistantWidth(requested){
  const max=Math.round(innerWidth*(innerWidth<=800?0.94:0.60));
  const min=Math.min(max,260);
  const width=Math.round(Math.max(min,Math.min(requested,max)));
  $('assistant').style.setProperty('--assistant-width',width+'px');
  grip.setAttribute('aria-valuenow',width);
  grip.setAttribute('aria-valuemin',min);
  grip.setAttribute('aria-valuemax',max);
  grip.setAttribute('aria-valuetext',width+' pixels');
  return width;
}
let drag=null;
grip.onpointerdown=e=>{
  if(e.button!==0||drag)return;
  drag={id:e.pointerId,x:e.clientX,width:$('assistant').getBoundingClientRect().width};
  grip.setPointerCapture(e.pointerId);
  document.body.classList.add('sizing-assistant');
};
grip.onpointermove=e=>{
  if(drag?.id===e.pointerId)applyAssistantWidth(drag.width+drag.x-e.clientX);
};
grip.onlostpointercapture=()=>{
  if(!drag)return;
  preferredWidth=Number(grip.getAttribute('aria-valuenow'));
  state.assistantWidth=preferredWidth;save();
  drag=null;document.body.classList.remove('sizing-assistant');
};
grip.onkeydown=e=>{
  const current=Number(grip.getAttribute('aria-valuenow'));
  const requested={ArrowLeft:current+20,ArrowRight:current-20,Home:0,End:innerWidth}[e.key];
  if(requested===undefined)return;
  e.preventDefault();
  preferredWidth=applyAssistantWidth(requested);state.assistantWidth=preferredWidth;save();
};
window.addEventListener('resize',()=>applyAssistantWidth(preferredWidth));
applyAssistantWidth(preferredWidth);

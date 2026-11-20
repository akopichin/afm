'use strict';
const gitModes={worktree:'New worktree',branch:'New branch',current:'Current checkout'};
const option=(v,t,selected)=>`<option value="${esc(v)}"${selected?' selected':''}>${esc(t)}</option>`;
const gitOptions=selected=>Object.entries(gitModes).map(([v,t])=>option(v,t,v===selected)).join('');
const profile=()=>state.models.find(m=>m.id===state.profile)||state.models[0];
let choice=0, matches=[], settingsTab='models', onboarding=false;
const permissionLabels={ask:'Ask before actions',read:'Read only',edit:'Allow workspace edits',yolo:'YOLO · Full access'};
function permissionDetail(m){
  const mode=m.permission||'ask';
  if(!isDemo)return m.modes[mode]||m.problem||(m.found===null?'Checking…':'Not available with this provider.');
  if(m.id==='claude')return {ask:'--permission-mode default',read:'--permission-mode plan',edit:'--permission-mode acceptEdits',yolo:'--dangerously-skip-permissions'}[mode];
  if(m.id==='codex')return {ask:'--sandbox workspace-write · --ask-for-approval on-request',read:'--sandbox read-only',edit:'--sandbox workspace-write · --ask-for-approval never',yolo:'--dangerously-bypass-approvals-and-sandbox'}[mode];
  return 'Desktop tool permissions';
}
function reasoningOptions(m){
  if(!isDemo)return option('default','Provider default',!m.reasoning||m.reasoning==='default')+m.levels.map(v=>option(v,v,v===m.reasoning)).join('');
  const reasoning=m.id==='claude'?['low','medium','high','xhigh','max']:m.id==='codex'?['minimal','low','medium','high','xhigh']:['low','medium','high'];
  return option('default','Provider default',!m.reasoning||m.reasoning==='default')+reasoning.map(v=>option(v,v,v===m.reasoning)).join('');
}
function modelOptions(m){
  const selected=m.model==='CLI default'?'':m.model;
  const models=m.catalog||[];
  const pick=models.some(v=>v.id===selected)?selected:selected?'__custom':'';
  const suggested=models.find(v=>v.default);
  return option('',suggested?'Provider default · '+suggested.label:'Provider default',!pick)
    +models.map(v=>option(v.id,v.label||v.id,pick===v.id)).join('')+option('__custom','Custom model…',pick==='__custom');
}
function bindModelPicker(select,input,change){
  input.hidden=select.value!=='__custom';
  select.onchange=()=>{
    input.hidden=select.value!=='__custom';
    if(!input.hidden){input.focus();input.select();return}
    input.value=select.value;
    change?.(input.value);
  };
}
function profileFields(m){
  return `<div class="profile-fields"><label>Model<select id="profile-model-list">${modelOptions(m)}</select><input id="profile-model" value="${esc(m.model==='CLI default'?'':m.model)}" placeholder="CLI default" autocomplete="off"></label>
    <label>Reasoning<select id="profile-effort">${reasoningOptions(m)}</select></label>
    <label>Permissions<select id="profile-permissions">${Object.entries(permissionLabels).filter(([k])=>isDemo||m.permissions.includes(k)).map(([k,v])=>option(k,v,k===(m.permission||'ask'))).join('')}</select></label></div>
    <p class="hint profile-mode" id="permission-detail">${esc(permissionDetail(m))}</p>`;
}
function wireProfileFields(){
  const m=profile();
  bindModelPicker($('profile-model-list'),$('profile-model'),model=>{if(isDemo){m.model=model||'CLI default';save();drawChat()}else configureAssistant({model})});
  if(!isDemo){
    $('profile-model').onchange=e=>configureAssistant({model:e.target.value.trim()});
    $('profile-effort').onchange=e=>configureAssistant({reasoning:e.target.value});
    $('profile-permissions').onchange=e=>configureAssistant({permission:e.target.value});
    return;
  }
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
  loadLaunchData();
}
// Flows, pipelines, Git state and preferences come from the main process, for the project chosen in the launcher.
let launchLoad=0;
async function loadLaunchData(){
  if(isDemo)return;
  const token=++launchLoad,p=targetProject();
  if(!p)return;
  try{
    const [details,prefs]=await Promise.all([nativeAFM.projectDetails(p.id,p.path),nativeAFM.prefs()]);
    if(token!==launchLoad||!$('launch').open)return;
    const policyMode=policy=>policy?(policy.enabled?'worktree':'current'):undefined;
    p.git=details.git.git;
    p.flows=details.flows.map(f=>({name:f.name,kind:'Flow',path:f.file,git:policyMode(f.policy)}));
    state.gitDefault=prefs.gitMode;
    state.sources=details.sources.map(nativeSource);
    updateWorkflows();
  }catch(error){showNativeError(error)}
}
const nativeSource=source=>({id:source.id,name:basename(source.location),location:source.location,error:source.error,
  items:source.entries.map(entry=>({name:entry.name,kind:'Pipeline',path:entry.id,git:entry.policy?(entry.policy.enabled?'worktree':'current'):undefined}))});
function targetProject(){return projects.find(item=>item.id===$('launch-project').value)}
function workspace(){
  const p=targetProject(),item=matches[choice],manual=$('launch-git').value;
  if(!p.git)return {mode:'folder',name:'Current folder',from:'Project has no Git repository'};
  if(manual!=='auto')return {mode:manual,name:gitModes[manual],from:'This run'};
  const mode=item?.git||state.gitDefault;
  return {mode,name:gitModes[mode],from:item?.git?'Flow setting':'Desktop default'};
}
const modeSources={explicit:'This run',flow:'Flow setting',env:'Environment setting',config:'Project setting',preference:'Desktop default',folder:'Not a Git repository'};
let previewSerial=0,attempt={signature:'',id:''};
const slugOf=text=>text.toLowerCase().replace(/[^a-z0-9-]+/g,'-').replace(/^-+|-+$/g,'').slice(0,32)||'run';
// One request id per distinct launch, so repeating the same click never starts a second run.
function launchRequest(projectId,item){
  const explicit=$('launch-git').value==='auto'?undefined:$('launch-git').value;
  const suffix=(attempt.suffix??=uid().slice(0,5)),name='desktop-'+slugOf(item.name)+'-'+suffix;
  const request={projectId,kind:item.kind,file:item.path,explicit,branch:name,worktree:explicit==='worktree'?name:undefined};
  const signature=JSON.stringify(request);
  if(attempt.signature!==signature)attempt={...attempt,signature,id:uid()};
  return {...request,requestId:attempt.id};
}
function updateGit(){
  const p=targetProject();
  $('launch-options').hidden=isDemo||matches[choice]?.kind!=='Pipeline';
  $('launch-git').disabled=!p?.git;
  if(!isDemo){showEffectiveMode(p);return}
  const w=workspace();
  $('git-hint').textContent=w.from+' · '+w.name;
}
async function showEffectiveMode(p){
  const item=matches[choice],serial=++previewSerial;
  if(!p||!item||p.external){$('git-hint').textContent=p?.external?'Folder is added to Home when the run starts':'';return}
  try{
    const {effective,git}=await nativeAFM.preview(launchRequest(p.id,item));
    if(serial!==previewSerial)return;
    $('launch-submit').disabled=launching||Boolean(effective.blocked);
    const names={current:'Current checkout'+(effective.branch?' · '+effective.branch:''),branch:'New branch · '+effective.branch,worktree:'New worktree · '+effective.worktree};
    $('git-hint').textContent=effective.blocked||`${modeSources[effective.source]} · ${names[effective.mode]}${git.dirty&&effective.mode==='branch'?' · uncommitted changes stay':''}`;
  }catch(error){if(serial===previewSerial){$('launch-submit').disabled=true;$('git-hint').textContent=error.message}}
}
function updateWorkflows(){
  const p=targetProject();
  const sources=[{name:'This project',items:p?.flows||[]},...state.sources];
  const query=$('workflow-search').value.toLowerCase().trim();
  matches=sources.flatMap(s=>s.items.map(item=>({...item,source:s.name}))).filter(item=>[item.name,item.kind,item.source,item.path].join(' ').toLowerCase().includes(query));
  choice=Math.max(0,Math.min(choice,matches.length-1));
  $('workflows').innerHTML=matches.map((item,i)=>`<button class="workflow ${i===choice?'chosen':''}" aria-pressed="${i===choice}" data-choice="${i}" title="${esc(item.path)}"><span class="glyph">${item.kind==='Flow'?'⑂':'▦'}</span><span><strong>${esc(item.name)}</strong><small>${esc(item.source)} · ${esc(item.kind)}</small></span><span class="tick">${i===choice?'✓':''}</span></button>`).join('')||'<p class="empty">No matching workflows. Add a source in Settings › Library.</p>';
  $('launch-submit').disabled=launching||!matches.length;updateGit();
}
function startRun(){
  const p=targetProject(),item=matches[choice];
  if(!p||!item)return;
  if(!isDemo){startNative(p,item);return}
  const ws=workspace(),id=uid();
  const branch=(item.name.toLowerCase().replace(/[^a-z0-9-]+/g,'-').slice(0,32)||'run')+'-'+id.slice(0,5);
  const r={id,flow:item.name,kind:item.kind,workflowPath:item.path,library:item.source,source:'Desktop',state:'running',
    live:item.name==='feature-delivery',
    workspace:ws.name+(['branch','worktree'].includes(ws.mode)?' · '+branch:''), workspaceSource:ws.from};
  if(item.name==='code-review'){r.state='review';r.scenario='review'}
  r.noticeAt=Date.now();r.liveNotice=r.live;
  p.runs.push(r);save();$('launch').close();openProject(p,r,'run');
}
let launching=false;
function setLaunching(busy){
  launching=busy;
  const button=$('launch-submit');
  button.disabled=busy||!matches.length;
  button.textContent=busy?'Starting…':'Start run';
  if(busy)button.setAttribute('aria-busy','true');
  else button.removeAttribute('aria-busy');
}
function launchStatus(message, id){
  const status=$('launch-status'),log=$('launch-log');
  status.hidden=!message;status.textContent=message;
  log.hidden=!id;
  log.onclick=id?()=>nativeAFM.openLaunchLog(id).catch(showNativeError):null;
}
async function startNative(p,item){
  if(launching)return;
  setLaunching(true);
  launchStatus('Starting AFM…');
  try{
    const projectId=p.external?(await nativeAFM.addProject(p.path)).id:p.id;
    const request=launchRequest(projectId,item);
    let started=await nativeAFM.start(request);
    if(started?.status==='external_host'){
      // The host belongs to another tool; restarting it keeps runs alive but needs the user's consent.
      if(!confirm(started.message+'\n\nRestart that host and start the run?')){launchStatus('Run not started.');return}
      started=await nativeAFM.start({...request,confirmHostRestart:true});
    }
    if(started?.status==='pipeline_options'){
      launchStatus('Review pipeline options before starting.');
      openPipelineOptions(started.options.id, projectId, started.options);
      return;
    }
    if(started?.state!=='pending')attempt={signature:'',id:''};
    if(started?.message){
      launchStatus(started.message, started.logPath ? started.launch_id : null);
      return;
    }
    launchStatus('');
    $('launch').close();
    await refreshNative();
    const opened=projects.find(project=>project.id===projectId)||projects.find(project=>project.path===p.path);
    if(opened)openProject(opened);
  }catch(error){showNativeError(error);launchStatus(error.message)}
  finally{setLaunching(false);if($('launch').open)updateGit()}
}
let editingPipeline=null, editorAssets;
function loadCodeEditor(){
  if(globalThis.AFMCodeEditor)return Promise.resolve(globalThis.AFMCodeEditor);
  editorAssets??=Promise.all(['css','js'].map(extension=>new Promise((resolve,reject)=>{
    const asset=document.createElement(extension==='js'?'script':'link');
    const url='../.generated/editor/code-editor.'+extension;
    if(extension==='js')asset.src=url;
    else{asset.rel='stylesheet';asset.href=url}
    asset.onload=resolve;
    asset.onerror=()=>{asset.remove();reject(Error('Could not load the code editor.'))};
    document.head.append(asset);
  }))).then(()=>globalThis.AFMCodeEditor).catch(error=>{editorAssets=null;throw error});
  return editorAssets;
}
function optionsStatus(state,message){
  const status=$('pipeline-options-status');
  status.dataset.state=state;
  status.textContent=message;
}
function beginPipelineOptions(projectId){
  editingPipeline?.editor?.dispose();
  const editing={projectId};
  editingPipeline=editing;
  $('pipeline-options-title').textContent='Pipeline options';
  $('pipeline-options-path').textContent='';
  $('pipeline-options-details').hidden=true;
  $('pipeline-options-editor').hidden=true;
  $('pipeline-options-editor-label').hidden=true;
  $('pipeline-options-save').disabled=true;
  $('pipeline-options-save').textContent='Validate and save';
  optionsStatus('busy','Loading pipeline options…');
  if(!$('pipeline-options').open)$('pipeline-options').showModal();
  return editing;
}
function describeOptions(options){
  $('pipeline-options-title').textContent=options.name+' · Pipeline options';
  $('pipeline-options-path').textContent=options.previous && options.previous!==options.file
    ? 'Currently reading '+options.previous+'. Save to '+options.file : options.file;
  $('pipeline-options-roles').replaceChildren(...options.roles.map(role=>{
    const row=document.createElement('li');
    row.textContent=[role.id,role.command,role.profile,role.client,role.model,role.reasoning,role.auth_from,role.command_layer].filter(Boolean).join(' · ');
    return row;
  }));
  $('pipeline-options-details').hidden=!options.roles.length;
}
async function openPipelineOptions(id,projectId,provided=null){
  if(!id && !provided)return;
  const editing=beginPipelineOptions(projectId);
  try{
    const [options,monaco]=await Promise.all([provided??nativeAFM.pipelineOptions(id,projectId),loadCodeEditor()]);
    if(editingPipeline!==editing||!$('pipeline-options').open)return;
    editing.options=options;
    describeOptions(options);
    const surface=$('pipeline-options-editor');
    surface.hidden=false;
    $('pipeline-options-editor-label').hidden=false;
    editing.editor=monaco.createCodeEditor(surface,{text:options.text,language:'yaml',label:'Pipeline options YAML',readOnly:Boolean(options.conflict)});
    const notes=[options.template ? 'Loaded '+options.template+' from the package. Edit it before saving.' : null,options.error,...options.issues].filter(Boolean);
    optionsStatus(options.error||options.conflict||options.issues.length?'error':'',
      options.conflict?'Another source shares this name. Existing options are protected.':notes.join('\n'));
    const save=$('pipeline-options-save');
    save.disabled=Boolean(options.conflict);
    editing.editor.onChange(()=>{
      if(editingPipeline!==editing||save.disabled)return;
      if(save.textContent==='Saved'){
        save.textContent='Validate and save';
        optionsStatus('','Unsaved changes');
      }
    });
  }catch(error){if(editingPipeline===editing&&$('pipeline-options').open)optionsStatus('error',error.message)}
}
$('pipeline-options').addEventListener('close',()=>{
  editingPipeline?.editor?.dispose();
  editingPipeline=null;
});
$('launch-options').onclick=()=>openPipelineOptions(matches[choice]?.path,targetProject()?.external?undefined:targetProject()?.id);
$('pipeline-options-form').onsubmit=async event=>{
  event.preventDefault();
  const editing=editingPipeline,save=$('pipeline-options-save');
  if(!editing?.editor||save.disabled)return;
  save.disabled=true;
  save.textContent='Saving…';
  editing.editor.setReadOnly(true);
  optionsStatus('busy','Validating and saving options…');
  try{
    const {options,projectId,editor}=editing;
    const updated=await nativeAFM.savePipelineOptions({id:options.id,projectId,text:editor.getValue(),revision:options.revision});
    if(editingPipeline!==editing||!$('pipeline-options').open)return;
    editing.options=updated;
    describeOptions(updated);
    optionsStatus('saved',['Options saved.',...updated.issues].join('\n'));
    save.textContent='Saved';
    launchStatus('');
  }catch(error){if(editingPipeline===editing)optionsStatus('error',error.message)}
  finally{
    if(editingPipeline===editing){
      if(save.textContent==='Saving…')save.textContent='Validate and save';
      save.disabled=false;
      editing.editor.setReadOnly(false);
    }
  }
};
$('launch-submit').onclick=startRun;
$('launch-project').onchange=()=>{choice=0;updateWorkflows();loadLaunchData()};
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
  loadSettingsData();
}
// Library sources and Git preference are stored by the main process.
async function loadSettingsData(){
  if(isDemo)return;
  try{
    const [sources,prefs]=await Promise.all([nativeAFM.sources(),nativeAFM.prefs()]);
    state.sources=sources.map(nativeSource);state.gitDefault=prefs.gitMode;
    if(prefs.environment)savedEnvironment=prefs.environment;
    if(prefs.notifications)savedNotifications=prefs.notifications;
    if($('settings').open&&settingsTab!=='models')drawSettings();
  }catch(error){showNativeError(error)}
}
const demoModelForm=`<form id="model-form" hidden><label>Provider or CLI<input name="name" required placeholder="Ollama, Gemini CLI…"></label><label>Model or deployment<input name="model" required placeholder="Model ID"></label><label>Credential source<select name="auth"><option>Existing CLI login</option><option>Environment variable</option><option>Config file</option><option>Local endpoint</option></select></label><label id="reference-label" hidden><span>Source</span><input name="reference"></label><div class="actions"><button class="primary">Add model</button></div></form>`;
function drawSettings(){
  $('settings-title').textContent=onboarding?'Set up AFM Desktop':'Settings';
  const tabs=onboarding?'':`<nav class="segments">${[['models','Models'],['library','Library'],['runs','Runs'],['appearance','Appearance'],...(!isDemo?[['environment','Environment'],['notifications','Notifications']]:[])].map(([v,t])=>`<button data-panel="${v}" aria-pressed="${v===settingsTab}">${t}</button>`).join('')}</nav>`;
  const body=$('settings-body');
  if(settingsTab==='models'){
    body.innerHTML=tabs+`<p class="muted">${onboarding?'Choose the model for your desktop assistant.':'Default assistant model'}</p>
      <div class="choices">${state.models.map(m=>`<button class="workflow ${m.id===state.profile?'chosen':''}" data-profile="${esc(m.id)}" aria-pressed="${m.id===state.profile}"><span class="glyph">${m.id==='claude'?'✳':'›_'}</span><span><strong>${esc(m.name)}</strong><small>${esc(m.model)} · ${esc(m.auth)}</small></span><span class="tick">${m.id===state.profile?'✓':''}</span></button>`).join('')}</div>
      ${profileFields(profile())}<div class="links"><button id="scan-models">Scan again</button>${isDemo?'':'<button id="test-model">Test sign-in</button>'+(profile().custom?'<button id="remove-model">Remove</button>':'')}<button id="add-model">＋ Other model</button></div><p class="hint" role="status">${esc(modelScan)}</p>
      ${isDemo?demoModelForm:providerForm()}
      ${onboarding?'<div class="actions"><button class="primary" id="finish-setup">Continue to projects →</button></div>':''}`;
    wireProfileFields();
    $('scan-models').onclick=scanModels;
    $('add-model').onclick=()=>{const f=$('model-form');f.hidden=!f.hidden;if(!f.hidden)f.elements.name.focus()};
    if(!isDemo){wireProviderForm();if(onboarding)$('finish-setup').onclick=()=>$('settings').close();return}
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
    body.innerHTML=tabs+`<p class="muted">Flows and pipelines available in every project.</p><div class="source-list">${state.sources.map(s=>`<div><span><strong>${esc(s.name)}</strong><small>${s.error?esc(s.error):s.items.length+' workflows'}</small></span>${s.id!=='starter'?`<button class="icon" data-refresh="${esc(s.id)}" aria-label="Refresh ${esc(s.name)}">↻</button><button class="icon" data-remove="${esc(s.id)}" aria-label="Remove ${esc(s.name)}">×</button>`:''}</div>`).join('')}</div>
      ${!isDemo?`<div class="source-list">${state.sources.flatMap(source=>source.items).map(item=>`<div><span>${esc(item.name)}</span><button data-pipeline-options="${esc(item.path)}">Pipeline options</button></div>`).join('')}</div>`:''}
      <div class="links"><button id="add-folder">＋ Local folder</button><button id="add-repo">＋ ${isDemo?'GitHub repository':'Git repository'}</button></div>
      <form id="repo-form" hidden><label>${isDemo?'Public GitHub repository':'Git repository URL'}<input name="repo" required placeholder="${isDemo?'owner/repository':'SSH or HTTPS URL'}"></label><div class="actions"><button class="primary">Connect repository</button></div></form><p class="hint" role="status">${esc(sourceMessage)}</p>`;
    for(const button of body.querySelectorAll('[data-pipeline-options]')){
      button.onclick=()=>openPipelineOptions(button.dataset.pipelineOptions,selection().p?.external?undefined:selection().p?.id);
    }
    $('add-folder').onclick=()=>isDemo?importFolder('source'):changeSource(()=>nativeAFM.pickSource());
    $('add-repo').onclick=()=>{$('repo-form').hidden=false;$('repo-form').elements.repo.focus()};
    $('repo-form').onsubmit=async e=>{
      e.preventDefault();if(remoteBusy)return;remoteBusy=true;
      const name=e.target.elements.repo.value;
      sourceMessage='Reading workflow manifests…';drawSettings();
      if(!isDemo){remoteBusy=false;changeSource(()=>nativeAFM.addSource(/^[\w.-]+\/[\w.-]+$/.test(name.trim())?'https://github.com/'+name.trim():name.trim()));return}
      try{addSource(await gitHubSource(name))}catch(err){sourceMessage=err.message}
      finally{remoteBusy=false;if(settingsTab==='library')drawSettings()}
    };
  }else if(settingsTab==='appearance'){
    body.innerHTML=tabs+`<label>Theme<select id="desktop-theme">${[['dark','Dark'],['light','Light']].map(([id,label])=>option(id,label,id===document.documentElement.dataset.theme)).join('')}</select></label>`;
    $('desktop-theme').onchange=event=>{state.theme=event.target.value;document.documentElement.dataset.theme=state.theme;save()};
  }else if(settingsTab==='environment'){
    drawEnvironment(body,tabs);
  }else if(settingsTab==='notifications'){
    drawNotifications(body,tabs);
  }else{
    body.innerHTML=tabs+`<label>Default workspace for Git projects<select id="default-git">${gitOptions(state.gitDefault)}</select></label>
    <p class="muted">A flow’s workspace preference takes precedence. You can change it for an individual run in the launcher.</p>
    <div class="setting-row"><span>Settings</span><kbd>${modKey} ,</kbd></div>
    <div class="setting-row"><span>All projects</span><kbd>${modKey} H</kbd></div>
    <div class="setting-row"><span>Next / previous notification</span><kbd>${modKey} J / K</kbd></div>
    <div class="setting-row"><span>Switch project</span><kbd>${modKey} Shift K</kbd></div>
    <div class="setting-row"><span>Switch run tab</span><kbd>${modKey} 1–9</kbd></div>
    <div class="setting-row"><span>Stage focus</span><kbd>F · Esc</kbd></div>
    <div class="setting-row"><span>Open / close assistant</span><kbd>Alt A</kbd></div>
    <div class="setting-row"><span>Assistant model</span><kbd>${modKey} M</kbd></div>
    <div class="setting-row"><span>Quick launch</span><kbd>${modKey} Shift Enter</kbd></div>`;
    $('default-git').onchange=e=>{
      state.gitDefault=e.target.value;save();
      if(!isDemo)nativeAFM.setPrefs({gitMode:e.target.value}).catch(showNativeError);
    };
  }
}
let savedEnvironment={shell:'none',file:'',names:[]}, environmentStatus='';
const notificationKinds=[['question','Questions'],['approval','Plan approvals'],['decision','Decisions'],['failed','Failed stages']];
let savedNotifications={enabled:true,kinds:{question:true,approval:true,decision:true,failed:false}};
// System banners are optional; the in-app notification queue always lists everything that needs you.
function drawNotifications(body,tabs){
  const {enabled,kinds}=savedNotifications;
  body.innerHTML=tabs+`<label class="setting-row"><span>System notifications</span><input type="checkbox" id="notify-enabled"${enabled?' checked':''}></label>
    ${notificationKinds.map(([kind,label])=>`<label class="setting-row"><span>${label}</span><input type="checkbox" data-notify-kind="${kind}"${kinds[kind]?' checked':''}${enabled?'':' disabled'}></label>`).join('')}
    <p class="muted">A banner appears once for each request while AFM Desktop is running, unless you are already looking at that run. It names the project, flow and stage, never the question. Allow AFM in macOS System Settings → Notifications; Focus modes silence banners, and the in-app queue still lists them.</p>
    <p class="hint" role="status">${esc(notificationStatus)}</p>`;
  const save=async next=>{
    try{savedNotifications=(await nativeAFM.setPrefs({notifications:next})).notifications;notificationStatus=''}
    catch(error){notificationStatus=error.message}
    if($('settings').open&&settingsTab==='notifications')drawSettings();
  };
  $('notify-enabled').onchange=event=>save({enabled:event.target.checked,kinds});
  for(const box of body.querySelectorAll('[data-notify-kind]'))box.onchange=()=>save({enabled,kinds:{...kinds,[box.dataset.notifyKind]:box.checked}});
}
let notificationStatus='';
function drawEnvironment(body,tabs){
  const {shell,file,names}=savedEnvironment;
  body.innerHTML=tabs+`<form id="env-source">
    <label>Import exported shell variables<select name="shell">${[['none','None'],['zsh','.zshrc'],['bash','.bashrc']].map(([id,label])=>option(id,label,id===shell)).join('')}</select></label>
    <label>Environment file<input name="file" placeholder="~/.config/afm/desktop.env" value="${esc(file)}"></label>
    <div class="actions"><button class="primary">Save sources</button></div></form>
    <form id="env-variable"><label>Name<input name="name" required pattern="[A-Za-z_][A-Za-z0-9_]*"></label>
    <label>Value<input name="value" type="password" autocomplete="off"></label>
    <div class="actions"><button class="primary">Set variable</button></div></form>
    <div class="source-list">${names.map(name=>`<div><span>${esc(name)}</span><button data-env-remove="${esc(name)}">Remove</button></div>`).join('')}</div>
    <p class="muted">Restart AFM host to apply these settings to new runs. Existing runs keep their environment.</p>
    <button id="env-restart">Restart AFM host</button><p class="hint" role="status">${esc(environmentStatus)}</p>`;
  async function update(changes){
    try {savedEnvironment=(await nativeAFM.updateEnvironment(changes)).environment;environmentStatus='Saved.';assistantChat.scanned=false}
    catch(error){environmentStatus=error.message}
    if($('settings').open&&settingsTab==='environment')drawSettings();
  }
  $('env-source').onsubmit=event=>{
    event.preventDefault();
    update({shell:event.target.elements.shell.value,file:event.target.elements.file.value.trim()});
  };
  $('env-variable').onsubmit=event=>{
    event.preventDefault();
    update({name:event.target.elements.name.value.trim(),value:event.target.elements.value.value});
  };
  for(const button of body.querySelectorAll('[data-env-remove]'))button.onclick=()=>update({remove:button.dataset.envRemove});
  $('env-restart').onclick=async()=>{
    try {
      const host=await nativeAFM.hostStatus();
      const external=host.ownership==='external';
      if(external&&!confirm('Restart the externally started AFM host? Existing runs keep running.'))return;
      await nativeAFM.restartHost(external);
      environmentStatus='AFM host restarted.';
      await refreshNative();
    }catch(error){environmentStatus=error.message}
    if($('settings').open&&settingsTab==='environment')drawSettings();
  };
}
// Runs one source operation in the main process, then redraws the library from its stored inventory.
async function changeSource(operation){
  try{
    const source=await operation();
    sourceMessage=source?.error||(source?`${source.entries.length} workflows available in New run.`:'');
    await loadSettingsData();
  }catch(error){sourceMessage=error.message}
  if(settingsTab==='library')drawSettings();
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
  if(!isDemo){await scanAssistant();return}
  if(window.nativeAFM?.models){
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
// A folder chosen in the real app is registered by the main process; the page then shows it from the next snapshot.
async function openNativeProject(summary){
  if(!summary)return;
  $('open-project').close();
  await refreshNative();
  let project=projects.find(item=>item.id===summary.id);
  if(!project){
    // A poll started before registration can finish with the previous project list.
    await refreshNative();
    project=projects.find(item=>item.id===summary.id);
  }
  if(project)openProject(project);
}
$('browse-project').onclick=async()=>{
  try{
    const folder=await window.nativeAFM.pickProject();
    if(!folder)return;
    if(isDemo)addProject(folder.path,folder.git,folder.flows);else await openNativeProject(folder);
  }catch(error){showFolderError('project',error)}
};
$('path-form').onsubmit=async e=>{
  e.preventDefault();const path=$('project-path').value.trim();
  if(!path)return;
  if(isDemo){addProject(path,$('path-git').checked);e.target.reset();return}
  try{await openNativeProject(await nativeAFM.addProject(path));e.target.reset()}
  catch(error){showFolderError('project',error)}
};
$('settings-toggle').onclick=()=>openSettings();
document.addEventListener('keydown',event=>{
  if(event.repeat||event.isComposing||!primary(event)||event.altKey||event.shiftKey||event.key!==',')return;
  if(document.querySelector('dialog[open]')&&!$('settings').open)return;
  event.preventDefault();
  if($('settings').open){if(!$('pipeline-options').open)$('settings').close()}
  else navigateApp('settings');
});
$('settings').addEventListener('close',()=>{
  if(onboarding){state.setup=true;save();onboarding=false}
});
document.addEventListener('click',e=>{
  const el=e.target.closest('button');if(!el)return;
  if(el.hasAttribute('data-close'))el.closest('dialog')?.close();
  if(el.dataset.panel){settingsTab=el.dataset.panel;drawSettings()}
  if(el.dataset.profile&&!isDemo)configureAssistant({provider:el.dataset.profile});
  else if(el.dataset.profile){state.profile=el.dataset.profile;save();drawSettings();drawChat()}
  if(el.dataset.remove&&!isDemo)changeSource(async()=>{await nativeAFM.removeSource(el.dataset.remove);return null});
  else if(el.dataset.remove){state.sources=state.sources.filter(s=>s.id!==el.dataset.remove);save();drawSettings()}
  if(el.dataset.refresh)changeSource(()=>nativeAFM.refreshSource(el.dataset.refresh));
});
document.addEventListener('keydown',e=>{
  if(primary(e)&&e.shiftKey&&!e.altKey&&!e.isComposing&&!e.repeat&&e.key==='Enter'){e.preventDefault();openLauncher(selection().p)}
});
function drawChat(){
  if(!isDemo)return drawAssistant();
  const {p,r}=selection(),m=profile();
  $('chat-context').textContent=p?`${p.name}${r?' / '+r.flow:''}`:'All projects';
  $('chat-model').textContent=[m.name,m.model,m.reasoning&&m.reasoning!=='default'?m.reasoning:null,permissionLabels[m.permission||'ask'],'Demo chat'].filter(Boolean).join(' · ');
  $('messages').innerHTML=state.chat.length?state.chat.map(msg=>`<div class="bubble ${msg.role==='user'?'mine':''}">${msg.context?`<small>${esc(msg.context)}</small>`:''}<p>${esc(msg.text)}</p>${(msg.links||[]).map(link=>`<button class="inline-link" data-project="${esc(link.project)}" data-run="${esc(link.run)}">${esc(link.name)} ↗</button>`).join('')}</div>`).join(''):'<div class="chat-empty"><span>✧</span><h3>What’s running?</h3><p>Explore your projects or launch a workflow.</p></div>';
  $('messages').scrollTop=$('messages').scrollHeight;
}
function toggleChat(open){
  $('assistant').hidden=!open;
  $('assistant-toggle').setAttribute('aria-expanded',String(open));
  if(open){drawChat();$('chat-input').focus();if(!isDemo)ensureAssistantScan()}else $('assistant-toggle').focus();
}
function sendMessage(text){
  if(!text.trim())return;
  if(!isDemo)return sendAssistant(text);
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
$('chat-form').onsubmit=e=>{e.preventDefault();if(!isDemo&&assistantChat.busy)stopAssistant();else sendMessage($('chat-input').value)};
$('chat-input').onkeydown=e=>{if(e.key==='Enter'&&!e.shiftKey&&!e.isComposing){e.preventDefault();if(!isDemo&&assistantChat.busy)return;sendMessage(e.target.value)}};
document.querySelector('.suggestions').onclick=e=>{if(e.target.dataset.prompt)sendMessage(e.target.dataset.prompt)};
document.addEventListener('workspace-change',drawChat);
$('launch-hint').title=`${modKey} Shift Enter`;
document.querySelectorAll('[data-start]').forEach(el=>el.title=`New run (${modKey} Shift Enter)`);
function showQuickModel(){
  if(document.querySelector('dialog[open]'))return;
  delete $('quick-model-form').dataset.edited;
  if(!isDemo)ensureAssistantScan();
  $('quick-agent').innerHTML=state.models.map(m=>option(m.id,m.name+(m.model==='CLI default'?'':' · '+m.model),m.id===state.profile)).join('');
  fillQuickModel();$('quick-model').showModal();$('quick-model-list').focus();
}
function fillQuickModel(){
  const m=state.models.find(item=>item.id===$('quick-agent').value);
  $('quick-model-list').innerHTML=modelOptions(m);
  bindModelPicker($('quick-model-list'),$('quick-model-id'));
  $('quick-model-id').value=m.model==='CLI default'?'':m.model;
  $('quick-effort').innerHTML=reasoningOptions(m);
}
$('quick-agent').onchange=fillQuickModel;
$('quick-model-form').addEventListener('input',()=>{$('quick-model-form').dataset.edited='true'});
$('quick-model-form').onsubmit=e=>{
  e.preventDefault();
  if(!isDemo){
    configureAssistant({provider:$('quick-agent').value,model:$('quick-model-id').value.trim(),reasoning:$('quick-effort').value});
    $('quick-model').close();return;
  }
  const m=state.models.find(item=>item.id===$('quick-agent').value);
  m.model=$('quick-model-id').value.trim()||'CLI default';
  m.reasoning=$('quick-effort').value;
  state.profile=m.id;save();drawChat();$('quick-model').close();
};
$('chat-model').onclick=()=>openSettings();
$('quick-model-key').onclick=showQuickModel;
$('quick-model-key').textContent=`${modKey} M`;
$('quick-model-key').setAttribute('aria-keyshortcuts',`${ariaModKey}+M`);
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

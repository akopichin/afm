'use strict';
// The real assistant. Providers, settings and the conversation live in the main process; this page shows them,
// streams the reply as events arrive and sends the user's choices back. The demo keeps its own sample replies.
const assistantChat={messages:[],busy:false,local:'',scanned:false,scanning:null};
let assistantInfo=null,chatDrawQueued=false;

function authLabel(p){
  if(p.found===false)return 'Not installed';
  if(p.auth.state==='ready')return p.auth.detail;
  return p.auth.state==='unknown'?'Checking…':'Needs sign-in';
}
// A provider as the settings UI expects it: model and reasoning belong to the selected provider only.
function providerModel(p,settings){
  const selected=p.id===settings.provider;
  const unsupported=selected&&!p.permissions.includes(settings.permission);
  return {id:p.id,name:p.name,custom:!p.builtIn,kind:p.kind,found:p.found,auth:authLabel(p),authDetail:p.auth.detail,authState:p.auth.state,
    catalog:p.models||[],model:selected&&settings.model?settings.model:'CLI default',reasoning:selected?settings.reasoning:'default',
    permission:selected?settings.permission:'ask',levels:p.reasoning,permissions:p.permissions,modes:p.modes,
    problem:unsupported?'This provider cannot enforce the chosen permission mode. Pick another.':p.found===false||p.auth.state==='missing'?p.auth.detail:''};
}
function applyAssistantInfo(info){
  assistantInfo=info;
  state.models=info.providers.map(p=>providerModel(p,info.settings));
  state.profile=info.settings.provider;
  assistantChat.scanned=info.scanned;
  drawChat();
  if($('settings').open&&settingsTab==='models'&&!$('settings-body').querySelector('input:focus,select:focus'))drawSettings();
}
async function configureAssistant(changes){
  try{applyAssistantInfo(await nativeAFM.assistant.configure(changes))}catch(error){modelScan=error.message;drawSettings();showNativeError(error)}
}
// Detection runs the CLIs, so it waits until the user opens the assistant or settings.
function scanAssistant(){
  modelScan='Scanning command-line agents…';
  assistantChat.scanning??=nativeAFM.assistant.scan().then(info=>{
    applyAssistantInfo(info);
    const ready=info.providers.filter(p=>p.auth.state==='ready').map(p=>p.name);
    modelScan=ready.length?'Ready to use: '+ready.join(', ')+'.':'No signed-in provider found. Sign in to Claude Code or Codex, or add a provider.';
  }).catch(error=>{modelScan=error.message}).finally(()=>{assistantChat.scanning=null;
    if($('settings').open&&settingsTab==='models')drawSettings();
    if($('quick-model').open&&!$('quick-model-form').dataset.edited)refreshQuickModel()});
  const status=document.querySelector('#settings-body [role="status"]');if(status)status.textContent=modelScan;
  return assistantChat.scanning;
}
const ensureAssistantScan=()=>assistantChat.scanned?Promise.resolve():scanAssistant();
function refreshQuickModel(){
  const chosen=$('quick-agent').value||state.profile;
  $('quick-agent').innerHTML=state.models.map(m=>option(m.id,m.name,m.id===chosen)).join('');
  fillQuickModel();
}

function providerForm(){
  return `<form id="model-form" hidden><label>Name<input name="name" required maxlength="60" placeholder="My Claude, Local model…"></label>
    <label>Type<select name="kind"><option value="claude">Claude Code CLI</option><option value="codex">Codex CLI</option><option value="openai">OpenAI-compatible endpoint</option></select></label>
    <label id="location-label"><span>Executable path</span><input name="location" required placeholder="/opt/tools/claude"></label>
    <label>Credential<select name="credential"><option value="cli">Existing CLI login</option><option value="env">Environment variable</option><option value="file">Key file</option></select></label>
    <label id="reference-label" hidden><span>Variable name</span><input name="reference"></label>
    <div class="actions"><button class="primary">Add provider</button></div></form>`;
}
function wireProviderForm(){
  const form=$('model-form');
  const refresh=()=>{
    const kind=form.elements.kind.value,credential=form.elements.credential.value;
    $('location-label').querySelector('span').textContent=kind==='openai'?'Endpoint URL':'Executable path';
    form.elements.location.placeholder=kind==='openai'?'http://127.0.0.1:11434/v1':'/opt/tools/'+kind;
    const cli=form.elements.credential.querySelector('[value=cli]');cli.disabled=kind==='openai';
    if(kind==='openai'&&credential==='cli')form.elements.credential.value='env';
    $('reference-label').hidden=form.elements.credential.value==='cli';
    form.elements.reference.required=!$('reference-label').hidden;
    $('reference-label').querySelector('span').textContent=form.elements.credential.value==='env'?'Variable name':'Key file path';
  };
  form.elements.kind.onchange=refresh;form.elements.credential.onchange=refresh;refresh();
  $('add-model').onclick=()=>{form.hidden=!form.hidden;if(!form.hidden)form.elements.name.focus()};
  $('scan-models').onclick=scanAssistant;
  $('test-model').onclick=async()=>{
    modelScan='Checking '+profile().name+'…';$('settings-body').querySelector('[role=status]').textContent=modelScan;
    try{
      const result=await nativeAFM.assistant.testProvider(profile().id);
      modelScan=!result.found?'Not found on this machine.':result.ok?`${profile().name} ${result.version||''} is installed and signed in.`.replace('  ',' '):result.auth.detail;
    }catch(error){modelScan=error.message}
    if($('settings').open&&settingsTab==='models')drawSettings();
  };
  if($('remove-model'))$('remove-model').onclick=async()=>{
    try{applyAssistantInfo(await nativeAFM.assistant.removeProvider(profile().id));modelScan='Provider removed.'}catch(error){modelScan=error.message}
    drawSettings();
  };
  form.onsubmit=async e=>{
    e.preventDefault();
    const f=new FormData(form),kind=f.get('kind'),credential=f.get('credential');
    try{
      applyAssistantInfo(await nativeAFM.assistant.addProvider({name:f.get('name').trim(),kind,
        ...(kind==='openai'?{endpoint:f.get('location').trim()}:{executable:f.get('location').trim()}),
        credential:{type:credential,ref:credential==='cli'?undefined:f.get('reference').trim()}}));
      modelScan='Provider added. Pick it and set its model.';
    }catch(error){modelScan=error.message}
    drawSettings();
  };
}

// ---- conversation ----

function currentScope(){
  const {p,r}=selection();
  if(r)return {runId:r.rawId,instanceId:r.instance};
  return p?{projectId:p.id}:{};
}
function scopeText(){
  const {p,r}=selection();
  return 'Scope: '+(p?p.name+(r?' / '+r.flow:''):'All projects');
}
const effectiveLine=()=>{
  const m=profile(),s=assistantInfo?.settings;
  if(!m||!s)return '';
  return [m.name,s.model||'CLI default',s.reasoning!=='default'?s.reasoning:null,permissionLabels[s.permission],m.problem?'Needs setup':null].filter(Boolean).join(' · ');
};
function providerError(raw){
  try {
    const detail=JSON.parse(raw)?.error;
    if(detail?.code==='model_not_found')return (detail.message||'Model unavailable.')+' Choose another model in the model picker.';
    return typeof detail?.message==='string' ? detail.message : raw;
  } catch {return raw}
}
function messageHTML(msg){
  if(msg.role==='user')return `<div class="bubble mine"><small>${esc(msg.context||'')}</small><p>${esc(msg.text)}</p></div>`;
  const tools=(msg.tools||[]).map(tool=>`<div class="tool ${tool.status}"><code>${esc(tool.name)}</code> <small>${esc(tool.input||'')}</small>${tool.status==='error'?`<p class="tool-error">${esc(tool.text||'Failed')}</p>`:''}</div>`).join('');
  const approvals=(msg.approvals||[]).map(a=>`<div class="approval" role="group" aria-label="Approval needed"><p><strong>Approve ${esc(a.tool)}?</strong> <small>${esc(a.scope)}</small></p><code>${esc(a.summary)}</code><div class="actions"><button data-approve="${esc(a.approvalId)}" data-yes="no">Deny</button><button class="primary" data-approve="${esc(a.approvalId)}" data-yes="yes">Approve</button></div></div>`).join('');
  const end=msg.status==='error'?`<p class="bubble-error" role="alert">${esc(providerError(msg.error||'The reply failed.'))}</p>`:msg.status==='cancelled'?'<small>Stopped</small>':msg.status==='streaming'&&!msg.text?'<small>Thinking…</small>':'';
  return `<div class="bubble ${msg.status}"><small>${esc(msg.context||'')}${msg.effective?' · '+esc(msg.effective.label):''}</small>${(msg.notes||[]).map(n=>`<small>${esc(n)}</small>`).join('')}${msg.text?`<p>${esc(msg.text)}</p>`:''}${tools}${approvals}${end}</div>`;
}
function drawAssistant(){
  $('chat-context').textContent=scopeText();
  $('chat-model').textContent=effectiveLine()||'Choose a provider';
  const box=$('messages'),stick=box.scrollHeight-box.scrollTop-box.clientHeight<80;
  const messages=assistantChat.messages;
  box.innerHTML=(messages.length?messages.map(messageHTML).join(''):'<div class="chat-empty"><span>✧</span><h3>What’s running?</h3><p>Explore your projects or launch a workflow.</p></div>')
    +(assistantChat.local?`<div class="bubble error"><p class="bubble-error" role="alert">${esc(assistantChat.local)}</p></div>`:'');
  if(stick)box.scrollTop=box.scrollHeight;
  const send=document.querySelector('#chat-form .send');
  send.textContent=assistantChat.busy?'■':'↑';
  send.setAttribute('aria-label',assistantChat.busy?'Stop reply':'Send message');
  $('chat-input').required=!assistantChat.busy;
}
// Streaming events arrive faster than the page needs to repaint.
function queueChatDraw(){
  if(chatDrawQueued)return;
  chatDrawQueued=true;
  requestAnimationFrame(()=>{chatDrawQueued=false;drawAssistant()});
}
async function sendAssistant(text){
  if(assistantChat.busy)return;
  assistantChat.local='';assistantChat.busy=true;drawAssistant();
  try{
    await nativeAFM.assistant.send({text,scope:currentScope()});
    $('chat-input').value='';
  }catch(error){
    assistantChat.busy=false;assistantChat.local=error.message;drawAssistant();
  }
}
function stopAssistant(){nativeAFM.assistant.cancel().catch(showNativeError)}
const replyFor=event=>assistantChat.messages.find(m=>m.turnId===event.turnId&&m.role==='assistant');
function handleAssistantEvent(event){
  const {messages}=assistantChat;
  if(event.type==='start'){
    assistantChat.busy=true;assistantChat.local='';
    if(!messages.some(m=>m.id===event.message.id))messages.push(event.message,{...event.reply,turnId:event.turnId,approvals:[],notes:[]});
  }else{
    const reply=replyFor(event);
    if(event.type==='delta'&&reply){reply.text+=event.text}
    else if(event.type==='tool'&&reply){reply.tools.push({...event.tool,status:'running'})}
    else if(event.type==='tool_result'&&reply){const tool=reply.tools.find(t=>t.id===event.id);if(tool)Object.assign(tool,{status:event.error?'error':'done',text:event.text})}
    else if(event.type==='approval'&&reply){reply.approvals.push(event)}
    else if(event.type==='approval_done'&&reply){reply.approvals=reply.approvals.filter(a=>a.approvalId!==event.approvalId)}
    else if(event.type==='notice'&&reply){reply.notes.push(event.text)}
    else if(['done','error','cancelled'].includes(event.type)){
      const final={...event.reply,turnId:event.turnId,approvals:[],notes:reply?.notes??[]};
      const at=messages.indexOf(reply);
      if(at>=0)messages[at]=final;else messages.push(final);
      assistantChat.busy=false;
      // Reply finished while the assistant was open: the input is ready for the next message.
      if(!$('assistant').hidden&&document.activeElement===document.body)$('chat-input').focus();
    }
  }
  queueChatDraw();
}
$('messages').addEventListener('click',e=>{
  const button=e.target.closest('[data-approve]');
  if(button)nativeAFM.assistant.approve(button.dataset.approve,button.dataset.yes==='yes').catch(showNativeError);
});
// Called by native.js once every script has loaded.
function startAssistant(){
  nativeAFM.assistant.onEvent(handleAssistantEvent);
  nativeAFM.assistant.state().then(applyAssistantInfo).catch(showNativeError);
  nativeAFM.assistant.history().then(history=>{
    if(!assistantChat.messages.length){assistantChat.messages=history.map(m=>({...m,approvals:[],notes:[]}));drawAssistant()}
  }).catch(showNativeError);
}

'use strict';
// The real assistant. Providers, settings and the conversation live in the main process; this page shows them,
// streams the reply as events arrive and sends the user's choices back. The demo keeps its own sample replies.
const newChatState=()=>({messages:[],queue:[],images:[],busy:false,local:''});
let assistantChat=newChatState();
const assistantDiscovery={scanned:false,scanning:null};
let activeConversation='main';
const chatStates=new Map([['main',assistantChat]]);
const stateFor=id=>{
  if(!chatStates.has(id))chatStates.set(id,newChatState());
  return chatStates.get(id);
};
const MAX_QUEUED_MESSAGES=8, MAX_CHAT_IMAGES=4, MAX_CHAT_IMAGE_BYTES=5*1024*1024;
const chatImageTypes=new Set(['image/png','image/jpeg','image/webp','image/gif']);
let nextQueuedMessage=0;
const toolErrorClock=new Intl.DateTimeFormat(undefined,{day:'numeric',month:'short',hour:'2-digit',minute:'2-digit'});
const toolErrorOverrides=new Map(Array.isArray(state.assistantToolErrors)?state.assistantToolErrors:[]);
let assistantInfo=null,chatDrawQueued=false,addingProvider=false,savedWorkspaceId='';

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
    remoteCwd:p.remoteCwd,catalog:p.models||[],model:selected&&settings.model?settings.model:'CLI default',reasoning:selected?settings.reasoning:'default',
    permission:selected?settings.permission:'ask',levels:p.reasoning,permissions:p.permissions,modes:p.modes,agentModes:p.agentModes||[],acpMode:selected?settings.acpMode:'',
    problem:unsupported?'This provider cannot enforce the chosen permission mode. Pick another.':p.found===false||p.auth.state==='missing'?p.auth.detail:''};
}
function applyAssistantInfo(info){
  assistantInfo=info;
  state.models=info.providers.map(p=>providerModel(p,info.settings));
  state.profile=info.settings.provider;
  assistantDiscovery.scanned=info.scanned;
  drawChat();
  if(!$('settings-body').querySelector('input:focus,select:focus'))refreshModelPanel();
}
function refreshModelPanel(){
  if(!$('settings').open||settingsTab!=='models')return;
  const body=$('settings-body');
  if(body.querySelector('.workspace-editor:not([hidden]),[data-model-progress]')){
    const status=$('model-status');
    if(status)status.textContent=modelScan;
  }else drawSettings();
}
function beginModelAction(button,label){
  const content=[...button.childNodes];
  button.disabled=true;
  button.dataset.modelProgress='';
  button.setAttribute('aria-busy','true');
  if(label)button.textContent=label;
  return ()=>{
    button.disabled=false;
    delete button.dataset.modelProgress;
    button.removeAttribute('aria-busy');
    if(label)button.replaceChildren(...content);
  };
}
async function configureAssistant(changes){
  try{applyAssistantInfo(await nativeAFM.assistant.configure(changes))}catch(error){modelScan=error.message;drawSettings();showNativeError(error)}
}
// Detection runs the CLIs, so it waits until the user opens the assistant or settings.
function scanAssistant(){
  modelScan='Scanning command-line agents…';
  assistantDiscovery.scanning??=nativeAFM.assistant.scan().then(info=>{
    applyAssistantInfo(info);
    const ready=info.providers.filter(p=>p.auth.state==='ready').map(p=>p.name);
    modelScan=ready.length?'Ready to use: '+ready.join(', ')+'.':'No signed-in provider found. Sign in to Claude Code or Codex, or add a provider.';
  }).catch(error=>{modelScan=error.message}).finally(()=>{assistantDiscovery.scanning=null;
    refreshModelPanel();
    if($('quick-model').open&&!$('quick-model-form').dataset.edited)refreshQuickModel()});
  const status=$('model-status');if(status)status.textContent=modelScan;
  return assistantDiscovery.scanning;
}
const ensureAssistantScan=()=>assistantDiscovery.scanned?Promise.resolve():scanAssistant();
function refreshQuickModel(){
  const chosen=$('quick-agent').value||state.profile;
  $('quick-agent').innerHTML=state.models.map(m=>option(m.id,m.name,m.id===chosen)).join('');
  fillQuickModel();
}

function providerForm(){
  return `<form id="model-form" ${addingProvider?'aria-busy="true"':'hidden'}><label>Name<input name="name" required maxlength="60" placeholder="My Claude, Local model…"></label>
    <label>Type<select name="kind"><option value="claude">Claude Code CLI</option><option value="codex">Codex CLI</option><option value="openai">OpenAI-compatible endpoint</option><option value="openai-agent">OpenAI · Deep Agent</option><option value="anthropic-agent">Anthropic · Deep Agent</option><option value="acp">ACP agent</option><option value="remote-acp">Claude Code · Remote ACP</option></select></label>
    <label id="location-label"><span>Executable path</span><input name="location" required placeholder="/opt/tools/claude"></label>
    <label id="remote-workspace-label" hidden>Container workspace<input name="remoteCwd" value="/workspace"></label>
    <label id="acp-args" hidden>Arguments, one per line
      <textarea name="args" rows="3" spellcheck="false" aria-describedby="acp-args-help" placeholder="-y
@agentclientprotocol/codex-acp"></textarea>
      <small class="field-help" id="acp-args-help">Optional. Leave blank when the adapter runs without arguments.</small></label>
    <label>Credential<select name="credential"><option value="cli">Existing CLI login</option><option value="env">Environment variable</option><option value="file">Key file</option><option value="token" disabled>API token</option></select></label>
    <label id="reference-label" hidden><span>Environment variable name</span>
      <input name="reference" autocomplete="off" spellcheck="false" aria-describedby="credential-help">
      <small class="field-help" id="credential-help"></small></label>
    <div class="actions"><button type="submit" class="primary" ${addingProvider?'disabled aria-busy="true" data-model-progress':''}>${addingProvider?'Adding…':'Add provider'}</button></div></form>`;
}
function wireProviderForm(){
  const transport=$('codex-transport');
  if(transport)transport.onchange=()=>configureAssistant({codexTransport:transport.value});
  const form=$('model-form'),toggle=$('add-model'),submit=form.querySelector('button[type="submit"]');
  const refresh=()=>{
    const kind=form.elements.kind.value,credential=form.elements.credential.value;
    const agent=kind==='openai-agent'||kind==='anthropic-agent';
    const remote=kind==='remote-acp';
    const endpoint=kind==='openai'||agent||remote;
    $('remote-workspace-label').hidden=!remote;
    form.elements.credential.querySelector('[value=token]').disabled=!agent&&!remote;
    if(!agent&&!remote&&credential==='token')form.elements.credential.value='env';
    $('location-label').querySelector('span').textContent=endpoint?'Endpoint URL':'Executable path';
    form.elements.location.placeholder=remote?'wss://host.example/acp':endpoint?'http://127.0.0.1:11434/v1':kind==='acp'?'/opt/homebrew/bin/codex-acp':'/opt/tools/'+kind;
    const cli=form.elements.credential.querySelector('[value=cli]');cli.disabled=endpoint&&!remote;
    cli.textContent=remote?'Existing container login (no bridge token)':'Existing CLI login';
    if(endpoint&&!remote&&credential==='cli')form.elements.credential.value=agent?'token':'env';
    form.elements.credential.querySelector('[value=file]').disabled=kind==='acp';
    if(kind==='acp'&&credential==='file')form.elements.credential.value='cli';
    $('acp-args').hidden=kind!=='acp';
    const reference=form.elements.reference;
    const fromEnv=form.elements.credential.value==='env';
    $('reference-label').hidden=form.elements.credential.value==='cli';
    const fromToken=form.elements.credential.value==='token';
    reference.type=fromToken?'password':'text';
    reference.required=!$('reference-label').hidden;
    $('reference-label').querySelector('span').textContent=fromToken?(remote?'Bridge token':'API token'):fromEnv?'Environment variable name':'Key file path';
    reference.placeholder=fromToken?'Paste API token':fromEnv?(kind==='claude'?'ANTHROPIC_API_KEY':'OPENAI_API_KEY'):'/absolute/path/to/key';
    if(fromEnv)reference.pattern='[A-Za-z_][A-Za-z0-9_]*';else reference.removeAttribute('pattern');
    $('credential-help').textContent=fromEnv?'Enter the variable name only. Set its value in Settings → Environment or import it from your shell.'
      :'Enter the absolute path to the file that contains the key.';
  };
  form.elements.kind.onchange=refresh;form.elements.credential.onchange=refresh;refresh();
  toggle.disabled=addingProvider;
  toggle.onclick=()=>{form.hidden=!form.hidden;if(!form.hidden)form.elements.name.focus()};
  const scanButton=$('scan-models'),testButton=$('test-model');
  scanButton.onclick=async()=>{
    const finish=beginModelAction(scanButton,'Scanning…');
    try{await scanAssistant()}finally{
      finish();
      refreshModelPanel();
    }
  };
  testButton.onclick=async()=>{
    const {id,name,kind}=profile();
    const finish=beginModelAction(testButton,'Checking…');
    modelScan='Checking '+name+'…';$('model-status').textContent=modelScan;
    try{
      const result=await nativeAFM.assistant.testProvider(id);
      if(kind==='acp')applyAssistantInfo(await nativeAFM.assistant.state());
      modelScan=!result.found?'Not found on this machine.':!result.ok?result.auth.detail:kind==='acp'
        ?[name+' connected',result.version,result.capabilities?.image?'Images':null,result.capabilities?.resume?'Resume':null].filter(Boolean).join(' · ')
        :`${name} ${result.version||''} is installed and signed in.`.replace('  ',' ');
    }catch(error){modelScan=ipcMessage(error)}finally{finish()}
    refreshModelPanel();
  };
  for(const button of $('settings-body').querySelectorAll('[data-edit-workspace]')){
    const id=button.dataset.editWorkspace,row=button.closest('.provider-row');
    const editor=row.nextElementSibling;
    const cwd=editor.elements.workspace,status=editor.querySelector('[role="status"]');
    const [save,cancel]=editor.querySelectorAll('button');
    const saved=row.querySelector('.workspace-saved');
    const toggle=open=>{
      editor.hidden=!open;
      button.setAttribute('aria-expanded',String(open));
      if(open&&savedWorkspaceId===id){savedWorkspaceId='';saved.textContent=''}
      (open?cwd:button).focus();
    };
    button.onclick=()=>toggle(editor.hidden);
    cancel.onclick=()=>toggle(false);
    editor.onsubmit=async e=>{
      e.preventDefault();
      if(cwd.disabled)return;
      const next=cwd.value.trim();
      const finish=beginModelAction(save,'Saving…');
      editor.setAttribute('aria-busy','true');
      for(const control of [cwd,button,cancel])control.disabled=true;
      status.textContent='Checking workspace…';
      let info;
      try{
        info=await nativeAFM.assistant.updateProviderWorkspace(id,next);
      }catch(error){
        if(editor.isConnected)status.textContent=ipcMessage(error);
      }finally{
        finish();
        editor.removeAttribute('aria-busy');
        for(const control of [cwd,button,cancel])control.disabled=false;
      }
      if(info){
        editor.hidden=true;
        button.setAttribute('aria-expanded','false');
        savedWorkspaceId=id;
        saved.textContent='Saved';
        modelScan='';
        applyAssistantInfo(info);
        if(editor.isConnected)refreshModelPanel();
      }else if(editor.isConnected){
        cwd.focus();
      }
    };
  }
  for(const button of $('settings-body').querySelectorAll('[data-remove-provider]'))button.onclick=async()=>{
    const id=button.dataset.removeProvider,name=state.models.find(m=>m.id===id).name;
    if(!confirm(`Remove “${name}” from Assistant providers?`))return;
    const finish=beginModelAction(button,'Removing…');
    try{
      const info=await nativeAFM.assistant.removeProvider(id);
      modelScan='Provider removed.';
      applyAssistantInfo(info);
    }catch(error){modelScan=ipcMessage(error)}finally{finish()}
    refreshModelPanel();
  };
  form.onsubmit=async e=>{
    e.preventDefault();
    if(addingProvider)return;
    const f=new FormData(form),kind=f.get('kind'),credential=f.get('credential');
    addingProvider=true;
    form.setAttribute('aria-busy','true');
    toggle.disabled=true;
    const finish=beginModelAction(submit,'Adding…');
    let info;
    try{
      info=await nativeAFM.assistant.addProvider({name:f.get('name').trim(),kind:kind==='remote-acp'?'acp':kind,
        ...((kind==='openai'||kind.endsWith('-agent')||kind==='remote-acp')?{endpoint:f.get('location').trim()}:{executable:f.get('location').trim()}),
        ...(kind==='remote-acp'?{remoteCwd:f.get('remoteCwd').trim()}:{}),
        ...(kind==='acp'?{args:f.get('args').split(/\r?\n/).map(value=>value.trim()).filter(Boolean)}:{}),
        ...(credential==='token'?{token:f.get('reference').trim()}:{credential:{type:credential,ref:credential==='cli'?undefined:f.get('reference').trim()}})});
      modelScan='Provider added. Pick it and set its model.';
    }catch(error){modelScan=ipcMessage(error)}finally{addingProvider=false;finish()}
    if(info){
      applyAssistantInfo(info);
      if($('model-form')?.hasAttribute('aria-busy'))drawSettings();
    }else if(form.isConnected){
      form.removeAttribute('aria-busy');
      toggle.disabled=false;
      $('model-status').textContent=modelScan;
    }else if($('settings').open&&settingsTab==='models')drawSettings();
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
  const agentMode=m.agentModes?.find(mode=>mode.id===s.acpMode)?.label;
  return [m.name,s.model||'CLI default',s.reasoning!=='default'?s.reasoning:null,agentMode,
    m.kind==='acp'?'AFM · '+permissionLabels[s.permission]:permissionLabels[s.permission],m.problem?'Needs setup':null].filter(Boolean).join(' · ');
};
function providerError(raw){
  try {
    const detail=JSON.parse(raw)?.error;
    if(detail?.code==='model_not_found')return (detail.message||'Model unavailable.')+' Choose another model in the model picker.';
    return typeof detail?.message==='string' ? detail.message : raw;
  } catch {return raw}
}
function assistantToolHTML(msg,tool,at){
  const summary=`<code>${esc(tool.name)}</code>`;
  const detail=typeof tool.input==='object'?JSON.stringify(tool.input):tool.input;
  const input=detail?` <small>${esc(detail)}</small>`:'';
  const stampFor=(value,label)=>{
    const time=new Date(value);
    return Number.isFinite(+time)?`<time class="tool-error-time" datetime="${time.toISOString()}" title="${esc(label+' '+chatDate.format(time))}">${esc(label+' '+toolErrorClock.format(time))}</time>`:'';
  };
  const times=stampFor(tool.startedAt,'Started')+stampFor(tool.finishedAt,'Finished');
  const timestamps=times?`<div class="tool-timestamps">${times}</div>`:'';
  if(tool.status!=='error')return `<div class="tool ${esc(tool.status)}">${summary}${timestamps}${input}</div>`;
  const key=JSON.stringify([msg.id,tool.id]);
  const control=`data-tool-error="${esc(key)}"`;
  const time=at?new Date(at):null;
  const stamp=timestamps||(time&&Number.isFinite(+time)?`<time class="tool-error-time" datetime="${time.toISOString()}" title="${esc(chatDate.format(time))}">${esc(toolErrorClock.format(time))}</time>`:'');
  if(toolErrorOverrides.get(key)){
    return `<button type="button" class="tool error tool-error-summary" ${control} aria-expanded="false" aria-label="Expand error from ${esc(tool.name)}"><span>${summary}<small> Error</small></span>${stamp}<span aria-hidden="true">›</span></button>`;
  }
  return `<div class="tool error"><div class="tool-error-heading">${summary}
    <button type="button" class="icon" ${control} aria-expanded="true" aria-label="Collapse error from ${esc(tool.name)}">×</button></div>
    ${stamp}${input}<p class="tool-error">${esc(tool.text||'Failed')}</p></div>`;
}
function assistantTextHTML(text){
  return text.trim()?`<p class="assistant-text">${esc(text.trim())}</p>`:'';
}
function messageHTML(msg){
  if(msg.role==='user'){
    const status=msg.error?`<p class="bubble-error" role="alert">${esc(msg.error)}</p>`:msg.pending?'<small>Sending…</small>'
      :msg.queued?`<div class="chat-queue-state"><small>Queued</small><button type="button" class="icon" data-remove-queued="${msg.queueId}" aria-label="Remove queued message">×</button></div>`:'';
    return `<div class="bubble mine${msg.queued?' queued':''}">${chatMetaHTML(msg.context,msg.at)}${msg.text?`<p>${esc(msg.text)}</p>`:''}
      ${msg.imageCount?`<small>${esc(msg.imageCount)} ${msg.imageCount===1?'image':'images'} attached</small>`:''}${status}</div>`;
  }
  const approvals=(msg.approvals||[]).map(a=>`<div class="approval" role="group" aria-label="Approval needed"><p><strong>Approve ${esc(a.tool)}?</strong> <small>${esc(a.scope)}</small></p><code>${esc(a.summary)}</code><div class="actions"><button data-approve="${esc(a.approvalId)}" data-yes="no">Deny</button><button class="primary" data-approve="${esc(a.approvalId)}" data-yes="yes">Approve</button></div></div>`).join('');
  const end=msg.status==='error'?`<p class="bubble-error" role="alert">${esc(providerError(msg.error||'The reply failed.'))}</p>`:msg.status==='yielded'?'<small>Continued with your next message</small>':msg.status==='cancelled'?'<small>Stopped</small>':msg.status==='streaming'&&!msg.text?'<small>Thinking…</small>':'';
  const context=[msg.context,msg.effective?.label].filter(Boolean).join(' · ');
  const tools=new Map((msg.tools||[]).map(tool=>[tool.id,tool]));
  const errors=(msg.toolErrors||[]).filter(error=>tools.get(error.id)?.status==='error');
  const sections=(msg.segments||[]).filter(section=>section.offset<msg.text.length);
  if(!sections.length)sections.push({offset:0,at:msg.at});
  let nextError=0;
  return sections.map((section,i)=>{
    const last=i===sections.length-1;
    const boundary=last?msg.text.length:sections[i+1].offset;
    let cursor=section.offset,body='';
    while(nextError<errors.length&&(last||errors[nextError].offset<=boundary)){
      const {id,offset,at}=errors[nextError++];
      const position=Math.max(cursor,Math.min(boundary,offset));
      body+=assistantTextHTML(msg.text.slice(cursor,position))+assistantToolHTML(msg,tools.get(id),at);
      cursor=position;
    }
    body+=assistantTextHTML(msg.text.slice(cursor,boundary));
    if(last){
      body+=[...tools.values()].filter(tool=>tool.status!=='error').map(tool=>assistantToolHTML(msg,tool)).join('');
      body+=approvals+(msg.cards||[]).map(watchCardHTML).join('')+end;
      const notes=(msg.notes||[]).map(n=>`<small>${esc(n)}</small>`).join('');
      body=notes+body;
    }
    return `<div class="bubble${last?' '+esc(msg.status):''}">${chatMetaHTML(context,section.at||msg.at)}${body}</div>`;
  }).join('');
}
let viewedAgent=null;
function delegatedAgents(){
  return assistantChat.messages.flatMap(message=>(message.tools||[])
    .filter(tool=>tool.name==='task'||tool.name==='afm_run_start'||tool.name?.endsWith('__afm_run_start'))
    .map(tool=>({ key:message.id+':'+tool.id, tool, message })));
}
function closeAgentList(){
  const list=$('assistant-agents');
  if(list)list.hidden=true;
  $('assistant-agent-bar')?.setAttribute('aria-expanded','false');
}
function chooseAgent(key){
  viewedAgent=key;closeAgentList();drawAssistant();
  (key?$('assistant-agent-bar'):$('chat-input')).focus();
}
function showAgentList(){
  if(!$('tasks').hidden||!$('assistant-resume').hidden||!$('assistant-clear').hidden)return;
  const list=$('assistant-agents');
  list.replaceChildren();
  const entries=[{key:null,label:'Main',status:'Primary assistant'},...delegatedAgents().map(({key,tool},i)=>({
    key,label:(tool.name==='task'?'Subagent':'AFM run')+' '+(i+1),
    status:tool.status==='done'?(tool.name==='task'?'Finished':'Started'):tool.status==='running'?'Running':'Error',
  }))];
  for(const entry of entries){
    const button=document.createElement('button');
    button.type='button';button.textContent=entry.label+' · '+entry.status;
    button.setAttribute('aria-current',String(entry.key===viewedAgent));
    button.onclick=()=>chooseAgent(entry.key);list.append(button);
  }
  list.hidden=false;$('assistant-agent-bar').setAttribute('aria-expanded','true');
  list.querySelector('[aria-current=true]').focus();
}
$('assistant-agent-bar')?.addEventListener('click',()=>{if($('assistant-agents').hidden)showAgentList();else closeAgentList()});
$('assistant')?.addEventListener('keydown',event=>{
  if(event.isComposing||$('assistant').hidden)return;
  if(event.shiftKey&&event.key==='ArrowDown'&&$('tasks').hidden){
    event.preventDefault();showAgentList();return;
  }
  const list=$('assistant-agents');
  if(event.key==='Escape'&&(!list.hidden||viewedAgent)){
    event.preventDefault();
    if(!list.hidden){closeAgentList();$('assistant-agent-bar').focus()}
    else chooseAgent(null);
  }else if(!list.hidden&&['ArrowUp','ArrowDown'].includes(event.key)){
    const buttons=[...list.querySelectorAll('button')],index=buttons.indexOf(document.activeElement);
    if(index>=0){event.preventDefault();buttons[(index+buttons.length+(event.key==='ArrowUp'?-1:1))%buttons.length].focus()}
  }
});
function drawAssistant(){
  const agents=delegatedAgents();
  const agent=agents.find(item=>item.key===viewedAgent);
  if(!agent)viewedAgent=null;
  const bar=$('assistant-agent-bar');
  if(bar)bar.innerHTML=`${agent?'Agent activity':'Main'} · ${agents.length} delegated <kbd>⇧ ↓</kbd>`;
  if(agent){
    $('messages').innerHTML=`<div class="bubble"><strong>Agent activity</strong>${assistantToolHTML(agent.message,agent.tool)}
      ${agent.tool.status==='done'&&agent.tool.text?`<p>${esc(agent.tool.text)}</p>`:''}</div>`;
    $('chat-form').hidden=true;return;
  }
  if($('assistant-clear')?.hidden&&$('assistant-resume')?.hidden)$('chat-form').hidden=false;
  $('chat-context').textContent=[scopeText(), assistantChat.name].filter(Boolean).join(' · ');
  $('chat-model').textContent=effectiveLine()||'Choose a provider';
  const box=$('messages'),scrollTop=box.scrollTop,stick=box.scrollHeight-scrollTop-box.clientHeight<80;
  const messages=[...assistantChat.messages,...(assistantChat.pending?[assistantChat.pending]:[]),...assistantChat.queue];
  paintKeepingFocus('messages',()=>{
    box.innerHTML=(messages.length?messages.map(messageHTML).join(''):'<div class="chat-empty"><span>✧</span><h3>What’s running?</h3><p>Explore your projects or launch a workflow.</p></div>')
      +(assistantChat.local?`<div class="bubble error"><p class="bubble-error" role="alert">${esc(assistantChat.local)}</p></div>`:'');
  });
  if(stick)scrollChatToEnd();else box.scrollTop=scrollTop;
  $('chat-stop').hidden=!assistantChat.busy;
  const send=document.querySelector('#chat-form .send');
  const sendLabel=assistantChat.busy?'Queue message':'Send message';
  send.setAttribute('aria-label',sendLabel);
  send.title=sendLabel;
  send.disabled=Boolean(assistantChat.readingImages||assistantChat.clearing);
}
function drawChatImages(){
  const list=$('chat-images');
  list.hidden=!assistantChat.images.length;
  list.innerHTML=assistantChat.images.map(({type,data},index)=>`<div class="chat-image">
    <img src="data:${esc(type)};base64,${esc(data)}" alt="Attachment ${index+1}">
    <button type="button" class="icon" data-remove-image="${index}" aria-label="Remove image ${index+1}">×</button></div>`).join('');
}
function imageData(file){
  return new Promise((resolve,reject)=>{
    const reader=new FileReader();
    reader.onerror=()=>reject(Error('Could not read the pasted image.'));
    reader.onload=()=>resolve({type:file.type,data:reader.result.split(',')[1]});
    reader.readAsDataURL(file);
  });
}
async function pasteAssistantImages(event){
  const target=assistantChat;
  const files=[...(event.clipboardData?.files??[])].filter(file=>file.type.startsWith('image/'));
  if(!files.length)return;
  event.preventDefault();
  if(target.readingImages||target.clearing)return;
  if(files.length+target.images.length>MAX_CHAT_IMAGES)target.local='Attach up to four images per message.';
  else if(files.some(file=>!chatImageTypes.has(file.type)))target.local='Attach PNG, JPEG, WebP or GIF images.';
  else if(files.some(file=>file.size>MAX_CHAT_IMAGE_BYTES))target.local='Each image must be at most 5 MiB.';
  else{
    target.local='';
    target.readingImages=true;
    drawAssistant();
    try{target.images.push(...await Promise.all(files.map(imageData)))}
    catch(error){target.local=error.message}
    target.readingImages=false;
    if(target===assistantChat)drawChatImages();
  }
  drawAssistant();
}
// Streaming events arrive faster than the page needs to repaint.
function queueChatDraw(){
  if(chatDrawQueued)return;
  chatDrawQueued=true;
  requestAnimationFrame(()=>{chatDrawQueued=false;drawAssistant()});
}
function notifyAssistantFollowUp(chat=assistantChat){
  if(chat.turnId)nativeAFM.assistant.followUp?.(chat.turnId,chat.queue.length>0).catch(showNativeError);
}
function sendAssistant(text,fromDraft){
  if(assistantChat.clearing||(fromDraft&&assistantChat.readingImages))return;
  const images=fromDraft?assistantChat.images:[];
  if(!text.trim()&&!images.length)return;
  if(assistantChat.queue.length>=MAX_QUEUED_MESSAGES){
    assistantChat.local='The message queue is full.';
    drawAssistant();return;
  }
  const message={role:'user',text,images,imageCount:images.length,scope:currentScope(),context:scopeText().slice(7),
    at:new Date().toISOString(),queueId:++nextQueuedMessage,queued:true,restoreDraft:fromDraft&&!assistantChat.busy};
  if(fromDraft){
    $('chat-input').value='';
    drawAssistantCommands(true);
    assistantChat.images=[];
    drawChatImages();
  }
  assistantChat.local='';
  assistantChat.queue.push(message);
  if(assistantChat.busy){notifyAssistantFollowUp();drawAssistant()}
  else drainAssistantQueue();
}
async function drainAssistantQueue(id=activeConversation){
  const chat=stateFor(id);
  if(chat.busy||chat.clearing||!chat.queue.length)return;
  const message=chat.queue.shift();
  const {text,images,scope}=message;
  const pending={...message,pending:true,queued:false};
  const input=$('chat-input');
  chat.busy=true;
  chat.pending=pending;
  if(id===activeConversation)drawAssistant();
  try{
    await nativeAFM.assistant.send({...(id==='main'?{}:{conversationId:id}),text,scope,...(images.length?{images}:{})});
  }catch(error){
    if(chat.pending!==pending)return;
    chat.busy=false;
    chat.pending=null;
    chat.messages.push({role:'user',text,context:message.context,at:message.at,imageCount:images.length,error:ipcMessage(error)});
    if(id===activeConversation&&message.restoreDraft&&!input.value&&!chat.images.length&&!chat.readingImages){
      input.value=text;
      chat.images=images;
      drawChatImages();
    }
    if(id===activeConversation)drawAssistant();
    drainAssistantQueue(id);
  }
}
// `/clear` is handled here, never sent to a provider: the main process drops the transcript and the provider session together.
const assistantCommands=[
  ['/new','Start a new conversation'],
  ['/resume','Resume a saved conversation'],
  ['/clear','Clear this conversation'],
  ['/backlog ','Manage saved plans'],
];
const backlogCommands=[
  ['/backlog list','List open plans'],
  ['/backlog done ','Complete a plan or manage completed plans'],
  ['/backlog delete ','Delete an open plan'],
];
const completedCommands=[
  ['/backlog done delete ','Delete a completed plan'],
  ['/backlog done clear','Clear completed plans'],
];
const backlogNumberPrefix=/^(\/backlog (?:done delete|delete|done) )\d*$/;
const isBacklogCommand=text=>/^\/backlog(?:\s|$)/.test(text.trim());
let commandIndex=0, resumeRows=[],resumeIndex=0, backlogSuggestions=[], hintRequest=0, confirmAction=null;
function refreshBacklogSuggestions(){
  const request=++hintRequest;
  const id=activeConversation, text=$('chat-input').value;
  const prefix=backlogNumberPrefix.exec(text)?.[1];
  if(!prefix||!nativeAFM.assistant?.backlogPlans)return;
  nativeAFM.assistant.backlogPlans(currentScope(),prefix==='/backlog done delete ').then(rows=>{
    if(request!==hintRequest||id!==activeConversation||text!==$('chat-input').value)return;
    backlogSuggestions=rows.map(row=>[prefix+row.number,row.title]);
    drawAssistantCommands(true);
  }).catch(()=>{});
}

function openAssistantConfirmation({title,detail,cancel='Cancel',action,run}){
  $('assistant-confirm-title').textContent=title;
  $('assistant-confirm-detail').textContent=detail;
  $('assistant-confirm-cancel').textContent=cancel;
  $('assistant-confirm-action').textContent=action;
  $('assistant-clear').setAttribute('aria-label',title);
  confirmAction=run;
  $('chat-form').hidden=true;
  $('assistant-clear').hidden=false;
  $('assistant-keep').focus();
}

function confirmAssistantAction(){
  const run=confirmAction;
  closeAssistantPanel();
  run?.();
}

function closeAssistantPanel(){
  confirmAction=null;
  $('assistant-resume').hidden=true;
  $('assistant-clear').hidden=true;
  $('chat-form').hidden=false;
  $('chat-input').focus();
}

async function openAssistantConversation(row){
  if(activeConversation!==row.id){
    assistantChat.draft=$('chat-input').value;
    viewedAgent=null;closeAgentList();
    activeConversation=row.id;
    assistantChat=stateFor(row.id);
    assistantChat.name=row.name;
    $('chat-input').value=assistantChat.draft||'';
    drawChatImages();
    drawAssistant();
    const current=assistantChat;
    if(!current.loaded){
      const history=await nativeAFM.assistant.history(row.id);
      const seen=new Set(current.messages.map(message=>message.id));
      current.messages.unshift(...history.filter(message=>!seen.has(message.id)));
      current.loaded=true;
    }
  }
  if(activeConversation===row.id){
    closeAssistantPanel();drawAssistant();scrollChatToEnd();drawAssistantCommands(true);
  }
}

function drawResume(reset){
  const query=$('assistant-resume-query').value.trim().toLowerCase();
  const rows=resumeRows.filter(row=>row.name.toLowerCase().includes(query));
  if(reset)resumeIndex=Math.max(0,rows.findIndex(row=>row.id===activeConversation));
  const box=$('assistant-resume-list');
  box.innerHTML=rows.length?rows.map(({name,id},i)=>`<button type="button" data-resume="${esc(id)}" aria-current="${i===resumeIndex}">${esc(name)}${id===activeConversation?' <small>Current</small>':''}</button>`).join(''):'<p>No matching conversations.</p>';
  return rows;
}

function executeBacklogCommand(target,id,command,scope){
  target.local='';
  if(target===assistantChat){$('chat-input').value='';drawAssistantCommands(true)}
  nativeAFM.assistant.backlog(id,command,scope).then(messages=>{
    target.messages.push(...messages);
    if(target===assistantChat)drawAssistant();
  }).catch(error=>{
    target.local=ipcMessage(error);
    if(target===assistantChat)drawAssistant();
  });
}

function handleAssistantCommand(text){
  const command=text.trim();
  if(isBacklogCommand(command)){
    const target=assistantChat, id=activeConversation, scope=currentScope();
    const run=()=>executeBacklogCommand(target,id,command,scope);
    $('assistant-command-menu').hidden=true;
    const clear=command==='/backlog done clear';
    const number=/^\/backlog (?:done )?delete (\d+)$/.exec(command)?.[1];
    if(clear||number){
      openAssistantConfirmation({ title:clear?'Clear completed plans?':`Delete plan #${number}?`,
        detail:scopeText(), action:clear?'Clear completed':'Delete', run });
    }else run();
    return true;
  }
  switch(command){
    case '/new':
      $('chat-input').value='';
      nativeAFM.assistant.newConversation().then(openAssistantConversation).catch(showNativeError);
      break;
    case '/resume':
      nativeAFM.assistant.conversations().then(rows=>{
        resumeRows=rows;
        $('chat-input').value='';$('assistant-resume-query').value='';
        $('chat-form').hidden=true;$('assistant-resume').hidden=false;
        drawResume(true);$('assistant-resume-query').focus();
      }).catch(showNativeError);
      break;
    case '/clear':
      if(assistantChat.busy){clearAssistant();break}
      openAssistantConfirmation({ title:'Clear this conversation?', detail:'Remove its messages and reset the provider session.',
        cancel:'Keep chat', action:'Clear', run:clearAssistant });
      break;
    default:return false;
  }
  $('assistant-command-menu').hidden=true;
  return true;
}

$('assistant-keep')?.addEventListener('click',closeAssistantPanel);
$('assistant-erase')?.addEventListener('click',confirmAssistantAction);
$('assistant-clear')?.addEventListener('keydown',e=>{
  if(e.isComposing||e.metaKey||e.ctrlKey||e.altKey)return;
  if(['n','escape'].includes(e.key.toLowerCase())){e.preventDefault();closeAssistantPanel()}
  if(e.key.toLowerCase()==='y'){e.preventDefault();confirmAssistantAction()}
});
$('assistant-resume-list')?.addEventListener('click',e=>{
  const row=resumeRows.find(row=>row.id===e.target.closest('[data-resume]')?.dataset.resume);
  if(row)openAssistantConversation(row).catch(showNativeError);
});
$('assistant-resume-query')?.addEventListener('input',()=>drawResume(true));
$('assistant-resume')?.addEventListener('keydown',e=>{
  if(e.isComposing)return;
  if(e.key==='Escape'){e.preventDefault();closeAssistantPanel();return}
  const rows=drawResume(false);
  if(!rows.length)return;
  if(e.key==='ArrowUp'||e.key==='ArrowDown'){
    e.preventDefault();resumeIndex=(resumeIndex+rows.length+(e.key==='ArrowUp'?-1:1))%rows.length;drawResume(false);
  }else if(e.key==='Enter'&&e.target===$('assistant-resume-query')){
    e.preventDefault();openAssistantConversation(rows[resumeIndex]).catch(showNativeError);
  }
});

function commandSuggestions(){
  const input=$('chat-input').value;
  if(!input.startsWith('/')||input.includes('\n'))return [];
  const commands=input.startsWith('/backlog done ')?[...completedCommands,...backlogSuggestions]
    :input.startsWith('/backlog delete ')?backlogSuggestions
    :input.startsWith('/backlog list ')?[['/backlog list done','List completed plans']]
    :input.startsWith('/backlog ')?backlogCommands:assistantCommands;
  return commands.filter(([command])=>command.startsWith(input)&&command!==input);
}

function drawAssistantCommands(reset){
  const box=$('assistant-command-menu');
  if(!box)return;
  if(reset)commandIndex=0;
  const options=commandSuggestions();
  box.hidden=!options.length;
  box.innerHTML=options.map(([command,label],i)=>`<button type="button" data-command="${esc(command)}" aria-current="${commandIndex===i}"><code>${esc(command)}</code> <small>${esc(label)}</small></button>`).join('');
  const field=$('chat-input'), overlay=$('chat-command-color');
  const value=field.value;
  const numberCommand=backlogNumberPrefix.exec(value)?.[1];
  const matched=numberCommand?.trim()||[...assistantCommands,...backlogCommands,...completedCommands,['/backlog list done']]
    .find(([command])=>value.trimEnd()===command.trim())?.[0].trim();
  if(overlay){
    overlay.hidden=!matched;
    overlay.replaceChildren();
    if(matched){
      const command=document.createElement('span');command.textContent=matched;
      overlay.append(command,value.slice(matched.length));overlay.scrollTop=field.scrollTop;
    }
  }
  field.classList.toggle('assistant-slash',Boolean(matched));
}

function completeAssistantCommand(command){
  $('chat-input').value=command;$('chat-input').focus();backlogSuggestions=[];drawAssistantCommands(true);refreshBacklogSuggestions();
}

function assistantCommandKey(e){
  const box=$('assistant-command-menu');
  if(e.isComposing||e.shiftKey||!box||box.hidden)return false;
  const options=commandSuggestions();
  if(!options.length)return false;
  if(e.key==='ArrowUp'||e.key==='ArrowDown'){
    commandIndex=(commandIndex+options.length+(e.key==='ArrowUp'?-1:1))%options.length;
    drawAssistantCommands(false);
  }else if(e.key==='Tab'||(e.key==='ArrowRight'&&e.target.selectionStart===e.target.value.length)){
    completeAssistantCommand(options[commandIndex][0]);
  }else if(e.key==='Escape')box.hidden=true;
  else return false;
  e.preventDefault();return true;
}

$('chat-input').addEventListener('input',()=>{
  backlogSuggestions=[];drawAssistantCommands(true);refreshBacklogSuggestions();
});
$('chat-input').addEventListener('scroll',()=>{
  const overlay=$('chat-command-color');
  if(overlay)overlay.scrollTop=$('chat-input').scrollTop;
});
$('assistant-command-menu')?.addEventListener('click',e=>{
  const command=e.target.closest('[data-command]')?.dataset.command;
  if(command)completeAssistantCommand(command);
});
// Electron prefixes errors thrown in the main process.
const ipcMessage=error=>String(error?.message||error).replace(/^Error invoking remote method '[^']+': (Error: )?/,'');
async function clearAssistant(){
  const chat=assistantChat,id=activeConversation;
  if(chat.clearing||chat.readingImages)return;
  chat.local='';
  if(chat.busy){chat.local='A reply is still running. Stop it first, then clear the chat.';drawAssistant();return}
  // Nothing is sent while the main process rewrites the chat, and the first history load can no longer fill it back in.
  chat.clearing=true;
  try{
    await nativeAFM.assistant.clear(id==='main'?undefined:id);
    Object.assign(chat,{messages:[],pending:null,queue:[],images:[],cleared:true});
    chat.draft='';
    if(id===activeConversation){$('chat-input').value='';drawChatImages();drawAssistantCommands(true)}
  }catch(error){chat.local=ipcMessage(error)}
  chat.clearing=false;
  drawAssistant();
}
function stopAssistant(){nativeAFM.assistant.cancel(activeConversation==='main'?undefined:activeConversation).catch(showNativeError)}
$('chat-stop').onclick=stopAssistant;
$('chat-input').addEventListener('paste',pasteAssistantImages);
$('chat-images').addEventListener('click',event=>{
  const button=event.target.closest('[data-remove-image]');
  if(!button)return;
  assistantChat.images.splice(Number(button.dataset.removeImage),1);
  drawChatImages();
  $('chat-input').focus();
});

function handleAssistantEvent(event){
  const id=event.conversationId||'main';
  const chat=stateFor(id);
  const {messages}=chat;
  if(event.type==='start'){
    chat.busy=true;chat.local='';chat.pending=null;chat.turnId=event.turnId;
    notifyAssistantFollowUp(chat);
    if(!messages.some(m=>m.id===event.message.id))messages.push(event.message,{...event.reply,turnId:event.turnId,approvals:[],notes:[]});
  }else{
    const reply=messages.find(m=>m.turnId===event.turnId&&m.role==='assistant');
    if(event.type==='delta'&&reply){reply.text+=event.text;if(event.segments)reply.segments=event.segments}
    else if(event.type==='tool'&&reply){reply.tools.push({...event.tool,status:'running'})}
    else if(event.type==='tool_result'&&reply){
      const tool=reply.tools.find(t=>t.id===event.id);
      if(tool){
        Object.assign(tool,{status:event.error?'error':'done',text:event.text,finishedAt:event.finishedAt});
        if(event.error){
          const errors=reply.toolErrors??=[];
          if(!errors.some(error=>error.id===event.id))errors.push({id:event.id,offset:event.offset??reply.text.length,at:event.at??new Date().toISOString()});
        }
      }
    }
    else if(event.type==='approval'&&reply){reply.approvals.push(event)}
    else if(event.type==='approval_done'&&reply){reply.approvals=reply.approvals.filter(a=>a.approvalId!==event.approvalId)}
    else if(event.type==='notice'&&reply){reply.notes.push(event.text)}
    else if(event.type==='watch_card'&&reply){(reply.cards??=[]).push(event.card)}
    else if(['done','error','cancelled','yielded'].includes(event.type)){
      const final={...event.reply,turnId:event.turnId,approvals:[],notes:reply?.notes??[],cards:reply?.cards??[]};
      const at=messages.indexOf(reply);
      if(at>=0)messages[at]=final;else messages.push(final);
      chat.busy=false;chat.local='';chat.turnId=null;
      // Reply finished while the assistant was open: the input is ready for the next message.
      if(id===activeConversation&&!$('assistant').hidden&&document.activeElement===document.body)$('chat-input').focus();
      drainAssistantQueue(id);
    }
  }
  if(id===activeConversation)queueChatDraw();
}
$('messages').addEventListener('click',e=>{
  const error=e.target.closest('[data-tool-error]');
  if(error){
    const key=error.dataset.toolError;
    toolErrorOverrides.delete(key);
    toolErrorOverrides.set(key,error.getAttribute('aria-expanded')==='true');
    if(toolErrorOverrides.size>1200)toolErrorOverrides.delete(toolErrorOverrides.keys().next().value);
    state.assistantToolErrors=[...toolErrorOverrides];
    save();
    drawAssistant();
    return;
  }
  const queued=e.target.closest('[data-remove-queued]');
  if(queued){
    assistantChat.queue=assistantChat.queue.filter(message=>message.queueId!==Number(queued.dataset.removeQueued));
    notifyAssistantFollowUp();
    drawAssistant();
    $('chat-input').focus();
    return;
  }
  const button=e.target.closest('[data-approve]');
  if(button)nativeAFM.assistant.approve(button.dataset.approve,button.dataset.yes==='yes').catch(showNativeError);
});
// Called by native.js once every script has loaded.
function startAssistant(){
  const main=stateFor('main');
  nativeAFM.assistant.onEvent(handleAssistantEvent);
  nativeAFM.assistant.state().then(applyAssistantInfo).catch(showNativeError);
  nativeAFM.assistant.history().then(history=>{
    if(!main.messages.length&&!main.cleared){
      main.messages=history.map(m=>({...m,approvals:[],notes:[]}));
      main.loaded=true;
      drawAssistant();
      scrollChatToEnd();
    }
  }).catch(showNativeError);
}

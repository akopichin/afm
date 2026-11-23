const mcpStatusLabels={untested:'Not tested',checking:'Checking…',connected:'Connected',error:'Connection failed'};

function mcpCodeField(name,label,text='',language='plaintext',required=false){
  return `<div class="mcp-code-field">
    <label for="mcp-code-${name}">${label}</label>
    <textarea id="mcp-code-${name}" name="${name}" data-language="${language}" spellcheck="false"${required?' required':''}>${esc(text)}</textarea>
    <div class="mcp-code-surface" hidden></div>
  </div>`;
}

function mountMcpCodeEditors(form){
  const editors=[];
  let disposed=false;
  loadCodeEditor().then(monaco=>{
    if(disposed)return;
    for(const field of form.querySelectorAll('.mcp-code-field > textarea')){
      const surface=field.nextElementSibling;
      const label=field.labels[0];
      const wasFocused=document.activeElement===field;
      surface.hidden=false;
      let code;
      try{
        code=monaco.createCodeEditor(surface,{text:field.value,language:field.dataset.language,label:label.textContent});
      }catch{surface.hidden=true;continue}
      editors.push(code);
      code.onChange(()=>{field.value=code.getValue()});
      field.hidden=true;
      field.required=false;
      label.onclick=event=>{event.preventDefault();code.focus()};
      if(wasFocused)code.focus();
    }
  }).catch(()=>{});
  return ()=>{disposed=true;for(const code of editors)code.dispose()};
}

function drawMcpSettings(panel){
  let servers=[],busy=false,editing=null;
  let disposeEditor;
  const api=nativeAFM.assistant;
  panel.innerHTML=`
    <p class="muted">Custom servers are used by local ACP assistants with tool access.</p>
    <div class="mcp-servers"></div>
    <div class="links"><button type="button" data-mcp-add>＋ Add server</button><button type="button" data-mcp-import>Import OpenCode config</button></div>
    <div class="mcp-editor"></div>
    <p class="hint" role="status"></p>`;
  const list=panel.querySelector('.mcp-servers');
  const editor=panel.querySelector('.mcp-editor');
  const add=panel.querySelector('[data-mcp-add]');
  const status=panel.querySelector('[role="status"]');
  const projectName=id=>projects.find(p=>p.id===id)?.name??'Unavailable project';
  function scopeOptions(id=''){
    const items=projects.filter(p=>!p.external);
    const options=items.map(p=>option(p.id,p.name,p.id===id));
    if(id&&!items.some(p=>p.id===id))options.push(option(id,'Unavailable project',true));
    return option('','Global',!id)+options.join('');
  }
  const closeEditor=()=>{disposeEditor?.();disposeEditor=null;editor.replaceChildren();editing=null;drawList();add.focus()};
  const lines=value=>value.replace(/\r/g,'').split('\n').filter(line=>line.length);

  function drawList(){
    list.innerHTML=servers.map(server=>{
      const {state,detail,checkedAt}=server.status;
      const scope=server.projectId?projectName(server.projectId):'Global';
      return `<div class="setting-row mcp-server">
        <div><strong>${esc(server.name)}</strong>
          <small>${server.transport==='http'?'HTTP':'stdio'} · ${esc(scope)}${server.hasToken?' · Token saved':server.tokenFile?' · Token file':''}</small>
          <small class="mcp-status" data-state="${esc(state)}" title="${esc(checkedAt?new Date(checkedAt).toLocaleString():'')}">
            <span class="dot" aria-hidden="true"></span>${mcpStatusLabels[state]??mcpStatusLabels.untested}${detail?' · '+esc(detail):''}</small></div>
        <div class="links">
          <button type="button" data-action="test" data-id="${esc(server.id)}" aria-label="Test ${esc(server.name)}">Test connection</button>
          <button type="button" data-action="edit" data-id="${esc(server.id)}" aria-label="Edit ${esc(server.name)}">Edit</button>
          <button type="button" data-action="remove" data-id="${esc(server.id)}" aria-label="Remove ${esc(server.name)}">Remove</button>
        </div></div>`;
    }).join('')||'<p class="muted">No MCP servers configured.</p>';
    for(const button of panel.querySelectorAll('.mcp-servers button,[data-mcp-add],[data-mcp-import]')) button.disabled=busy||Boolean(editing);
  }

  async function update(action,message=''){
    busy=true;status.textContent=message;drawList();
    try{servers=await action();status.textContent='';return true}
    catch(error){status.textContent=error.message;return false}
    finally{busy=false;drawList()}
  }

  function edit(server={}){
    editing=server;status.textContent='';drawList();
    editor.innerHTML=`<form autocomplete="off">
      <label>Name<input name="name" required maxlength="64" value="${esc(server.name??'')}"></label>
      <label>Transport<select name="transport">${option('stdio','stdio',server.transport!=='http')}${option('http','HTTP',server.transport==='http')}</select></label>
      <label>Scope<select name="projectId">${scopeOptions(server.projectId??'')}</select></label>
      <div data-mcp-stdio>
        <label>Command<input name="command" placeholder="npx or /absolute/path/to/server" value="${esc(server.command??'')}"></label>
        ${mcpCodeField('args','Arguments',(server.args??[]).join('\n'))}
        ${mcpCodeField('env','Environment variables',(server.env??[]).map(({name,ref,file,value})=>name===ref?name:name+'='+(file?'@'+file:ref?'$'+ref:value)).join('\n'))}
        <p class="hint">Enter one mapping per line. Use NAME=value for settings, NAME=$SOURCE for a Desktop variable, or NAME=@~/path for a credential file.</p>
        <label>Token environment variable<input name="tokenEnv" placeholder="API_TOKEN" value="${esc(server.tokenEnv??'')}"></label>
      </div>
      <div data-mcp-http><label>URL<input name="url" type="url" placeholder="https://example.com/mcp" value="${esc(server.url??'')}"></label></div>
      <label>Token<input name="token" type="password" autocomplete="new-password" maxlength="4096"
        placeholder="${server.hasToken?'Leave blank to keep the saved token':'Optional'}"></label>
      <label>Token file<input name="tokenFile" placeholder="~/.afm/secrets/token" value="${esc(server.tokenFile??'')}"></label>
      ${server.hasToken?'<label class="mcp-remove-token"><input name="removeToken" type="checkbox"> Remove saved token</label>':''}
      <div class="actions"><button type="button" data-mcp-cancel>Cancel</button><button type="submit" class="primary">Save</button></div>
    </form>`;
    const form=editor.querySelector('form');
    const fields=form.elements;
    const transport=()=>{
      const isHttp=fields.transport.value==='http';
      editor.querySelector('[data-mcp-http]').hidden=!isHttp;
      editor.querySelector('[data-mcp-stdio]').hidden=isHttp;
      fields.url.disabled=!isHttp;fields.url.required=isHttp;
      fields.command.required=!isHttp;
    };
    fields.transport.onchange=transport;
    transport();
    fields.name.focus();
    disposeEditor=mountMcpCodeEditors(form);
    form.querySelector('[data-mcp-cancel]').onclick=closeEditor;
    form.onsubmit=async event=>{
      event.preventDefault();
      if(busy)return;
      const input={id:server.id,name:fields.name.value,transport:fields.transport.value,projectId:fields.projectId.value,
        url:fields.url.value,command:fields.command.value,args:lines(fields.args.value),
        env:lines(fields.env.value).filter(line=>line.trim()).map(line=>{
          const equals=line.indexOf('=');
          if(equals<0)return {name:line.trim(),ref:line.trim()};
          const name=line.slice(0,equals).trim(),value=line.slice(equals+1);
          if(value.startsWith('$'))return {name,ref:value.slice(1).trim()};
          if(value.startsWith('@'))return {name,file:value.slice(1).trim()};
          return {name,value};
        }),
        tokenEnv:fields.tokenEnv.value.trim(),token:fields.token.value,tokenFile:fields.tokenFile.value.trim(),removeToken:fields.removeToken?.checked};
      for(const button of form.querySelectorAll('button')) button.disabled=true;
      if(await update(()=>api.mcpSave(input),'Saving…'))closeEditor();
      else for(const button of form.querySelectorAll('button')) button.disabled=false;
    };
  }

  add.onclick=()=>edit();
  panel.querySelector('[data-mcp-import]').onclick=()=>{
    editing=true;status.textContent='';drawList();
    editor.innerHTML=`<form autocomplete="off">
      ${mcpCodeField('configuration','OpenCode configuration','','yaml',true)}
      <p class="hint">Enabled local servers are imported. Credential placeholders refer to matching Desktop environment variables.</p>
      <label>Scope<select name="projectId">${scopeOptions()}</select></label>
      <div class="actions"><button type="button" data-mcp-cancel>Cancel</button><button type="submit" class="primary">Import</button></div>
    </form>`;
    const form=editor.querySelector('form');
    form.elements.configuration.focus();
    disposeEditor=mountMcpCodeEditors(form);
    form.querySelector('[data-mcp-cancel]').onclick=closeEditor;
    form.onsubmit=async event=>{
      event.preventDefault();
      if(busy)return;
      const text=form.elements.configuration.value;
      if(!text.trim()){status.textContent='Paste an OpenCode MCP configuration.';return}
      for(const button of form.querySelectorAll('button'))button.disabled=true;
      if(await update(()=>api.mcpImport(text,form.elements.projectId.value),'Importing…'))closeEditor();
      else for(const button of form.querySelectorAll('button'))button.disabled=false;
    };
  };
  list.onclick=event=>{
    const button=event.target.closest('button[data-action]');
    if(!button||busy||editing)return;
    const server=servers.find(item=>item.id===button.dataset.id);
    if(!server)return;
    if(button.dataset.action==='test'){
      servers=servers.map(item=>item===server?{...item,status:{state:'checking'}}:item);
      update(async()=>{
        try{return await api.mcpTest(server.id)}
        catch(error){servers=servers.map(item=>item.id===server.id?server:item);throw error}
      },'Testing connection…');
    }else if(button.dataset.action==='edit')edit(server);
    else update(()=>api.mcpRemove(server.id),'Removing…');
  };
  update(()=>api.mcpList(),'Loading MCP servers…');
  return ()=>{disposeEditor?.();disposeEditor=null};
}

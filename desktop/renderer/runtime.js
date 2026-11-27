'use strict';
(() => {
  if (isDemo || !window.nativeAFM?.runtimeCheck) return;
  const splash = $('runtime-splash');
  const titles = {
    ready: 'Ready to run', native: 'Docker mode is off', cli: 'Docker CLI is unavailable to the AFM host',
    engine: 'Docker Engine is unavailable', missing: 'AFM image is unavailable', mismatch: 'AFM versions differ',
    host: 'Restart the AFM host to enable runtime checks', error: 'Runtime check failed'
  };
  let result, busy = '', revision = 0, project, resume, matching = false;
  let startup = true;
  const ready = () => ['native','ready'].includes(result?.state);

  function dismiss() {
    startup = false;
    splash.hidden = true;
    $('runtime-art').replaceChildren();
  }
  function environment() {
    dismiss();
    openSettings(false,'environment');
  }
  function button(title, action) {
    const node = document.createElement('button');
    node.type = 'button';
    node.textContent = title;
    node.onclick = action;
    return node;
  }
  function checkDone() {
    if (!busy && resume && ready() && $('settings').open && $('launch').open) {
      const retry = resume;
      resume = null;
      $('settings').close();
      retry(matching);
    }
  }
  async function check(pull = false) {
    const own = ++revision;
    busy = pull ? 'Downloading the AFM image…' : 'Checking Docker…';
    render();
    try {
      const value = await nativeAFM.runtimeCheck(project,pull,matching);
      if (own === revision) result = {...value, checked:new Date()};
    } catch (error) {
      if (own === revision) result = {state:'error',detail:error.message,checked:new Date()};
    } finally {
      if (own === revision) {
        busy = '';
        render();
        checkDone();
      }
    }
  }
  function render() {
    $('runtime-notice').hidden=!result || ready();
    const root = $('runtime-settings');
    if (!root) return;
    root.replaceChildren();
    const title = document.createElement('h3');
    title.textContent = 'Docker for runs';
    const status = document.createElement('p');
    status.setAttribute('role','status');
    status.textContent = busy || titles[result?.state] || 'Ready to check';
    root.append(title,status);
    if (result) {
      const list = document.createElement('dl');
      const rows = [['Docker CLI',result.cli],['Docker Engine',result.engine],['AFM image',result.image],
        ['Installed AFM',result.installed],['Required AFM',result.required]];
      for (const [label,value] of rows.filter(row=>row[1])) {
        const term=document.createElement('dt'), detail=document.createElement('dd');
        term.textContent=label; detail.textContent=value; list.append(term,detail);
      }
      root.append(list);
      if (result.detail) {
        const details=document.createElement('details'), summary=document.createElement('summary'), text=document.createElement('p');
        summary.textContent='Technical details'; text.textContent=result.detail; details.append(summary,text); root.append(details);
      }
    }
    if (busy) return;
    const actions = document.createElement('div');
    actions.className='actions';
    actions.append(button('Check again',()=>check()));
    if (['cli','host'].includes(result?.state)) {
      actions.append(button('Restart AFM host',()=>repair(async()=>{
        const host = await nativeAFM.hostStatus();
        if (host.ownership==='external' && !confirm('Restart the externally started AFM host?')) return;
        await nativeAFM.restartHost(host.ownership==='external');
      })));
    }
    if (result?.state==='engine' && /Mac/i.test(navigator.platform)) {
      actions.append(button('Open Docker Desktop',()=>repair(async()=>{
        await nativeAFM.openDocker();
        for(let i=0;i<15;i++){
          await new Promise(resolve=>setTimeout(resolve,1000));
          if ((await nativeAFM.runtimeCheck(project)).state!=='engine') break;
        }
      })));
    }
    if (['missing','mismatch'].includes(result?.state)) {
      if (project && result.standard && result.image!==result.standard) {
        actions.append(button('Use matching image for this run',()=>{matching=true;check()}));
      }
      if (!project && result.state==='mismatch' && result.standard && result.image!==result.standard) {
        actions.append(button('Use '+result.standard+' for Desktop runs',()=>repair(async()=>{
          const host=await nativeAFM.hostStatus();
          if (host.ownership==='external' && !confirm('Restart the external host to apply this image?')) return;
          await nativeAFM.updateEnvironment({name:'AFM_DOCKER_IMAGE',value:result.standard});
          await nativeAFM.restartHost(host.ownership==='external');
        })));
      } else if(result.state!=='mismatch' || result.image===result.standard || !resume) {
        actions.append(button('Download '+result.image,()=>check(true)));
      }
    }
    root.append(actions);
    if(result?.checked){const time=document.createElement('small');time.textContent='Checked at '+result.checked.toLocaleTimeString();root.append(time)}
  }
  async function repair(action) {
    const own=++revision;
    busy='Applying runtime change…';render();
    try {await action();if(own===revision)await check()}
    catch(error){if(own===revision){busy='';result={state:'error',detail:error.message};render()}}
  }
  window.runtimePanel = render;
  window.runtimeIssue = (value,id,retry) => {
    revision++; busy='';result=value;project=id;resume=retry;matching=false;environment();render();
  };
  $('settings').addEventListener('close',()=>{resume=null});
  $('launch').addEventListener('close',()=>{if(project){revision++;project=undefined;resume=null;matching=false;result=undefined;busy='';render()}});
  document.querySelector('.shellbar').addEventListener('click',dismiss,true);
  $('runtime-notice').onclick=environment;
  $('runtime-skip').onclick=dismiss;
  document.addEventListener('keydown',event=>{if(startup && event.key==='Escape')dismiss()});
  if (!document.querySelector('dialog[open]')) {
    splash.hidden=false;
    const frame=document.createElement('iframe');
    frame.title='Interactive AFM assembly';frame.src='assembly/index.html';
    $('runtime-art').append(frame);
  } else dismiss();
  const timeout=setTimeout(dismiss,3000);
  check().finally(()=>{
    clearTimeout(timeout);
    const showIssue=startup && !ready() && !document.querySelector('dialog[open]');
    dismiss();
    if(showIssue) environment();
  });
})();

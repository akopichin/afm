'use strict';
window.addEventListener('DOMContentLoaded', () => {
  const electron = parent === window && typeof require === 'function' ? require('electron') : null;
  const ipc = electron?.ipcRenderer;
  if (!ipc && parent === window) return;
  if (electron?.webFrame) {
    electron.webFrame.insertCSS(`
      .global-header .gh-mark, .global-header .gh-wordmark { display: none; }
      .global-header .gh-flow { margin-left: 0; padding-left: 0; border-left: 0; }
    `).catch(() => {});
  }
  const send = message => ipc ? ipc.send('dashboard:event', message) : parent.postMessage(message, '*');
  {
    let focus=false, ready=false;
    function sync(){
      const next=!!document.querySelector('.dashboard-body--feed-focus');
      if(next!==focus){focus=next;send({type:'stage-focus',active:focus})}
      if(!ready&&document.querySelector('.stage-row')){ready=true;send('stage-ready')}
    }
    const root=document.getElementById('root');
    if(root)new MutationObserver(sync).observe(root,{attributes:true,attributeFilter:['class'],childList:true,subtree:true});
    function command(data){
      if(data==='toggle-stage-focus'||(data==='exit-stage-focus'&&focus))document.querySelector('.feed-focus-toggle')?.click();
      if(data?.type==='open-stage'&&ready){
        const row=document.querySelector('[data-stage-id="'+CSS.escape(data.stage)+'"] .stage-row');
        if(!row)return;
        row.click();
        requestAnimationFrame(()=>{
          document.querySelector('[data-tab-id="detail"]')?.click();
          send('stage-opened');
        });
      }
    }
    if (ipc) ipc.on('dashboard:command', (_event, data) => { if (data === 'ready') {ready=false;sync()} else command(data); });
    else window.addEventListener('message', e => { if (e.source === parent) command(e.data); });
    sync();
  }
  document.addEventListener('keydown',e=>{
    if(e.isComposing||e.repeat||document.querySelector('dialog[open]'))return;
    if(!ipc&&e.altKey&&!e.ctrlKey&&!e.metaKey&&!e.shiftKey){
      const navigate={KeyA:'assistant',KeyL:'live'}[e.code];
      if(navigate){e.preventDefault();send({navigate})}
      return;
    }
    // Command on a Mac, Control elsewhere; the other key keeps its text-editing meaning.
    const primary=/Mac/i.test(navigator.platform)?e.metaKey&&!e.ctrlKey:e.ctrlKey&&!e.metaKey;
    if(!primary||e.altKey)return;
    if(e.shiftKey){
      const message=e.key==='Enter'?'new-run':!ipc&&e.code==='KeyK'?'switch-project':null;
      if(message){e.preventDefault();send(message)}
    }else if(!ipc){
      const navigate={KeyH:'home',KeyJ:'next',KeyK:'previous',KeyM:'model'}[e.code]
        ?? (e.key===','?'settings':/^[1-9]$/.test(e.key)?Number(e.key):null);
      if(navigate){e.preventDefault();send({navigate})}
    }
  });
});

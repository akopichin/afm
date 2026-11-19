'use strict';
window.addEventListener('DOMContentLoaded', () => {
  if(parent!==window){
    let focus=false, ready=false;
    function sync(){
      const next=!!document.querySelector('.dashboard-body--feed-focus');
      if(next!==focus){focus=next;parent.postMessage({type:'stage-focus',active:focus},'*')}
      if(!ready&&document.querySelector('.stage-row')){ready=true;parent.postMessage('stage-ready','*')}
    }
    const root=document.getElementById('root');
    if(root)new MutationObserver(sync).observe(root,{attributes:true,attributeFilter:['class'],childList:true,subtree:true});
    window.addEventListener('message',e=>{
      if(e.source!==parent)return;
      if(e.data==='toggle-stage-focus'||(e.data==='exit-stage-focus'&&focus))document.querySelector('.feed-focus-toggle')?.click();
      if(e.data?.type==='open-stage'&&ready){
        const row=document.querySelector('[data-stage-id="'+CSS.escape(e.data.stage)+'"] .stage-row');
        if(!row)return;
        row.click();
        requestAnimationFrame(()=>{
          document.querySelector('[data-tab-id="detail"]')?.click();
          parent.postMessage('stage-opened','*');
        });
      }
    });
  }
  document.addEventListener('keydown',e=>{
    if(e.isComposing||e.repeat||parent===window||document.querySelector('dialog[open]'))return;
    if(e.altKey&&!e.ctrlKey&&!e.metaKey&&!e.shiftKey){
      const navigate={KeyH:'home',KeyJ:'next',KeyK:'previous',KeyM:'model',KeyA:'assistant'}[e.code];
      if(navigate){e.preventDefault();parent.postMessage({navigate},'*')}
      return;
    }
    // Command on a Mac, Control elsewhere; the other key keeps its text-editing meaning.
    const primary=/Mac/i.test(navigator.platform)?e.metaKey&&!e.ctrlKey:e.ctrlKey&&!e.metaKey;
    if(!primary||e.altKey)return;
    const key=e.key.toLowerCase();
    if(e.shiftKey?key==='enter':key==='k'){e.preventDefault();parent.postMessage(key==='k'?'switch-project':'new-run','*')}
  });
});

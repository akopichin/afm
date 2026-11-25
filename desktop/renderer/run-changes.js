'use strict';

const changesFileLimit=1600;
const changesRefreshMs=6000;
let changesView=null;
const changesViews=new Map();

function changeLayout(){return state.runDiffLayout==='split'?'split':'unified'}
function changesLabel(run){
  const count=changesView?.run.key===run.key?changesView.result?.files.length:undefined;
  return count===undefined?'Changes':`Changes · ${count}`;
}
function toggleRunChanges(){
  const {p,r,tab}=selection();
  if(!r||!p.path||isDemo)return;
  closeFocus();
  closeQueue();
  window.nativeAFM?.focusShell().catch(showNativeError);
  openProject(p,r,tab==='changes'?'run':'changes');
}
function closeRunChanges(){
  if(!changesView)return;
  clearTimeout(changesView.timer);
  const {run,query,mode,selected,collapsed}=changesView;
  changesViews.delete(run.key);
  changesViews.set(run.key,{query,mode,selected,collapsed});
  while(changesViews.size>12)changesViews.delete(changesViews.keys().next().value);
  changesView=null;
}
function mountRunChanges(run,area){
  if(changesView?.run.key!==run.key){
    closeRunChanges();
    const prior=changesViews.get(run.key);
    const view=changesView={run,query:prior?.query??'',mode:prior?.mode??'start',selected:prior?.selected??null,
      collapsed:prior?.collapsed??new Set(),hunk:0};
    view.panel=document.createElement('section');
    view.panel.className='run-changes';
    view.panel.innerHTML=`<div class="run-changes-header"><div><h1>Changes</h1><p data-diff-meta class="muted"></p></div>
      <div class="run-changes-tools"><div class="run-diff-segments" role="group" aria-label="Comparison base">
        <button type="button" data-diff-mode="start" aria-pressed="true">Since run start</button>
        <button type="button" data-diff-mode="main" aria-pressed="false">Against main</button>
      </div><button type="button" class="action" data-diff-refresh>Refresh</button></div></div>
      <div class="run-changes-body"><aside class="run-change-files" aria-label="Changed files">
        <div class="run-change-files-head"><strong data-diff-count>Changed files</strong>
          <label class="run-change-search"><span aria-hidden="true">⌕</span><input type="search" aria-label="Search changed files" placeholder="Find a changed file…"></label></div>
        <div class="run-change-tree"></div><p class="run-change-status" role="status">Loading changes…</p>
      </aside><div class="run-change-preview"><header class="run-change-preview-head">
        <strong class="run-change-file-name"></strong><span class="run-change-stats"></span><span class="spacer"></span>
        <div class="run-diff-segments" role="group" aria-label="Diff layout">
          <button type="button" data-diff-layout="unified">Unified</button><button type="button" data-diff-layout="split">Split</button>
        </div><button type="button" class="icon" data-diff-step="-1" aria-label="Previous change" title="Previous change">↑</button>
        <button type="button" class="icon" data-diff-step="1" aria-label="Next change" title="Next change">↓</button>
      </header><div class="run-diff-scroll" tabindex="0" aria-label="Code changes"></div></div></div>`;
    const search=view.panel.querySelector('input');
    search.value=view.query;
    search.oninput=()=>{view.query=search.value;paintRunChangeTree(view)};
    view.panel.onclick=event=>handleRunChangesClick(view,event);
    paintRunPatch(view);
    readRunChanges(view);
  }
  area.append(changesView.panel);
}
function handleRunChangesClick(view,event){
  const button=event.target.closest('button');
  if(!button||button.disabled)return;
  const {diffMode,diffLayout,diffFolder,diffFile,diffStep}=button.dataset;
  if(diffMode){
    view.mode=diffMode;
    view.hunk=0;
    readRunChanges(view,true);
  }else if(diffLayout){
    state.runDiffLayout=diffLayout;
    save();
    paintRunPatch(view,true);
  }else if(diffFolder){
    if(!view.collapsed.delete(diffFolder))view.collapsed.add(diffFolder);
    paintRunChangeTree(view);
    [...view.panel.querySelectorAll('[data-diff-folder]')].find(folder=>folder.dataset.diffFolder===diffFolder)?.focus();
  }else if(diffFile){
    if(view.selected===diffFile)return;
    view.selected=diffFile;
    view.hunk=0;
    paintRunChangeTree(view);
    [...view.panel.querySelectorAll('[data-diff-file]')].find(file=>file.dataset.diffFile===diffFile)?.focus();
    readRunChangeFile(view,true);
  }else if(diffStep){
    const next=view.hunk+Number(diffStep);
    const target=view.panel.querySelector(`[data-diff-hunk="${next}"]`);
    if(target){
      view.panel.querySelector('.run-diff-hunk.current')?.classList.remove('current');
      view.hunk=next;
      target.firstElementChild.classList.add('current');
      view.panel.querySelector('[data-diff-step="-1"]').disabled=next===0;
      view.panel.querySelector('[data-diff-step="1"]').disabled=!target.nextElementSibling?.hasAttribute('data-diff-hunk');
      scrollToRunHunk(view,target);
    }
  }else if(button.hasAttribute('data-diff-refresh'))readRunChanges(view,true);
}
function changeTreeFiles(view){
  const query=view.query.trim().toLowerCase();
  return (view.result?.files??[]).filter(file=>file.path.toLowerCase().includes(query));
}
function paintRunChangeTree(view){
  const list=view.panel.querySelector('.run-change-tree'), files=changeTreeFiles(view);
  const top=list.scrollTop, total=view.result?.files.length??0;
  view.panel.querySelector('[data-diff-count]').textContent=view.query.trim()?`${files.length} of ${total} files`:`${total} changed ${total===1?'file':'files'}`;
  const root={folders:new Map(),files:[]};
  for(const file of files){
    const parts=file.path.split('/'), name=parts.pop();
    let folder=root, path='';
    for(const part of parts){
      path=path?path+'/'+part:part;
      if(!folder.folders.has(part))folder.folders.set(part,{folders:new Map(),files:[],path});
      folder=folder.folders.get(part);
    }
    folder.files.push({...file,name});
  }
  const tree=folder=>`<ul>${[...folder.folders].map(([name,child])=>{
    const expanded=Boolean(view.query.trim())||!view.collapsed.has(child.path);
    return `<li><button type="button" data-diff-folder="${esc(child.path)}" aria-expanded="${expanded}" ${view.query.trim()?'disabled':''}><span aria-hidden="true">${expanded?'⌄':'›'}</span><span>${esc(name)}</span></button>${expanded?tree(child):''}</li>`;
  }).join('')}${folder.files.map(file=>`<li><button type="button" data-diff-file="${esc(file.path)}" aria-pressed="${file.path===view.selected}" title="${esc(file.path)}"><span class="run-diff-kind" data-kind="${esc(file.status)}">${esc(file.status)}</span><span>${esc(file.name)}</span></button></li>`).join('')}</ul>`;
  list.innerHTML=files.length?tree(root):view.result?`<p class="empty">${view.query.trim()?'No matching files':'No changed files'}</p>`:'';
  list.scrollTop=top;
}
function setRunChangesStatus(view,message){view.panel.querySelector('.run-change-status').textContent=message}
function scheduleRunChanges(view){
  clearTimeout(view.timer);
  if(changesView!==view||document.hidden||!view.result)return;
  view.timer=setTimeout(()=>readRunChanges(view),changesRefreshMs);
}
async function readRunChanges(view,manual=false){
  if(view.listBusy)return;
  clearTimeout(view.timer);
  view.listBusy=true;
  const refresh=view.panel.querySelector('[data-diff-refresh]');
  const modes=[...view.panel.querySelectorAll('[data-diff-mode]')];
  refresh.disabled=true;
  for(const button of modes)button.disabled=true;
  if(manual)setRunChangesStatus(view,'Refreshing…');
  try{
    const next=await nativeAFM.runChanges(view.run.rawId,view.run.instance,view.mode);
    if(changesView!==view)return;
    const fileSignature=JSON.stringify(next.files);
    const changed=fileSignature!==view.fileSignature||next.comparison!==view.result?.comparison||next.base!==view.result?.base;
    view.result=next;
    view.fileSignature=fileSignature;
    view.mode=next.comparison;
    if(!next.files.some(file=>file.path===view.selected))view.selected=next.files[0]?.path??null;
    if(changed)paintRunChangeTree(view);
    for(const button of modes)button.setAttribute('aria-pressed',String(button.dataset.diffMode===view.mode));
    const meta=[next.branch,next.kind==='worktree'?'Worktree':'Branch',`Base ${next.base.slice(0,8)}`];
    if(!next.working)meta.push('Committed changes');
    view.panel.querySelector('[data-diff-meta]').textContent=meta.join(' · ');
    const notes=[];
    if(!next.choices.start)notes.push('Run-start commit unavailable');
    if(!next.working)notes.push('Run branch is not checked out');
    if(next.dirty)notes.push('Checkout had changes at launch');
    if(next.truncated)notes.push('File list limited to 1,000 entries');
    setRunChangesStatus(view,notes.join(' · ')||'Workspace up to date');
    const opener=document.querySelector('[data-run-changes]');
    if(opener)opener.textContent=changesLabel(view.run);
    if(view.selected)readRunChangeFile(view,manual||changed);
    else{view.patch=null;paintRunPatch(view)}
  }catch(error){
    if(changesView===view)setRunChangesStatus(view,error.message);
  }finally{
    view.listBusy=false;
    refresh.disabled=false;
    for(const button of modes)button.disabled=!view.result?.choices[button.dataset.diffMode];
    scheduleRunChanges(view);
  }
}
async function readRunChangeFile(view,showLoading){
  if(!view.selected||!view.result)return;
  if(view.patchBusy){view.patchQueued=true;return}
  view.patchBusy=true;
  const file=view.selected, token=view.result.token, base=view.result.base;
  if(showLoading&&view.patch?.path!==file){view.patch=null;view.patchMessage='Loading diff…';paintRunPatch(view)}
  try{
    const patch=await nativeAFM.runChangePatch(token,file);
    if(changesView!==view||view.selected!==file||view.result.base!==base)return;
    if(view.patch?.patch!==patch.patch||view.patch.path!==patch.path){
      view.patch=patch;
      view.patchMessage='';
      paintRunPatch(view);
    }
  }catch(error){
    if(changesView===view&&view.selected===file&&view.result.base===base){
      view.patch=null;
      view.patchMessage=error.message;
      paintRunPatch(view);
    }
  }finally{
    view.patchBusy=false;
    if(view.patchQueued){
      view.patchQueued=false;
      if(changesView===view)readRunChangeFile(view,true);
    }
  }
}
function splitPatchRows(lines){
  const rows=[];
  for(let i=0;i<lines.length;){
    const line=lines[i];
    if(line.text[0]!=='-'&&line.text[0]!=='+'){rows.push([line,line]);i++;continue}
    const left=[],right=[];
    while(i<lines.length&&(lines[i].text[0]==='-'||lines[i].text[0]==='+')){
      const edit=lines[i++];(edit.text[0]==='-'?left:right).push(edit);
    }
    for(let j=0;j<Math.max(left.length,right.length);j++)rows.push([left[j],right[j]]);
  }
  return rows;
}
function scrollToRunHunk(view,target){
  const box=view.panel.querySelector('.run-diff-scroll');
  box.scrollTop=target.getBoundingClientRect().top-box.getBoundingClientRect().top+box.scrollTop;
}
function paintRunPatch(view,scroll=false){
  const box=view.panel.querySelector('.run-diff-scroll'), top=box.scrollTop, layout=changeLayout();
  view.panel.querySelector('.run-change-file-name').textContent=view.selected??'';
  for(const button of view.panel.querySelectorAll('[data-diff-layout]'))button.setAttribute('aria-pressed',String(button.dataset.diffLayout===layout));
  const source=view.patch?.patch??'';
  const lines=source.split('\n');
  const cap=Math.min(lines.length,changesFileLimit);
  const hunks=[];
  let current;
  for(const line of lines.slice(0,cap)){
    const header=line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if(header){current={header:line,lines:[],old:Number(header[1]),next:Number(header[2])};hunks.push(current)}
    else if(current&&line)current.lines.push({text:line,old:/^[ -]/.test(line)?current.old++:null,next:/^[ +]/.test(line)?current.next++:null});
  }
  view.hunk=Math.min(view.hunk,Math.max(0,hunks.length-1));
  const lineHTML=(line,side)=>{
    const text=line?.text??'';
    const type=text[0]==='+'?'add':text[0]==='-'?'delete':'context';
    const numbers=(side?[line?.[side]]:[line?.old,line?.next]).map(number=>`<span class="run-diff-number" aria-hidden="true">${number??''}</span>`).join('');
    return `<div class="run-diff-line ${type}">${numbers}<code>${esc(text)||'&nbsp;'}</code></div>`;
  };
  const patchHTML=hunks.map((hunk,index)=>`<section data-diff-hunk="${index}"><div class="run-diff-hunk ${index===view.hunk?'current':''}">${esc(hunk.header)}</div>
    ${layout==='split'?splitPatchRows(hunk.lines).map(([left,right])=>`<div class="run-diff-pair">${lineHTML(left,'old')}${lineHTML(right,'next')}</div>`).join(''):hunk.lines.map(line=>lineHTML(line)).join('')}</section>`).join('');
  const plain=!hunks.length&&source?lines.slice(0,cap).filter(Boolean).map(line=>`<div class="run-diff-line">${esc(line)}</div>`).join(''):'';
  const headings=layout==='split'&&hunks.length?`<div class="run-diff-pair run-diff-columns"><span>${view.mode==='main'?'Main merge base':'Run start'}</span><span>${view.result?.working?'Working tree':'Run branch'}</span></div>`:'';
  box.innerHTML=headings+(patchHTML||plain||`<p class="empty">${esc(view.patchMessage||(view.selected?'No text changes':'Select a file to see its diff'))}</p>`)
    +(lines.length>cap?'<p class="empty">Diff limited to the first 1,600 lines.</p>':'');
  const visible=hunks.flatMap(hunk=>hunk.lines);
  const additions=visible.filter(line=>line.text.startsWith('+')).length;
  const removals=visible.filter(line=>line.text.startsWith('-')).length;
  view.panel.querySelector('.run-change-stats').innerHTML=hunks.length?`<span>+${additions}</span> <span>−${removals}</span>`:'';
  view.panel.querySelector('[data-diff-step="-1"]').disabled=view.hunk===0;
  view.panel.querySelector('[data-diff-step="1"]').disabled=view.hunk>=hunks.length-1;
  box.scrollTop=top;
  if(scroll){
    const target=box.querySelector(`[data-diff-hunk="${view.hunk}"]`);
    if(target)scrollToRunHunk(view,target);
  }
}
document.addEventListener('visibilitychange',()=>{
  if(!changesView)return;
  if(document.hidden)clearTimeout(changesView.timer);
  else readRunChanges(changesView);
});

'use strict';

const projectPathPanel=document.getElementById('project-path-panel');
const projectPathText=projectPathPanel.querySelector('code');
const projectPathCopy=projectPathPanel.querySelector('button');
const copyPathIcon='<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>';
const copiedPathIcon='<path d="m5 12 4 4L19 6"/>';
let projectPathAnchor, projectPathHideTimer, projectPathCopyTimer, restoringPathFocus=false;

function projectPathCopyState(label='Copy'){
  const copied=label==='Copied';
  projectPathCopy.classList.toggle('copied',copied);
  projectPathCopy.innerHTML=`<svg viewBox="0 0 24 24" aria-hidden="true">${copied?copiedPathIcon:copyPathIcon}</svg>${label}`;
}

function positionProjectPath(){
  if(!projectPathAnchor)return;
  const rect=projectPathAnchor.getBoundingClientRect(), bounds=$('screen').getBoundingClientRect();
  if(rect.bottom<bounds.top||rect.top>bounds.bottom){closeProjectPath();return}
  const height=projectPathPanel.offsetHeight;
  const above=rect.bottom+height+16>bounds.bottom&&rect.top-height-16>=bounds.top;
  projectPathPanel.classList.toggle('above',above);
  projectPathPanel.style.left=`${Math.min(Math.max(rect.left-8,12),Math.max(12,innerWidth-projectPathPanel.offsetWidth-12))}px`;
  projectPathPanel.style.top=`${above?rect.top-height-6:rect.bottom+6}px`;
}

function closeProjectPath(){
  clearTimeout(projectPathHideTimer);
  clearTimeout(projectPathCopyTimer);
  projectPathAnchor?.setAttribute('aria-expanded','false');
  projectPathAnchor=null;
  projectPathPanel.hidden=true;
}

function openProjectPath(anchor){
  clearTimeout(projectPathHideTimer);
  if(projectPathAnchor===anchor)return;
  closeProjectPath();
  const project=projects.find(p=>p.id===anchor.dataset.projectPath);
  if(!project)return;
  projectPathAnchor=anchor;
  anchor.setAttribute('aria-expanded','true');
  projectPathText.textContent=project.path;
  projectPathCopyState();
  // Keeping the popover next to its trigger preserves natural Tab order.
  anchor.after(projectPathPanel);
  projectPathPanel.hidden=false;
  positionProjectPath();
}

function scheduleProjectPathClose(){
  clearTimeout(projectPathHideTimer);
  projectPathHideTimer=setTimeout(()=>{
    if(!projectPathAnchor?.matches(':hover,:focus-visible')&&!projectPathPanel.matches(':hover,:focus-within'))closeProjectPath();
  },140);
}

function wireProjectPaths(screen){
  for(const anchor of screen.querySelectorAll('[data-project-path]')){
    anchor.addEventListener('pointerenter',event=>{if(event.pointerType!=='touch')openProjectPath(anchor)});
    anchor.addEventListener('pointerleave',scheduleProjectPathClose);
    anchor.onfocus=()=>{if(!restoringPathFocus&&anchor.matches(':focus-visible'))openProjectPath(anchor)};
    anchor.onblur=scheduleProjectPathClose;
    anchor.onclick=()=>openProjectPath(anchor);
  }
}

projectPathPanel.addEventListener('pointerenter',()=>clearTimeout(projectPathHideTimer));
projectPathPanel.addEventListener('pointerleave',scheduleProjectPathClose);
projectPathPanel.addEventListener('focusout',scheduleProjectPathClose);
projectPathCopy.onclick=async()=>{
  const anchor=projectPathAnchor;
  if(!anchor)return;
  try{
    await window.nativeAFM.copyProjectPath(projectPathText.textContent);
    if(projectPathAnchor!==anchor)return;
    clearTimeout(projectPathCopyTimer);
    projectPathCopyState('Copied');
    projectPathCopyTimer=setTimeout(projectPathCopyState,1600);
  }catch{
    if(projectPathAnchor===anchor)projectPathCopyState('Retry');
  }
};
document.addEventListener('pointerdown',event=>{
  if(!projectPathPanel.contains(event.target)&&!event.target.closest('[data-project-path]'))closeProjectPath();
});
document.addEventListener('keydown',event=>{
  if(event.key!=='Escape'||!projectPathAnchor)return;
  if(projectPathPanel.contains(document.activeElement)){
    restoringPathFocus=true;
    projectPathAnchor.focus({preventScroll:true});
    restoringPathFocus=false;
  }
  closeProjectPath();
});
document.getElementById('screen').addEventListener('scroll',positionProjectPath,{passive:true});
addEventListener('resize',positionProjectPath);

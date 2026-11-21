'use strict';
// The Tasks tab: background watches Desktop keeps for the user, their results and written summaries.
// The tasks live in the main process; this page shows the latest list it was sent and sends back the user's choices.
const watchState={tasks:[],view:'chat',note:''};
const WATCH_STATUS={active:'Watching',paused:'Paused',stopped:'Stopped',completed:'Completed'};
const WATCH_KIND={question:'Question',approval:'Approval needed',decision:'Decision needed',failed:'Failed',finished:'Finished'};

// Panels are repainted whenever the list changes, so a control is found again by what it says, not by identity.
function rememberControl(element){
  const panel=element?.closest?.('#tasks,#messages');
  return {element,panel:panel?.id,data:panel?{...element.dataset}:null};
}
function findControl({element,panel,data}){
  if(element?.isConnected)return element;
  const buttons=panel?[...$(panel).querySelectorAll('button')]:[];
  return buttons.find(button=>Object.entries(data).every(([key,value])=>button.dataset[key]===value))||null;
}
// Repaints a panel and puts the keyboard back on the control that had it.
function paintKeepingFocus(panel,paint){
  const active=document.activeElement,had=$(panel).contains(active)&&active!==$(panel)?rememberControl(active):null;
  paint();
  if(had)findControl(had)?.focus({preventScroll:true});
}

function watchScopeText(task){
  const {kind,projectId,runId,instanceId}=task.scope;
  if(kind==='all')return 'All projects';
  if(kind==='project')return projects.find(p=>p.id===projectId)?.name||'A project';
  return projects.flatMap(p=>p.runs).find(r=>r.rawId===runId&&r.instance===instanceId)?.flow||'A run';
}
const watchTime=iso=>new Date(iso).toLocaleString([],{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
function watchUntilText(task){
  const parts=[task.deadline?'Until '+watchTime(task.deadline):'No end time'];
  if(task.summaryAt&&!task.summaryDone)parts.push('summary at '+watchTime(task.summaryAt));
  return parts.join(' · ');
}
const watchUnreadTotal=()=>watchState.tasks.reduce((sum,task)=>sum+task.unread,0);
// Results and written summaries together, newest first; the newest `unread` of them are the unread ones.
function watchHistory(task){
  return [...task.results.map(item=>({type:'result',id:item.id,at:item.at,item})),...task.reports.map(item=>({type:'report',id:item.id,at:item.at,item}))]
    .sort((a,b)=>b.at.localeCompare(a.at));
}
const bell='<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 11h10l-1.5-2.1V6a3.5 3.5 0 0 0-7 0v2.9zM6.5 13a1.7 1.7 0 0 0 3 0"/></svg>';
const radar='<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="5.7"/><circle cx="8" cy="8" r="2.4"/><path d="m8 8 5-5"/></svg>';
const reportIcon='<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2.5h6l2.5 2.5v8.5H4zM6.5 8h3.5m-3.5 2.5H10"/></svg>';

function historyRowHTML(task,row,unread){
  const dot=unread?'<i class="dot" role="img" aria-label="Unread"></i>':'';
  if(row.type==='report')return `<button type="button" class="watch-event watch-result" data-watch-open="report" data-task="${esc(task.id)}" data-item="${esc(row.id)}">${reportIcon}<span><strong>${dot}${esc(row.item.title)} · summary</strong><small>${esc(watchTime(row.at))}</small><span class="watch-result-link">Open summary</span></span></button>`;
  const {item}=row;
  return `<button type="button" class="watch-event" data-watch-open="result" data-task="${esc(task.id)}" data-item="${esc(item.id)}">${bell}<span><strong>${dot}${esc(WATCH_KIND[item.kind]||item.kind)}${item.runName?' · '+esc(item.runName):''}</strong><small>${esc([item.stageName,watchTime(item.at)].filter(Boolean).join(' · '))}</small></span></button>`;
}
function taskCardHTML(task){
  const live=task.status==='active'||task.status==='paused';
  const buttons=live?`<button type="button" class="action" data-watch-action="${task.status==='paused'?'resume':'pause'}" data-task="${esc(task.id)}">${task.status==='paused'?'Resume':'Pause'}</button><button type="button" class="watch-stop" data-watch-action="stop" data-task="${esc(task.id)}">Stop task</button>`:'';
  // Summaries are never cut off by newer results.
  const history=watchHistory(task),rows=[...history.slice(0,20),...history.slice(20).filter(row=>row.type==='report')];
  return `<article class="watch-card" aria-label="${esc(task.title)}">
    <div class="watch-title">${radar}<strong>${esc(task.title)}</strong><span class="watch-state" role="status" ${task.status==='active'?'':'data-inactive'}><i class="dot"></i>${esc(WATCH_STATUS[task.status]||task.status)}</span></div>
    <dl class="watch-meta"><div><dt>Scope</dt><dd>${esc(watchScopeText(task))}</dd></div><div><dt>Notify</dt><dd>${esc(task.events.map(kind=>WATCH_KIND[kind]||kind).join(', '))}</dd></div><div><dt>Until</dt><dd>${esc(watchUntilText(task))}</dd></div></dl>
    ${buttons?`<div class="watch-buttons">${buttons}</div>`:''}
    <div class="watch-activity">Results and events</div>
    ${rows.length?rows.map((row,index)=>historyRowHTML(task,row,index<task.unread)).join(''):'<p class="watch-none">Nothing has needed attention yet.</p>'}
  </article>`;
}
function drawTasks(){
  const total=watchUnreadTotal(),badge=$('tasks-unread');
  badge.hidden=!total;badge.textContent=total;
  badge.setAttribute('aria-label',total+(total===1?' unread result':' unread results'));
  paintKeepingFocus('tasks',()=>{$('tasks').innerHTML=(watchState.note?`<p class="watch-note" role="status">${esc(watchState.note)}</p>`:'')
    +(watchState.tasks.length?[...watchState.tasks].reverse().map(taskCardHTML).join(''):'<div class="chat-empty"><span>◎</span><h3>No watches yet</h3><p>Ask the assistant to watch your runs and report back.</p></div>');});
  queueChatDraw();
}
// A task card in the chat: what was set up and where to find it. It reads the live task, so a finished summary shows up there too.
function watchCardHTML(card){
  const task=watchState.tasks.find(item=>item.id===card.taskId);
  const report=task?.reports.at(-1);
  return `<div class="watch-card compact" role="group" aria-label="Watch task">
    <div class="watch-title">${radar}<strong>${esc(card.title)}</strong>${task?`<span class="watch-state" ${task.status==='active'?'':'data-inactive'}><i class="dot"></i>${esc(WATCH_STATUS[task.status]||task.status)}</span>`:''}</div>
    <p class="watch-line">${esc([card.scope,card.deadline?'until '+watchTime(card.deadline):null].filter(Boolean).join(' · '))}</p>
    <div class="watch-buttons"><button type="button" class="action" data-watch-card="tasks" data-task="${esc(card.taskId)}">Open in Tasks</button>${report?`<button type="button" class="action" data-watch-card="summary" data-task="${esc(card.taskId)}" data-item="${esc(report.id)}">Open summary</button>`:''}</div>
  </div>`;
}

function setAssistantView(view){
  const returningToChat=view==='chat'&&watchState.view!=='chat';
  watchState.view=view;
  for(const button of $('assistant-tabs').querySelectorAll('[data-assistant-view]'))button.setAttribute('aria-pressed',String(button.dataset.assistantView===view));
  $('messages').hidden=view!=='chat';
  $('tasks').hidden=view!=='tasks';
  document.querySelector('.chat-foot').hidden=view!=='chat';
  if(view==='tasks')drawTasks();
  if(returningToChat)scrollChatToEnd();
}
// Opens a run at its waiting stage when it still waits, else the run itself; a missing run is said so.
function openRunLink(instance,runId,stage){
  const target=projects.flatMap(p=>p.runs.map(r=>({p,r}))).find(({r})=>r.rawId===runId&&r.instance===instance);
  if(!target){watchState.note='That run is no longer in the inventory.';drawTasks();return}
  openRun(target,stage||undefined);
}
async function changeTask(action,id){
  try{await nativeAFM.watch[action](id)}catch(error){watchState.note=ipcMessage(error);drawTasks();return}
  [...$('tasks').querySelectorAll('[data-watch-action]')].find(button=>button.dataset.task===id)?.focus();
}
function openHistoryItem(task,kind,itemId){
  if(kind==='report'){const report=task.reports.find(item=>item.id===itemId);if(report)openReport(task,report);return}
  const result=task.results.find(item=>item.id===itemId);
  nativeAFM.watch.markRead(task.id).catch(showNativeError);
  const target=result&&projects.flatMap(p=>p.runs.map(r=>({p,r}))).find(({r})=>r.key===result.runKey);
  if(!target){watchState.note='That run is no longer in the inventory.';drawTasks();return}
  watchState.note='';
  openRun(target,result.stageId||undefined);
}
let watchSignature='[]';
function acceptTasks(tasks){
  // The main process repeats the list on every save; an unchanged list repaints nothing.
  const signature=JSON.stringify(tasks);
  if(signature===watchSignature)return;
  watchSignature=signature;
  watchState.tasks=tasks;
  if(queuedWatchTarget&&tasks.some(item=>item.id===queuedWatchTarget.taskId))openWatchTarget(queuedWatchTarget);
  if(watchState.view==='tasks')drawTasks();
  else{const badge=$('tasks-unread'),total=watchUnreadTotal();badge.hidden=!total;badge.textContent=total;badge.setAttribute('aria-label',total+(total===1?' unread result':' unread results'));queueChatDraw()}
}
$('assistant-tabs').addEventListener('click',e=>{const button=e.target.closest('[data-assistant-view]');if(button)setAssistantView(button.dataset.assistantView)});
$('tasks').addEventListener('click',e=>{
  const action=e.target.closest('[data-watch-action]');
  if(action){changeTask(action.dataset.watchAction,action.dataset.task);return}
  const open=e.target.closest('[data-watch-open]'),task=open&&watchState.tasks.find(item=>item.id===open.dataset.task);
  if(task)openHistoryItem(task,open.dataset.watchOpen,open.dataset.item);
});
$('messages').addEventListener('click',e=>{
  const card=e.target.closest('[data-watch-card]'),task=card&&watchState.tasks.find(item=>item.id===card.dataset.task);
  if(!task)return;
  if(card.dataset.watchCard==='summary')openHistoryItem(task,'report',card.dataset.item);
  else setAssistantView('tasks');
});
// A clicked watch banner opens the Tasks tab at its result or summary. A task the list has not received yet is only shown.
// The newest click wins: a click before the task list has arrived waits for it.
let queuedWatchTarget=null;
function openWatchTarget(target){
  pendingNotice=null;queuedWatchTarget=target;
  const {taskId,itemId}=target;
  for(const dialog of document.querySelectorAll('dialog[open]'))dialog.close();
  if($('assistant').hidden)toggleChat(true);
  setAssistantView('tasks');
  const task=watchState.tasks.find(item=>item.id===taskId);
  if(!task)return;
  queuedWatchTarget=null;
  openHistoryItem(task,task.reports.some(report=>report.id===itemId)?'report':'result',itemId);
}
// Called by native.js once every script has loaded.
function startTasks(){
  nativeAFM.onWatchOpen(openWatchTarget);
  $('assistant-tabs').hidden=false;
  nativeAFM.watch.onChange(acceptTasks);
  nativeAFM.watch.list().then(acceptTasks).catch(showNativeError);
}

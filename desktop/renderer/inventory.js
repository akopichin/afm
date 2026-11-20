'use strict';
const MAX_RUNS_PER_PROJECT=20;
const tones=['var(--violet)','var(--attention)','var(--success)'];
const hashOf=text=>[...text].reduce((n,c)=>(Math.imul(n,31)+c.charCodeAt(0))>>>0,7).toString(36);
const baseName=path=>path.split(/[\\/]/).filter(Boolean).at(-1)||path;
const runLabels={running:'Running',finished:'Finished',offline:'Offline · reconnecting','api-only':'Core run · API only',mismatch:'This run changed'};
// The UI knows four run states; what a person must do comes from the host's notices, not from the run row.
function uiRunState(run,kind){
  if(run.state!=='running')return 'stopped';
  return kind==='approval'?'review':kind==='question'?'question':'running';
}
function runSignature(run,state,live){
  return [run.key,run.name,run.origin,run.mode,run.state,run.status,state,run.can.open,run.can.cancel,run.can.release,run.can.resume,live?.handle,live?.stageId,live?.url,live?.title].join('|');
}
function toRun(run,state,signature,live){
  return {id:encodeURIComponent(run.key),key:run.key,rawId:run.runId,instance:run.instanceId,flow:run.name||run.runId,
    source:run.origin==='desktop'?'Desktop':'Terminal',mode:run.mode,state,status:run.status,hostState:run.state,can:run.can,live:Boolean(live),liveStage:live||null,signature};
}
// Runs whose folder is not on Home still show up, so nothing the host knows about is invisible.
function externalProjects(runs){
  const groups=new Map();
  for(const run of runs){
    if(!run.projectRoot||!run.runId||!run.instanceId)continue;
    if(!groups.has(run.projectRoot))groups.set(run.projectRoot,[]);
    groups.get(run.projectRoot).push(run);
  }
  return [...groups].map(([root,list])=>({id:'external-'+hashOf(root),name:baseName(root),path:root,external:true,runs:list}));
}
// Keeps project and run objects stable on idle snapshots so drafts, focus and open dashboards survive.
function reconcileInventory(projects,snapshot){
  const kinds=new Map();
  for(const item of snapshot.attention)if(!kinds.has(item.runKey))kinds.set(item.runKey,item.kind);
  const browsers=new Map((snapshot.stageBrowsers||[]).map(item=>[item.runKey,item]));
  const wanted=[...snapshot.projects,...externalProjects(snapshot.unassigned)];
  const byId=new Map(projects.map(p=>[p.id,p]));
  const changed=new Set();
  const next=wanted.map(source=>{
    let p=byId.get(source.id);
    if(!p){p={id:source.id,name:source.name,path:source.path,tone:tones[parseInt(hashOf(source.id),36)%tones.length],external:Boolean(source.external),runs:[]};changed.add(p.id)}
    else if(p.name!==source.name||p.path!==source.path){p.name=source.name;p.path=source.path;changed.add(p.id)}
    const previous=new Map(p.runs.map(r=>[r.key,r]));
    const runs=source.runs.filter(run=>run.runId&&run.instanceId).slice(-MAX_RUNS_PER_PROJECT).map(run=>{
      const state=uiRunState(run,kinds.get(run.key)),live=browsers.get(run.key),signature=runSignature(run,state,live),old=previous.get(run.key);
      return old?.signature===signature?old:toRun(run,state,signature,live);
    });
    if(runs.length!==p.runs.length||runs.some((r,i)=>r!==p.runs[i])){p.runs=runs;changed.add(p.id)}
    return p;
  });
  const removed=projects.filter(p=>!wanted.some(source=>source.id===p.id));
  for(const p of removed)changed.add(p.id);
  const reordered=removed.length>0||next.length!==projects.length||next.some((p,i)=>p!==projects[i]);
  if(reordered)projects.splice(0,projects.length,...next);
  return {changed,reordered};
}

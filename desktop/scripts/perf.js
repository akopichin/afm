import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInThisContext} from 'node:vm';
import {performance} from 'node:perf_hooks';
import os from 'node:os';
import path from 'node:path';
import {mkdtemp,rm,stat} from 'node:fs/promises';
import {WatchService} from '../src/watch/index.js';

const reconcile=runInThisContext(readFileSync(new URL('../renderer/inventory.js',import.meta.url),'utf8')+'\nreconcileInventory');
const projects=[];
const view=i=>({key:'instance-'+i+':'+i,runId:String(i),instanceId:'instance-'+i,name:'flow-'+i,projectRoot:'/work/p'+(i%100),
  mode:'dashboard',state:'running',origin:'external',can:{open:true,cancel:false,release:false}});
const runs=Array.from({length:1000},(_,i)=>view(i));
const snapshotOf=()=>({host:{state:'connected'},unassigned:[],attention:[],
  projects:Array.from({length:100},(_,p)=>({id:'p'+p,name:'p'+p,path:'/work/p'+p,runs:runs.filter((_,i)=>i%100===p).map(run=>({...run}))}))});
const initial=reconcile(projects,snapshotOf());
assert.equal(initial.changed.size,100);
const original=projects.map(p=>p.runs);
const samples=[];
for(let i=0;i<55;i++){
  const next=snapshotOf();
  const start=performance.now();
  const result=reconcile(projects,next);
  if(i>=5)samples.push(performance.now()-start);
  assert.equal(result.changed.size,0,'An idle poll requested a renderer update');
  assert.equal(result.reordered,false,'An idle poll reordered Home');
}
projects.forEach((p,i)=>assert.equal(p.runs,original[i]));
const changed=snapshotOf();
changed.projects[87].runs[0].state='finished';
assert.equal(reconcile(projects,changed).changed.size,1);
samples.sort((a,b)=>a-b);
const p95=samples[Math.ceil(.95*samples.length)-1];
console.log(JSON.stringify({platform:process.platform,arch:process.arch,node:process.version,cpu:os.cpus()[0]?.model,
  projects:100,runs:1000,warmups:5,samples:samples.length,p50_ms:samples[24],p95_ms:p95,unchanged_updates:0}));
assert.ok(p95<50,'Inventory reconciliation exceeded its 50 ms p95 budget');

// Watch burst: ten active tasks over the same 1000 runs see 500 new requests at once, then idle polls; every change is saved.
const watchHome=await mkdtemp(path.join(os.tmpdir(),'afm-perf-watch-'));
try{
  const service=new WatchService({home:watchHome});
  const tasks=10,burst=500;
  for(let i=0;i<tasks;i++)service.create({title:'watch '+i});
  const asks=Array.from({length:burst},(_,i)=>({id:runs[i].key+':ask',runKey:runs[i].key,stageId:'ask',stageName:'Ask',kind:'question',
    requestAt:JSON.stringify(['autonomous_execution','q'+i,''])}));
  const watchSnapshot=attention=>({...snapshotOf(),host:{state:'connected',error:null},attention});
  service.onSnapshot(watchSnapshot([]));
  await service.flush();
  let start=performance.now();
  service.onSnapshot(watchSnapshot(asks));
  const burstMs=performance.now()-start;
  start=performance.now();
  await service.flush();
  const saveMs=performance.now()-start;
  const idle=[];
  for(let i=0;i<55;i++){
    const next=watchSnapshot(asks);
    start=performance.now();
    assert.equal(service.onSnapshot(next),false,'An idle poll changed a watch task');
    if(i>=5)idle.push(performance.now()-start);
  }
  idle.sort((a,b)=>a-b);
  const idleP95=idle[Math.ceil(.95*idle.length)-1];
  service.onSnapshot(watchSnapshot([]));
  await service.flush();
  const bytes=(await stat(service.store.file)).size;
  for(const task of service.list()){
    assert.equal(task.results.length,200,'A watch task kept more results than its bound');
    assert.ok(task.cursor.seen.length<=200,'Resolved keys outgrew the cursor horizon');
  }
  console.log(JSON.stringify({workload:'watch-burst',tasks,runs:1000,burst,burst_poll_ms:burstMs,save_ms:saveMs,
    idle_p50_ms:idle[24],idle_p95_ms:idleP95,file_bytes:bytes}));
  assert.ok(burstMs<250,'A watch burst poll exceeded its 250 ms budget');
  assert.ok(idleP95<50,'An idle watch poll exceeded its 50 ms p95 budget');
}finally{
  await rm(watchHome,{recursive:true,force:true});
}

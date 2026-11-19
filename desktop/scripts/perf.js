import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInThisContext} from 'node:vm';
import {performance} from 'node:perf_hooks';
import os from 'node:os';

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

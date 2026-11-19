import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInThisContext} from 'node:vm';
import {performance} from 'node:perf_hooks';
import {randomUUID} from 'node:crypto';
import os from 'node:os';

const reconcile=runInThisContext(readFileSync(new URL('../renderer/inventory.js',import.meta.url),'utf8')+'\nreconcileInventory');
const projects=[];
const rows=Array.from({length:1000},(_,i)=>({run_id:String(i),instance_id:'instance-'+i,
  mode:'dashboard',project_root:'/work/p'+(i%100),run_status:'running',ownership:'attached',liveness:'active'}));
const initial=reconcile(projects,rows,randomUUID);
assert.equal(initial.changed.size,100);
const original=projects.map(p=>p.runs);
const samples=[];
for(let i=0;i<55;i++){
  const start=performance.now();
  const result=reconcile(projects,rows,randomUUID);
  if(i>=5)samples.push(performance.now()-start);
  assert.equal(result.changed.size,0,'An idle poll requested a renderer update');
  assert.equal(result.added,false,'An idle poll requested a settings write');
}
projects.forEach((p,i)=>assert.equal(p.runs,original[i]));
rows[87].run_status='finished';
assert.equal(reconcile(projects,rows,randomUUID).changed.size,1);
samples.sort((a,b)=>a-b);
const p95=samples[Math.ceil(.95*samples.length)-1];
console.log(JSON.stringify({platform:process.platform,arch:process.arch,node:process.version,cpu:os.cpus()[0]?.model,
  projects:100,runs:1000,warmups:5,samples:samples.length,p50_ms:samples[24],p95_ms:p95,unchanged_updates:0}));
assert.ok(p95<50,'Inventory reconciliation exceeded its 50 ms p95 budget');

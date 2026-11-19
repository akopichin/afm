import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm, readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { Host, dashboardURL } from '../src/host.js';
import { afmBinary } from '../src/binary.js';

async function eventually(get) {
  const deadline=Date.now()+18000;
  while(Date.now()<deadline){const value=await get();if(value)return value;await delay(100)}
  throw Error('Timed out waiting for the test AFM process');
}

test('real AFM host starts a script run, serves its dashboard and cancels it', {timeout:45000}, async t=>{
  const binary=await afmBinary();
  const home=await mkdtemp(path.join(os.tmpdir(),'desktop-host-'));
  const project=path.join(home,'sample');
  const root=path.join(home,'.afm');
  await mkdir(path.join(project,'.afm','flows'),{recursive:true});
  await mkdir(root);
  await writeFile(path.join(root,'serve.yaml'), JSON.stringify({projects:[{id:'sample',path:project}]}));
  await writeFile(path.join(root,'config.yaml'),'docker:\n  enabled: false\nbrowser:\n  enabled: false\n');
  await writeFile(path.join(project,'.afm','flows','smoke.yaml'),'name: desktop-smoke\nstages:\n  - id: wait\n    script: |\n      sleep 12\n');
  const child=spawn(binary,['serve','--port','0'],{cwd:project,env:{PATH:process.env.PATH,HOME:home,USERPROFILE:home,AFM_DOCKER:'0'},stdio:'ignore'});
  const done=once(child,'exit');
  let client;
  t.after(async()=>{
    if(client)await client.close();
    child.kill();await done;
    const dir=path.join(root,'active-runs');
    for(const file of await readdir(dir).catch(()=>[])){
      const record=JSON.parse(await readFile(path.join(dir,file),'utf8'));
      if(record.pid>1)try{process.kill(record.pid,'SIGTERM')}catch{}
    }
    await rm(home,{recursive:true,force:true});
  });
  const file=path.join(root,'serve','host.json');
  await eventually(async()=>{try{return JSON.parse(await readFile(file,'utf8')).endpoint}catch{return false}});
  const host=new Host(home,async()=>{throw Error('The existing test host should be reused')});
  client=await host.connect();
  assert.deepEqual(await host.runs(),[]);
  const args={request_id:'desktop-smoke-1',project:'sample',flow:'smoke.yaml',mode:'dashboard'};
  const launch=await host.tool('afm_run_start',args);
  assert.ok(launch.launch_id);
  assert.equal((await host.tool('afm_run_start',args)).launch_id,launch.launch_id);
  const run=await eventually(async()=>(await host.runs()).find(r=>r.run_id&&r.liveness==='active'&&r.launch?.state==='ready'));
  assert.equal(run.ownership,'managed');
  const origin=await dashboardURL(run);
  assert.match(await (await fetch(origin)).text(),/id="root"/);
  await host.tool('afm_run_cancel',{run_id:run.run_id});
});

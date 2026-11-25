import test from 'node:test';
import assert from 'node:assert/strict';
import { dashboardURL, localURL } from '../src/host.js';

test('only verified dashboard endpoints open, including after port reuse', async()=>{
  const row={mode:'dashboard',ownership:'attached',liveness:'active',api_url:'http://127.0.0.1:8080',instance_id:'a',run_id:'1'};
  const probe=()=>Promise.resolve(Response.json({ready:true,mode:'dashboard',instance_id:'a',run_id:'1'}));
  assert.equal(await dashboardURL(row,probe),row.api_url);
  assert.equal(await dashboardURL({...row,api_url:'http://localhost'},probe),'http://localhost');
  await assert.rejects(dashboardURL({...row,instance_id:'reused'},probe),/changed/);
  for(const bad of [{mode:'core'},{ownership:'discovered'},{liveness:'stale'}])
    await assert.rejects(dashboardURL({...row,...bad},probe),/unavailable/);
  for(const url of ['https://example.com','file:///etc/hosts','http://user@localhost:80','http://localhost.example.com:123'])
    assert.throws(()=>localURL(url));
});

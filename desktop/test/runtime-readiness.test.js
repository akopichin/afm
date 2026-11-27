import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp, mkdir, writeFile, rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {runtimeEnvironment} from '../src/runtime.js';
import {detectProvider} from '../src/assistant/detect.js';

test('a Finder launch recovers Docker without replacing an explicit image',async t=>{
  const home=await mkdtemp(path.join(os.tmpdir(),'runtime-env-'));
  t.after(()=>rm(home,{recursive:true,force:true}));
  const bin=path.join(home,'.docker/bin');
  await mkdir(bin,{recursive:true});
  await writeFile(path.join(bin,'docker'),'#!/bin/sh\n',{mode:0o755});
  const env=await runtimeEnvironment({PATH:'/bin',AFM_DOCKER_IMAGE:'custom'},home,'darwin');
  assert.equal(env.PATH,'/bin:'+bin);
  assert.equal(env.AFM_DESKTOP_MANAGED_RUNTIME,'1');
  assert.equal(env.AFM_DOCKER_IMAGE,'custom');
});

for(const kind of ['anthropic-agent','openai-agent','openai']){
  test(kind+' model connection separates network, timeout and sign-in',async t=>{
    const provider={id:'test',name:'llm-portal',kind,endpoint:'https://example.test/v1',credential:{type:'env',ref:'TEST_KEY'}};
    const detect=()=>detectProvider(provider,{env:{TEST_KEY:'fixture'},secret:async()=> 'fixture'});
    const mock=t.mock.method(globalThis,'fetch',async()=>{throw TypeError('fetch failed')});
    assert.deepEqual((await detect()).auth,{state:'error',detail:'Cannot reach llm-portal. Check your connection.'});
    mock.mock.mockImplementation(async()=>{throw new DOMException('deadline','TimeoutError')});
    assert.deepEqual((await detect()).auth,{state:'error',detail:'Request timed out'});
    mock.mock.mockImplementation(async()=>new Response(null,{status:401}));
    assert.equal((await detect()).auth.state,'missing');
  });
}

test('startup overlay releases the toolbar and a stalled host cannot reclaim focus',async t=>{
  const {JSDOM}=await import('jsdom');
  const {readFile}=await import('node:fs/promises');
  const script=await readFile(new URL('../renderer/runtime.js',import.meta.url),'utf8');
  for(const leave of ['timeout','toolbar','escape','skip']){
    const {window}=new JSDOM('<header class="shellbar"><button id="cost">Costs</button></header><button id="runtime-notice"></button><section id="runtime-splash" hidden><div id="runtime-art"></div><button id="runtime-skip"></button></section><dialog id="settings"></dialog><dialog id="launch"></dialog>',{runScripts:'outside-only'});
    t.after(()=>window.close());
    let resolve,deadline,opened=0;
    window.$=id=>window.document.getElementById(id);
    window.isDemo=false;
    window.nativeAFM={runtimeCheck:()=>new Promise(r=>resolve=r)};
    window.openSettings=()=>opened++;
    window.setTimeout=(callback,ms)=>{assert.equal(ms,3000);deadline=callback;return 1};
    window.eval(script);
    assert.equal(window.$('runtime-splash').hidden,false);
    if (leave==='timeout') deadline();
    if (leave==='toolbar') window.$('cost').click();
    if (leave==='escape') window.document.dispatchEvent(new window.KeyboardEvent('keydown',{key:'Escape'}));
    if (leave==='skip') window.$('runtime-skip').click();
    assert.equal(window.$('runtime-splash').hidden,true);
    resolve({state:'engine'});await new Promise(r=>setImmediate(r));
    assert.equal(opened,0);
    assert.equal(window.$('runtime-notice').hidden,false);
  }
});

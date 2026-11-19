import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { JSDOM } from 'jsdom';

test('accepted Desktop keyboard routes work through the real shell with a focused run',async t=>{
  const files=new URL('../renderer/',import.meta.url);
  const dom=new JSDOM(await readFile(new URL('index.html',files),'utf8'),{url:'http://desktop.test/?demo=1',runScripts:'outside-only'});
  t.after(()=>dom.window.close());
  const w=dom.window,d=w.document;
  w.nativeAFM={models:async()=>[]};
  w.HTMLDialogElement.prototype.showModal=function(){this.open=true};
  w.HTMLDialogElement.prototype.close=function(){this.open=false;this.dispatchEvent(new w.Event('close'))};
  const scripts=['store','desktop','library','controls','attention'];
  const js=await Promise.all(scripts.map(name=>readFile(new URL(name+'.js',files),'utf8')));
  w.eval(js.join('\n;\n'));
  const get=id=>d.getElementById(id);
  const tick=()=>new Promise(resolve=>setTimeout(resolve,10));
  const key=(code,key,mods)=>d.body.dispatchEvent(new w.KeyboardEvent('keydown',{code,key,bubbles:true,cancelable:true,...mods}));
  get('finish-setup').click();
  key('KeyA','a',{altKey:true});assert.equal(d.activeElement.id,'chat-input');
  key('KeyA','a',{altKey:true});assert.equal(get('assistant').hidden,true);
  key('KeyM','m',{altKey:true});assert.ok(get('quick-model').open);
  get('quick-model').close();
  key('KeyK','k',{ctrlKey:true});assert.ok(get('picker').open);get('picker').close();
  key('Enter','Enter',{metaKey:true,shiftKey:true});assert.ok(get('launch').open);get('launch').close();
  key('KeyJ','j',{altKey:true});await tick();assert.match(w.location.hash,/afm\/review/);
  key('KeyJ','j',{altKey:true});await tick();assert.match(w.location.hash,/delivery-check/);
  key('KeyK','k',{altKey:true});await tick();assert.match(w.location.hash,/afm\/review/);
  const frame=d.querySelector('#dashboard-host iframe:not([hidden])');
  function fromRun(data){w.dispatchEvent(new w.MessageEvent('message',{source:frame.contentWindow,data}))}
  fromRun({type:'stage-focus',active:true});assert.ok(d.body.classList.contains('desktop-focus'));
  fromRun({navigate:'assistant'});assert.equal(get('assistant').hidden,false);
  assert.equal(d.body.classList.contains('desktop-focus'),false);
  fromRun({navigate:'home'});await tick();assert.equal(w.location.hash,'');
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, rm, stat } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { retryWithSSHKey, unlockSSHKey } from '../src/ssh-key.js';
import { eventually } from './support.js';

const source = 'git@git.example.test:team/workflows.git';
const denied = () => Object.assign(new Error('Git failed'),{stderr:'git@git.example.test: Permission denied (publickey).'});

test('an SSH authentication failure unlocks once and retries; other errors or cancellation stop', async () => {
  let calls = 0;
  const env = {SSH_AUTH_SOCK:'/example/agent.sock'};
  const result = await retryWithSSHKey(async () => {
    if (++calls === 1) throw denied();
    return 'fetched';
  }, {location:source, env, unlock:async request => {
    assert.deepEqual(request,{location:source,env});
    return true;
  }});
  assert.equal(result,'fetched');
  await assert.rejects(retryWithSSHKey(async()=>{throw denied()}, {
    location:source, env, unlock:async()=>false,
  }), /cancelled/);
  let asked = 0;
  await assert.rejects(retryWithSSHKey(async()=>{throw denied()}, {
    location:'https://git.example.test/team/workflows', env, unlock:async()=>{asked++;return true},
  }), /Git failed/);
  await assert.rejects(retryWithSSHKey(async()=>{
    throw Object.assign(new Error('Unknown host'),{stderr:'Host key verification failed.'});
  }, {location:source, env, unlock:async()=>{asked++;return true}}), /Unknown host/);
  assert.equal(asked,0);
});

test('the local askpass helper unlocks a real encrypted key in an isolated SSH agent', {skip:process.platform==='win32'}, async t => {
  const home = await mkdtemp(path.join(process.platform === 'darwin' ? '/tmp' : os.tmpdir(),'afm-key-test-'));
  const key = path.join(home,'encrypted');
  const socket = path.join(home,'agent');
  const passphrase = 'fixture-only-passphrase';
  execFileSync('ssh-keygen',['-q','-t','ed25519','-N',passphrase,'-f',key],{stdio:'ignore'});
  const agent = spawn('ssh-agent',['-D','-a',socket],{stdio:'ignore'});
  const ended = once(agent,'exit');
  t.after(async () => {
    if (agent.exitCode === null && agent.signalCode === null) { agent.kill(); await ended; }
    await rm(home,{recursive:true,force:true});
  });
  await eventually(async()=> (await stat(socket).catch(()=>null))?.isSocket(),{what:'test SSH agent'});
  const env = {...process.env, SSH_AUTH_SOCK:socket};
  const launcher = {node:process.execPath,script:fileURLToPath(new URL('../src/ssh-askpass-main.js',import.meta.url))};
  const observed = [];
  const cancelled = await unlockSSHKey({key,env,launcher,ask:async chosen=>{observed.push(chosen);return null}});
  assert.equal(cancelled,false);
  assert.throws(()=>execFileSync('ssh-add',['-l'],{env,stdio:'pipe'}),{status:1});
  const unlocked = await unlockSSHKey({key,env,launcher,ask:async chosen=>{observed.push(chosen);return passphrase}});
  assert.equal(unlocked,true);
  assert.deepEqual(observed,[key,key]);
  assert.match(execFileSync('ssh-add',['-l'],{env,encoding:'utf8'}), /ED25519/);
});

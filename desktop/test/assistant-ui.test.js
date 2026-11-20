import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeAssistant, fakeNative, loadShell, projectRow, providerInfo, runRow, snapshotOf, tick } from './shell-support.js';

const ra = runRow('ra');
const snapshot = snapshotOf([projectRow('a', [ra])]);

async function openShell(t, { assistant = fakeAssistant(), saved = [] } = {}) {
  assistant.saved = saved;
  const native = fakeNative(snapshot);
  native.assistant = assistant;
  const shell = await loadShell(t, { demo: false, native });
  await tick();
  shell.get('finish-setup')?.click();
  await tick();
  return { ...shell, assistant };
}
const frames = w => new Promise(resolve => w.requestAnimationFrame(() => resolve()));
const type = (w, get, text) => { get('chat-input').value = text; };
const submit = (w, get) => get('chat-form').dispatchEvent(new w.Event('submit', { cancelable: true, bubbles: true }));
const start = (id, text, extra = {}) => ({ type: 'start', turnId: id, message: { id: 'u' + id, role: 'user', text, context: 'a / flow-ra' },
  reply: { id: 'r' + id, role: 'assistant', text: '', status: 'streaming', tools: [], context: 'a / flow-ra', effective: { label: 'Ask before actions' }, ...extra } });

test('detection waits until the assistant or settings opens; first paint needs no provider call', async t => {
  const { assistant, key, get } = await openShell(t);
  assert.equal(assistant.calls.scan, 1, 'first-run setup is the one place that scans on load');
  const second = fakeAssistant();
  const shell = await loadShell(t, { demo: false, native: { ...fakeNative(snapshot), assistant: second } });
  await tick();
  shell.get('finish-setup').click();
  await tick();
  const before = second.calls.scan;
  shell.key('KeyA', 'a', { altKey: true });
  await tick();
  assert.equal(second.calls.scan, before, 'a scan already ran, so opening the chat does not repeat it');
  assert.equal(get('assistant').hidden, true);
});

test('a message goes to the main process with the selected run as scope and the reply streams in place', async t => {
  const { w, d, get, key, assistant } = await openShell(t);
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0]);
  await tick();
  key('KeyA', 'a', { altKey: true });
  assert.match(get('chat-context').textContent, /Scope: a \/ flow-ra/);
  assert.equal(get('chat-model').textContent, 'Claude Code · CLI default · Ask before actions');
  type(w, get, 'what is running?');
  submit(w, get);
  assert.equal(get('chat-input').value, '');
  assert.match(d.querySelector('.bubble.mine').textContent, /what is running/);
  await tick();
  assert.deepEqual(assistant.calls.send, [{ text: 'what is running?', scope: { runId: 'ra', instanceId: 'i-ra' } }]);
  assert.equal(get('chat-input').value, '', 'the field clears once the request was accepted');
  assert.equal(d.querySelector('.send').getAttribute('aria-label'), 'Stop reply');

  assistant.emit(start('t1', 'what is running?'));
  await frames(w);
  assert.match(get('messages').textContent, /Thinking…/);
  assistant.emit({ type: 'delta', turnId: 't1', text: 'One run' });
  await frames(w);
  assert.match(get('messages').textContent, /One run/);
  assistant.emit({ type: 'delta', turnId: 't1', text: ' is active.' });
  await frames(w);
  assert.match(get('messages').textContent, /One run is active\./);
  assert.equal(d.querySelectorAll('.bubble').length, 2);
  assistant.emit({ type: 'done', turnId: 't1', reply: { id: 'rt1', role: 'assistant', text: 'One run is active.', status: 'done', tools: [], context: 'a / flow-ra' } });
  await frames(w);
  assert.equal(d.querySelector('.send').getAttribute('aria-label'), 'Send message');
  assert.equal(d.querySelectorAll('.bubble.streaming').length, 0);
});

test('tool calls, tool errors and approvals are shown, and the answer goes to the main process', async t => {
  const { w, d, get, key, assistant } = await openShell(t);
  key('KeyA', 'a', { altKey: true });
  type(w, get, 'stop it');
  submit(w, get);
  await tick();
  assistant.emit(start('t1', 'stop it'));
  assistant.emit({ type: 'tool', turnId: 't1', tool: { id: 'c1', name: 'afm_run_cancel', input: '{"run_id":"ra"}' } });
  assistant.emit({ type: 'approval', turnId: 't1', approvalId: 'ap1', tool: 'afm_run_cancel', scope: 'a / flow-ra', summary: '{"run_id":"ra"}' });
  await frames(w);
  assert.match(d.querySelector('.approval').textContent, /Approve afm_run_cancel\?/);
  d.querySelector('[data-approve="ap1"][data-yes="yes"]').click();
  assert.deepEqual(assistant.calls.approve, [['ap1', true]]);
  assistant.emit({ type: 'approval_done', turnId: 't1', approvalId: 'ap1', approved: true });
  assistant.emit({ type: 'tool_result', turnId: 't1', id: 'c1', error: true, text: 'afm_run_cancel: out_of_scope: outside' });
  await frames(w);
  assert.equal(d.querySelector('.approval'), null);
  assert.match(d.querySelector('.tool.error').textContent, /out_of_scope/);
});

test('stop cancels, a failure keeps the typed text and shows the reason, and cancelled replies say so', async t => {
  const { w, d, get, key, assistant } = await openShell(t);
  key('KeyA', 'a', { altKey: true });
  assistant.rejectSend = 'Claude Code is not signed in. Run `claude` once to sign in.';
  type(w, get, 'hello');
  submit(w, get);
  await tick(); await frames(w);
  assert.match(get('messages').textContent, /not signed in/);
  assert.equal(get('chat-input').value, 'hello', 'a refused message is not lost');
  assert.equal(d.querySelector('.send').getAttribute('aria-label'), 'Send message');

  assistant.rejectSend = null;
  submit(w, get);
  await tick();
  assistant.emit(start('t2', 'hello'));
  await frames(w);
  submit(w, get);
  assert.equal(assistant.calls.cancel, 1, 'submitting while a reply runs stops it');
  assistant.emit({ type: 'cancelled', turnId: 't2', reply: { id: 'rt2', role: 'assistant', text: 'partial', status: 'cancelled', tools: [] } });
  await frames(w);
  assert.match(get('messages').textContent, /partial[\s\S]*Stopped/);
  assistant.emit(start('t3', 'again'));
  assistant.emit({ type: 'error', turnId: 't3', message: 'Codex exited with code 3. boom', reply: { id: 'rt3', role: 'assistant', text: '', status: 'error', error: 'Codex exited with code 3. boom', tools: [] } });
  await frames(w);
  assert.match(d.querySelector('.bubble-error').textContent, /exited with code 3/);
});

test('saved history is restored and only streaming events of the current turn change the reply', async t => {
  const saved = [{ id: 'u0', role: 'user', text: 'earlier', context: 'All projects' }, { id: 'r0', role: 'assistant', text: 'old answer', status: 'done', tools: [] }];
  const { w, get, key, assistant } = await openShell(t, { saved });
  key('KeyA', 'a', { altKey: true });
  assert.match(get('messages').textContent, /earlier[\s\S]*old answer/);
  assistant.emit({ type: 'delta', turnId: 'stranger', text: 'LEAK' });
  await frames(w);
  assert.doesNotMatch(get('messages').textContent, /LEAK/);
});

test('settings show detected providers, supported modes only and the effective flags, and changes go to the main process', async t => {
  const assistant = fakeAssistant();
  assistant.info.providers = [providerInfo('claude'), providerInfo('codex', { permissions: ['read'], reasoning: [], auth: { state: 'missing', via: null, detail: 'Run `codex login`.' } })];
  const { w, d, get, key } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  assert.match(get('settings-body').textContent, /Claude Code[\s\S]*Existing login/);
  assert.match(get('settings-body').textContent, /Codex[\s\S]*Needs sign-in/);
  assert.equal(get('permission-detail').textContent, '--claude-ask');
  get('profile-permissions').value = 'yolo';
  get('profile-permissions').dispatchEvent(new w.Event('change'));
  await tick();
  assert.deepEqual(assistant.calls.configure.at(-1), { permission: 'yolo' });
  assert.equal(get('permission-detail').textContent, '--claude-yolo');
  d.querySelector('[data-profile="codex"]').click();
  await tick();
  assert.deepEqual(assistant.calls.configure.at(-1), { provider: 'codex' });
  assert.deepEqual([...get('profile-permissions').options].map(o => o.value), ['read'], 'a mode the CLI cannot enforce is not offered');
  assert.deepEqual([...get('profile-effort').options].map(o => o.value), ['default'], 'no reasoning levels without a way to set them');
});

test('the quick chooser (Alt M) applies provider, model and reasoning through the main process', async t => {
  const { w, get, key, assistant } = await openShell(t);
  key('KeyM', 'm', { altKey: true });
  await tick();
  get('quick-agent').value = 'codex';
  get('quick-agent').dispatchEvent(new w.Event('change'));
  get('quick-model-list').value = '__custom';
  get('quick-model-list').dispatchEvent(new w.Event('change'));
  assert.equal(get('quick-model-id').hidden, false);
  get('quick-model-id').value = 'gpt-test';
  get('quick-effort').value = 'high';
  get('quick-model-form').dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick();
  assert.deepEqual(assistant.calls.configure.at(-1), { provider: 'codex', model: 'gpt-test', reasoning: 'high' });
});

test('settings and the quick chooser offer the detected models, a default and custom input', async t => {
  const assistant=fakeAssistant();
  assistant.info.providers[0].models=[{id:'sonnet',label:'Sonnet',default:true}];
  const { w,get,key }=await openShell(t,{assistant});
  get('settings-toggle').click();
  await tick();
  const picker=get('profile-model-list');
  assert.deepEqual([...picker.options].map(o=>o.value),['','sonnet','__custom']);
  picker.value='sonnet';
  picker.dispatchEvent(new w.Event('change'));
  await tick();
  assert.equal(assistant.calls.configure.at(-1).model,'sonnet');
  get('settings').close();
  key('KeyM','m',{altKey:true});
  assert.equal(get('quick-model-list').value,'sonnet');
});

test('a provider is added as a reference to its credential and can be tested and removed', async t => {
  const assistant = fakeAssistant();
  assistant.info.providers.push(providerInfo('custom-1', { name: 'Mine', builtIn: false }));
  const { w, d, get } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  get('add-model').click();
  const form = get('model-form');
  form.elements.name.value = 'Local';
  form.elements.kind.value = 'openai';
  form.elements.kind.dispatchEvent(new w.Event('change'));
  form.elements.location.value = 'http://127.0.0.1:11434/v1';
  form.elements.credential.value = 'env';
  form.elements.credential.dispatchEvent(new w.Event('change'));
  form.elements.reference.value = 'LOCAL_KEY';
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick();
  assert.deepEqual(assistant.calls.add[0], { name: 'Local', kind: 'openai', endpoint: 'http://127.0.0.1:11434/v1', credential: { type: 'env', ref: 'LOCAL_KEY' } });
  d.querySelector('[data-profile="custom-1"]').click();
  await tick();
  get('test-model').click();
  await tick();
  assert.deepEqual(assistant.calls.test, ['custom-1']);
  get('remove-model').click();
  await tick();
  assert.deepEqual(assistant.calls.remove, ['custom-1']);
});

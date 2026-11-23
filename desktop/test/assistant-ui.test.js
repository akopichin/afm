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
const enter = (w, get) => get('chat-input').dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
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
  assert.equal(d.querySelector('.send').getAttribute('aria-label'), 'Queue message');
  assert.equal(get('chat-stop').hidden, false);

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

test('parallel tool errors stay ordered between text and keep their own collapse state', async t => {
  const { w, get, d, assistant } = await openShell(t);
  get('assistant-toggle').click();
  const turnId = 'errors', box = get('messages');
  const atB = '2026-11-21T13:00:02.000Z', atA = '2026-11-21T13:01:59.000Z';
  const emit = event => assistant.emit({ ...event, turnId });
  const paragraphsAndErrors = panel => [...panel.querySelectorAll('.assistant-text,.tool.error')].map(element =>
    element.matches('.tool') ? element.querySelector('code').textContent : element.textContent);
  const button = name => [...box.querySelectorAll('[data-tool-error]')].find(element => element.getAttribute('aria-label').endsWith(name));

  assistant.emit(start(turnId, 'Check both'));
  for (const id of ['a', 'b']) emit({ type: 'tool', tool: { id, name: 'tool_' + id } });
  emit({ type: 'delta', text: 'Before' });
  emit({ type: 'tool_result', id: 'b', error: true, text: '<MCP error -32603>', offset: 6, at: atB });
  emit({ type: 'delta', text: 'Between' });
  emit({ type: 'tool_result', id: 'a', error: true, text: 'A failed', offset: 13, at: atA });
  await frames(w);
  assert.deepEqual(paragraphsAndErrors(box), ['Before', 'tool_b', 'Between', 'tool_a']);
  assert.match(box.querySelector('.tool-error').textContent, /<MCP error -32603>/);
  button('tool_b').focus();
  button('tool_b').click();
  assert.equal(button('tool_b').getAttribute('aria-expanded'), 'false');
  assert.equal(button('tool_a').getAttribute('aria-expanded'), 'true');
  assert.equal(d.activeElement, button('tool_b'));
  assert.doesNotMatch(box.textContent, /MCP error/);

  const segments = [{ offset: 0, at: '2026-11-21T13:00:00Z' }, { offset: 13, at: '2026-11-21T13:02:00Z' }];
  emit({ type: 'delta', text: 'After', segments });
  await frames(w);
  assert.equal(button('tool_b').getAttribute('aria-expanded'), 'false');
  assert.equal(d.activeElement, button('tool_b'));
  const reply = { ...start(turnId, '').reply, status: 'done', text: 'BeforeBetweenAfter', segments,
    tools: [{ id: 'a', name: 'tool_a', status: 'error', text: 'A failed' }, { id: 'b', name: 'tool_b', status: 'error', text: '<MCP error -32603>' }],
    toolErrors: [{ id: 'b', offset: 6, at: atB }, { id: 'a', offset: 13, at: atA }] };
  emit({ type: 'done', reply });
  await frames(w);
  const expected = ['Before', 'tool_b', 'Between', 'tool_a', 'After'];
  assert.deepEqual(paragraphsAndErrors(box), expected);
  assert.equal(button('tool_b').getAttribute('aria-expanded'), 'false');
  assert.equal(button('tool_b').querySelector('time').dateTime, atB);
  assert.equal(box.querySelectorAll('.bubble').item(1).querySelectorAll('.tool.error').length, 2, 'a result before a new bubble stays in the earlier bubble');

  const reopened = await openShell(t, { saved: [reply] });
  assert.deepEqual(paragraphsAndErrors(reopened.get('messages')), expected);
  assert.deepEqual([...reopened.get('messages').querySelectorAll('.tool-error-time')].map(time => time.dateTime), [atB, atA]);
});

test('parallel successful calls stay at their starts after finishing and reopening', async t => {
  const { w, get, assistant } = await openShell(t);
  get('assistant-toggle').click();
  const turnId = 'tools';
  const emit = event => assistant.emit({ ...event, turnId });
  const startedAt = '2026-11-21T13:00:01.000Z';
  const segments = [{offset: 0, at: '2026-11-21T13:00:00.000Z'}, {offset: 13, at: '2026-11-21T13:01:00.000Z'}];
  const tools = [
    {id: 'a', name: 'first_call', offset: 6, order: 1, startedAt},
    {id: 'b', name: 'second_call', offset: 13, order: 2, startedAt},
  ];
  const order = box => [...box.querySelectorAll('.assistant-text,.tool code')].map(element => element.textContent);
  assistant.emit(start(turnId, 'Run both'));
  emit({type: 'delta', text: 'Before'});
  emit({type: 'tool', tool: tools[0]});
  emit({type: 'delta', text: 'Between'});
  emit({type: 'tool', tool: tools[1]});
  emit({type: 'delta', text: 'After', segments});
  emit({type: 'tool_result', id: 'b', finishedAt: '2026-11-21T13:01:01.000Z'});
  emit({type: 'tool_result', id: 'a', finishedAt: '2026-11-21T13:01:02.000Z'});
  await frames(w);
  const expected = ['Before', 'first_call', 'Between', 'second_call', 'After'];
  assert.deepEqual(order(get('messages')), expected);
  const reply = { ...start(turnId, '').reply, status: 'done', text: 'BeforeBetweenAfter', segments,
    tools: tools.map(tool => ({...tool, status: 'done'})) };
  emit({type: 'done', reply});
  await frames(w);
  assert.deepEqual(order(get('messages')), expected);
  const reopened = await openShell(t, {saved: [reply, {...reply, id: 'legacy-tools',
    tools: [{id: 'old', name: 'old_call', status: 'done', startedAt}]}]});
  assert.deepEqual(order(reopened.get('messages')), [...expected, 'BeforeBetween', 'old_call', 'After']);
});

test('old tool errors without positions are hidden while the original reply remains', async t => {
  const saved = [{ id: 'legacy', role: 'assistant', status: 'cancelled', text: 'Saved text',
    tools: [{ id: '1', status: 'error', name: 'afm_runs_list', text: 'MCP error -32603: fetch failed' },
      { id: '2', status: 'done', name: 'afm_run_status' }] }];
  const { get } = await openShell(t, { saved });
  const box = get('messages');
  assert.match(box.textContent, /Saved text.*afm_run_status/s);
  assert.doesNotMatch(box.textContent, /afm_runs_list|fetch failed/);
  assert.equal(box.querySelector('[data-tool-error]'), null);
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
  get('chat-stop').click();
  assert.equal(assistant.calls.cancel, 1);
  assistant.emit({ type: 'cancelled', turnId: 't2', reply: { id: 'rt2', role: 'assistant', text: 'partial', status: 'cancelled', tools: [] } });
  await frames(w);
  assert.match(get('messages').textContent, /partial[\s\S]*Stopped/);
  assistant.emit(start('t3', 'again'));
  assistant.emit({ type: 'error', turnId: 't3', message: 'Codex exited with code 3. boom', reply: { id: 'rt3', role: 'assistant', text: '', status: 'error', error: 'Codex exited with code 3. boom', tools: [] } });
  await frames(w);
  assert.match(d.querySelector('.bubble.error .bubble-error').textContent, /exited with code 3/);
});

test('messages wait in FIFO order and keep the scope where they were queued', async t => {
  const { w, d, get, assistant } = await openShell(t);
  get('assistant-toggle').click();
  type(w, get, 'first');
  submit(w, get);
  assistant.emit(start('t1', 'first'));
  await frames(w);
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0]);
  await tick();
  type(w, get, 'second');
  enter(w, get);
  type(w, get, 'remove me');
  submit(w, get);
  d.querySelectorAll('[data-remove-queued]')[1].click();
  get('home').click();
  await tick();
  type(w, get, 'third');
  submit(w, get);
  assert.deepEqual(assistant.calls.send, [{ text: 'first', scope: {} }]);
  assert.deepEqual([...d.querySelectorAll('.bubble.queued p')].map(el => el.textContent), ['second', 'third']);
  assert.equal(assistant.calls.cancel, 0);
  assert.ok(assistant.calls.followUp.length > 0);
  assert.deepEqual(assistant.calls.followUp.at(-1), ['t1', true]);

  const done = id => assistant.emit({ type: 'done', turnId: id, reply: { id: 'r'+id, role: 'assistant', text: 'Done', status: 'done' } });
  assistant.emit({ type: 'yielded', turnId: 't1', reply: { id: 'rt1', role: 'assistant', text: 'Waiting for AFM', status: 'yielded' } });
  assert.deepEqual(assistant.calls.send[1], { text: 'second', scope: { runId: 'ra', instanceId: 'i-ra' } });
  assistant.emit(start('t2', 'second'));
  done('t2');
  assert.deepEqual(assistant.calls.send[2], { text: 'third', scope: {} });
  assert.equal(get('chat-input').value, '');
});

test('pasted images are removable before sending and travel with an image-only queued message', async t => {
  const { w, d, get, assistant } = await openShell(t);
  type(w, get, 'first');
  submit(w, get);
  assistant.emit(start('first', 'first'));
  const data = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aTbEAAAAASUVORK5CYII=';
  const file = new w.File([w.Uint8Array.from(w.atob(data), char => char.charCodeAt(0))], 'screenshot.png', { type: 'image/png' });
  const paste = new w.Event('paste', { bubbles: true, cancelable: true });
  Object.defineProperty(paste, 'clipboardData', { value: { files: [file, file] } });
  get('chat-input').dispatchEvent(paste);
  for (let attempt=0;attempt<10&&!d.querySelector('.chat-image');attempt++) await tick();
  assert.equal(paste.defaultPrevented, true);
  assert.equal(d.querySelectorAll('#chat-images img').length, 2);
  d.querySelector('[data-remove-image]').click();
  assert.equal(d.querySelectorAll('#chat-images img').length, 1);
  enter(w, get);
  assert.equal(d.querySelector('#chat-images img'), null);
  assert.match(d.querySelector('.bubble.queued').textContent, /1 image attached/);
  assistant.emit({ type: 'done', turnId: 'first', reply: { id: 'rfirst', role: 'assistant', text: 'Done', status: 'done' } });
  assert.deepEqual(assistant.calls.send[1], { text: '', scope: {}, images: [{ type: 'image/png', data }] });
});

test('Shift+Down shows live reasoning and keeps a completed agent open until leaving its view', async t => {
  const { w, get, assistant } = await openShell(t);
  get('assistant-toggle').click();
  assistant.emit(start('delegation', 'Delegate two tasks'));
  get('chat-input').dispatchEvent(new w.KeyboardEvent('keydown', { key: 'ArrowDown', shiftKey: true, bubbles: true }));
  const entries = () => [...get('assistant-agents').querySelectorAll('button')].map(button => button.textContent);
  assert.deepEqual(entries(), ['Main · Primary assistant']);
  assistant.emit({ type: 'delegate', turnId: 'delegation', delegate: { id: 'native-agent', name: 'Reviewer', status: 'running' } });
  assistant.emit({ type: 'tool', turnId: 'delegation', tool: { id: 'deep-agent', name: 'task' } });
  await frames(w);
  assert.deepEqual(entries(), ['Main · Primary assistant', 'Reviewer · Running', 'Subagent 2 · Running']);
  get('assistant-agents').querySelectorAll('button')[1].click();
  assistant.emit({ type: 'delegate', turnId: 'delegation', delegate: { id: 'native-agent', text: 'Working',
    messages: 'To Main: [Message content unavailable]', reasoning: 'Check the delegated task.',
    tools: [{ id: 'child-bash', name: 'Bash', input: 'pwd', status: 'running' }] } });
  await frames(w);
  assert.match(get('messages').textContent, /Reviewer · RunningReasoningCheck the delegated task\.Tool callsBash pwd/);
  assistant.emit({ type: 'delegate', turnId: 'delegation', delegate: { id: 'native-agent', status: 'done' } });
  await frames(w);
  assert.match(get('messages').textContent, /Reviewer · FinishedReasoningCheck the delegated task\.Tool callsBash pwd/);
  assert.doesNotMatch(get('messages').textContent, /Working|unavailable/);
  get('assistant-agent-bar').dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  assert.match(get('assistant-agent-bar').textContent, /Main · 1 delegated/);
  get('assistant-agent-bar').click();
  assert.deepEqual(entries(), ['Main · Primary assistant', 'Subagent 2 · Running']);
  assistant.emit({ type: 'cancelled', turnId: 'delegation', reply: { id: 'rdelegation', role: 'assistant',
    status: 'cancelled', text: '', tools: [{ id: 'deep-agent', name: 'task', status: 'running' }] } });
  await frames(w);
  assert.deepEqual(entries(), ['Main · Primary assistant']);
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

test('opening Chat always shows the latest messages and saved user and assistant messages show local time', async t => {
  const userAt = '2026-11-21T08:07:00.000Z', replyAt = '2026-11-21T08:09:00.000Z';
  const assistant = fakeAssistant();
  let restore;
  assistant.history = () => new Promise(resolve => { restore = resolve; });
  const { w, d, get } = await openShell(t, { assistant });
  const box = get('messages');
  let scrollTop = 0, scrollHeight = 1000;
  Object.defineProperties(box, {
    clientHeight: { value: 200 },
    scrollHeight: { get: () => scrollHeight },
    scrollTop: { get: () => scrollTop, set: value => { scrollTop = Math.min(value, scrollHeight - 200); } },
  });
  get('assistant-toggle').click();
  assert.equal(box.scrollTop, 800);
  box.scrollTop = 0;
  scrollHeight = 1400;
  restore([
    { id: 'legacy', role: 'user', text: 'Older entry' },
    { id: 'question', role: 'user', text: 'Question', at: userAt },
    { id: 'answer', role: 'assistant', text: 'Most recent reply', status: 'done', at: replyAt },
  ]);
  await tick();
  assert.equal(box.scrollTop, 1200, 'late history lands at the end');
  assert.deepEqual([...box.querySelectorAll('time.bubble-time')].map(el => el.dateTime), [userAt, replyAt]);
  const clock = new w.Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
  assert.equal(box.querySelector('.bubble.done time').textContent, clock.format(new w.Date(replyAt)));
  assert.ok(box.querySelector('.bubble.done time').title);

  box.scrollTop = 0;
  get('close-chat').click();
  get('assistant-toggle').click();
  assert.equal(box.scrollTop, 1200, 'reopening ignores the old scroll position');
  box.scrollTop = 0;
  d.querySelector('[data-assistant-view="tasks"]').click();
  d.querySelector('[data-assistant-view="chat"]').click();
  assert.equal(box.scrollTop, 1200, 'returning from Tasks also shows the latest message');
});

test('a long pause splits one streamed reply into independently timestamped bubbles', async t => {
  const first = '2026-11-21T13:00:00.000Z', second = '2026-11-21T17:30:00.000Z';
  const { w, get, assistant } = await openShell(t);
  get('assistant-toggle').click();
  assistant.emit(start('split', 'Keep me updated'));
  assistant.emit({ type: 'delta', turnId: 'split', text: 'Started.', segments: [{ at: first, offset: 0 }] });
  await frames(w);
  assistant.emit({ type: 'delta', turnId: 'split', text: '\n\nFinished.', segments: [{ at: first, offset: 0 }, { at: second, offset: 8 }] });
  await frames(w);
  const box = get('messages');
  assert.deepEqual([...box.querySelectorAll('.bubble:not(.mine) time')].map(el => el.dateTime), [first, second]);
  assert.deepEqual([...box.querySelectorAll('.bubble:not(.mine) p')].map(el => el.textContent), ['Started.', 'Finished.']);
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

test('the quick chooser (Command M) applies provider, model and reasoning through the main process', async t => {
  const { w, get, key, assistant } = await openShell(t);
  key('KeyM', 'm', { metaKey: true });
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

test('the quick chooser tests the draft automatically and can retry without applying it', async t => {
  const assistant = fakeAssistant();
  assistant.info.providers.push(providerInfo('portal', { kind: 'anthropic-agent', reasoning: [], models: [
    { id: 'dashscope/kimi-k3', reasoning: ['low', 'medium', 'high', 'xhigh'] },
  ] }));
  const attempts = [];
  assistant.testModel = (force, draft) => {
    const result = Promise.withResolvers();
    attempts.push({ force, draft: JSON.parse(JSON.stringify(draft)), result });
    return result.promise;
  };
  const { w, get, key } = await openShell(t, { assistant });
  key('KeyM', 'm', { metaKey: true });
  await tick();
  get('quick-agent').value = 'portal';
  get('quick-agent').dispatchEvent(new w.Event('change'));
  get('quick-model-list').value = 'dashscope/kimi-k3';
  get('quick-model-list').dispatchEvent(new w.Event('change'));
  assert.deepEqual([...get('quick-effort').options].map(option => option.value), ['default', 'low', 'medium', 'high', 'xhigh']);
  get('quick-effort').value = 'high';
  get('quick-effort').dispatchEvent(new w.Event('change'));
  await tick(240);
  const draft = { provider: 'portal', model: 'dashscope/kimi-k3', reasoning: 'high' };
  assert.equal(attempts.length, 1);
  assert.equal(attempts[0].force, false);
  assert.deepEqual(attempts[0].draft, draft);
  assert.equal(get('quick-model-test-status').getAttribute('aria-busy'), 'true');
  assert.equal(get('quick-apply-model').disabled, true);
  assert.deepEqual(assistant.calls.configure, []);
  attempts[0].result.resolve({ ok: false, detail: 'High was rejected.' });
  await tick();
  assert.equal(get('quick-model-test-status').textContent, 'High was rejected.');
  assert.equal(get('quick-model-test-status').getAttribute('aria-busy'), 'false');
  get('quick-test-model').click();
  await tick();
  assert.equal(attempts[1].force, true);
  assert.deepEqual(attempts[1].draft, draft);
  attempts[1].result.resolve({ ok: true, detail: 'Test request succeeded.' });
  await tick();
  get('quick-model-form').dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick();
  assert.deepEqual(assistant.calls.configure, [draft]);
  assert.equal(attempts.length, 2);
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
  key('KeyM','m',{metaKey:true});
  assert.equal(get('quick-model-list').value,'sonnet');
});

test('a provider is added as a reference to its credential and can be tested', async t => {
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
  assert.equal(get('acp-args').hidden, true);
  assert.equal(form.elements.reference.placeholder, 'OPENAI_API_KEY');
  assert.match(get('credential-help').textContent, /variable name only.*Settings → Environment/);
  form.elements.reference.value = 'LOCAL_KEY';
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick();
  assert.deepEqual(assistant.calls.add[0], { name: 'Local', kind: 'openai', endpoint: 'http://127.0.0.1:11434/v1', credential: { type: 'env', ref: 'LOCAL_KEY' } });
  d.querySelector('[data-profile="custom-1"]').click();
  await tick();
  get('test-model').click();
  await tick();
  assert.deepEqual(assistant.calls.test, ['custom-1']);
});

test('selecting a Deep Agent reasoning level tests it and shows a rejection beside the settings', async t => {
  const assistant = fakeAssistant();
  const model = 'qwen3.8-max';
  assistant.info.providers.push(providerInfo('gateway', { kind: 'openai-agent', reasoning: [], models: [{ id: model, reasoning: ['low', 'medium'] }] }));
  assistant.info.settings = { ...assistant.info.settings, provider: 'gateway', model };
  const calls = [];
  const pending = Promise.withResolvers();
  assistant.testModel = async force => {
    calls.push([assistant.info.settings.reasoning, force]);
    return pending.promise;
  };
  const { w, get } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  assert.deepEqual([...get('profile-effort').options].map(choice => choice.value), ['default', 'low', 'medium']);
  get('profile-effort').value = 'medium';
  get('profile-effort').dispatchEvent(new w.Event('change'));
  await tick();
  assert.deepEqual(calls, [['medium', false]]);
  assert.equal(get('model-test-status').getAttribute('aria-busy'), 'true');
  pending.resolve({ ok: false, detail: 'This route rejected reasoning “medium”.' });
  await tick();
  assert.match(get('model-test-status').textContent, /rejected reasoning/);
  get('test-model-reply').click();
  await tick();
  assert.deepEqual(calls.at(-1), ['medium', true]);
  assert.deepEqual(assistant.calls.send, []);
});

test('checking a Deep Agent connection refreshes its catalog in the model picker', async t => {
  const assistant = fakeAssistant();
  const provider = providerInfo('portal', { name: 'llm-portal', kind: 'anthropic-agent', models: [] });
  assistant.info.providers.push(provider);
  assistant.info.settings.provider = provider.id;
  const check = assistant.testProvider;
  assistant.testProvider = async id => {
    provider.models = [{ id: 'dashscope/qwen3.8-max', label: 'Qwen 3.8 Max' }];
    return check(id);
  };
  const { get } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  const modelIds = () => [...get('profile-model-list').options].map(option => option.value);
  assert.deepEqual(modelIds(), ['', '__custom']);
  get('test-model').click();
  await tick();
  assert.deepEqual(modelIds(), ['', 'dashscope/qwen3.8-max', '__custom']);
  assert.match(get('model-status').textContent, /1 model found/);
  assert.match(get('profile-permissions').parentElement.textContent, /AFM tool permissions/);
  assert.equal(get('profile-permissions').querySelector('[value="yolo"]').textContent, 'Allow AFM actions without approval');
});

test('a remote ACP card edits the workspace in place', async t => {
  const assistant = fakeAssistant();
  const remote = providerInfo('custom-remote', { name: 'Clodik', builtIn: false, kind: 'acp', remoteCwd: '/workspace' });
  assistant.info.providers.push(remote);
  const updates = [];
  const update = Promise.withResolvers();
  assistant.updateProviderWorkspace = async (id, cwd) => {
    updates.push({ id, cwd });
    if (cwd === '/outside') throw Error('Workspace is outside the bridge root.');
    await update.promise;
    remote.remoteCwd = cwd;
    return assistant.info;
  };
  const { w, d, get } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  const edit = d.querySelector('[data-edit-workspace]');
  assert.equal(d.querySelectorAll('[data-edit-workspace]').length, 1);
  edit.click();
  const form = get('workspace-custom-remote');
  assert.equal(form.elements.workspace.value, '/workspace');
  form.elements.workspace.value = '/outside';
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick();
  assert.equal(form.elements.workspace.value, '/outside');
  assert.match(form.querySelector('[role="status"]').textContent, /outside/);
  form.elements.workspace.value = '/workspace/project';
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  const save = form.querySelector('button[type="submit"]');
  assert.equal(save.disabled, true);
  assert.equal(save.getAttribute('aria-busy'), 'true');
  assert.equal(save.textContent, 'Saving…');
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  assert.equal(updates.length, 2);
  assert.equal(form.hidden, false);
  update.resolve();
  await tick();
  assert.deepEqual(updates.at(-1), { id: 'custom-remote', cwd: '/workspace/project' });
  assert.equal(get('workspace-custom-remote').hidden, true);
  assert.equal(get('workspace-custom-remote').elements.workspace.value, '/workspace/project');
  assert.equal(d.querySelector('.workspace-saved').textContent, 'Saved');
});

test('only custom provider rows offer removal, and confirmation targets the clicked provider', async t => {
  const assistant = fakeAssistant();
  assistant.info.providers.push(providerInfo('custom-1', { name: 'Mine', builtIn: false }));
  assistant.removeProvider = async id => {
    assistant.calls.remove.push(id);
    assistant.info.providers = assistant.info.providers.filter(provider => provider.id !== id);
    return assistant.info;
  };
  const { w, d, get } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  const buttons = d.querySelectorAll('[data-remove-provider]');
  assert.equal(buttons.length, 1);
  const [button] = buttons;
  assert.equal(button.getAttribute('aria-label'), 'Remove Mine');
  const confirmations = [];
  w.confirm = message => { confirmations.push(message); return confirmations.length === 2; };
  button.click();
  assert.match(confirmations[0], /Remove “Mine”/);
  assert.deepEqual(assistant.calls.remove, []);
  button.click();
  await tick();
  assert.deepEqual(assistant.calls.remove, ['custom-1']);
  assert.equal(d.querySelector('[data-profile="custom-1"]'), null);
  assert.equal(d.querySelector('[data-profile="claude"]').getAttribute('aria-pressed'), 'true');
});

test('adding a provider ignores repeat submissions while pending, even across settings tabs', async t => {
  const assistant = fakeAssistant();
  const pending = Promise.withResolvers();
  assistant.addProvider = input => {
    assistant.calls.add.push(input);
    return pending.promise;
  };
  const { w, d, get } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  get('add-model').click();
  const submit = () => get('model-form').dispatchEvent(new w.Event('submit', { cancelable: true }));
  const button = () => get('model-form').querySelector('button[type="submit"]');
  get('model-form').elements.name.value = 'OpenCode';
  get('model-form').elements.location.value = '/opt/homebrew/bin/opencode';
  submit();
  assert.equal(button().disabled, true);
  assert.equal(button().textContent, 'Adding…');
  submit();
  d.querySelector('[data-panel="appearance"]').click();
  d.querySelector('[data-panel="models"]').click();
  assert.equal(button().disabled, true);
  submit();
  assert.equal(assistant.calls.add.length, 1);

  assistant.info.providers.push(providerInfo('custom-opencode', { name: 'OpenCode', builtIn: false, kind: 'acp' }));
  pending.resolve(assistant.info);
  await tick();
  assert.equal(d.querySelectorAll('[data-profile="custom-opencode"]').length, 1);
  assert.equal(button().disabled, false);
  assert.equal(get('model-form').hidden, true);
  assert.match(get('settings-body').textContent, /Provider added/);
});

test('an add provider error unlocks the form and keeps its values for a retry', async t => {
  const assistant = fakeAssistant();
  assistant.addProvider = async () => { throw Error('Could not add provider.'); };
  const { w, get } = await openShell(t, { assistant });
  get('settings-toggle').click();
  await tick();
  get('add-model').click();
  const form = get('model-form');
  form.elements.name.value = 'OpenCode';
  form.elements.location.value = '/opt/homebrew/bin/opencode';
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick();
  assert.equal(form.hidden, false);
  assert.equal(form.elements.name.value, 'OpenCode');
  assert.equal(form.querySelector('button[type="submit"]').disabled, false);
  assert.match(get('settings-body').textContent, /Could not add provider/);
});

test('ACP arguments and the environment variable reference use separate fields', async t => {
  const { w, get, assistant } = await openShell(t);
  get('settings-toggle').click();
  get('add-model').click();
  assert.equal(get('acp-args').hidden, true);
  const form = get('model-form');
  const change = (control, value) => {
    control.value = value;
    control.dispatchEvent(new w.Event('change'));
  };
  change(form.elements.kind, 'acp');
  change(form.elements.credential, 'env');
  assert.equal(get('acp-args').hidden, false);
  assert.match(get('acp-args-help').textContent, /Optional/);
  assert.equal(form.elements.reference.pattern, '[A-Za-z_][A-Za-z0-9_]*');
  form.elements.name.value = 'Codex ACP';
  form.elements.location.value = '/opt/homebrew/bin/npx';
  form.elements.args.value = '-y\n\n@agentclientprotocol/codex-acp';
  form.elements.reference.value = 'OPENAI_API_KEY';
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick();
  assert.deepEqual(assistant.calls.add.at(-1), { name: 'Codex ACP', kind: 'acp', executable: '/opt/homebrew/bin/npx',
    args: ['-y', '@agentclientprotocol/codex-acp'], credential: { type: 'env', ref: 'OPENAI_API_KEY' } });
});

test('the composer takes the message at once and stays usable while the provider warms up', async t => {
  const assistant = fakeAssistant();
  let release;
  assistant.send = request => new Promise(resolve => { assistant.calls.send.push(request); release = resolve; });
  const { w, d, get, key } = await openShell(t, { assistant });
  key('KeyA', 'a', { altKey: true });
  type(w, get, 'first question');
  submit(w, get);
  await tick();
  assert.match(d.querySelector('.bubble.mine').textContent, /first question[\s\S]*Sending…/);
  assert.equal(get('chat-input').disabled, false);
  get('chat-input').value = 'next draft';
  assert.equal(get('chat-input').value, 'next draft', 'typing is not blocked while the first message is pending');
  release({ turnId: 't1' });
  await tick();
  assert.equal(get('chat-input').value, 'next draft', 'the accepted request does not wipe the draft');
});

test('Settings → Environment refresh asks the main process to import again and reports it', async t => {
  const native = fakeNative(snapshot);
  native.prefs = async () => ({ gitMode: 'current', environment: { shell: 'zsh', file: '', names: [] } });
  native.refreshEnvironment = async () => { native.refreshes = (native.refreshes ?? 0) + 1; if (native.refreshFails) throw Error('Could not load the exported environment from .zshrc.'); };
  native.assistant = fakeAssistant();
  const { d, get } = await loadShell(t, { demo: false, native }).then(async shell => { await tick(); shell.get('finish-setup')?.click(); await tick(); return shell; });
  get('settings-toggle').click();
  await tick();
  d.querySelector('[data-panel="environment"]').click();
  await tick();
  get('env-refresh').click();
  await tick();
  assert.equal(native.refreshes, 1);
  assert.match(get('settings-body').textContent, /Environment reloaded\./);
  native.refreshFails = true;
  get('env-refresh').click();
  await tick();
  assert.match(get('settings-body').textContent, /Could not load the exported environment/);
});

test('/clear is handled in the window: the main process drops the chat, nothing is sent to a provider', async t => {
  const saved = [{ id: 'u1', role: 'user', text: 'old question' }, { id: 'r1', role: 'assistant', text: 'old answer', status: 'done', tools: [] }];
  const { w, d, get, key, assistant } = await openShell(t, { saved });
  key('KeyA', 'a', { altKey: true });
  await tick();
  assert.match(get('messages').textContent, /old answer/);
  type(w, get, '  /clear ');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  assert.equal(assistant.calls.clear, 1);
  assert.deepEqual(assistant.calls.send, [], '/clear is never a message to the provider');
  assert.equal(get('chat-input').value, '', 'the command is consumed');
  assert.doesNotMatch(get('messages').textContent, /old answer|old question/);
  assert.equal(d.querySelectorAll('.bubble').length, 0);
  assert.match(get('messages').textContent, /What’s running/);
});

test('/clear while a reply is running explains itself, keeps the chat and does not stop the reply', async t => {
  const { w, d, get, key, assistant } = await openShell(t);
  key('KeyA', 'a', { altKey: true });
  type(w, get, 'question');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  assistant.emit(start('t1', 'question'));
  await frames(w);
  type(w, get, '/clear');
  enter(w, get);
  await frames(w);
  assert.equal(assistant.calls.clear, 0, 'the main process is not asked while a reply runs');
  assert.equal(assistant.calls.cancel, 0, 'the running reply is not stopped by the command');
  assert.match(get('messages').textContent, /Stop it first, then clear the chat/);
  assert.match(get('messages').textContent, /question/);
  assert.equal(get('chat-input').value, '/clear', 'the command stays so it can be repeated');
  assistant.emit({ type: 'done', turnId: 't1', reply: { id: 'rt1', role: 'assistant', text: 'fine', status: 'done', tools: [], context: 'a / flow-ra' } });
  await frames(w);
  assert.doesNotMatch(get('messages').textContent, /Stop it first/, 'the refusal is gone once the reply ended');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  assert.equal(assistant.calls.clear, 1, 'once the reply ended the same command clears');
  assert.equal(d.querySelectorAll('.bubble.mine').length, 0);
});

test('a refused /clear from the main process shows why and keeps the messages', async t => {
  const saved = [{ id: 'u1', role: 'user', text: 'keep me' }];
  const assistant = fakeAssistant();
  assistant.rejectClear = 'Stop the running reply before clearing the chat.';
  const { w, get, key } = await openShell(t, { assistant, saved });
  key('KeyA', 'a', { altKey: true });
  await tick();
  type(w, get, '/clear');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  assert.match(get('messages').textContent, /Stop the running reply before clearing the chat/);
  assert.match(get('messages').textContent, /keep me/);
});

test('the stop button stops a running reply even when the field holds /clear', async t => {
  const { w, get, key, assistant } = await openShell(t);
  key('KeyA', 'a', { altKey: true });
  type(w, get, 'question');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  assistant.emit(start('t1', 'question'));
  await frames(w);
  type(w, get, '/clear');
  get('chat-stop').click();
  await tick();
  assert.equal(assistant.calls.cancel, 1);
  assert.equal(assistant.calls.clear, 0);
});

test('nothing is sent while /clear is being written, and the first history load cannot bring the chat back', async t => {
  const saved = [{ id: 'u1', role: 'user', text: 'old question' }];
  const assistant = fakeAssistant();
  let release;
  assistant.clear = () => new Promise(resolve => { assistant.calls.clear++; release = () => { assistant.saved = []; resolve(); }; });
  let loadHistory;
  assistant.history = () => new Promise(resolve => { loadHistory = () => resolve(saved); });
  const { w, get, key } = await openShell(t, { assistant });
  key('KeyA', 'a', { altKey: true });
  type(w, get, '/clear');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  type(w, get, 'sent too early');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  assert.deepEqual(assistant.calls.send, [], 'the composer is held while the chat is rewritten');
  release();
  await tick();
  loadHistory();
  await tick();
  assert.doesNotMatch(get('messages').textContent, /old question/, 'a history read that started earlier does not refill a cleared chat');
});

test('a /clear that main refuses shows the reason without the Electron prefix', async t => {
  const assistant = fakeAssistant();
  assistant.rejectClear = "Error invoking remote method 'assistant:clear': Error: Stop the running reply before clearing the chat.";
  const { w, get, key } = await openShell(t, { assistant });
  key('KeyA', 'a', { altKey: true });
  await tick();
  type(w, get, '/clear');
  submit(w, get);
  if(!get('assistant-clear').hidden)get('assistant-erase').click();
  await tick();
  assert.match(get('messages').textContent, /^.*Stop the running reply before clearing the chat\./s);
  assert.doesNotMatch(get('messages').textContent, /Error invoking remote method/);
});

test('/resume filters saved conversations and keeps the draft and a background reply in their own chat', async t => {
  const assistant = fakeAssistant();
  const id = 'deac0d00-0000-4000-8000-000000000001';
  assistant.conversations = async () => [{ id: 'main', name: 'Main conversation' }, { id, name: 'Complex Feature' }];
  assistant.history = async target => target === id ? [{ id: 'second', role: 'user', text: 'Second chat' }] : [];
  const { w, d, get } = await openShell(t, { assistant });
  get('assistant-toggle').click();
  type(w, get, '/resume');
  submit(w, get);
  await tick();
  const query = get('assistant-resume-query');
  assert.equal(d.activeElement, query);
  query.value = 'plEX';
  query.dispatchEvent(new w.Event('input', { bubbles: true }));
  assert.equal(get('assistant-resume-list').querySelectorAll('button').length, 1);
  query.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  await tick();
  assert.match(get('messages').textContent, /Second chat/);
  assistant.emit(start('background', 'Main run progress'));
  await frames(w);
  assert.doesNotMatch(get('messages').textContent, /Main run progress/);
});

test('cancelling the inline clear confirmation keeps the current conversation', async t => {
  const { w, d, get, assistant } = await openShell(t, { saved: [{ id: 'u', role: 'user', text: 'Keep me' }] });
  get('assistant-toggle').click();
  await tick();
  type(w, get, '/clear');
  submit(w, get);
  assert.equal(d.activeElement.id, 'assistant-keep');
  d.activeElement.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'n', bubbles: true, cancelable: true }));
  assert.equal(get('assistant-clear').hidden, true);
  assert.equal(assistant.calls.clear, 0);
  assert.match(get('messages').textContent, /Keep me/);
});

test('backlog completion reveals one command level and confirms deletion locally', async t => {
  const assistant = fakeAssistant();
  assistant.info.providers.push(providerInfo('clodik', { name: 'Clodik', builtIn: false, kind: 'acp', remoteCwd: '/workspace' }));
  assistant.info.settings.provider = 'clodik';
  const calls = [];
  assistant.backlog = async (id, command, scope) => {
    calls.push({ id, command, scope });
    return [{ id: 'backlog-result-' + calls.length, role: 'assistant', text: command === '/backlog' ? 'Backlog commands' : 'Deleted #2.', status: 'done', tools: [] }];
  };
  assistant.backlogPlans = async (_scope, completed) => completed ? [{ number: 2, title: 'Done' }] : [];
  const { w, d, get } = await openShell(t, { assistant });
  get('assistant-toggle').click();
  const input = get('chat-input');
  const choices = () => Array.from(get('assistant-command-menu').querySelectorAll('code'), element => element.textContent);
  type(w, get, '/');
  input.dispatchEvent(new w.Event('input', { bubbles: true }));
  assert.deepEqual(choices(), ['/new', '/resume', '/clear', '/backlog ']);
  type(w, get, '/backlog');
  submit(w, get);
  await tick();
  assert.deepEqual(calls.map(call => call.command), ['/backlog']);
  assert.deepEqual(assistant.calls.send, []);
  assert.match(get('messages').textContent, /Backlog commands/);
  type(w, get, '/back');
  input.dispatchEvent(new w.Event('input', { bubbles: true }));
  assert.deepEqual(choices(), ['/backlog ']);
  input.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
  assert.equal(input.value, '/backlog ');
  assert.deepEqual(choices(), ['/backlog list', '/backlog done ', '/backlog delete ']);
  type(w, get, '/backlog done delete ');
  input.dispatchEvent(new w.Event('input', { bubbles: true }));
  await tick();
  assert.deepEqual(choices(), ['/backlog done delete 2']);
  type(w, get, '/backlog done delete 2');
  submit(w, get);
  assert.equal(d.activeElement.id, 'assistant-keep');
  get('assistant-keep').click();
  assert.equal(calls.length, 1);
  submit(w, get);
  get('assistant-erase').click();
  await tick();
  assert.deepEqual(calls.map(call => call.command), ['/backlog', '/backlog done delete 2']);
  assert.match(get('messages').textContent, /Deleted #2/);
});

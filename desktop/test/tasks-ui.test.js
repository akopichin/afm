import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeAssistant, fakeNative, fakeWatch, loadShell, primaryMods, notice, projectRow, runRow, snapshotOf, tick, watchTask } from './shell-support.js';

const ra = runRow('ra');
const queue = [notice(ra.key, 'ask', 1, 'question')];
const snapshot = snapshotOf([projectRow('a', [ra])], queue);
const frames = w => new Promise(resolve => w.requestAnimationFrame(() => resolve()));

const result = (id, extra = {}) => ({ id, key: id, kind: 'question', at: '2026-06-01T08:30:00.000Z', runKey: ra.key, runId: 'ra', instanceId: 'i-ra',
  runName: 'flow-ra', projectId: 'a', stageId: 'ask', stageName: 'ASK', ...extra });
const report = (id, markdown, extra = {}) => ({ id, at: '2026-06-01T09:00:00.000Z', title: 'Morning', markdown, source: 'model', ...extra });

async function openShell(t, tasks) {
  const watch = fakeWatch(tasks);
  const native = fakeNative(snapshot);
  native.watch = watch;
  native.assistant = fakeAssistant();
  const shell = await loadShell(t, { demo: false, native });
  await tick();
  shell.get('finish-setup')?.click();
  await tick();
  shell.key('KeyA', 'a', { altKey: true });
  await tick();
  return { ...shell, watch, assistant: native.assistant, native };
}
const openTasks = async ({ d, get }) => { d.querySelector('[data-assistant-view="tasks"]').click(); await tick(); return get('tasks'); };

test('Chat and Tasks switch in place, the Tasks tab counts unread results, and opening a result clears the count', async t => {
  const shell = await openShell(t, [watchTask('t1', { results: [result('r1'), result('r2', { at: '2026-06-01T08:40:00.000Z', kind: 'failed', stageId: null })], unread: 2 })]);
  const { d, get, watch } = shell;
  assert.equal(get('tasks-unread').textContent, '2');
  assert.equal(get('tasks-unread').getAttribute('aria-label'), '2 unread results');
  assert.equal(get('messages').hidden, false);
  assert.equal(get('tasks').hidden, true);
  const tasks = await openTasks(shell);
  assert.deepEqual([get('messages').hidden, tasks.hidden, d.querySelector('.chat-foot').hidden], [true, false, true]);
  assert.equal(d.querySelector('[data-assistant-view="tasks"]').getAttribute('aria-pressed'), 'true');
  assert.match(tasks.textContent, /Overnight watch[\s\S]*Watching[\s\S]*All projects/);
  assert.equal(tasks.querySelectorAll('.watch-event .dot').length, 2, 'both results are marked unread');
  tasks.querySelector('[data-watch-open="result"]').click();
  await tick();
  assert.deepEqual(watch.calls.markRead, ['t1']);
  assert.equal(get('tasks-unread').hidden, true, 'the count is gone once the result was opened');
  d.querySelector('[data-assistant-view="chat"]').click();
  assert.deepEqual([get('messages').hidden, get('tasks').hidden, d.querySelector('.chat-foot').hidden], [false, true, false]);
});

test('a result opens the run at its waiting stage; a run that left the inventory says so', async t => {
  const shell = await openShell(t, [watchTask('t1', { results: [result('r1'), result('gone', { runKey: 'i-x:x' })], unread: 0 })]);
  const tasks = await openTasks(shell);
  const row = id => [...tasks.querySelectorAll('[data-watch-open="result"]')].find(item => item.dataset.item === id);
  row('gone').click();
  await tick();
  assert.match(tasks.textContent, /no longer in the inventory/);
  row('r1').click();
  await tick(30);
  assert.match(shell.w.location.hash, /i-ra/);
});

test('pause, resume and stop call the main process and repaint from what it reports', async t => {
  const shell = await openShell(t, [watchTask('t1')]);
  const tasks = await openTasks(shell);
  const click = async action => { tasks.querySelector(`[data-watch-action="${action}"]`).click(); await tick(); };
  await click('pause');
  assert.deepEqual(shell.watch.calls.pause, ['t1']);
  assert.match(tasks.textContent, /Paused/);
  assert.equal(tasks.querySelector('[data-watch-action="pause"]'), null);
  assert.equal(shell.d.activeElement.dataset.watchAction, 'resume', 'the keyboard stays on the control that changed');
  await click('resume');
  assert.deepEqual(shell.watch.calls.resume, ['t1']);
  assert.match(tasks.textContent, /Watching/);
  await click('stop');
  assert.deepEqual(shell.watch.calls.stop, ['t1']);
  assert.match(tasks.textContent, /Stopped/);
  assert.equal(tasks.querySelector('[data-watch-action]'), null, 'a stopped task offers no controls');
  shell.watch.tasks[0].status = 'active';
  shell.watch.stop = async () => { throw Error("Error invoking remote method 'watch:stop': Error: That watch task no longer exists."); };
  shell.watch.emit();
  await tick();
  tasks.querySelector('[data-watch-action="stop"]').click();
  await tick();
  assert.match(tasks.textContent, /^That watch task no longer exists\./, 'a refusal is shown without the Electron prefix');
});

test('a task changed by someone else, or by a finished summary, shows up without a click', async t => {
  const shell = await openShell(t, []);
  const tasks = await openTasks(shell);
  assert.match(tasks.textContent, /No watches yet/);
  shell.watch.tasks.push(watchTask('t2', { title: 'Nightly', results: [result('r1')], unread: 1 }));
  shell.watch.emit();
  await tick();
  assert.match(tasks.textContent, /Nightly/);
  assert.equal(shell.get('tasks-unread').textContent, '1');
});

test('the chat shows a card for a new watch, which opens Tasks and later the summary dialog, and focus comes back', async t => {
  const shell = await openShell(t, [watchTask('t1')]);
  const { d, get, w, assistant } = shell;
  assistant.emit({ type: 'start', turnId: 'x', message: { id: 'ux', role: 'user', text: 'watch', context: 'All projects' },
    reply: { id: 'rx', role: 'assistant', text: '', status: 'streaming', tools: [], context: 'All projects', effective: { label: 'Ask' } } });
  assistant.emit({ type: 'watch_card', turnId: 'x', card: { taskId: 't1', title: 'Overnight watch', scope: 'All projects', events: ['question'], deadline: '2026-06-02T09:00:00.000Z', summaryAt: null } });
  assistant.emit({ type: 'done', turnId: 'x', reply: { id: 'rx', role: 'assistant', text: 'Done', status: 'done', tools: [], context: 'All projects' } });
  await frames(w);
  const card = get('messages').querySelector('.watch-card.compact');
  assert.ok(card, 'the card survives the end of the reply');
  assert.match(card.textContent, /Overnight watch[\s\S]*All projects/);
  assert.equal(card.querySelector('[data-watch-card="summary"]'), null, 'no summary yet');
  shell.watch.tasks[0].reports = [report('p1', '# Morning\n\nAll quiet.')];
  shell.watch.tasks[0].unread = 1;
  shell.watch.emit();
  await frames(w);
  const opener = get('messages').querySelector('[data-watch-card="summary"]');
  opener.focus();
  opener.click();
  assert.equal(get('watch-report').open, true);
  assert.equal(get('watch-report-title').textContent, 'Morning');
  assert.match(get('watch-report-scope').textContent, /All projects/);
  assert.match(get('watch-report-body').innerHTML, /<h2>Morning<\/h2>/);
  assert.equal(d.activeElement, get('watch-report-title'), 'the dialog takes focus on its title');
  assert.deepEqual(shell.watch.calls.markRead, ['t1']);
  await frames(w);
  get('watch-report').close();
  await frames(w);
  assert.equal(d.activeElement.dataset.watchCard, 'summary', 'focus returns to the button that opened it');
});

test('Command/Control H, J and K close the summary and then run their normal navigation', async t => {
  const shell = await openShell(t, [watchTask('t1', { reports: [report('p1', 'Text')], unread: 1 })]);
  const { d, get, w, key } = shell;
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0]);
  await tick();
  await openTasks(shell);
  const open = () => { d.querySelector('[data-watch-open="report"]').click(); assert.equal(get('watch-report').open, true); };
  const press = code => {
    const event = new w.KeyboardEvent('keydown', { code, key: code, ...primaryMods(w.navigator.platform), bubbles: true, cancelable: true });
    get('watch-report-title').dispatchEvent(event);
    return event;
  };
  open();
  get('watch-report-title').dispatchEvent(new w.KeyboardEvent('keydown', { code: 'KeyH', key: 'h', altKey: true, bubbles: true, cancelable: true }));
  assert.equal(get('watch-report').open, true, 'the old Option/Alt+H no longer navigates');
  const homed = press('KeyH');
  assert.equal(homed.defaultPrevented, true);
  assert.equal(get('watch-report').open, false);
  assert.equal(w.location.hash, '', 'Primary+H went home');
  assert.notEqual(d.activeElement, get('watch-report-title'));
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0]);
  await tick();
  open();
  press('KeyJ');
  await tick(30);
  assert.equal(get('watch-report').open, false);
  assert.match(w.location.hash, /i-ra/, 'Primary+J went to the next waiting request');
  // From the native Live view the shortcut is a command, not a key event; the summary gives way the same way.
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0]);
  await tick();
  open();
  w.navigateApp('home');
  assert.equal(get('watch-report').open, false);
  assert.equal(w.location.hash, '');
});

test('report text is data: markup and scripts stay inert, run links navigate, other links are only text', async t => {
  const hostile = [
    '# <img src=x onerror="window.pwned=1"> Title',
    '<script>window.pwned = 2</script>',
    '**bold** and `<b>code</b>` and [site](https://example.com/x?a=1&b=2) and [bad](javascript:alert(1))',
    '- item <i onclick="window.pwned=3">one</i>',
    '```mermaid', 'graph TD; A-->B', '```',
    '<details>', '<summary>More <b>info</b></summary>', 'hidden text', '</details>',
    '[Open flow-ra](afm://run/i-ra/ra/ask)',
  ].join('\n');
  const shell = await openShell(t, [watchTask('t1', { reports: [report('p1', hostile)], unread: 1 })]);
  const { w, d, get } = shell;
  await openTasks(shell);
  d.querySelector('[data-watch-open="report"]').click();
  const body = get('watch-report-body');
  assert.equal(w.pwned, undefined);
  assert.equal(body.querySelector('script, img, i, b, a'), null, 'no element came from the report text');
  assert.equal(body.querySelector('[onclick], [onerror]'), null);
  assert.match(body.textContent, /<script>window\.pwned = 2<\/script>/, 'the markup is shown as text');
  assert.match(body.textContent, /site \(https:\/\/example\.com\/x\?a=1&b=2\)/, 'an outside link is not clickable');
  assert.match(body.textContent, /bad \(javascript:alert\(1\)\)/);
  assert.equal(body.querySelector('strong').textContent, 'bold');
  assert.equal(body.querySelector('li').textContent, 'item <i onclick="window.pwned=3">one</i>');
  assert.match(body.querySelector('pre').textContent, /graph TD; A-->B/);
  assert.match(body.querySelector('.diagram-note').textContent, /Diagram/);
  assert.equal(body.querySelectorAll('details').length, 1);
  assert.equal(body.querySelector('summary').textContent, 'More <b>info</b>');
  const link = body.querySelector('[data-report-run]');
  assert.deepEqual([link.dataset.reportInstance, link.dataset.reportRun, link.dataset.reportStage], ['i-ra', 'ra', 'ask']);
  link.click();
  await tick(30);
  assert.equal(get('watch-report').open, false, 'following a run closes the summary');
  assert.match(w.location.hash, /i-ra/);
});

test('a summary body scrolls on its own and an unclosed <details> cannot swallow the dialog', async t => {
  const shell = await openShell(t, [watchTask('t1', { reports: [report('p1', '<details>\n<summary>Open</summary>\ntext\n' + 'line\n'.repeat(300))], unread: 0 })]);
  await openTasks(shell);
  shell.d.querySelector('[data-watch-open="report"]').click();
  const body = shell.get('watch-report-body');
  assert.equal(body.querySelectorAll('details').length, 1);
  assert.equal(shell.get('watch-report').querySelector('header').parentElement, shell.get('watch-report'), 'header is outside the scrolling body');
  assert.equal(body.contains(shell.get('watch-report-title')), false);
});

test('the demo has no tabs and no watch calls', async t => {
  const { get } = await loadShell(t, { demo: true });
  assert.equal(get('assistant-tabs').hidden, true);
});

test('focus returns to the summary button after the read-triggered repaint, with the shell restore also running', async t => {
  const shell = await openShell(t, [watchTask('t1', { reports: [report('p1', 'x')], unread: 1 })]);
  await openTasks(shell);
  const button = shell.d.querySelector('[data-watch-open="report"]');
  button.focus();
  button.click();
  await tick();
  shell.get('watch-report').close();
  assert.equal(shell.d.activeElement.dataset.watchOpen, 'report');
});

test('an old summary stays reachable behind many newer results', async t => {
  const results = Array.from({ length: 30 }, (_, i) => result('r' + i, { at: `2026-06-02T08:${String(10 + i).padStart(2, '0')}:00.000Z` }));
  const shell = await openShell(t, [watchTask('t1', { results, reports: [report('p1', 'x')] })]);
  const tasks = await openTasks(shell);
  assert.equal(tasks.querySelectorAll('[data-watch-open="report"]').length, 1);
});

test('a clicked watch banner opens the Tasks tab at its summary, or at its result, even from a closed assistant', async t => {
  const shell = await openShell(t, [watchTask('t1', { reports: [report('p1', 'Body')], results: [result('r1')], unread: 2 })]);
  shell.key('KeyA', 'a', { altKey: true });
  await tick();
  assert.equal(shell.get('assistant').hidden, true);
  shell.native.openWatch({ taskId: 't1', itemId: 'p1' });
  await tick();
  assert.equal(shell.get('assistant').hidden, false);
  assert.equal(shell.get('tasks').hidden, false);
  assert.equal(shell.get('watch-report').open, true);
  assert.match(shell.get('watch-report-body').textContent, /Body/);
  shell.native.openWatch({ taskId: 't1', itemId: 'r1' });
  await tick(30);
  assert.equal(shell.get('watch-report').open, false, 'a result opens its run, closing any dialog');
  assert.match(shell.w.location.hash, /i-ra/);
  shell.native.openWatch({ taskId: 'unknown', itemId: 'x' });
  await tick();
  assert.equal(shell.get('tasks').hidden, false);
});

test('a banner click before the task list arrives opens the summary once it does, and the newest click wins', async t => {
  const shell = await openShell(t, []);
  shell.native.openWatch({ taskId: 't1', itemId: 'p1' });
  await tick();
  assert.equal(shell.get('watch-report').open, false);
  shell.watch.tasks.push(watchTask('t1', { reports: [report('p1', 'Late body')], unread: 1 }));
  shell.watch.emit();
  await tick();
  assert.equal(shell.get('watch-report').open, true);
  assert.match(shell.get('watch-report-body').textContent, /Late body/);
});

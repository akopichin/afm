import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { taskView, WatchService } from '../src/watch/index.js';
import { fallbackReport, modelReport, SummaryRunner } from '../src/watch/summary.js';
import { fakeAssistant, fakeNative, fakeWatch, loadShell, primaryMods, notice, projectRow, runRow, snapshotOf, tick, watchTask } from './shell-support.js';

const ra = runRow('ra');
const queue = [notice(ra.key, 'ask', 1, 'question')];
const snapshot = snapshotOf([projectRow('a', [ra])], queue);
const frames = w => new Promise(resolve => w.requestAnimationFrame(() => resolve()));

const result = (id, extra = {}) => ({ id, key: id, kind: 'question', at: '2026-06-01T08:30:00.000Z', runKey: ra.key, runId: 'ra', instanceId: 'i-ra',
  runName: 'flow-ra', projectId: 'a', stageId: 'ask', stageName: 'ASK', ...extra });
const report = (id, markdown, extra = {}) => ({ id, at: '2026-06-01T09:00:00.000Z', title: 'Morning', markdown, source: 'model', ...extra });
// A report as Desktop writes it: the markdown together with where its linked events start.
const written = (id, { markdown, eventsOffset }) => report(id, markdown, { eventsOffset });

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

test('a report link opens the exact instance; another instance with the same run id and a gone run are not opened', async t => {
  const links = [
    '[Other instance](afm://run/i-other/ra/ask)',
    '[Waiting stage](afm://run/i-ra/ra/ask)',
  ].join('\n\n');
  const shell = await openShell(t, [watchTask('t1', { reports: [report('p1', links)], unread: 0 })]);
  const open = async label => {
    await openTasks(shell);
    shell.d.querySelector('[data-watch-open="report"]').click();
    [...shell.get('watch-report-body').querySelectorAll('[data-report-run]')].find(link => link.textContent.startsWith(label)).click();
    await tick(30);
  };
  await open('Other instance');
  assert.doesNotMatch(shell.w.location.hash, /i-/, 'the run id alone does not match another instance');
  assert.match(shell.get('tasks').textContent, /no longer in the inventory/);
  await open('Waiting stage');
  assert.match(shell.w.location.hash, /i-ra/);
});

test('a report link to a stage that no longer waits opens the run and says so in Tasks', async t => {
  const shell = await openShell(t, [watchTask('t1', { reports: [report('p1', '[Old stage](afm://run/i-ra/ra/build)')], unread: 0 })]);
  await openTasks(shell);
  shell.d.querySelector('[data-watch-open="report"]').click();
  shell.get('watch-report-body').querySelector('[data-report-run]').click();
  await tick(30);
  assert.match(shell.w.location.hash, /i-ra/, 'the run is shown');
  await openTasks(shell);
  assert.match(shell.get('tasks').textContent, /no longer waiting/);
});

test('Desktop-written reports render their events as links, also after an unfinished model block, and hostile names add none', async t => {
  const own = result('r1');
  const hostile = result('r2', { stageId: 'invalid stage', runName: '[Open another run](afm://run/i-other/other/ask)' });
  const task = { title: 'Morning', results: [own, hostile] };
  const unfinished = modelReport(task, '## Status\n\n```text\nA question is waiting.\n<details>\n<summary>More</summary>');
  const shell = await openShell(t, [watchTask('t1', { reports: [written('p1', fallbackReport(task)), written('p2', unfinished)], unread: 0 })]);
  await openTasks(shell);
  const buttons = id => {
    shell.d.querySelector(`[data-watch-open="report"][data-item="${id}"]`).click();
    const links = [...shell.get('watch-report-body').querySelectorAll('[data-report-run]')].map(link => [link.dataset.reportInstance, link.dataset.reportRun, link.dataset.reportStage]);
    shell.get('watch-report').close();
    return links;
  };
  assert.deepEqual(buttons('p1'), [['i-ra', 'ra', 'ask']], 'only the event with valid ids is a link');
  assert.deepEqual(buttons('p2'), [['i-ra', 'ra', 'ask']], 'the events section is not swallowed by the model\'s open fence or details');
});

// Model texts that leave a block open, close one or only look like one, in each line ending the renderer reads.
const MODEL_TEXTS = {
  'LF open fence': 'Intro\n```text\nWaiting',
  'CRLF open fence': 'Intro\r\n```text\r\nWaiting',
  'CR open fence': 'Intro\r```text\rWaiting',
  'LF closed fence': '```text\nWaiting\n```',
  'CRLF closed fence': '```text\r\nWaiting\r\n```',
  'CR closed fence': '```text\rWaiting\r```',
  'mixed endings': 'Intro\r```text\nWaiting\r```\r\nDone',
  'CR details': 'Intro\r<details>\r<summary>More</summary>\rWaiting',
  'nested details': '<details>\n<details>\nWaiting',
  'closing details before an opener': '</details>\n<details>\nWaiting',
  'fence in details': '<details>\n<summary>More</summary>\n```text\nWaiting',
  'details in fence': '```\n<details>\n<summary>More</summary>',
  'tilde fence': '~~~text\n<details>\nWaiting',
  'indented fence': '    ```text\nWaiting',
  'tab fence': '\t```text\nWaiting',
  'four backticks': '````text\nWaiting',
  'closing fence with text': '```text\nWaiting\n``` trailing',
  'inline details': '<details><summary>More</summary>Waiting',
  'details closed inline': '<details>\nWaiting</details>',
  'escaped details': '&lt;details&gt;\n&lt;summary&gt;More&lt;/summary&gt;',
  'unfinished code and link': '`unterminated [unfinished](',
  'unfinished list and heading': '- unfinished\n## unfinished',
  'trailing CR': 'Waiting\r',
  'astral prefix': '😀 Intro\r```text\rWaiting 😀',
  'empty': '',
};
// One event with valid ids and one per id the link alphabet rejects, all with names that try to add markup or a link.
const HOSTILE_NAMES = { runName: '[Open another run](afm://run/i-other/other/ask) <img src=x onerror="window.pwned=1">', stageName: '`x` **y** </details>' };
const hostileTask = { title: 'Morning', results: [result('r1', HOSTILE_NAMES),
  ...['instanceId', 'runId', 'stageId'].map(field => result('bad-' + field, { ...HOSTILE_NAMES, [field]: 'bad id' }))] };

test('Desktop events stay visible and clickable after any model text, outside its code and <details>, and names stay inert', async t => {
  const cases = [...Object.entries(MODEL_TEXTS).map(([name, text]) => [name, modelReport(hostileTask, text)]),
    ['fallback with a title that opens a fence', fallbackReport({ ...hostileTask, title: 'Night\n```' })]];
  const shell = await openShell(t, [watchTask('t1', { reports: cases.map(([, parts], i) => written('c' + i, parts)), unread: 0 })]);
  await openTasks(shell);
  for (const [i, [name, parts]] of cases.entries()) {
    await t.test(name, () => {
      shell.d.querySelector(`[data-watch-open="report"][data-item="c${i}"]`).click();
      const body = shell.get('watch-report-body');
      const links = [...body.querySelectorAll('[data-report-run]')];
      assert.deepEqual(links.map(link => [link.dataset.reportInstance, link.dataset.reportRun, link.dataset.reportStage]), [['i-ra', 'ra', 'ask']]);
      assert.equal(links[0].closest('pre, code, details, summary'), null, 'the link is not inside a model block');
      const heading = [...body.querySelectorAll('h3')].find(item => item.textContent === 'Events');
      assert.equal(heading?.closest('pre, code, details'), null, 'the Events heading is visible');
      assert.equal(body.querySelector('img, script, a'), null);
      assert.equal(body.children.length, 2, 'text and events are rendered apart');
      assert.equal(body.children[1].querySelector('pre, details'), null, 'no model block reaches the events');
      if (parts.markdown.slice(0, parts.eventsOffset).includes('Waiting')) assert.match(body.children[0].textContent, /Waiting/, 'the model text is still shown');
    }).finally(() => shell.get('watch-report').close());
  }
  assert.equal(shell.w.pwned, undefined);
});

test('a report stored without a valid events boundary renders as one body with its links, as before', async t => {
  const markdown = '# Old\n\n[Open flow-ra](afm://run/i-ra/ra/ask)';
  const invalid = [undefined, null, -1, 0.5, '3', markdown.length + 1];
  const reports = [...invalid.map((eventsOffset, i) => report('o' + i, markdown, eventsOffset === undefined ? {} : { eventsOffset })),
    report('edge-start', markdown, { eventsOffset: 0 }), report('edge-end', markdown, { eventsOffset: markdown.length })];
  const shell = await openShell(t, [watchTask('t1', { reports, unread: 0 })]);
  await openTasks(shell);
  for (const { id, eventsOffset } of reports) {
    shell.d.querySelector(`[data-watch-open="report"][data-item="${id}"]`).click();
    const body = shell.get('watch-report-body');
    assert.equal(body.children.length, id.startsWith('edge') ? 2 : 1, `offset ${eventsOffset}`);
    assert.equal(body.querySelector('h2').textContent, 'Old');
    assert.equal(body.querySelectorAll('[data-report-run]').length, 1, `offset ${eventsOffset}`);
    shell.get('watch-report').close();
  }
});

test('a model report written, saved and loaded again opens with its events clickable outside the model\'s open blocks', async t => {
  const home = await mkdtemp(path.join(tmpdir(), 'afm-report-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const reply = '😀 Intro\r<details>\r<summary>More</summary>\r```text\rWaiting';
  const writer = new WatchService({ home });
  await writer.load();
  const created = writer.create({ title: 'Morning' });
  Object.assign(writer.store.get(created.id), { summaryDue: true, results: [result('r1')] });
  await new SummaryRunner({ watch: writer, ask: async () => reply }).run(writer.store.get(created.id));
  await writer.flush();
  const reader = new WatchService({ home });
  await reader.load();
  const shell = await openShell(t, [taskView(reader.store.get(created.id))]);
  await openTasks(shell);
  shell.d.querySelector('[data-watch-open="report"]').click();
  const body = shell.get('watch-report-body');
  assert.ok(body.querySelector('details pre code'), 'the model text keeps its own blocks');
  const links = [...body.querySelectorAll('[data-report-run]')];
  assert.equal(links.length, 1);
  assert.equal(links[0].closest('pre, code, details, summary'), null);
  links[0].click();
  await tick(30);
  assert.equal(shell.get('watch-report').open, false);
  assert.match(shell.w.location.hash, /i-ra/);
});

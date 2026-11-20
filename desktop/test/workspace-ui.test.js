import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeNative, loadShell, notice, projectRow, runRow, snapshotOf, tick } from './shell-support.js';

const ra = runRow('ra'), rb = runRow('rb');
const twoProjects = (attention, runs = [ra, rb]) => snapshotOf([projectRow('a', [runs[0]]), projectRow('b', [runs[1]])], attention);
const queue = [notice(ra.key, 's1', 1), notice(ra.key, 's2', 2, 'question'), notice(rb.key, 's1', 3)];

async function openShell(t, snapshot, extra = {}) {
  const native = fakeNative(snapshot);
  Object.assign(native, extra);
  const shell = await loadShell(t, { demo: false, native });
  shell.get('finish-setup')?.click();
  await tick();
  return { ...shell, native };
}

test('the macOS title follows the project and the native controls overlay', async t => {
  const {get,w} = await loadShell(t, {demo: false, native: fakeNative(twoProjects([])), windowControls: true});
  await tick();
  assert.equal(get('window-titlebar').hidden, false);
  assert.equal(get('window-title').textContent, 'Projects · AFM Desktop');
  w.document.querySelector('[data-project="a"]').click();
  await tick();
  assert.equal(get('window-title').textContent, 'a · AFM Desktop');
  const overlay = w.navigator.windowControlsOverlay;
  overlay.visible = false;
  overlay.dispatchEvent(new w.Event('geometrychange'));
  assert.equal(get('window-titlebar').hidden, true);
});

test('SSH key prompt masks the passphrase and clears the field on submit or cancellation', async t => {
  const replies = [];
  let openKey;
  const {w,get} = await openShell(t,twoProjects([]),{
    onSSHKeyPrompt:callback=>{openKey=callback},
    answerSSHKey:async (...reply)=>{replies.push(reply)},
  });
  const field = get('ssh-key-answer');
  openKey({id:'first', key:'id_ed25519'});
  assert.equal(field.type,'password');
  assert.equal(w.document.activeElement,field);
  field.value='test-only answer';
  get('ssh-key-form').dispatchEvent(new w.Event('submit',{cancelable:true}));
  assert.equal(field.value,'');
  assert.deepEqual(replies,[['first','test-only answer']]);
  openKey({id:'cancelled', key:'id_ed25519'});
  field.value='unsent answer';
  get('ssh-key-dialog').close();
  assert.equal(field.value,'');
  assert.deepEqual(replies.at(-1),['cancelled',null]);
});

test('a project can be removed from Home without opening it', async t => {
  const calls=[];
  const {w,d,native}=await openShell(t,twoProjects([]),{removeProject:async id=>{
    calls.push(id);
    native.publish(snapshotOf([projectRow('b',[rb])]));
  }});
  const remove=d.querySelector('[data-forget="a"]');
  assert.ok(remove.getAttribute('aria-label').includes('recent projects'));
  remove.click();
  await tick();
  assert.deepEqual(calls,['a']);
  assert.equal(d.querySelector('[data-forget="a"]'),null);
  assert.equal(w.location.hash,'');
});

test('Continue run waits and selects the replacement instance of the same run', async t => {
  const stopped = runRow('resume', {state:'finished', can:{resume:true, open:false}});
  const restarted = runRow('resume', {key:'new:resume', instanceId:'new'});
  let finish;
  const response = new Promise(resolve => {finish = resolve});
  const {w,d,native} = await openShell(t, snapshotOf([projectRow('a',[stopped])]), {resume:async (...args)=>{
    assert.deepEqual(args, ['resume','i-resume']);
    return response;
  }});
  d.querySelector('[data-project="a"]').click();
  await tick();
  d.querySelector('[data-resume]').click();
  assert.equal(d.querySelector('[data-resume]').disabled,true);
  assert.equal(d.querySelector('[data-resume]').textContent,'Resuming…');
  native.snapshot = snapshotOf([projectRow('a',[restarted])]);
  finish({run_id:'resume',instance_id:'new',state:'ready'});
  await tick();
  assert.equal(decodeURIComponent(w.location.hash),'#a/new:resume/run');
  assert.equal(d.querySelector('[data-resume]'),null);
});

test('Delete run stays on a cancelled confirmation and moves to the remaining tab after deletion', async t => {
  const stopped = runRow('stopped',{state:'finished',can:{delete:true,open:false}});
  let cancelled = true;
  const snapshot = snapshotOf([projectRow('a',[ra,stopped])]);
  const {w,d,native} = await openShell(t,snapshot,{deleteRun:async (...args)=>{
    assert.deepEqual(args,['stopped','i-stopped']);
    if (cancelled) return {deleted:false};
    native.snapshot = snapshotOf([projectRow('a',[ra])]);
    return {deleted:true};
  }});
  d.querySelector('[data-project="a"]').click();
  await tick();
  d.querySelector('[data-delete-run]').click();
  await tick();
  assert.equal(decodeURIComponent(w.location.hash),'#a/i-stopped:stopped/run');
  cancelled = false;
  d.querySelector('[data-delete-run]').click();
  await tick();
  assert.equal(decodeURIComponent(w.location.hash),'#a/i-ra:ra/run');
  assert.equal(d.querySelector('[data-delete-run]'),null);
});

test('notices lead to the exact project, run and stage, in a stable queue, whichever project is open', async t => {
  const { w, d, get, key, native } = await openShell(t, twoProjects(queue));
  assert.equal(get('notice-count').textContent, '3');
  assert.equal(get('attention').hidden, false);
  assert.match(get('notice-list').textContent, /S1/);
  const visit = async code => {
    key(code, code.slice(3).toLowerCase(), { metaKey: true });
    await tick();
    const frame = d.querySelector('#dashboard-host [data-run-key]:not([hidden])');
    // The page announces itself; only then is the stage request delivered.
    native.emitDashboard(frame.dataset.runKey, 'stage-ready');
    await tick();
    return { hash: decodeURIComponent(w.location.hash), delivered: native.dashboardMessages.get(frame.dataset.runKey).filter(m => m.type === 'open-stage').at(-1)?.stage };
  };
  let at = await visit('KeyJ');
  assert.deepEqual([at.hash, at.delivered], ['#a/i-ra:ra/run', 's1']);
  at = await visit('KeyJ');
  assert.deepEqual([at.hash, at.delivered], ['#a/i-ra:ra/run', 's2'], 'a second stage of the same run is a separate stop');
  at = await visit('KeyJ');
  assert.deepEqual([at.hash, at.delivered], ['#b/i-rb:rb/run', 's1']);
  at = await visit('KeyJ');
  assert.deepEqual([at.hash, at.delivered], ['#a/i-ra:ra/run', 's1'], 'the queue wraps');
  at = await visit('KeyK');
  assert.deepEqual([at.hash, at.delivered], ['#b/i-rb:rb/run', 's1']);
  assert.equal(native.calls.dashboard, 2, 'each page was created once and reused');
  assert.equal(get('notice-count').textContent, '3', 'visiting a notice leaves the work unresolved');
});

test('a reply or an outside change updates the queue without duplicates or lost position', async t => {
  const { d, get, key, native } = await openShell(t, twoProjects(queue));
  key('KeyJ', 'j', { metaKey: true }); await tick();
  key('KeyJ', 'j', { metaKey: true }); await tick();
  const list = () => [...d.querySelectorAll('#notice-list [data-notice]')].map(el => el.dataset.notice);
  native.publish(twoProjects([queue[0], queue[2]]));
  assert.deepEqual(list(), [queue[0].id, queue[2].id]);
  assert.equal(get('notice-count').textContent, '2');
  native.publish(twoProjects([queue[0], queue[2]]));
  assert.equal(new Set(list()).size, list().length, 'a repeated snapshot duplicated a notice');
  key('KeyJ', 'j', { metaKey: true }); await tick();
  assert.match(decodeURIComponent(d.defaultView.location.hash), /^#b\//, 'the next stop follows the one that was answered');
  native.publish(twoProjects([]));
  assert.equal(get('attention').hidden, true);
});

test('identical snapshots cause no rendering, persistence or page churn', async t => {
  const { w, d, get, native } = await openShell(t, twoProjects(queue));
  let mutations = 0, changes = 0;
  new w.MutationObserver(records => { mutations += records.length; }).observe(d.body, { subtree: true, childList: true, attributes: true, characterData: true });
  d.addEventListener('workspace-change', () => changes++);
  const writes = [];
  const original = w.Storage.prototype.setItem;
  w.Storage.prototype.setItem = function (...args) { writes.push(args[0]); return original.apply(this, args); };
  for (let i = 0; i < 50; i++) native.publish(JSON.parse(JSON.stringify(twoProjects(queue))));
  await tick(20);
  assert.deepEqual([mutations, changes, writes.length], [0, 0, 0]);
  assert.equal(get('notice-count').textContent, '3');
});

test('dashboard pages are created lazily, bounded across 100 project switches and released with their runs', async t => {
  const rows = Array.from({ length: 20 }, (_, i) => runRow('r' + i));
  const snapshot = snapshotOf(rows.map((run, i) => projectRow('p' + i, [run])));
  const { w, d, native } = await openShell(t, snapshot);
  assert.equal(d.querySelectorAll('#dashboard-host [data-run-key]').length, 0, 'nothing is opened before the user visits a run');
  assert.equal(native.calls.dashboard, 0);
  for (let i = 0; i < 100; i++) {
    w.shell.openProject(w.shell.projects[(i * 7) % 20]);
    await tick(1);
  }
  await tick(20);
  const frames = [...d.querySelectorAll('#dashboard-host [data-run-key]')];
  assert.ok(frames.length <= 8, `${frames.length} pages stayed open`);
  assert.equal(frames.filter(frame => !frame.hidden).length, 1, 'exactly one page is shown');
  assert.equal(native.calls.dashboard, native.calls.settled, 'a dashboard request is still outstanding');
  assert.equal(native.listeners.size, 1, 'the page subscribed more than once');
  native.publish(snapshotOf(rows.slice(0, 3).map((run, i) => projectRow('p' + i, [run]))));
  await tick(10);
  assert.ok(d.querySelectorAll('#dashboard-host [data-run-key]').length <= 3, 'pages of vanished runs stayed open');
  native.publish(snapshotOf([]));
  await tick(10);
  assert.equal(d.querySelectorAll('#dashboard-host [data-run-key]').length, 0);
});

test('a page waiting for a person is the last one a full pool closes', async t => {
  const rows = Array.from({ length: 12 }, (_, i) => runRow('r' + i));
  const waiting = notice(rows[0].key, 'ask', 1, 'question');
  const { w, d } = await openShell(t, snapshotOf(rows.map((run, i) => projectRow('p' + i, [run])), [waiting]));
  for (let i = 0; i < 12; i++) { w.shell.openProject(w.shell.projects[i]); await tick(2); }
  const titles = [...d.querySelectorAll('#dashboard-host [data-run-key]')].map(frame => frame.title);
  assert.ok(titles.includes('flow-r0 dashboard'), 'the run that needs a reply was closed first');
  assert.ok(titles.length <= 8);
});

test('closing a modal returns focus to the dashboard page it came from', async t => {
  const { w, d, get, native } = await openShell(t, twoProjects([]));
  w.shell.openProject(w.shell.projects[0]);
  await tick();
  const frame = d.querySelector('#dashboard-host [data-run-key]:not([hidden])');
  assert.equal(d.activeElement, frame, 'opening a run focuses its page');
  native.emitDashboard(frame.dataset.runKey, 'switch-project');
  assert.ok(get('picker').open);
  get('picker').close();
  assert.equal(d.activeElement, frame);
  get('settings-toggle').focus();
  get('settings-toggle').click();
  get('settings').close();
  assert.equal(d.activeElement, get('settings-toggle'), 'a shell control gets its own focus back');
  assert.equal(native.calls.dashboard, 1);
});

test('run states are shown as they are and offer only what the host allows', async t => {
  const finished = runRow('fin', { state: 'finished', can: { open: false, cancel: false } });
  const offline = runRow('off', { state: 'offline', can: { open: false, cancel: false } });
  const attached = runRow('att', { origin: 'external', ownership: 'attached', can: { open: true, cancel: false } });
  const { w, d, native } = await openShell(t, snapshotOf([projectRow('a', [finished, offline, attached])]));
  const show = async run => { w.location.hash = `#a/${encodeURIComponent(run.key)}`; await tick(15); return d.getElementById('content').textContent; };
  assert.match(await show(finished), /finished/);
  assert.match(await show(offline), /offline/);
  assert.equal(native.calls.dashboard, 0, 'an unavailable run never asks for a dashboard');
  await show(attached);
  assert.equal(d.querySelector('[data-stop]'), null, 'an attached run has no stop button');
  assert.equal(native.calls.dashboard, 1);
  native.publish(snapshotOf([projectRow('a', [{ ...attached, can: { open: true, cancel: true } }])]));
  await tick(10);
  assert.ok(d.querySelector('[data-stop]'));
  d.querySelector('[data-stop]').click();
  await tick();
  assert.deepEqual(native.calls.cancel, [[attached.runId, attached.instanceId]]);
  d.querySelector('a.shell-link').click();
  await tick();
  assert.deepEqual(native.opened, [[attached.runId, attached.instanceId]], 'the browser opens the same verified run');
});

test('an unreachable host is shown until it answers, and runs outside Home still appear', async t => {
  const outside = runRow('x', { projectRoot: '/elsewhere/tool' });
  const { d, get, native } = await openShell(t, snapshotOf([], [], { unassigned: [outside] }));
  assert.match(d.getElementById('screen').textContent, /tool/);
  assert.equal(get('native-error').hidden, true);
  native.publish(snapshotOf([], [], { unassigned: [outside], host: { state: 'offline', error: { code: 'host_unreachable', message: 'Cannot connect to afm serve' } } }));
  assert.equal(get('native-error').hidden, false);
  assert.match(get('native-error').textContent, /Cannot connect/);
  native.publish(snapshotOf([], [], { unassigned: [outside] }));
  assert.equal(get('native-error').hidden, true);
});

test('the launcher shows the effective Git mode before launch and repeats the same request id', async t => {
  const previews = [], starts = [];
  const blockedFlows = new Set();
  const { w, d, get, native } = await openShell(t, twoProjects([]), {
    projectDetails: async () => ({ git: { git: true }, flows: [{ name: 'ship', file: 'ship.yaml', policy: { enabled: true, source: 'flow' } }, { name: 'plain', file: 'plain.yaml', policy: null }], sources: [] }),
    prefs: async () => ({ gitMode: 'branch' }),
    preview: async request => {
      previews.push(request);
      const blocked = blockedFlows.has(request.file) || (request.explicit === 'current' && request.file === 'ship.yaml') ? 'This flow declares worktrees: true.' : null;
      return { git: { git: true, dirty: true }, effective: blocked ? { blocked } : request.explicit === 'worktree' || request.file === 'ship.yaml'
        ? { mode: 'worktree', source: request.explicit ? 'explicit' : 'flow', worktree: request.worktree ?? 'ship' } : { mode: 'branch', source: 'preference', branch: request.branch } };
    },
    start: async request => { starts.push(request); if (starts.length === 1) throw Object.assign(Error('host busy'), { code: 'limit_starting' }); return { launch_id: 'L' }; },
  });
  w.shell.openLauncher(w.shell.projects[0]);
  await tick(30);
  assert.match(get('workflows').textContent, /ship/);
  assert.match(get('git-hint').textContent, /Flow setting · New worktree · ship/);
  w.shell.chooseWorkflow(1);
  await tick(30);
  assert.match(get('git-hint').textContent, /Desktop default · New branch · desktop-plain-/);
  assert.match(get('git-hint').textContent, /uncommitted changes stay/);
  get('launch-git').value = 'current';
  get('launch-git').dispatchEvent(new w.Event('change'));
  w.shell.chooseWorkflow(0);
  await tick(30);
  assert.match(get('git-hint').textContent, /worktrees: true/);
  assert.equal(get('launch-submit').disabled, true, 'a blocked choice cannot be launched');
  get('launch-git').value = 'auto';
  get('launch-git').dispatchEvent(new w.Event('change'));
  await tick(30);
  get('launch-submit').click();
  await tick(30);
  assert.match(get('native-error').textContent, /host busy/);
  get('launch-submit').click();
  await tick(30);
  assert.equal(starts.length, 2);
  assert.equal(starts[0].requestId, starts[1].requestId, 'a retry of the same launch must reuse its request id');
  assert.equal(starts[1].projectId, 'a');
  assert.equal(get('launch').open, false);
});

test('a blocked pipeline launch opens options and saving leaves the launcher available', async t => {
  const entry = {name:'doc-flow',id:'doc-from-library'};
  const options = {id:entry.id,name:entry.name,file:'/example/options.yaml',text:'docker: {}',revision:'old',roles:[],issues:['Missing key']};
  let saved;
  const {w,get} = await openShell(t,twoProjects([]),{
    projectDetails: async () => ({git:{git:false},flows:[],sources:[{id:'library', location:'/library',entries:[entry]}]}),
    preview: async () => ({git:{git:false},effective:{mode:'current',source:'folder'}}),
    start: async () => ({status:'pipeline_options',options}),
    savePipelineOptions: async input => {saved=input;return {...options,text:input.text,issues:[]}},
  });
  let draft;
  w.AFMCodeEditor={createCodeEditor:(_surface,{text})=>{
    draft=text;
    return {getValue:()=>draft,setReadOnly(){},onChange(){},dispose(){}};
  }};
  w.shell.openLauncher(w.shell.projects[0]);
  await tick(30);
  assert.equal(get('launch-options').hidden,false);
  get('launch-submit').click();
  await tick();
  assert.ok(get('pipeline-options').open);
  assert.match(get('pipeline-options-status').textContent,/Missing key/);
  draft='collaboration: {}';
  get('pipeline-options-form').dispatchEvent(new w.Event('submit',{cancelable:true}));
  await tick();
  assert.equal(saved.text,'collaboration: {}');
  assert.match(get('pipeline-options-status').textContent,/Options saved/);
  assert.equal(get('pipeline-options-save').textContent,'Saved');
  assert.ok(get('launch').open);
});

test('Start run shows progress immediately and stays busy across a preview refresh', async t => {
  let failLaunch;
  const launch = new Promise((_, reject) => { failLaunch = reject; });
  const {w,get} = await openShell(t,twoProjects([]),{
    projectDetails: async () => ({git:{git:false},flows:[{name:'ship',file:'ship.yaml'}],sources:[]}),
    preview: async () => ({git:{git:false},effective:{mode:'current',source:'folder'}}),
    start: () => launch,
  });
  w.shell.openLauncher(w.shell.projects[0]);
  await tick(30);
  const button=get('launch-submit');
  button.click();
  assert.equal(button.textContent,'Starting…');
  assert.equal(button.getAttribute('aria-busy'),'true');
  assert.equal(button.disabled,true);

  get('workflow-search').dispatchEvent(new w.Event('input'));
  await tick();
  assert.equal(button.disabled,true);
  failLaunch(Error('The run could not start.'));
  await tick();
  assert.equal(button.textContent,'Start run');
  assert.equal(button.hasAttribute('aria-busy'),false);
  assert.equal(button.disabled,false);
  assert.match(get('launch-status').textContent,/could not start/);
});

test('a clicked system banner opens its exact stage, or only the run once the request is resolved', async t => {
  const { w, get, native } = await openShell(t, twoProjects(queue));
  native.openNotice({ runKey: rb.key, stageId: 's1' });
  await tick();
  const frame = () => w.document.querySelector('#dashboard-host [data-run-key]:not([hidden])');
  native.emitDashboard(frame().dataset.runKey, 'stage-ready');
  await tick();
  assert.equal(decodeURIComponent(w.location.hash), '#b/i-rb:rb/run');
  assert.equal(native.dashboardMessages.get(rb.key).filter(m => m.type === 'open-stage').at(-1).stage, 's1');
  native.publish(twoProjects([]));
  native.openNotice({ runKey: ra.key, stageId: 's1' });
  await tick();
  assert.equal(decodeURIComponent(w.location.hash), '#a/i-ra:ra/run');
  assert.equal(native.dashboardMessages.get(ra.key)?.some(m => m.type === 'open-stage') ?? false, false, 'a resolved request names no stage');
  assert.equal(get('attention').hidden, true);
});

test('the shell tells the main process which run page is open, only when it changes', async t => {
  const { w, native } = await openShell(t, twoProjects([]));
  assert.deepEqual(native.selections, [null]);
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0]);
  await tick();
  assert.deepEqual(native.selections, [null, ra.key]);
  native.publish(twoProjects([]));
  await tick();
  assert.deepEqual(native.selections, [null, ra.key], 'an unchanged page is not reported again');
  w.shell.projects[0].runs[0].live = true;
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0], 'live');
  await tick();
  assert.deepEqual(native.selections.at(-1), null, 'the Live tab covers the dashboard');
});

test('notification settings are saved per category and leave the in-app queue alone', async t => {
  const { d, get, native } = await openShell(t, twoProjects(queue));
  get('settings-toggle').click();
  d.querySelector('[data-panel="notifications"]').click();
  await tick();
  const kinds = { question: true, approval: true, decision: true, failed: false };
  d.querySelector('[data-notify-kind="failed"]').click();
  await tick();
  assert.deepEqual(JSON.parse(JSON.stringify(native.prefChanges.at(-1))), { notifications: { enabled: true, kinds: { ...kinds, failed: true } } });
  get('notify-enabled').click();
  await tick();
  assert.equal(native.prefChanges.at(-1).notifications.enabled, false);
  assert.ok(d.querySelector('[data-notify-kind="question"]').disabled);
  assert.equal(get('notice-count').textContent, '3', 'turning banners off does not shrink the queue');
});

test('a banner click that arrives before the first inventory waits for it', async t => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  const native = fakeNative(twoProjects(queue));
  native.workspace = async () => { await gate; return native.snapshot; };
  const { w } = await loadShell(t, { demo: false, native });
  native.openNotice({ runKey: rb.key, stageId: 's1' });
  assert.equal(w.location.hash, '');
  release();
  await tick();
  assert.equal(decodeURIComponent(w.location.hash), '#b/i-rb:rb/run');
});

test('a clicked banner keeps its exact stage during an outage after successful discovery', async t => {
  const { w, d, native } = await openShell(t, twoProjects(queue));
  native.publish({...snapshotOf([]),host:{state:'offline',error:{message:'down'}}});
  native.openNotice({runKey:rb.key,stageId:'s1'});
  assert.equal(w.location.hash,'');
  native.publish(twoProjects(queue));
  await tick();
  assert.equal(decodeURIComponent(w.location.hash),'#b/i-rb:rb/run');
  const frame = d.querySelector('#dashboard-host [data-run-key]:not([hidden])');
  native.emitDashboard(frame.dataset.runKey,'stage-ready');
  await tick();
  assert.equal(native.dashboardMessages.get(rb.key).filter(event=>event.type==='open-stage').at(-1).stage,'s1');
});

test('a banner click closes an open dialog instead of navigating behind it', async t => {
  const { w, d, get, native } = await openShell(t, twoProjects(queue));
  get('settings-toggle').click();
  assert.equal(get('settings').open, true);
  native.openNotice({ runKey: ra.key, stageId: 's1' });
  await tick();
  assert.equal(d.querySelector('dialog[open]'), null);
  assert.equal(decodeURIComponent(w.location.hash), '#a/i-ra:ra/run');
});

test('a banner click made while the host is unreachable survives until the inventory is real', async t => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  const native = fakeNative(twoProjects(queue));
  native.workspace = async () => { await gate; return native.snapshot; };
  const { w } = await loadShell(t, { demo: false, native });
  native.openNotice({ runKey: rb.key, stageId: 's1' });
  native.snapshot = snapshotOf([], [], { host: { state: 'offline', error: { code: 'host_unreachable', message: 'down' } } });
  release();
  await tick();
  assert.equal(w.location.hash, '', 'an offline inventory says nothing about the run');
  native.publish(twoProjects(queue));
  await tick();
  assert.equal(decodeURIComponent(w.location.hash), '#b/i-rb:rb/run');
});

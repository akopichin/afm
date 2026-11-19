import test from 'node:test';
import assert from 'node:assert/strict';
import { Desktop } from '../src/desktop.js';
import { afmBinary } from '../src/binary.js';
import { makeAssistant, providerHome, settled } from './assistant-support.js';
import { eventually, makeProject, sleepFlow, tempHome } from './support.js';

const slow = { timeout: 120000 };

// Two projects with one running run each, a real AFM host, and an assistant whose fake provider calls AFM tools
// through the real tool bridge exactly as a provider would.
async function world(t, kind = 'claude') {
  const afm = await tempHome(t);
  const desktop = new Desktop({ home: afm.home, binary: afmBinary, env: afm.env });
  t.after(() => desktop.close());
  const projects = {};
  for (const name of ['alpha', 'beta']) {
    const folder = await makeProject(afm.home, name, { flows: { 'sleepy.yaml': sleepFlow(name) } });
    projects[name] = await desktop.projects.add(folder);
  }
  for (const name of ['alpha', 'beta']) await desktop.runs.start({ requestId: 'start-' + name, projectId: projects[name].id, kind: 'Flow', file: 'sleepy.yaml' });
  const runs = await eventually(async () => {
    const list = await desktop.runs.list();
    return list.filter(run => run.state === 'running').length === 2 && list;
  }, { what: 'two running runs' });
  const byName = Object.fromEntries(runs.map(run => [run.projectRoot.split('/').pop(), run]));
  const ctx = await providerHome(t, { FAKE_SCENARIO: 'bridge' });
  const { assistant, events } = makeAssistant(t, ctx, { context: {
    endpoint: async () => { await desktop.host.connect(); return desktop.host.state.endpoint; },
    runs: () => desktop.host.runs(), project: id => desktop.projects.get(id) } });
  await assistant.configure({ provider: kind });
  const approvals = [];
  let decision = true;
  assistant.onEvent(event => { if (event.type === 'approval') { approvals.push(event); if (decision !== 'hold') assistant.approve(event.approvalId, decision); } });
  const ask = async ({ calls, permission, scope }) => {
    ctx.env.FAKE_CALLS = JSON.stringify(calls);
    await assistant.configure({ permission });
    events.length = 0;
    await assistant.send({ scope, text: 'go' });
    await settled(events);
    const finish = events.find(e => ['done', 'error', 'cancelled'].includes(e.type));
    assert.equal(finish.type, 'done', finish.message);
    const text = events.filter(e => e.type === 'delta').map(e => e.text).join('');
    return { text, tools: text.match(/^tools: (.*)$/m)?.[1].split(',').filter(Boolean) ?? [], results: Object.fromEntries([...text.matchAll(/^(afm_\w+):(ok|error):(.*)$/gm)].map(m => [m[1], { status: m[2], body: m[3] }])), events: [...events] };
  };
  return { desktop, projects, runs: byName, assistant, ask, approvals, decide: value => { decision = value; } };
}

const stillRunning = async (desktop, run) => (await desktop.runs.list()).find(item => item.key === run.key)?.state === 'running';

for (const kind of ['claude', 'codex']) {
  test(`${kind}: read-only mode offers no control tools and a project scope sees only its own runs`, slow, async t => {
    const w = await world(t, kind);
    const result = await w.ask({ permission: 'read', scope: { projectId: w.projects.alpha.id }, calls: [{ name: 'afm_runs_list', args: {} }, { name: 'afm_run_cancel', args: { run_id: w.runs.alpha.runId } }] });
    assert.deepEqual(result.tools, ['afm_runs_list'], 'control tools are not even offered');
    const rows = JSON.parse(result.results.afm_runs_list.body).runs;
    assert.deepEqual(rows.map(row => row.run_id), [w.runs.alpha.runId]);
    assert.equal(result.results.afm_run_cancel.status, 'error');
    assert.match(result.results.afm_run_cancel.body, /not_allowed.*Read only/);
    assert.equal(await stillRunning(w.desktop, w.runs.alpha), true);
    const shown = result.events.filter(e => e.type === 'tool_result').map(e => e.error);
    if (kind === 'claude') assert.deepEqual(shown, [false, true], 'a refused tool call is surfaced as an error in the feed');
  });
}

test('ask mode waits for the user: a declined action does nothing, an approved one runs', slow, async t => {
  const w = await world(t);
  w.decide(false);
  const declined = await w.ask({ permission: 'ask', scope: { kind: 'all' }, calls: [{ name: 'afm_run_cancel', args: { run_id: w.runs.beta.runId } }] });
  assert.equal(w.approvals.length, 1);
  assert.equal(w.approvals[0].tool, 'afm_run_cancel');
  assert.equal(w.approvals[0].scope, 'All projects');
  assert.match(declined.results.afm_run_cancel.body, /declined/);
  assert.equal(await stillRunning(w.desktop, w.runs.beta), true);

  w.decide(true);
  const approved = await w.ask({ permission: 'ask', scope: { kind: 'all' }, calls: [{ name: 'afm_run_cancel', args: { run_id: w.runs.beta.runId } }] });
  assert.equal(approved.results.afm_run_cancel.status, 'ok');
  await eventually(async () => !(await stillRunning(w.desktop, w.runs.beta)), { what: 'cancelled run' });
  assert.equal(await stillRunning(w.desktop, w.runs.alpha), true, 'the other run is untouched');
});

test('edit mode starts runs in scope without asking, refuses other projects, still asks for destructive actions', slow, async t => {
  const w = await world(t);
  const scope = { projectId: w.projects.alpha.id };
  const result = await w.ask({ permission: 'edit', scope, calls: [
    { name: 'afm_run_start', args: { project: w.projects.beta.id, flow: 'sleepy.yaml', request_id: 'outside-1' } },
    { name: 'afm_run_cancel', args: { run_id: w.runs.beta.runId } },
    { name: 'afm_run_attach', args: { endpoint: 'http://127.0.0.1:9' } },
  ] });
  assert.ok(result.tools.includes('afm_run_start') && result.tools.includes('afm_run_cancel'));
  assert.equal(result.tools.includes('afm_run_attach'), false, 'attaching is an all-projects action');
  assert.match(result.results.afm_run_start.body, /out_of_scope.*spider|out_of_scope.*alpha/);
  assert.match(result.results.afm_run_cancel.body, /out_of_scope/);
  assert.equal(w.approvals.length, 0, 'refused by scope before anyone is asked');
  assert.equal(await stillRunning(w.desktop, w.runs.beta), true);

  const inScope = await w.ask({ permission: 'edit', scope, calls: [{ name: 'afm_run_cancel', args: { run_id: w.runs.alpha.runId } }] });
  assert.equal(w.approvals.length, 1, 'cancelling is destructive, so edit mode still asks');
  assert.equal(inScope.results.afm_run_cancel.status, 'ok');
});

test('YOLO acts without approval; a run scope is limited to that one run', slow, async t => {
  const w = await world(t);
  const single = await w.ask({ permission: 'yolo', scope: { runId: w.runs.alpha.runId, instanceId: w.runs.alpha.instanceId }, calls: [
    { name: 'afm_runs_list', args: {} }, { name: 'afm_run_cancel', args: { run_id: w.runs.beta.runId } }] });
  assert.deepEqual(single.tools, ['afm_run_cancel', 'afm_runs_list'], 'neither start nor attach is offered for a single run');
  assert.deepEqual(JSON.parse(single.results.afm_runs_list.body).runs.map(row => row.run_id), [w.runs.alpha.runId]);
  assert.match(single.results.afm_run_cancel.body, /out_of_scope/);
  const all = await w.ask({ permission: 'yolo', scope: { kind: 'all' }, calls: [{ name: 'afm_run_cancel', args: { run_id: w.runs.beta.runId } }] });
  assert.equal(all.results.afm_run_cancel.status, 'ok');
  assert.equal(w.approvals.length, 0);
});

test('cancelling a reply denies its pending approval and never runs the action', slow, async t => {
  const w = await world(t);
  w.decide('hold');
  const ctx = w.assistant.baseEnv;
  ctx.FAKE_CALLS = JSON.stringify([{ name: 'afm_run_cancel', args: { run_id: w.runs.alpha.runId } }]);
  await w.assistant.configure({ permission: 'ask' });
  const events = [];
  w.assistant.onEvent(event => events.push(event));
  await w.assistant.send({ text: 'go' });
  await eventually(() => w.approvals.length === 1, { what: 'approval request' });
  assert.equal(w.assistant.cancel(), true);
  await eventually(() => events.find(e => e.type === 'cancelled'), { what: 'cancelled turn' });
  assert.equal(events.find(e => e.type === 'approval_done').approved, false);
  assert.equal(await stillRunning(w.desktop, w.runs.alpha), true);
  assert.equal(w.assistant.approve(w.approvals[0].approvalId, true), false, 'a late answer cannot revive the action');
});

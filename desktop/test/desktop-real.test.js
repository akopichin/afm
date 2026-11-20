import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { readFile, writeFile, access, mkdir, realpath, rmdir } from 'node:fs/promises';
import path from 'node:path';
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { Desktop } from '../src/desktop.js';
import { afmBinary } from '../src/binary.js';
import { Workspace, fetchAttention } from '../src/workspace.js';
import { DashboardOrigins } from '../src/protocol.js';
import { setTimeout as delay } from 'node:timers/promises';
import { eventually, makeProject, sleepFlow, tempHome } from './support.js';

const fast = { timeout: 90000 };
const git = (dir, ...args) => execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8' }).trim();

async function open(t, { home, env }) {
  const desktop = new Desktop({ home, binary: afmBinary, env });
  t.after(() => desktop.close());
  return desktop;
}
const active = desktop => desktop.runs.list().then(runs => runs.filter(run => run.state === 'running'));

test('an empty folder becomes a project and one request id starts one run', fast, async t => {
  const ctx = await tempHome(t);
  const folder = await makeProject(ctx.home, 'empty', { flows: { 'smoke.yaml': sleepFlow('smoke') } });
  const desktop = await open(t, ctx);
  const project = await desktop.projects.add(folder);
  assert.equal(project.registered, true);
  assert.deepEqual((await desktop.overview()).projects[0].runs, []);

  const request = { requestId: 'req-1', projectId: project.id, kind: 'Flow', file: 'smoke.yaml' };
  const [first, second] = await Promise.all([desktop.runs.start(request), desktop.runs.start(request)]);
  assert.equal(first.launch_id, second.launch_id);
  assert.equal((await desktop.runs.start(request)).launch_id, first.launch_id, 'a later retry is the same launch');
  await assert.rejects(desktop.runs.start({ ...request, file: 'other.yaml' }), { code: 'request_conflict' });

  const run = await eventually(async () => (await active(desktop))[0], { what: 'running run' });
  assert.equal(run.origin, 'desktop');
  assert.equal(run.ownership, 'managed');
  assert.equal(run.can.cancel, true);
  assert.equal(desktop.host.state.ownership, 'desktop');
  const origin = await desktop.runs.open(run.runId, run.instanceId);
  assert.match(await (await fetch(origin)).text(), /id="root"/);

  const overview = await desktop.overview();
  assert.equal(overview.projects[0].runs.length, 1);
  assert.deepEqual(overview.unassigned, []);

  await desktop.runs.cancel(run.runId, run.instanceId);
  const ended = await eventually(async () => (await desktop.runs.list()).find(r => r.key === run.key && r.state === 'finished'), { what: 'cancelled run' });
  assert.equal(ended.can.open, false);
  await assert.rejects(desktop.runs.open(run.runId, run.instanceId), /unavailable/);
});

test('Continue run reuses a saved worktree and pipeline snapshot after Desktop restarts', fast, async t => {
  const ctx = await tempHome(t);
  const root = await makeProject(ctx.home, 'repo', {git:true});
  const library = path.join(ctx.home, 'library');
  await mkdir(library);
  await writeFile(path.join(library, 'pipeline.yaml'), 'schema_version: 2\nname: resume\nversion: 1.0.0\nentrypoint: flow.yaml\n');
  await writeFile(path.join(library, 'flow.yaml'), sleepFlow('resume', 60));
  const desktop = await open(t, ctx);
  const project = await desktop.projects.add(root);
  const source = await desktop.sources.add({location:library});
  const worktree = 'desktop-resume-a1b2c';
  await desktop.runs.start({requestId:'original', projectId:project.id, kind:'Pipeline', file:source.entries[0].id,
    explicit:'worktree', worktree, task:'initial request'});
  const first = await eventually(async () => (await active(desktop))[0], {what:'original run'});
  await eventually(async () => {
    const status = await fetch(new URL('/api/status', first.apiUrl)).then(response=>response.json()).catch(()=>null);
    return status?.stages.some(stage=>stage.id === 'wait' && stage.status === 'running');
  }, {what:'original stage running'});
  const binding = path.join(root, '.afm', 'pipelines', 'runs', first.runId + '.json');
  const snapshot = JSON.parse(await readFile(binding, 'utf8')).content_sha256;
  await desktop.runs.cancel(first.runId, first.instanceId);
  await eventually(async () => (await desktop.runs.forProject(project.id)).find(run=>run.can.resume), {what:'continuable run'});
  await desktop.close();
  await writeFile(path.join(library, 'flow.yaml'), sleepFlow('different-flow', 60).replace('id: wait', 'id: changed'));

  const reopened = await open(t, ctx);
  const pending = () => reopened.runs.resume(first.runId, first.instanceId);
  const newer = path.join(root, '.afm', 'runs', worktree + '-29991231-235959-abcd');
  await mkdir(newer);
  await assert.rejects(pending(), {code:'run_changed'});
  await rmdir(newer);
  const [continued, repeated] = await Promise.all([pending(), pending()]);
  assert.equal(continued.launch_id, repeated.launch_id);
  assert.equal(continued.run_id, first.runId);
  assert.notEqual(continued.instance_id, first.instanceId);
  assert.equal(JSON.parse(await readFile(binding, 'utf8')).content_sha256, snapshot);
  const runs = await reopened.runs.forProject(project.id);
  assert.deepEqual(runs.map(run=>[run.runId, run.instanceId]), [[first.runId, continued.instance_id]]);
  assert.equal(git(path.join(root, '.worktrees', worktree), 'branch', '--show-current'), worktree);
  await reopened.runs.cancel(first.runId, continued.instance_id);
  await eventually(async () => (await reopened.runs.forProject(project.id)).some(run=>run.can.resume), {what:'resumed process exit'});
});

test('a project registered after the host started restarts the host and keeps running runs', fast, async t => {
  const ctx = await tempHome(t);
  const a = await makeProject(ctx.home, 'a', { flows: { 'one.yaml': sleepFlow('one') } });
  const b = await makeProject(ctx.home, 'b', { flows: { 'two.yaml': sleepFlow('two') } });
  const desktop = await open(t, ctx);
  const pa = await desktop.projects.add(a);
  await desktop.runs.start({ requestId: 'a-1', projectId: pa.id, kind: 'Flow', file: 'one.yaml' });
  const first = await eventually(async () => (await active(desktop))[0], { what: 'first run' });
  const hostPid = desktop.host.state.pid;

  const pb = await desktop.projects.add(b);
  await desktop.runs.start({ requestId: 'b-1', projectId: pb.id, kind: 'Flow', file: 'two.yaml' });
  assert.notEqual(desktop.host.state.pid, hostPid, 'the host restarted to read serve.yaml');
  assert.equal(desktop.host.state.ownership, 'desktop');
  const runs = await eventually(async () => { const all = await active(desktop); return all.length === 2 && all; }, { what: 'both runs' });
  assert.ok(runs.some(run => run.key === first.key), 'the first run kept its identity');
});

test('an external run and a managed run keep their identities across a Desktop restart', fast, async t => {
  const ctx = await tempHome(t);
  const managed = await makeProject(ctx.home, 'managed', { flows: { 'm.yaml': sleepFlow('m') } });
  const manual = await makeProject(ctx.home, 'manual', { flows: { 'x.yaml': sleepFlow('x') } });
  const desktop = await open(t, ctx);
  const pm = await desktop.projects.add(managed);
  const px = await desktop.projects.add(manual);
  await desktop.runs.start({ requestId: 'm-1', projectId: pm.id, kind: 'Flow', file: 'm.yaml' });

  const child = spawn(await afmBinary(), ['run', '--dir', manual, '--mode', 'dashboard', '--listen-any-port', path.join(manual, '.afm', 'flows', 'x.yaml')],
    { env: ctx.env, stdio: 'ignore' });
  t.after(() => child.kill());
  const both = await eventually(async () => {
    const runs = await active(desktop);
    return runs.length === 2 && runs;
  }, { what: 'both runs listed' });
  const external = both.find(run => run.name === 'x');
  const mine = both.find(run => run.name === 'm');
  assert.deepEqual([external.origin, external.ownership], ['external', 'attached']);
  assert.deepEqual([mine.origin, mine.ownership], ['desktop', 'managed']);
  assert.equal(external.can.cancel, false);
  await assert.rejects(desktop.runs.cancel(external.runId, external.instanceId), { code: 'not_supported' });
  assert.match(await desktop.runs.open(external.runId, external.instanceId), /^http:\/\/127\.0\.0\.1:\d+$/);

  await desktop.close();
  const reopened = await open(t, ctx);
  const overview = await reopened.overview();
  const byProject = Object.fromEntries(overview.projects.map(p => [p.id, p.runs.map(r => [r.key, r.origin, r.ownership])]));
  assert.deepEqual(byProject[pm.id], [[mine.key, 'desktop', 'managed']]);
  assert.deepEqual(byProject[px.id], [[external.key, 'external', 'attached']]);
  assert.equal(overview.host.ownership, 'desktop');
});

test('a run outside Home is offered as a candidate and attaching never grants control', fast, async t => {
  const ctx = await tempHome(t);
  const stray = await makeProject(ctx.home, 'stray', { flows: { 's.yaml': sleepFlow('s') } });
  const desktop = await open(t, ctx);
  await desktop.host.connect();
  const child = spawn(await afmBinary(), ['run', '--dir', stray, '--mode', 'dashboard', '--listen-any-port', path.join(stray, '.afm', 'flows', 's.yaml')],
    { env: ctx.env, stdio: 'ignore' });
  t.after(() => child.kill());
  const overview = await eventually(async () => { const o = await desktop.overview(); return o.unassigned[0]?.apiUrl && o; }, { what: 'stray run' });
  assert.deepEqual(overview.candidates.map(c => c.path), [stray]);
  const [run] = overview.unassigned;
  await desktop.runs.attach(run.apiUrl);
  const after = (await desktop.runs.list())[0];
  assert.equal(after.ownership, 'attached');
  assert.equal(after.can.cancel, false);
  assert.equal(after.can.release, false);
});

test('Git launch modes never discard project changes', fast, async t => {
  const ctx = await tempHome(t);
  const dir = await makeProject(ctx.home, 'repo', { git: true, flows: {
    'plain.yaml': sleepFlow('plain'), 'other.yaml': sleepFlow('other'),
    'wt.yaml': sleepFlow('wtflow', 30, 'worktrees: true\n'),
  } });
  const desktop = await open(t, ctx);
  const project = await desktop.projects.add(dir);
  await writeFile(path.join(dir, 'README.md'), 'dirty\n');
  const base = { projectId: project.id, kind: 'Flow' };

  const preview = await desktop.runs.preview({ ...base, file: 'wt.yaml' });
  assert.deepEqual([preview.effective.mode, preview.effective.source, preview.effective.worktree], ['worktree', 'flow', 'wtflow']);
  const override = (await desktop.runs.preview({ ...base, file: 'wt.yaml', explicit: 'current' })).effective;
  assert.deepEqual([override.blocked, override.mode, override.noWorktree], [null, 'current', true], 'an explicit choice outranks the flow policy');
  assert.equal((await desktop.runs.preview({ ...base, file: 'plain.yaml' })).effective.mode, 'current');

  // Branch mode keeps the dirty file and stays on the new branch.
  await desktop.runs.start({ ...base, file: 'plain.yaml', requestId: 'branch-1', explicit: 'branch', branch: 'feat-a' });
  assert.equal(git(dir, 'branch', '--show-current'), 'feat-a');
  assert.equal(await readFile(path.join(dir, 'README.md'), 'utf8'), 'dirty\n');
  await eventually(async () => (await active(desktop)).length === 1, { what: 'branch run' });

  // The same flow in the same checkout is refused; the branch asked for is given back.
  await assert.rejects(desktop.runs.start({ ...base, file: 'plain.yaml', requestId: 'branch-2', explicit: 'branch', branch: 'feat-b' }), { code: 'conflict' });
  assert.equal(git(dir, 'branch', '--show-current'), 'feat-a');
  assert.equal(git(dir, 'branch', '--list', 'feat-b'), '');
  assert.equal(await readFile(path.join(dir, 'README.md'), 'utf8'), 'dirty\n');
  // An existing branch is a conflict that changes nothing.
  await assert.rejects(desktop.runs.start({ ...base, file: 'other.yaml', requestId: 'branch-3', explicit: 'branch', branch: 'main' }), { code: 'branch_exists' });
  assert.equal(git(dir, 'branch', '--show-current'), 'feat-a');

  // A flow that declares worktrees runs in its own worktree, which is reported under the project.
  await desktop.runs.start({ ...base, file: 'wt.yaml', requestId: 'wt-1' });
  await eventually(() => access(path.join(dir, '.worktrees', 'wtflow')).then(() => true, () => false), { what: 'worktree folder' });
  await eventually(async () => (await desktop.runs.forProject(project.id)).some(run => run.name === 'wtflow'), { what: 'registered worktree run' });
  assert.equal(await readFile(path.join(dir, 'README.md'), 'utf8'), 'dirty\n');
});

test('a stale host file is replaced, and a foreign host is only restarted on confirmation', fast, async t => {
  const ctx = await tempHome(t);
  await mkdirp(path.join(ctx.home, '.afm', 'serve'));
  await writeFile(path.join(ctx.home, '.afm', 'serve', 'host.json'), JSON.stringify({ pid: 1, endpoint: 'http://127.0.0.1:1/mcp', started_at: '2020-01-01T00:00:00Z' }));
  const desktop = await open(t, ctx);
  await desktop.host.connect();
  assert.equal(desktop.host.state.ownership, 'desktop', 'Desktop started its own host over the stale file');
  await desktop.close();

  // Replace it with a host somebody else started.
  const foreign = await tempHome(t);
  const host = spawn(await afmBinary(), ['serve', '--port', '0'], { env: foreign.env, stdio: 'ignore' });
  t.after(() => host.kill());
  await eventually(() => readFile(path.join(foreign.home, '.afm', 'serve', 'host.json'), 'utf8').catch(() => false), { what: 'foreign host' });
  const other = await open(t, foreign);
  await other.host.connect();
  assert.equal(other.host.state.ownership, 'external');
  await assert.rejects(other.host.restart(), { code: 'external_host' });
  const pid = other.host.state.pid;
  await other.host.restart({ confirmExternal: true });
  assert.notEqual(other.host.state.pid, pid);
});

test('a service that is not AFM is reported as an incompatible host', fast, async t => {
  const ctx = await tempHome(t);
  const server = createServer(async (req, res) => {
    const mcp = new McpServer({ name: 'impostor', version: '1' });
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
    await mcp.connect(transport);
    await transport.handleRequest(req, res);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.close());
  await mkdirp(path.join(ctx.home, '.afm', 'serve'));
  await writeFile(path.join(ctx.home, '.afm', 'serve', 'host.json'),
    JSON.stringify({ pid: 1, endpoint: `http://127.0.0.1:${server.address().port}/mcp`, started_at: new Date().toISOString() }));
  const desktop = await open(t, ctx);
  await assert.rejects(desktop.host.connect(), { code: 'incompatible_host' });
  assert.equal(desktop.host.state.state, 'incompatible');
  const overview = await desktop.overview();
  assert.equal(overview.host.error.code, 'incompatible_host');
});

async function mkdirp(dir) { await (await import('node:fs/promises')).mkdir(dir, { recursive: true }); }

test('the workspace snapshot follows a real run, verifies its stage list and forgets it when it ends', fast, async t => {
  const ctx = await tempHome(t);
  const folder = await makeProject(ctx.home, 'live', { flows: { 'w.yaml': sleepFlow('w') } });
  const desktop = await open(t, ctx);
  const workspace = new Workspace({ desktop, interval: 200 });
  t.after(() => workspace.close());
  const project = await desktop.projects.add(folder);
  const updates = [];
  workspace.onChange(snapshot => updates.push(snapshot));
  await workspace.refresh();
  assert.deepEqual(updates[0].projects[0].runs, []);

  await desktop.runs.start({ requestId: 'ws-1', projectId: project.id, kind: 'Flow', file: 'w.yaml' });
  workspace.setVisible(true);
  const snapshot = await eventually(async () => workspace.snapshot.projects[0].runs.find(run => run.launchState === 'ready'), { what: 'ready run in snapshot' });
  assert.equal(snapshot.origin, 'desktop');
  // The real run's own API answers identity and stage list the way the notice reader expects.
  assert.deepEqual(await fetchAttention(snapshot), []);
  assert.deepEqual(workspace.snapshot.attention, []);
  await assert.rejects(fetchAttention({ ...snapshot, instanceId: 'someone-else' }), /Run changed/);

  const seen = updates.length;
  await delay(700);
  assert.equal(updates.length, seen, 'an idle run produced snapshot updates');

  await desktop.runs.cancel(snapshot.runId, snapshot.instanceId);
  // The host may list the ended run once as finished or stop listing it at all; it must never stay running.
  await eventually(async () => !workspace.snapshot.projects[0].runs.some(run => run.key === snapshot.key && run.state === 'running'), { what: 'ended run' });
  const origins = new DashboardOrigins();
  origins.add(new URL(snapshot.apiUrl).origin, snapshot.key);
  origins.retain(new Set());
  assert.equal(origins.size, 0);
});

test('an explicit current or branch launch of a worktrees: true flow really runs in the checkout', fast, async t => {
  const ctx = await tempHome(t);
  const probe = name => `name: ${name}\nworktrees: true\nstages:\n  - id: probe\n    script: |\n      pwd > "$AFM_PROBE_DIR/${name}.cwd"\n      git branch --show-current > "$AFM_PROBE_DIR/${name}.branch"\n      sleep 20\n`;
  const dir = await makeProject(ctx.home, 'repo', { git: true, flows: { 'cur.yaml': probe('cur'), 'br.yaml': probe('br') } });
  const out = path.join(ctx.home, 'probe');
  await mkdir(out);
  ctx.env.AFM_PROBE_DIR = out;
  const desktop = await open(t, ctx);
  const project = await desktop.projects.add(dir);
  const base = { projectId: project.id, kind: 'Flow' };
  const root = await realpath(dir);
  await desktop.runs.start({ ...base, file: 'cur.yaml', requestId: 'probe-cur', explicit: 'current' });
  const cwd = async name => (await eventually(() => readFile(path.join(out, name), 'utf8').catch(() => null), { what: name })).trim();
  assert.equal(await realpath(await cwd('cur.cwd')), root, 'the script ran in the project checkout, not .worktrees/');
  assert.equal(await cwd('cur.branch'), 'main');
  await desktop.runs.start({ ...base, file: 'br.yaml', requestId: 'probe-br', explicit: 'branch', branch: 'feat-probe' });
  assert.equal(await realpath(await cwd('br.cwd')), root);
  assert.equal(await cwd('br.branch'), 'feat-probe');
  await assert.rejects(access(path.join(dir, '.worktrees')), { code: 'ENOENT' }, 'no worktree was created');
  const running = await active(desktop);
  assert.ok(running.length === 2 && running.every(run => Number.isSafeInteger(run.pid) && run.pid > 1), 'the host reports each run\'s process id');
});

import { createHash, randomUUID } from 'node:crypto';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { open, readdir, realpath, rm, stat } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';
import { promisify } from 'node:util';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';
import { HostError, dashboardURL, localURL } from './host.js';
import { belongsTo } from './projects.js';
import { configFilesFor, effectivePolicy, inspectGit, prepareBranch, resolveMode, validWorktreeName } from './git-mode.js';
import { projectFlows } from './sources.js';
import { captureRunGit, listRunChanges, readRunPatch } from './run-changes.js';

const MAX_LEDGER = 200;
const FINISHED = ['finished', 'failed', 'interrupted'];
const REFUSALS = new Set(['external_host', 'invalid', 'conflict', 'limit_running', 'limit_starting', 'request_conflict']);
const exec = promisify(execFile);
const LIVE = ['active', 'active_orphaned'];

function stoppedDesktop(row, launch) {
  return row.ownership === 'managed' && ['exited', 'failed'].includes(row.launch?.state)
    && ['exited', undefined, ''].includes(row.liveness) && Boolean(launch?.hostArgs && row.run_id);
}

function resumable(row, launch) {
  return stoppedDesktop(row, launch) && ['interrupted', 'failed'].includes(row.run_status)
    && validWorktreeName(launch.hostArgs.worktree);
}

async function worktreeRuns(directory, name) {
  const prefix = name + '-';
  const entries = await readdir(directory, {withFileTypes:true});
  return entries.filter(entry => entry.isDirectory() && entry.name.startsWith(prefix)
    && /^[0-9]{8}-[0-9]{6}(?:-[0-9a-f]+)?$/.test(entry.name.slice(prefix.length))).map(entry => entry.name).sort();
}

// A run as the UI shows it: identity, an honest state, and only the operations the host allows now.
export function runView(row, ledger = {}) {
  const alive = LIVE.includes(row.liveness);
  const actions = row.actions ?? [];
  const launch = ledger[row.launch?.launch_id];
  let state = 'running';
  if (row.liveness === 'identity_mismatch') state = 'mismatch';
  else if (['unreachable', 'stale'].includes(row.liveness)) state = 'offline';
  else if (!alive) state = 'finished';
  else if (FINISHED.includes(row.run_status)) state = 'finished';
  else if (row.mode !== 'dashboard') state = 'api-only';
  const dashboard = alive && row.mode === 'dashboard' && ['attached', 'managed'].includes(row.ownership) && state !== 'mismatch';
  return {
    key: `${row.instance_id}:${row.run_id}`, runId: row.run_id, instanceId: row.instance_id, name: row.run_name,
    projectRoot: row.project_root, pid: row.pid ?? null, mode: row.mode, apiUrl: row.api_url, ownership: row.ownership,
    origin: launch ? 'desktop' : 'external', launchState: row.launch?.state ?? null,
    status: row.run_status ?? null, attention: Boolean(row.attention_required), state,
    // The run's API answers, whatever the UI may do with it; watch reads use this, not `can.open`.
    apiLive: alive,
    can: { open: dashboard, cancel: actions.includes('cancel'), release: actions.includes('release'),
      attach: actions.includes('attach'), report: actions.includes('report'), resume: resumable(row, launch), delete: stoppedDesktop(row, launch) },
  };
}

function fingerprintOf({ projectId, kind, file, explicit, branch, worktree, task }) {
  return createHash('sha256').update(JSON.stringify([projectId, kind, file, explicit ?? null, branch ?? null, worktree ?? null, task ?? ''])).digest('hex');
}

export class Runs {
  constructor({ home, host, projects, sources, prefs, serveConfig }) {
    Object.assign(this, { home, host, projects, sources, prefs, serveConfig });
    this.ledgerFile = path.join(desktopDir(home), 'launches.json');
    this.deletedFile = path.join(desktopDir(home), 'deleted-runs.json');
    this.ledgerLock = new Mutex();
    this.inflight = new Map();
    this.resuming = new Map();
    this.deleting = new Set();
    this.changeSnapshots = new Map();
  }

  async #ledger() { return (await readJSON(this.ledgerFile, { launches: {} })).launches; }

  #updateLedger(change) {
    return this.ledgerLock.run(async () => {
      const launches = await this.#ledger();
      const next = change(launches);
      const kept = Object.entries(next).sort((a, b) => a[1].at.localeCompare(b[1].at)).slice(-MAX_LEDGER);
      await writeJSON(this.ledgerFile, { launches: Object.fromEntries(kept) });
    });
  }

  // Launch ids Desktop issued, keyed by launch id, so a run shows where it came from after a restart.
  async #issued() {
    return Object.fromEntries(Object.values(await this.#ledger()).filter(item => item.launchId).map(item => [item.launchId, item]));
  }

  async list() {
    const [issued, rows, deleted] = await Promise.all([this.#issued(), this.host.runs(), readJSON(this.deletedFile,{})]);
    const replaced = new Set(rows.flatMap(row => {
      const launch = issued[row.launch?.launch_id];
      return launch?.resumedFrom && launch.resumeRunId === row.run_id ? [launch.resumedFrom] : [];
    }));
    const visible = rows.filter(row => {
      const id = row.launch?.launch_id;
      return !replaced.has(id) && (LIVE.includes(row.liveness) || !deleted[JSON.stringify([issued[id]?.projectId,row.run_id])]);
    });
    const missingProject = visible.some(row => !row.project_root && issued[row.launch?.launch_id]);
    const config = missingProject ? await this.serveConfig.read() : null;
    return visible.map(row => {
      const launch = issued[row.launch?.launch_id];
      if (!launch || row.project_root) return runView(row, issued);
      const root = launch.projectRoot ?? config?.projects.find(project => project.id === launch.projectId)?.path;
      return runView({...row, project_root: root, run_name: row.run_name || launch.hostArgs?.worktree}, issued);
    });
  }

  // Runs of one project plus the runs nobody on Home owns, newest information first.
  async forProject(projectId) {
    const project = await this.projects.get(projectId);
    return (await this.list()).filter(run => belongsTo(project.path, run.projectRoot));
  }

  async #find(runId, instanceId) {
    const row = (await this.host.runs()).find(item => item.run_id === runId && item.instance_id === instanceId);
    if (!row) throw new HostError('run_gone', 'This run is no longer known to the AFM host.');
    return row;
  }

  // Opens only the run the caller identified, never whatever answers on a reused port.
  async open(runId, instanceId) { return dashboardURL(await this.#find(runId, instanceId)); }

  report(runId, instanceId) {
    if (typeof runId !== 'string' || !runId || typeof instanceId !== 'string' || !instanceId) {
      throw new HostError('invalid', 'The run and instance IDs are required.');
    }
    return this.host.tool('afm_run_report', { run_id: runId, instance_id: instanceId });
  }

  async changes(runId, instanceId, mode) {
    const row = await this.#find(runId, instanceId);
    const launchId = row.launch?.launch_id;
    if (row.liveness === 'identity_mismatch') {
      throw Error('This run has no verified local checkout.');
    }
    const [receipt, issued, registry] = await Promise.all([
      /^[a-f0-9]{64}$/.test(launchId ?? '') ? readJSON(path.join(this.home, '.afm', 'serve', 'launches', launchId + '.json'), null) : null,
      launchId ? this.#issued() : {},
      LIVE.includes(row.liveness) && /^[a-f0-9]{32}$/.test(instanceId) ? readJSON(path.join(this.home, '.afm', 'active-runs', instanceId + '.json'), null) : null,
    ]);
    const launch = issued[launchId];
    const saved = receipt?.run_id === runId && receipt.instance_id === instanceId ? receipt : null;
    const observed = registry?.schema_version === 1 && registry.run_id === runId && registry.instance_id === instanceId &&
      registry.pid === row.pid && registry.project?.root === row.project_root ? registry.project.worktree : null;
    const root = observed || saved?.identity?.checkout;
    const inWorktree = typeof root === 'string' && (observed || path.basename(path.dirname(root)) === '.worktrees');
    const worktree = inWorktree ? path.basename(root) : saved?.params?.worktree;
    const kind = worktree ? 'worktree' : launch?.effective?.mode;
    const projectRoot = launch?.projectRoot ?? row.project_root;
    const branch = worktree ?? launch?.effective?.branch;
    if (typeof root !== 'string' || !path.isAbsolute(root) || typeof projectRoot !== 'string' || !path.isAbsolute(projectRoot) ||
      !['worktree','branch'].includes(kind) || !validWorktreeName(branch)) {
      throw Error('Changes is available for a run with a verified local worktree or branch.');
    }
    let start = launch?.gitStart;
    if (kind === 'worktree' && start?.checkout !== root) start = null;
    const {checkout, head, tip, files, ...summary} = await listRunChanges({ root, projectRoot, branch, kind, start, mode });
    const token = randomUUID();
    this.changeSnapshots.set(token, {checkout, head, tip, files, ...summary});
    while (this.changeSnapshots.size > 8) this.changeSnapshots.delete(this.changeSnapshots.keys().next().value);
    return {...summary, token, files:files.map(({path,status})=>({path,status}))};
  }

  changePatch(token, file) {
    const snapshot = this.changeSnapshots.get(token);
    if (!snapshot) throw Error('The file list expired. Refresh and try again.');
    return readRunPatch(snapshot, file);
  }

  async cancel(runId, instanceId) {
    const row = await this.#find(runId, instanceId);
    if (!row.actions?.includes('cancel')) throw new HostError('not_supported', 'This run cannot be cancelled from Desktop.');
    return this.host.tool('afm_run_cancel', { run_id: row.run_id });
  }

  resume(runId, instanceId, confirmHostRestart = false) {
    const key = JSON.stringify([runId, instanceId]);
    if (this.deleting.has(runId)) throw new HostError('busy','This run is being deleted.');
    if (this.resuming.has(key)) return this.resuming.get(key);
    const work = this.#resume(runId, instanceId, confirmHostRestart).finally(() => this.resuming.delete(key));
    this.resuming.set(key, work);
    return work;
  }

  async #resume(runId, instanceId, confirmHostRestart) {
    const row = await this.#find(runId, instanceId);
    const oldId = row.launch?.launch_id;
    const previous = (await this.#issued())[oldId];
    if (!resumable(row, previous) || !/^[a-f0-9]{64}$/.test(oldId)) {
      throw new HostError('not_supported', 'Only a stopped Desktop run with a saved worktree can be continued.');
    }
    const receipt = await readJSON(path.join(this.home, '.afm', 'serve', 'launches', oldId + '.json'), null);
    const worktree = previous.hostArgs.worktree;
    if (!receipt?.process_gone || receipt.run_id !== runId || receipt.instance_id !== instanceId
        || receipt.params?.worktree !== worktree || !path.isAbsolute(receipt.runs_dir ?? '')) {
      throw new HostError('run_changed', 'The previous AFM process has not finished stopping. Refresh and try again.');
    }
    const checkout = receipt.identity?.checkout;
    if (!checkout || !(await stat(path.join(checkout, '.git')).catch(() => null))) {
      throw new HostError('worktree_missing', 'The run’s worktree is missing. Restore it before continuing.');
    }
    const latest = (await worktreeRuns(receipt.runs_dir, worktree)).at(-1);
    if (latest !== runId) throw new HostError('run_changed', 'Only the latest saved run in this worktree can be continued.');

    const requestId = randomUUID();
    const { project, pipeline, flow, mode } = previous.hostArgs;
    if (pipeline) {
      const binding = await readJSON(path.join(path.dirname(receipt.runs_dir), 'pipelines', 'runs', runId + '.json'), null);
      const source = (await this.serveConfig.read()).pipelines.find(entry => entry.id === pipeline)?.source;
      if (binding?.run_id !== runId || binding.worktree !== worktree || !source
          || (binding.source?.kind === 'local' && binding.source.location !== source)) {
        throw new HostError('run_changed', 'Restore the original pipeline source before continuing this run.');
      }
    }
    const hostArgs = { request_id: requestId, project, mode, worktree, ...(pipeline ? {pipeline} : {flow}) };
    const effective = previous.effective;
    await this.#updateLedger(all => ({ ...all, [requestId]: {at:new Date().toISOString(), projectId:project,
      projectRoot:row.project_root || previous.projectRoot, hostArgs, effective, gitStart:previous.gitStart, resumedFrom:oldId, resumeRunId:runId} }));
    const launch = await this.#send(requestId, hostArgs, effective, null, confirmHostRestart);
    const result = await this.waitForLaunch(launch);
    if (result.run_id && result.run_id !== runId) {
      return {...result, message:'AFM selected a different run. Open the project to inspect it.'};
    }
    return result;
  }

  async #deletion(runId, instanceId) {
    const rows = await this.host.runs();
    const row = rows.find(item => item.run_id === runId && item.instance_id === instanceId);
    const id = row?.launch?.launch_id;
    const launch = (await this.#issued())[id];
    if (!row || !stoppedDesktop(row, launch) || !/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(runId) || !/^[a-f0-9]{64}$/.test(id)) {
      throw new HostError('not_supported','Only a stopped run started from Desktop can be deleted.');
    }
    const receipt = await readJSON(path.join(this.home,'.afm','serve','launches',id+'.json'),null);
    if (!receipt?.process_gone || receipt.run_id !== runId || receipt.instance_id !== instanceId
        || rows.some(item => item.run_id === runId && LIVE.includes(item.liveness))) {
      throw new HostError('run_changed','Stop every instance of the run before deleting it.');
    }
    const { projectId } = launch;
    const configured = await this.serveConfig.read();
    const projectRoot = launch.projectRoot ?? row.project_root ?? configured.projects.find(project => project.id === projectId)?.path;
    if (!projectRoot || !path.isAbsolute(projectRoot)) throw new HostError('run_changed','The original project folder is unavailable.');
    const project = await realpath(projectRoot);
    const afm = path.join(project,'.afm');
    const runs = path.join(afm,'runs');
    if (!path.isAbsolute(receipt.runs_dir ?? '') || await realpath(receipt.runs_dir) !== runs) {
      throw new HostError('run_changed','The saved run directory no longer matches its project.');
    }
    const optional = [path.join(afm,'pipelines','runs'),path.join(afm,'side-agent')];
    for (const directory of optional) {
      const resolved = await realpath(directory).catch(error => {
        if (error.code === 'ENOENT') return directory;
        throw error;
      });
      if (resolved !== directory) throw new HostError('run_changed','A run data directory points outside its saved path.');
    }
    const name = receipt.params?.worktree;
    const checkout = receipt.identity?.checkout;
    let worktree = null;
    let shared = false;
    if (validWorktreeName(name) && checkout && checkout !== project && path.basename(checkout) === name
        && path.basename(path.dirname(checkout)) === '.worktrees') {
      shared = (await worktreeRuns(runs,name)).some(other => other !== runId);
      const details = await stat(checkout).catch(error => {
        if (error.code === 'ENOENT') return null;
        throw error;
      });
      if (!shared && details?.isDirectory()) worktree = checkout;
    }
    return {projectId, project, afm, runs, worktree, shared};
  }

  async delete(runId, instanceId, confirm) {
    const key = JSON.stringify([runId,instanceId]);
    if (this.deleting.has(runId) || this.resuming.has(key)) throw new HostError('busy','This run already has an operation in progress.');
    this.deleting.add(runId);
    try {
      const preview = await this.#deletion(runId,instanceId);
      if (!await confirm(preview)) return {deleted:false};
      const current = await this.#deletion(runId,instanceId);
      if (current.project !== preview.project || current.worktree !== preview.worktree) {
        throw new HostError('run_changed','The worktree changed while the dialog was open. Try again.');
      }
      if (current.worktree) {
        try { await exec('git',['-C',current.project,'worktree','remove','--',current.worktree]); }
        catch { throw new HostError('worktree_busy','Git could not remove the worktree. Check for uncommitted changes or a worktree lock. No run data was deleted.'); }
        await exec('git',['-C',current.project,'branch','-d','--',path.basename(current.worktree)]).catch(() => {});
      }
      await rm(path.join(current.runs,runId),{recursive:true,force:true});
      await rm(path.join(current.afm,'pipelines','runs',runId+'.json'),{force:true});
      await rm(path.join(current.afm,'side-agent',runId),{recursive:true,force:true});
      await this.ledgerLock.run(async () => {
        const deleted = await readJSON(this.deletedFile,{});
        await writeJSON(this.deletedFile,{...deleted,[JSON.stringify([current.projectId,runId])]:true});
      });
      return {deleted:true};
    } finally { this.deleting.delete(runId); }
  }

  async release(runId, instanceId) {
    const row = await this.#find(runId, instanceId);
    if (!row.actions?.includes('release')) throw new HostError('not_supported', 'This run cannot be released from Desktop.');
    return this.host.tool('afm_run_release', { run_id: row.run_id });
  }

  attach(endpoint) {
    let url;
    try { url = localURL(endpoint); }
    catch { throw Error('Enter a local HTTP run URL, for example http://localhost:63439/.'); }
    return this.host.tool('afm_run_attach', { endpoint: url.origin });
  }

  // What a launch would do, shown before the user confirms it.
  async preview({ projectId, kind, file, explicit, branch, worktree }) {
    const project = await this.projects.get(projectId);
    const entry = await this.#entry(project, kind, file);
    const gitInfo = await inspectGit(project.path);
    const prefs = await this.prefs.get();
    // Pipelines keep only what their flow declares; the selected project's config and env apply now.
    const policy = entry.kind === 'Pipeline' ? await effectivePolicy(entry.policy, configFilesFor(this.home, project.path)) : entry.policy;
    const effective = resolveMode({ gitInfo, policy, preference: prefs.gitMode, explicit,
      flowName: entry.flowName, branchName: branch, worktreeName: worktree });
    return { project, entry, git: gitInfo, effective };
  }

  async #entry(project, kind, file) {
    if (kind === 'Pipeline') {
      const found = (await this.sources.list()).flatMap(source => source.entries).find(entry => entry.id === file);
      if (!found) throw new HostError('invalid', 'Unknown pipeline.');
      return { kind, id: found.id, flowName: found.flowName, policy: found.policy };
    }
    const flow = (await projectFlows(this.home, project.path)).find(item => item.file === file);
    if (!flow) throw new HostError('invalid', 'Flow not found in this project.');
    return { kind: 'Flow', id: flow.file, flowName: flow.name, policy: flow.policy };
  }

  // Starts a run once per requestId, even across retries, window reloads and Desktop restarts.
  // The same id with different parameters is refused, also while the first call is still running.
  async start(request) {
    if (!/^[A-Za-z0-9._:-]{1,128}$/.test(request?.requestId ?? '')) throw new HostError('invalid', 'A request id is required.');
    const fingerprint = fingerprintOf(request);
    const running = this.inflight.get(request.requestId);
    if (running) {
      if (running.fingerprint !== fingerprint) throw new HostError('request_conflict', 'This request id was already used for a different launch.');
      return running.work;
    }
    const work = this.#start(request, fingerprint).finally(() => this.inflight.delete(request.requestId));
    this.inflight.set(request.requestId, { fingerprint, work });
    return work;
  }

  async #start(request, fingerprint) {
    const { requestId, projectId, kind, file, explicit, branch, worktree, task, confirmHostRestart = false } = request;
    const seen = (await this.#ledger())[requestId];
    if (seen && seen.fingerprint !== fingerprint) throw new HostError('request_conflict', 'This request id was already used for a different launch.');
    // A retry resends exactly what the first attempt sent: flows, settings and Git state may have changed since.
    if (seen?.hostArgs) return this.#send(requestId, seen.hostArgs, seen.effective, null, confirmHostRestart);

    const plan = await this.preview({ projectId, kind, file, explicit, branch, worktree });
    if (plan.effective.blocked) throw new HostError('invalid', plan.effective.blocked);

    let undo = null;
    if (plan.effective.mode === 'branch' && !seen?.branchCreated) {
      await this.#updateLedger(all => ({ ...all, [requestId]: { fingerprint, at: new Date().toISOString(), projectId, branchCreated: false } }));
      undo = await prepareBranch(plan.project.path, plan.effective.branch);
      await this.#updateLedger(all => ({ ...all, [requestId]: { ...all[requestId], branchCreated: true } }));
    }
    const hostArgs = {
      request_id: requestId, project: plan.project.id, mode: 'dashboard',
      ...(kind === 'Pipeline' ? { pipeline: plan.entry.id } : { flow: plan.entry.id }),
      ...(plan.effective.mode === 'worktree' ? { worktree: plan.effective.worktree } : {}),
      ...(plan.effective.noWorktree ? { no_worktree: true } : {}),
      ...(task ? { task } : {}),
    };
    const gitStart = await captureRunGit(plan).catch(() => null);
    await this.#updateLedger(all => ({ ...all, [requestId]: { ...all[requestId], fingerprint, at: all[requestId]?.at ?? new Date().toISOString(),
      projectId, projectRoot: plan.project.path, hostArgs, effective: plan.effective, gitStart } }));
    return this.#send(requestId, hostArgs, plan.effective, undo, confirmHostRestart);
  }

  async #send(requestId, hostArgs, effective, undo, confirmHostRestart) {
    try {
      const launch = await this.#startOnHost(hostArgs, confirmHostRestart);
      await this.#updateLedger(all => ({ ...all, [requestId]: { ...all[requestId], launchId: launch.launch_id, instanceId: launch.instance_id } }));
      return { ...launch, effective };
    } catch (error) {
      // A refused request never started anything, so give the branch and the request id back.
      if (REFUSALS.has(error.code) && error.code !== 'request_conflict') {
        await undo?.().catch(() => {});
        await this.#updateLedger(all => { const { [requestId]: _dropped, ...rest } = all; return rest; });
      }
      throw error;
    }
  }

  async waitForLaunch(launch) {
    const id = launch.launch_id;
    if (!/^[a-f0-9]{64}$/.test(id)) throw Error('AFM returned an invalid launch ID.');
    const server = path.join(this.home, '.afm', 'serve');
    const receiptPath = path.join(server, 'launches', id + '.json');
    const logPath = path.join(server, 'logs', id + '.log');
    const deadline = Date.now() + 90000;
    while (Date.now() < deadline) {
      const current = await readJSON(receiptPath, launch);
      if (current.state === 'ready' || (current.state === 'exited' && current.run_id)) return {...launch, state: current.state, run_id: current.run_id, instance_id: current.instance_id};
      if (['failed', 'lost', 'exited'].includes(current.state)) {
        const missing = await missingLaunchSecrets(logPath);
        return {...launch, state: current.state, logPath, message: missing.length
          ? 'Missing environment variables: ' + missing.join(', ') + '. Add them in Settings → Environment or in .afm/secrets.env.'
          : 'AFM exited before the run was ready. See the launch log.'};
      }
      await delay(250);
    }
    return {...launch, state: 'pending', logPath, message: 'AFM is still starting. The run will appear in the project when ready.'};
  }

  // The host reads serve.yaml once at startup, so a project or pipeline registered since then is unknown
  // to it. Restart the host only when it says so, then repeat the same request.
  async #startOnHost(args, confirmHostRestart) {
    if (args.pipeline) {
      const config = await this.serveConfig.read();
      if (config.pipelines.some(item => item.id === args.pipeline && item.options)) {
        await this.host.connect();
        if (this.host.configStale(this.serveConfig.file)) await this.host.restart({confirmExternal: confirmHostRestart});
      }
    }
    try { return await this.host.tool('afm_run_start', args); }
    catch (error) {
      if (error.code !== 'invalid' || !/not in serve\.yaml/.test(error.message)) throw error;
      await this.host.restart({ confirmExternal: confirmHostRestart });
      return this.host.tool('afm_run_start', args);
    }
  }
}


// The log can contain provider diagnostics. Only extract names from the known AFM preflight error.
async function missingLaunchSecrets(file) {
  try {
    const log = await open(file);
    try {
      const prefix = Buffer.alloc(16384);
      const {bytesRead} = await log.read(prefix);
      const text = prefix.toString('utf8', 0, bytesRead);
      return [...new Set([...text.matchAll(/^\s*- secret:[^\n]*: secret not found: env:([A-Za-z_][A-Za-z0-9_]*) \(checked secrets file and process env\)/gm)]
        .map(match => match[1]))];
    } finally { await log.close(); }
  } catch { return []; }
}

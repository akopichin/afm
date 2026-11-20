import { createHash } from 'node:crypto';
import path from 'node:path';
import { open } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';
import { HostError, dashboardURL } from './host.js';
import { belongsTo } from './projects.js';
import { configFilesFor, effectivePolicy, inspectGit, prepareBranch, resolveMode } from './git-mode.js';
import { projectFlows } from './sources.js';

const MAX_LEDGER = 200;
const FINISHED = ['finished', 'failed', 'interrupted'];
const REFUSALS = new Set(['external_host', 'invalid', 'conflict', 'limit_running', 'limit_starting', 'request_conflict']);

// A run as the UI shows it: identity, an honest state, and only the operations the host allows now.
export function runView(row, ledger = {}) {
  const alive = ['active', 'active_orphaned'].includes(row.liveness);
  const actions = row.actions ?? [];
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
    origin: ledger[row.launch?.launch_id] ? 'desktop' : 'external', launchState: row.launch?.state ?? null,
    status: row.run_status ?? null, attention: Boolean(row.attention_required), state,
    can: { open: dashboard, cancel: actions.includes('cancel'), release: actions.includes('release'),
      attach: actions.includes('attach'), report: actions.includes('report') },
  };
}

function fingerprintOf({ projectId, kind, file, explicit, branch, worktree, task }) {
  return createHash('sha256').update(JSON.stringify([projectId, kind, file, explicit ?? null, branch ?? null, worktree ?? null, task ?? ''])).digest('hex');
}

export class Runs {
  constructor({ home, host, projects, sources, prefs, serveConfig }) {
    Object.assign(this, { home, host, projects, sources, prefs, serveConfig });
    this.ledgerFile = path.join(desktopDir(home), 'launches.json');
    this.ledgerLock = new Mutex();
    this.inflight = new Map();
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
    const issued = await this.#issued();
    return (await this.host.runs()).map(row => runView(row, issued));
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

  async cancel(runId, instanceId) {
    const row = await this.#find(runId, instanceId);
    if (!row.actions?.includes('cancel')) throw new HostError('not_supported', 'This run cannot be cancelled from Desktop.');
    return this.host.tool('afm_run_cancel', { run_id: row.run_id });
  }

  async release(runId, instanceId) {
    const row = await this.#find(runId, instanceId);
    if (!row.actions?.includes('release')) throw new HostError('not_supported', 'This run cannot be released from Desktop.');
    return this.host.tool('afm_run_release', { run_id: row.run_id });
  }

  attach(endpoint) { return this.host.tool('afm_run_attach', { endpoint }); }

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
    await this.#updateLedger(all => ({ ...all, [requestId]: { ...all[requestId], fingerprint, at: all[requestId]?.at ?? new Date().toISOString(),
      projectId, hostArgs, effective: plan.effective } }));
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

import { Costs, FETCH_LIMIT, cancellable } from './costs.js';
import { localURL } from './host.js';

const NOTHING_OBSERVED = { notices: [], status: {}, unavailable: [] };

// Stage statuses that wait for a person, with the notice each one produces.
function stageNotice(stage) {
  if (stage.status === 'awaiting_approval') return { kind: 'approval', text: 'Approve plan' };
  if (stage.status === 'awaiting_user_input') return { kind: 'question', text: 'Answer question' };
  if (stage.pre_completion_blocked || stage.awaiting_assistants_blocked) return { kind: 'decision', text: 'Decision needed' };
  if (stage.status === 'failed') return { kind: 'failed', text: 'Stage failed' };
  return null;
}

// Reads the run's own status, but only after the endpoint proves it is still the run we mean.
export async function readRunStatus(run, request = fetch) {
  const origin = localURL(run.apiUrl).origin;
  const runtime = await getJSON(origin, '/api/runtime', request);
  if (runtime.run_id !== run.runId || runtime.instance_id !== run.instanceId) throw Error('Run changed.');
  const status = await getJSON(origin, '/api/status', request);
  if (status.run_id && status.run_id !== run.runId) throw Error('Run changed.');
  return status;
}

async function getJSON(origin, route, request, missingIsEmpty = false) {
  const res = await request(origin + route, {redirect:'error', signal:AbortSignal.timeout(3000)});
  if (missingIsEmpty && res.status === 404) return null;
  if (!res.ok) throw Error('Run unavailable.');
  return res.json();
}

export async function fetchAttention(run, request = fetch, previous = []) {
  return noticesOf(run, await readRunStatus(run, request), request, previous);
}

async function noticesOf(run, status, request, previous = []) {
  const notices = [];
  for (const stage of status.stages ?? []) {
    const notice = stageNotice(stage);
    if (!notice) continue;
    const observedAt = stage.updated_at || '';
    let requestAt = stage.pre_completion?.round_id && notice.kind === 'decision' ? stage.pre_completion.round_id : observedAt;
    if (notice.kind === 'question') {
      const old = previous.find(item => item.stageId === stage.id && item.kind === 'question' && item.observedAt === observedAt);
      requestAt = old?.requestAt;
      if (requestAt === undefined) {
        try {
          const origin = localURL(run.apiUrl).origin;
          const dialog = await getJSON(origin, '/api/stages/' + encodeURIComponent(stage.id) + '/dialog', request, true);
          const question = Array.isArray(dialog) ? dialog.findLast(item => item.id && item.phase && item.answer == null) : null;
          // A host without a dialog endpoint still shows its in-app notice.
          if (!Array.isArray(dialog)) requestAt = null;
          if (question) requestAt = JSON.stringify([question.phase, question.id, question.ts ?? '']);
        } catch { /* Retry while the question remains actionable. */ }
      }
    }
    notices.push({id:`${run.key}:${stage.id}`, runKey:run.key, stageId:stage.id, stageName:stage.name || stage.id, requestAt, observedAt, ...notice});
  }
  return notices;
}

// The one background loop of the app. It builds a single snapshot of projects, runs and notices,
// sleeps while the window is hidden, never overlaps itself, and reports only real changes.
export class Workspace {
  // A hidden window still watches open stage browsers, more slowly, so a finished stage frees its page.
  // `watchActive` says whether a watch task needs the runs the host does not flag; without one they are not read.
  constructor({ desktop, interval = 3000, hiddenInterval = interval * 5, request = fetch, stageBrowsers = null, watchHidden = () => false, watchActive = () => false, costs = null }) {
    Object.assign(this, { desktop, interval, hiddenInterval, stageBrowsers, watchHidden, watchActive, costs });
    // Closing aborts it: requests of a closed Workspace stop, in flight or not.
    this.lifetime = new AbortController();
    this.request = cancellable(request, this.lifetime.signal);
    this.listeners = new Set();
    this.pollListeners = new Set();
    this.snapshot = null;
    this.signature = '';
    this.order = new Map();
    this.lastReads = new Map();
    this.sequence = 0;
    this.visible = false;
    this.inflight = null;
    this.timer = null;
    this.closed = false;
  }

  onChange(listener) { this.listeners.add(listener); return () => this.listeners.delete(listener); }

  // Called after every refresh, changed or not, so deadlines and summary times are met on an idle system.
  onPoll(listener) { this.pollListeners.add(listener); return () => this.pollListeners.delete(listener); }

  // Starts the hidden-window poll if `watchHidden` just became true.
  wake() { this.#scheduleNext(); }

  get() { return this.snapshot ?? this.refresh(); }

  // Hidden windows do no discovery unless `watchHidden` asks for it (system notifications); showing one refreshes at once.
  setVisible(visible) {
    this.visible = visible;
    this.costs?.setVisible(visible && !this.closed);
    clearTimeout(this.timer);
    this.timer = null;
    if (visible && !this.closed) this.refresh().catch(() => {});
    else this.#scheduleNext();
  }

  #scheduleNext() {
    if (this.closed || this.timer) return;
    const delay = this.visible ? this.interval : this.stageBrowsers?.entries.size || this.watchHidden() ? this.hiddenInterval : null;
    if (delay !== null) this.timer = setTimeout(() => { this.timer = null; this.refresh().catch(() => {}); }, delay);
  }

  refresh() {
    if (this.closed) return Promise.resolve(this.snapshot);
    if (!this.inflight) {
      this.inflight = this.#build().finally(() => {
        this.inflight = null;
        this.#scheduleNext();
      });
    }
    return this.inflight;
  }

  async #build() {
    const overview = await this.desktop.overview();
    // Costs work of this cycle follows the demand it starts with: once that ends it stops, even if Costs is wanted again.
    const costs = this.costs?.demand ?? AbortSignal.abort();
    const runs = [...overview.projects.flatMap(project => project.runs), ...overview.unassigned];
    const sets = this.#classify(runs);
    // While the host is unreachable the last known notices stay, so a reconnect does not reorder them.
    const statuses = overview.host.error ? new Map() : await this.#readStatuses(sets, overview, costs);
    const { attention, observed } = overview.host.error ? { attention: this.snapshot?.attention ?? [], observed: this.snapshot?.observed ?? NOTHING_OBSERVED }
      : await this.#read(sets, statuses);
    if (!costs.aborted) {
      if (overview.host.error) this.costs.markUnreachable();
      else await this.costs.observe(this.#costInventory(overview), statuses, costs);
    }
    if (this.closed) return this.snapshot;
    await this.stageBrowsers?.reconcile(runs);
    const snapshot = { host: overview.host, projects: overview.projects, unassigned: overview.unassigned,
      candidates: overview.candidates, attention, observed, stageBrowsers: this.stageBrowsers?.list() ?? [] };
    const signature = JSON.stringify(snapshot);
    const changed = signature !== this.signature;
    if (changed) { this.signature = signature; this.snapshot = snapshot; }
    // The watch sees the poll first, so the notifier knows which events it announces.
    if (this.snapshot) for (const listener of this.pollListeners) listener(this.snapshot);
    if (changed) for (const listener of this.listeners) listener(snapshot);
    return this.snapshot;
  }

  // `flagged` runs need a person; `watchable` ones only matter to watch tasks, and are `watched` (read) while one is active.
  #classify(runs) {
    const flagged = runs.filter(run => run.attention && (run.state === 'running' || run.can?.open));
    const watchable = runs.filter(run => run.apiLive && !flagged.includes(run) && (run.attention || run.ownership === 'discovered'));
    return { flagged, watchable, watched: this.watchActive() ? watchable : [] };
  }

  #costInventory(overview) {
    return Costs.inventoryOf([...overview.projects.flatMap(project => project.runs.map(run => ({ run, projectId: project.id }))),
      ...overview.unassigned.map(run => ({ run, projectId: null }))]);
  }

  // One verified status read per run and cycle, shared by notices, watch tasks and Costs; null where the read failed.
  // A run read only for Costs stops with the Costs demand, a run with notices or a watch keeps its read.
  async #readStatuses({ flagged, watched }, overview, costs) {
    const reads = new Map([...flagged, ...watched].map(run => [run.key, { run, request: this.request }]));
    if (!costs.aborted) {
      const forCosts = cancellable(this.request, costs);
      for (const { run, readable } of this.#costInventory(overview)) if (readable && !reads.has(run.key)) reads.set(run.key, { run, request: forCosts });
    }
    const statuses = new Map();
    const list = [...reads.values()];
    for (let i = 0; i < list.length; i += FETCH_LIMIT) {
      await Promise.all(list.slice(i, i + FETCH_LIMIT).map(async ({ run, request }) => {
        try { statuses.set(run.key, await readRunStatus(run, request)); }
        catch { statuses.set(run.key, null); }
      }));
    }
    return statuses;
  }

  // Precise notices for runs the host says need a person. A failed read keeps what was known, so one
  // dropped request neither removes nor re-adds a notice; a run never read successfully is `unavailable`.
  // Watch tasks also need live runs the host does not flag: discovered ones (the host never reports their
  // attention) and API-only ones. Those land in `observed`, apart from `attention`, so they start no banners,
  // and are read only while a watch task is active; otherwise they are `unavailable` too: unknown, not empty.
  async #read({ flagged, watchable, watched }, statuses) {
    const found = new Map();
    const reads = [...flagged, ...watched];
    // Notices may fetch a dialog each, so they run in bounded batches: slow dialogs must not add up.
    for (let i = 0; i < reads.length; i += FETCH_LIMIT) {
      await Promise.all(reads.slice(i, i + FETCH_LIMIT).map(async run => {
        const known = this.lastReads.get(run.key);
        const status = statuses.get(run.key);
        try {
          if (!status) throw Error('Run unavailable.');
          found.set(run.key, { status: status.run_status ?? null, notices: await noticesOf(run, status, this.request, known?.notices) });
        } catch {
          if (known) found.set(run.key, known);
        }
      }));
    }
    this.lastReads = found;
    const noticesFor = run => found.get(run.key)?.notices ?? [];
    // The status read that found the notices also tells a discovered run's outcome, so no second fetch is needed.
    const status = Object.fromEntries(watched.filter(run => found.has(run.key)).map(run => [run.key, found.get(run.key).status]));
    return { attention: this.#ordered(flagged.flatMap(noticesFor)),
      observed: { notices: watched.flatMap(noticesFor), status, unavailable: [...flagged, ...watchable].filter(run => !found.has(run.key)).map(run => run.key) } };
  }

  // Notices keep the order they first appeared in, so a poll never reshuffles the queue.
  #ordered(items) {
    const live = new Set(items.map(item => item.id));
    for (const id of this.order.keys()) if (!live.has(id)) this.order.delete(id);
    for (const item of items) if (!this.order.has(item.id)) this.order.set(item.id, ++this.sequence);
    return items.map(item => ({ ...item, seq: this.order.get(item.id) })).sort((a, b) => a.seq - b.seq);
  }

  close() {
    this.closed = true;
    this.lifetime.abort();
    this.costs?.setVisible(false);
    clearTimeout(this.timer);
    this.listeners.clear();
    this.pollListeners.clear();
  }
}

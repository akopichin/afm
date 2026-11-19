import { localURL } from './host.js';

const FETCH_LIMIT = 6;

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
  const get = async path => {
    const res = await request(origin + path, { redirect: 'error', signal: AbortSignal.timeout(3000) });
    if (!res.ok) throw Error('Run unavailable.');
    return res.json();
  };
  const runtime = await get('/api/runtime');
  if (runtime.run_id !== run.runId || runtime.instance_id !== run.instanceId) throw Error('Run changed.');
  const status = await get('/api/status');
  if (status.run_id && status.run_id !== run.runId) throw Error('Run changed.');
  return status;
}

export async function fetchAttention(run, request = fetch) {
  const status = await readRunStatus(run, request);
  return (status.stages ?? []).flatMap(stage => {
    const notice = stageNotice(stage);
    return notice ? [{ id: `${run.key}:${stage.id}`, runKey: run.key, stageId: stage.id, stageName: stage.name || stage.id, ...notice }] : [];
  });
}

// The one background loop of the app. It builds a single snapshot of projects, runs and notices,
// sleeps while the window is hidden, never overlaps itself, and reports only real changes.
export class Workspace {
  // A hidden window still watches open stage browsers, more slowly, so a finished stage frees its page.
  constructor({ desktop, interval = 3000, hiddenInterval = interval * 5, request = fetch, stageBrowsers = null }) {
    Object.assign(this, { desktop, interval, hiddenInterval, request, stageBrowsers });
    this.listeners = new Set();
    this.snapshot = null;
    this.signature = '';
    this.order = new Map();
    this.lastItems = new Map();
    this.sequence = 0;
    this.visible = false;
    this.inflight = null;
    this.timer = null;
    this.closed = false;
  }

  onChange(listener) { this.listeners.add(listener); return () => this.listeners.delete(listener); }

  get() { return this.snapshot ?? this.refresh(); }

  // Hidden windows do no discovery; showing one refreshes at once.
  setVisible(visible) {
    this.visible = visible;
    clearTimeout(this.timer);
    this.timer = null;
    if (visible && !this.closed) this.refresh().catch(() => {});
    else this.#scheduleNext();
  }

  #scheduleNext() {
    if (this.closed || this.timer) return;
    const delay = this.visible ? this.interval : this.stageBrowsers?.entries.size ? this.hiddenInterval : null;
    if (delay !== null) this.timer = setTimeout(() => { this.timer = null; this.refresh().catch(() => {}); }, delay);
  }

  refresh() {
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
    const runs = [...overview.projects.flatMap(project => project.runs), ...overview.unassigned];
    // While the host is unreachable the last known notices stay, so a reconnect does not reorder them.
    const attention = overview.host.error ? this.snapshot?.attention ?? [] : await this.#attention(runs);
    await this.stageBrowsers?.reconcile(runs);
    const snapshot = { host: overview.host, projects: overview.projects, unassigned: overview.unassigned,
      candidates: overview.candidates, attention, stageBrowsers: this.stageBrowsers?.list() ?? [] };
    const signature = JSON.stringify(snapshot);
    if (signature !== this.signature) {
      this.signature = signature;
      this.snapshot = snapshot;
      for (const listener of this.listeners) listener(snapshot);
    }
    return this.snapshot;
  }

  // Precise notices for runs the host says need a person. A failed read keeps what was known, so one
  // dropped request neither removes nor re-adds a notice.
  async #attention(runs) {
    const flagged = runs.filter(run => run.attention && run.state === 'running');
    const found = new Map();
    for (let i = 0; i < flagged.length; i += FETCH_LIMIT) {
      await Promise.all(flagged.slice(i, i + FETCH_LIMIT).map(async run => {
        try { found.set(run.key, await fetchAttention(run, this.request)); }
        catch { found.set(run.key, this.lastItems.get(run.key) ?? []); }
      }));
    }
    this.lastItems = found;
    const items = flagged.flatMap(run => found.get(run.key));
    const live = new Set(items.map(item => item.id));
    for (const id of this.order.keys()) if (!live.has(id)) this.order.delete(id);
    for (const item of items) if (!this.order.has(item.id)) this.order.set(item.id, ++this.sequence);
    return items.map(item => ({ ...item, seq: this.order.get(item.id) })).sort((a, b) => a.seq - b.seq);
  }

  close() {
    this.closed = true;
    clearTimeout(this.timer);
    this.listeners.clear();
  }
}

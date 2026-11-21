import { localURL } from './host.js';

// Requests in flight at once, for status reads and analytics alike.
export const FETCH_LIMIT = 6;

// `request` bound to `signal`: nothing starts once it is aborted, and aborting it cancels the request and the reading
// of its JSON body, which is therefore read here. The link ends with the read: AbortSignal.any on a long-lived signal leaks in Node 22.
export function cancellable(request, signal) {
  return async (url, options = {}) => {
    signal.throwIfAborted();
    const own = new AbortController();
    const abort = () => own.abort(signal.reason);
    signal.addEventListener('abort', abort);
    const cancelled = options.signal ? AbortSignal.any([own.signal, options.signal]) : own.signal;
    try {
      const res = await request(url, { ...options, signal: cancelled });
      if (!res.ok) return res;
      const body = await readJSON(res, cancelled);
      return { ok: true, status: res.status, json: async () => body };
    } finally { signal.removeEventListener('abort', abort); }
  };
}

// The JSON body, cut off once `signal` aborts. fetch's own signal no longer reaches a body after fetch's request
// object is garbage-collected (undici), so the body goes through a pipe that `signal` aborts.
function readJSON(res, signal) {
  if (!res.body) return res.json();
  return new Response(res.body.pipeThrough(new TransformStream(), { signal })).json();
}

const num = value => (Number.isFinite(value) ? value : 0);
const parseTime = value => {
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? null : ms;
};

// Time figures keep their server anchors instead of a value at the poll, so an unchanged run is an unchanged
// payload; the renderer adds `now - since` while an anchor is open (see liveMs) and stops when the data is stale.
const anchored = (accumulated, since) => ({ ms: num(accumulated), since: parseTime(since) });

export function liveMs({ ms, since }, now) {
  const open = Array.isArray(since) ? since : since === null ? [] : [since];
  return ms + open.reduce((sum, anchor) => sum + Math.max(0, now - anchor), 0);
}

// Work done by stages, summed. It can exceed Elapsed (parallel stages) and AFM has no run-level "running"
// field, so it is a different figure; unknown as soon as one started stage has an incomplete ledger.
function stageWork(stages) {
  let ms = 0;
  const since = [];
  for (const stage of stages) {
    const timing = stage.timing;
    // A missing ledger is not a stage that never started.
    if (!timing) return null;
    if (!timing.started) continue;
    if (!timing.complete) return null;
    ms += num(timing.work_ms);
    if (timing.open?.kind === 'work' && parseTime(timing.open.since) !== null) since.push(parseTime(timing.open.since));
  }
  return { ms, since };
}

// Money exists in an entry only when AFM allows it; Desktop's own switch is applied on the way out.
function money(cost) {
  if (!cost) return null;
  return { usd: num(cost.estimated_cost_usd), coverage: cost.coverage ?? 'none', unpriced: num(cost.unpriced), unmetered: num(cost.unmetered) };
}

function priceMode(status) {
  if (!status.run_cost || !status.accounting) return 'unavailable';
  return status.accounting.show_money ? 'visible' : 'afm-hidden';
}

// One run's figures from its verified /api/status. run_cost already contains the overhead and every stage, so
// stage and overhead rows are only for display and never added to it.
export function costEntry(run, projectId, status) {
  const price = priceMode(status);
  const accounted = price !== 'unavailable';
  const stages = (status.stages ?? []).flatMap(stage => {
    const work = stage.timing?.started && stage.timing.complete ? num(stage.timing.work_ms) : null;
    if (!stage.cost && work === null) return [];
    const row = { id: stage.id, name: stage.name || stage.id, status: stage.status, workMs: work,
      tokens: accounted && stage.cost ? num(stage.cost.total_tokens) : null };
    if (price === 'visible' && stage.cost) row.usd = num(stage.cost.estimated_cost_usd);
    return [row];
  });
  const overhead = accounted && status.run_overhead_cost ? { tokens: num(status.run_overhead_cost.total_tokens) } : null;
  if (overhead && price === 'visible') overhead.usd = num(status.run_overhead_cost.estimated_cost_usd);
  const entry = {
    key: run.key, projectId, name: run.name || run.runId, stale: false, runStatus: status.run_status ?? run.status,
    startedAt: parseTime(status.started_at),
    elapsed: anchored(status.elapsed_accumulated_ms, status.elapsed_since),
    idle: anchored(status.idle_accumulated_ms, status.idle_since),
    stageWork: stageWork(status.stages ?? []),
    tokens: accounted ? num(status.run_cost.total_tokens) : null,
    price, coverageIssues: accounted ? (status.coverage_issues ?? []).length : 0,
    stages, overhead,
    analytics: status.capabilities?.usage_analytics ? status.analytics?.revision ?? null : null,
  };
  if (price === 'visible') entry.money = money(status.run_cost);
  return entry;
}

// Removes every money field; used when the person hides prices in Desktop.
function withoutMoney(entry) {
  const { money: _money, ...rest } = entry;
  return { ...rest, price: entry.price === 'unavailable' ? 'unavailable' : 'desktop-hidden',
    stages: entry.stages.map(({ usd: _usd, ...stage }) => stage),
    overhead: entry.overhead && { tokens: entry.overhead.tokens } };
}

const withoutMoneyRow = ({ usd: _usd, ...row }) => row;

async function getJSON(origin, route, request) {
  const res = await request(origin + route, { redirect: 'error', signal: AbortSignal.timeout(3000) });
  if (!res.ok) throw Error('Run unavailable.');
  return res.json();
}

// Model rows of the run's usage analytics. The server already left out what the run's price mode hides.
export async function readModels(run, request = fetch) {
  const origin = localURL(run.apiUrl).origin;
  const runtime = await getJSON(origin, '/api/runtime', request);
  if (runtime.run_id !== run.runId || runtime.instance_id !== run.instanceId) throw Error('Run changed.');
  const answer = await getJSON(origin, '/api/usage/analytics?view=breakdown&group=model&scope=all', request);
  return (answer.breakdown?.rows ?? []).filter(row => !row.agent?.outside_run_total).map(row => {
    const item = { key: row.model?.key ?? row.key, label: row.label || row.model?.model || row.key, timeMs: row.agent_time?.duration_ms ?? null,
      tokens: row.tokens?.counts ? num(row.tokens.counts.total) : null };
    if (row.money) item.usd = row.money.estimated_cost_usd ?? null;
    return item;
  });
}

// Current time, tokens and money of every active run. It is fed by the Workspace loop, which already holds the
// verified status reads, and keeps nothing for runs that left the inventory.
export class Costs {
  #demand = new AbortController();

  constructor({ request = fetch, now = Date.now, hidePrices = false, history = null } = {}) {
    Object.assign(this, { request, now, hidePrices, history });
    this.entries = new Map();
    this.seenAt = new Map();
    this.models = new Map();
    this.listeners = new Set();
    this.watched = false;
    // The Workspace passes on whether its window is visible.
    this.visible = false;
    this.closed = false;
    this.signature = '';
    this.#updateDemand();
  }

  onChange(listener) { this.listeners.add(listener); return () => this.listeners.delete(listener); }

  // Costs is read only on Home and on its own screen of a visible, open window; elsewhere the loop adds no requests.
  setWatched(watched) { this.watched = watched === true; this.#updateDemand(); }

  setVisible(visible) { this.visible = visible === true; this.#updateDemand(); }

  // Aborted as soon as Costs is no longer wanted: work started under it stops, even if Costs is wanted again at once.
  get demand() { return this.#demand.signal; }

  #updateDemand() {
    const wanted = this.watched && this.visible && !this.closed;
    if (!wanted) this.#demand.abort();
    else if (this.#demand.signal.aborted) this.#demand = new AbortController();
  }

  setHidePrices(hide) {
    if (this.hidePrices === (hide === true)) return;
    this.hidePrices = hide === true;
    this.models.clear();
    if (this.hidePrices) this.history?.dropMoney().catch(() => {});
    this.#emit();
  }

  // Every run the inventory still lists once, in inventory order (the first project that lists it owns it).
  // `readable` runs answer HTTP now; the others keep their last figures, stale.
  static inventoryOf(snapshotRuns) {
    const seen = new Map();
    for (const { run, projectId } of snapshotRuns) {
      if (run.state === 'finished' || seen.has(run.key)) continue;
      seen.set(run.key, { run, projectId, readable: ['running', 'api-only'].includes(run.state) && Boolean(run.apiUrl) });
    }
    return [...seen.values()];
  }

  // The host cannot be reached: nothing is dropped, every clock stops at its last good read.
  markUnreachable() {
    for (const [key, entry] of this.entries) if (!entry.stale) this.entries.set(key, { ...entry, stale: true });
    this.history?.markOutage([...this.entries.keys()], this.now()).catch(() => {});
    this.#emit();
  }

  // `statuses` maps a run key to its verified status, or null when the read failed. `demand` is the one the
  // caller's cycle started with; once it is aborted nothing more is read, stored or reported.
  async observe(inventory, statuses, demand = this.demand) {
    if (demand.aborted) return;
    const observedAt = this.now();
    const listed = new Set(inventory.map(({ run }) => run.key));
    for (const key of [...this.entries.keys()]) if (!listed.has(key)) { this.entries.delete(key); this.models.delete(key); this.seenAt.delete(key); }
    for (const { run, projectId } of inventory) {
      const status = statuses.get(run.key);
      const old = this.entries.get(run.key);
      if (status) { this.entries.set(run.key, costEntry(run, projectId, status)); this.seenAt.set(run.key, observedAt); }
      else if (old && !old.stale) this.entries.set(run.key, { ...old, stale: true });
    }
    if (!demand.aborted) await this.history?.record([...this.entries.values()], observedAt, { hidePrices: this.hidePrices }).catch(() => {});
    await this.#readModels(inventory.filter(item => item.readable), demand);
    if (!demand.aborted) this.#emit();
  }

  // Analytics are fetched only while Costs is wanted and when a run's revision changed, a few at a time.
  async #readModels(items, demand) {
    const due = items.filter(({ run }) => {
      const entry = this.entries.get(run.key);
      return entry && !entry.stale && entry.analytics && this.models.get(run.key)?.revision !== entry.analytics;
    });
    const request = cancellable(this.request, demand);
    for (let i = 0; i < due.length; i += FETCH_LIMIT) {
      await Promise.all(due.slice(i, i + FETCH_LIMIT).map(async ({ run }) => {
        const revision = this.entries.get(run.key).analytics;
        try {
          const rows = await readModels(run, request);
          // An answer that arrives after Costs stopped is dropped.
          if (!demand.aborted) this.models.set(run.key, { revision, rows });
        } catch { /* A failed analytics read never hides the run itself. */ }
      }));
    }
  }

  // The runs that count at `to`: those Desktop observes now (when `to` is current, or once history had seen them) and
  // finished ones that were still followed at that moment. Each carries what a row needs without the live view.
  #subjects(to) {
    const current = to >= this.now() - 1000;
    const subjects = [];
    for (const entry of this.entries.values()) {
      if (current || this.history.presentAt(entry.key, to, true)) subjects.push({ key: entry.key, entry, name: entry.name, projectId: entry.projectId, startedAt: entry.startedAt, finished: false });
    }
    if (!current) {
      for (const key of this.history.keys()) {
        if (this.entries.has(key) || !this.history.presentAt(key, to, false)) continue;
        const run = this.history.run(key);
        subjects.push({ key, entry: null, name: run.name, projectId: run.projectId, startedAt: run.startedAt, finished: true });
      }
    }
    return subjects;
  }

  // Growth of every run between `from` and `to`, the same sliced into `buckets` for the chart, and the since-start
  // figures as of `to`. Money is read only for runs whose prices Desktop may show; a figure with no readings is null.
  async period(from, to, buckets = 0) {
    if (!this.history || !(to > from)) return { available: false, runs: {}, series: null };
    await this.history.load();
    const show = !this.hidePrices;
    // A finished run has no live price rule: what is stored was stored while its price was visible.
    const priced = subject => show && (!subject.entry || subject.entry.price === 'visible');
    const fields = { elapsed: 'elapsedMs', idle: 'idleMs', work: 'workMs', tokens: 'tokens', usd: 'usd' };
    const read = (subject, field, start, end) => (field === 'usd' && !priced(subject) ? null : this.history.delta(subject.key, fields[field], start, end));
    const runs = {};
    const step = (to - from) / Math.max(1, buckets);
    const slices = Array.from({ length: buckets }, (_, i) => [from + i * step, from + (i + 1) * step]);
    for (const subject of this.#subjects(to)) {
      const run = runs[subject.key] = Object.fromEntries(Object.keys(fields).map(name => [name, read(subject, name, from, to)]));
      run.meta = { name: subject.name, projectId: subject.projectId, startedAt: subject.startedAt, finished: subject.finished };
      // Since-start figures at `to` and when they were really read: the slider position is not an observation time.
      run.asOf = { elapsed: this.history.valueAt(subject.key, 'elapsedMs', to), idle: this.history.valueAt(subject.key, 'idleMs', to),
        work: this.history.valueAt(subject.key, 'workMs', to), tokens: this.history.valueAt(subject.key, 'tokens', to),
        usd: priced(subject) ? this.history.valueAt(subject.key, 'usd', to) : null, ...this.history.observation(subject.key, to) };
      if (!buckets) continue;
      // The values the slices lead up to come from the same snapshots: a reading newer than the history (one not
      // stored yet, or a clock still running) would move every earlier point of a cumulative line.
      run.anchors = { cost: run.asOf.usd, work: run.asOf.work };
      run.buckets = { cost: show ? slices.map(([a, b]) => read(subject, 'usd', a, b)) : null,
        tokens: slices.map(([a, b]) => read(subject, 'tokens', a, b)), time: slices.map(([a, b]) => read(subject, 'elapsed', a, b)),
        work: slices.map(([a, b]) => read(subject, 'work', a, b)) };
    }
    const series = buckets ? { from, to, buckets } : null;
    return { available: true, from, to, runs, series };
  }

  // The renderer's payload: only what the current price visibility allows.
  view() {
    const runs = [...this.entries.values()].map(entry => {
      const shown = this.hidePrices ? withoutMoney(entry) : entry;
      const rows = this.models.get(entry.key)?.rows;
      // A stale run's clocks stop at its last good read.
      return { ...shown, staleSince: entry.stale ? this.seenAt.get(entry.key) : null,
        models: rows ? rows.map(row => (this.hidePrices || entry.price !== 'visible' ? withoutMoneyRow(row) : row)) : null };
    });
    return { hidePrices: this.hidePrices, runs };
  }

  #emit() {
    const view = this.view();
    const signature = JSON.stringify(view);
    if (signature === this.signature) return;
    this.signature = signature;
    for (const listener of this.listeners) listener(view);
  }

  close() { this.closed = true; this.#updateDemand(); this.listeners.clear(); }
}

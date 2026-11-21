import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeAtomic } from './files.js';
import { liveMs } from './costs.js';

export const RETENTION_MS = 7 * 24 * 3600 * 1000;
// Longer silence than this between two snapshots is an observation gap.
export const GAP_MS = 5 * 60 * 1000;
// Within the last hour every changed snapshot is kept (at most one per SAMPLE_MS); older ones are thinned to one per bucket.
export const SAMPLE_MS = 30 * 1000;
export const MAX_SAMPLES = 2000;
const RECENT_MS = 3600 * 1000;
const BUCKET_MS = 10 * 60 * 1000;
const MAX_RUNS = 50;
const FIELDS = ['elapsedMs', 'idleMs', 'workMs', 'tokens', 'usd'];

// Aggregate snapshots of observed runs for the As of and Compare views, keyed by confirmed instance and run id so a
// reused port or new process never joins two histories. A gap or reset is kept on the snapshot after it (`gapFrom`,
// `breaks`) and a figure AFM cannot report is null in its snapshots; both sides of each are pinned, so thinning and
// caps never join snapshots that were not continuous.
export class CostsHistory {
  #runs = new Map();
  #lock = new Mutex();
  #loaded;

  constructor(home) { this.file = path.join(desktopDir(home), 'costs-history.json'); }

  load() { return (this.#loaded ??= this.#read()); }

  async #read() {
    const stored = await readJSON(this.file, {});
    for (const [key, run] of Object.entries(stored.runs ?? {})) {
      if (!Array.isArray(run?.samples) || !run.samples.length) continue;
      this.#runs.set(key, stored.version === 1 ? fromVersion1(run) : { ...run, outage: run.outage ?? null });
    }
  }

  // Records every observed run. A stale entry opens an outage that the next good snapshot closes as a gap.
  async record(entries, now, { hidePrices = false } = {}) {
    await this.load();
    let changed = false;
    // Runs being followed right now keep their history; only the others can give up a place.
    const live = new Set(entries.filter(entry => !entry.stale).map(entry => entry.key));
    for (const entry of entries) {
      const run = this.#runs.get(entry.key);
      if (entry.stale) { changed = this.#openOutage(run, now) || changed; continue; }
      if (!run && !this.#admit(live)) continue;
      changed = this.#add(entry, run, now, hidePrices) || changed;
    }
    if (this.#prune(now, live) || changed) await this.#save();
  }

  // The host or a run cannot be read: its history is unobserved from now on.
  async markOutage(keys, now) {
    await this.load();
    let changed = false;
    for (const key of keys) changed = this.#openOutage(this.#runs.get(key), now) || changed;
    if (changed) await this.#save();
  }

  // A new run needs a free place: the oldest retained run that is no longer followed gives it up. With none to give,
  // the run waits unrecorded until one finishes or goes stale, so followed runs never push each other out.
  #admit(live) {
    if (this.#runs.size < MAX_RUNS) return true;
    const victim = this.#oldestUnfollowed(live);
    if (victim === null) return false;
    this.#runs.delete(victim);
    return true;
  }

  #oldestUnfollowed(live) {
    let victim = null;
    for (const [key, run] of this.#runs) {
      if (!live.has(key) && (victim === null || run.samples.at(-1).t < this.#runs.get(victim).samples.at(-1).t)) victim = key;
    }
    return victim;
  }

  #openOutage(run, now) {
    if (!run || run.outage !== null) return false;
    run.outage = run.samples.at(-1)?.t ?? now;
    return true;
  }

  #add(entry, existing, now, hidePrices) {
    const sample = sampleOf(entry, now, hidePrices);
    const run = existing ?? { name: entry.name, projectId: entry.projectId, startedAt: entry.startedAt, samples: [], outage: null, truncated: false };
    const last = run.samples.at(-1);
    // Money the run no longer shows (AFM hides prices or accounting is off, or Desktop hides them) is removed.
    const hadMoney = sample.usd === undefined && run.samples.some(item => item.usd !== undefined);
    if (hadMoney) run.samples.forEach(forgetMoney);
    const gapFrom = run.outage ?? (last && now - last.t > GAP_MS ? last.t : null);
    if (last && gapFrom !== null) {
      sample.gapFrom = gapFrom;
      last.pin = sample.pin = true;
    } else if (last && !markBreaks(last, sample)) {
      if (FIELDS.every(field => last[field] === sample[field]) || now - last.t < SAMPLE_MS) return hadMoney;
    }
    run.outage = null;
    Object.assign(run, { name: entry.name, projectId: entry.projectId, startedAt: entry.startedAt });
    run.samples.push(sample);
    this.#thin(run, now);
    this.#runs.set(entry.key, run);
    return true;
  }

  // Older snapshots keep one per bucket (plus every pinned one next to a gap or reset), so seven days stay covered.
  // The cap drops the oldest snapshots, after which a period that needed them is partial.
  #thin(run, now) {
    const kept = [];
    for (const [index, sample] of run.samples.entries()) {
      const next = run.samples[index + 1];
      const old = now - sample.t > RECENT_MS;
      const lastInBucket = !next || Math.floor(next.t / BUCKET_MS) !== Math.floor(sample.t / BUCKET_MS);
      if (!old || sample.pin || lastInBucket || index === 0) kept.push(sample);
    }
    if (kept.length > MAX_SAMPLES) {
      run.truncated = true;
      run.samples = kept.slice(-MAX_SAMPLES);
    } else {
      run.samples = kept;
    }
  }

  // Turning prices off in Desktop removes all money from memory and disk.
  async dropMoney() {
    await this.load();
    for (const run of this.#runs.values()) run.samples.forEach(forgetMoney);
    await this.#save();
  }

  #prune(now, live = new Set()) {
    let pruned = false;
    for (const [key, run] of this.#runs) {
      const keep = run.samples.filter(sample => now - sample.t <= RETENTION_MS);
      if (keep.length === run.samples.length) continue;
      pruned = true;
      if (keep.length) Object.assign(run, { samples: keep, truncated: true });
      else this.#runs.delete(key);
    }
    // A store loaded over the cap sheds only runs no longer followed.
    while (this.#runs.size > MAX_RUNS) {
      const victim = this.#oldestUnfollowed(live);
      if (victim === null) break;
      this.#runs.delete(victim);
      pruned = true;
    }
    return pruned;
  }

  #save() {
    const runs = Object.fromEntries(this.#runs);
    // Each save rewrites the retained history, so skip indentation for this store.
    return this.#lock.run(() => writeAtomic(this.file, JSON.stringify({ version: 2, runs }) + '\n'));
  }

  keys() { return [...this.#runs.keys()]; }

  run(key) { return this.#runs.get(key) ?? null; }

  // The figure as of the last snapshot at or before `to`, or null. An earlier reading is never used instead: a figure
  // the last snapshot lacks was unknown then.
  valueAt(key, field, to) {
    return this.#runs.get(key)?.samples.findLast(sample => sample.t <= to)?.[field] ?? null;
  }

  // Whether Desktop was watching the run at `at`: a snapshot exists at or before it and it is either still being followed
  // or the last snapshot is not older than one observation gap. Beyond that a finished run is no longer part of the picture.
  presentAt(key, at, following) {
    const samples = this.#runs.get(key)?.samples;
    if (!samples?.length || samples[0].t > at) return false;
    return following || at <= samples.at(-1).t + GAP_MS;
  }

  // When the figures as of `at` were really read, and whether `at` falls into a stretch nobody observed.
  observation(key, at) {
    const run = this.#runs.get(key);
    const sample = run?.samples.findLast(item => item.t <= at);
    if (!sample) return null;
    const next = run.samples.find(item => item.t > at);
    const gap = Boolean((next?.gapFrom !== undefined && next.gapFrom < at) || (run.outage !== null && run.outage < at && !next));
    return { observedAt: sample.t, gap };
  }

  // Growth of `field` for one run between `from` and `to`. `partial` means there was no snapshot at or before
  // `from` and the run did not start inside the period, or older snapshots were dropped; `gaps` counts the stretches
  // inside the period in which this field was not followed (see missedStretches), whose change is not counted.
  delta(key, field, from, to) {
    return this.deltas(key, field, [[from, to]])[0];
  }

  deltas(key, field, ranges) {
    const run = this.#runs.get(key);
    if (!run) return ranges.map(() => null);
    // Every chart slice uses the same gaps, including stretches ending after that slice.
    const stretches = missedStretches(run, field);
    return ranges.map(([from, to]) => deltaWithin(run, field, from, to, stretches));
  }
}

function deltaWithin(run, field, from, to, stretches) {
  let first, base;
  for (const sample of run.samples) {
    if (!(sample.t <= to) || sample[field] == null) continue;
    first ??= sample;
    if (sample.t <= from) base = sample;
  }
  if (!first) return null;
  let value = 0;
  let previous = base;
  let unobserved = false;
  for (const sample of run.samples) {
    if (!(sample.t <= to) || (base && !(sample.t > base.t))) continue;
    if (sample.gapFrom !== undefined) unobserved = true;
    if (sample[field] == null) { unobserved = true; continue; }
    if (previous && !unobserved && !sample.breaks?.includes(field)) value += sample[field] - previous[field];
    previous = sample;
    unobserved = false;
  }
  const gaps = stretches.filter(([start, end]) => start < to && end > from).length;
  const covered = run.startedAt >= from && !run.truncated;
  return { value, partial: !base && !covered, gaps, from: (base ?? first).t };
}

// Stretches in which `field` was not followed: unobserved time (including an outage still going on), snapshots without
// the field (from its last reading before them to its next one, if any) and its resets. Built from every kept snapshot,
// also those after the period asked about; overlapping stretches are one.
function missedStretches(run, field) {
  const stretches = [];
  let last = null;
  let missingSince = null;
  for (const sample of run.samples) {
    if (sample.gapFrom !== undefined) stretches.push([sample.gapFrom, sample.t]);
    if (sample[field] == null) {
      missingSince ??= last?.t ?? sample.t;
      continue;
    }
    if (missingSince !== null || sample.breaks?.includes(field)) stretches.push([missingSince ?? last?.t ?? sample.t, sample.t]);
    last = sample;
    missingSince = null;
  }
  if (missingSince !== null) stretches.push([missingSince, Infinity]);
  if (run.outage !== null) stretches.push([run.outage, Infinity]);
  const joined = [];
  for (const [start, end] of stretches.sort((a, b) => a[0] - b[0])) {
    const previous = joined.at(-1);
    // Stretches that only touch stay apart.
    if (previous && start < previous[1]) previous[1] = Math.max(previous[1], end);
    else joined.push([start, end]);
  }
  return joined;
}

// Marks the figures that went back in `sample` and pins both snapshots wherever a figure goes back, appears or
// disappears, so the growth on either side and the stretch without it survive thinning. Returns whether it did.
function markBreaks(last, sample) {
  const breaks = FIELDS.filter(field => sample[field] != null && last[field] != null && sample[field] < last[field]);
  const switched = FIELDS.some(field => (sample[field] == null) !== (last[field] == null));
  if (breaks.length) sample.breaks = breaks;
  if (breaks.length || switched) last.pin = sample.pin = true;
  return breaks.length > 0 || switched;
}

// Version 1 kept gaps apart from the snapshots and could lose them, or the snapshot before a reset, when compacting:
// the stretch before each snapshot it marked counts as unobserved, and a period without a baseline as partial.
function fromVersion1(run) {
  const stored = new Map((run.gaps ?? []).map(gap => [gap.to, gap.from]));
  const samples = [];
  for (const { pin, breaks, gap, reset, ...sample } of run.samples) {
    const last = samples.at(-1);
    if (last && (pin || breaks || gap || reset || stored.has(sample.t))) {
      sample.gapFrom = stored.get(sample.t) ?? last.t;
      last.pin = sample.pin = true;
    } else if (last) markBreaks(last, sample);
    samples.push(sample);
  }
  return { name: run.name, projectId: run.projectId, startedAt: run.startedAt, samples, outage: run.outage ?? null, truncated: true };
}

// Removes money and the marks about it, so nothing about prices stays once they are hidden.
function forgetMoney(sample) {
  delete sample.usd;
  const breaks = sample.breaks?.filter(field => field !== 'usd');
  if (breaks?.length) sample.breaks = breaks;
  else delete sample.breaks;
}

function sampleOf(entry, now, hidePrices) {
  const sample = { t: now, elapsedMs: liveMs(entry.elapsed, now), idleMs: liveMs(entry.idle, now), tokens: entry.tokens };
  sample.workMs = entry.stageWork ? liveMs(entry.stageWork, now) : null;
  if (entry.money && !hidePrices) sample.usd = entry.money.usd;
  return sample;
}

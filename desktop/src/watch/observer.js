import { MAX_RESULTS } from './store.js';
import { stageKey, stillUnconfirmed, unconfirmedKeys } from '../unconfirmed.js';

const TERMINAL = ['finished', 'failed', 'interrupted'];
export const EVENT_KINDS = ['question', 'approval', 'decision', 'failed', 'finished'];
// Cursor capacity: keys still visible in the inventory are always kept (the host bounds them), plus this many
// resolved ones. A run or request that returns after more than this many newer resolutions can be reported again.
const RESOLVED_KEPT = 200;

export const isLive = snapshot => snapshot.host.state === 'connected' && !snapshot.host.error;

// Resolved keys first, in the order they resolved, then the visible ones; so the oldest resolution goes first.
function pruned(seen, visible) {
  const resolved = seen.filter(key => !visible.has(key)).slice(-RESOLVED_KEPT);
  return [...resolved, ...seen.filter(key => visible.has(key))];
}

// Every run in the snapshot with the project it belongs to (null for runs without a project).
function runsOf(snapshot, scope) {
  return [...snapshot.projects.flatMap(project => project.runs.map(run => ({ run, projectId: project.id }))),
    ...snapshot.unassigned.map(run => ({ run, projectId: null }))].filter(entry => inScope(scope, entry));
}

// A reused run id on another instance is another run: an exact watch needs both ids.
export function inScope(scope, { run, projectId }) {
  if (scope.kind === 'run') return run.runId === scope.runId && run.instanceId === scope.instanceId;
  if (scope.kind === 'project') return projectId === scope.projectId;
  return true;
}

// A run fails once: a failed stage and the failed run it ends in share this key, whichever is seen first.
const outcomeKey = (run, outcome) => JSON.stringify([run.instanceId, run.runId, outcome]);

// The host reports a run's outcome; for a live discovered run only the run's own status read does.
function outcomeOf(run, observed) {
  const status = run.state === 'finished' ? run.status : observed?.status?.[run.key];
  if (!run.runId || !TERMINAL.includes(status)) return null;
  return status === 'failed' ? 'failed' : 'finished';
}

// Attention and notice items of the runs in scope.
function itemsOf(snapshot, scope) {
  const keys = new Set(runsOf(snapshot, scope).map(entry => entry.run.key));
  return [...snapshot.attention, ...(snapshot.observed?.notices ?? [])].filter(item => keys.has(item.runKey));
}

// Events visible in one snapshot, each once, with a stable key so a repeat or a reconnect is not a new event.
// `requestAt` is an opaque identity, never a time. A row without a run id is a launch that has not started.
function eventsOf(snapshot, scope) {
  const runs = runsOf(snapshot, scope);
  const byKey = new Map(runs.map(entry => [entry.run.key, entry]));
  const found = new Map();
  const add = event => { if (!found.has(event.key)) found.set(event.key, event); };
  for (const item of [...snapshot.attention, ...(snapshot.observed?.notices ?? [])]) {
    const entry = byKey.get(item.runKey);
    if (!entry || !item.requestAt) continue;
    const { run } = entry;
    const key = item.kind === 'failed' ? outcomeKey(run, 'failed') : JSON.stringify([run.instanceId, run.runId, item.stageId, item.kind, item.requestAt]);
    add({ key, kind: item.kind, entry, stageId: item.stageId, stageName: item.stageName });
  }
  for (const entry of runs) {
    const outcome = outcomeOf(entry.run, snapshot.observed);
    if (outcome) add({ key: outcomeKey(entry.run, outcome), kind: outcome, entry });
  }
  return [...found.values()];
}

// Turns snapshots into stored results. Read-only: it never calls the host and never calls a model.
export class WatchObserver {
  constructor({ store, now = () => Date.now(), onResult = () => {}, onSummaryDue = () => {}, onError = () => {} }) {
    Object.assign(this, { store, now, onResult, onSummaryDue, onError });
    // Observation state that is never saved: a task has an entry only while its observation has been continuous
    // since its last baseline. A task loaded from disk, resumed or polled offline has none and re-baselines silently.
    this.live = new WeakMap();
    // Run + kind of every event recorded by the latest poll: those are the ones a watch announces.
    this.announced = new Set();
  }

  // True while any task needs the one observation loop to keep running.
  get active() { return this.store.tasks.some(task => task.status === 'active'); }

  // Observation was interrupted: the next connected poll is a new baseline and reports nothing.
  interrupt(task) { this.live.delete(task); }

  // Marks everything visible in a connected snapshot as already known, keeping the resolved history, so the task
  // reports only later changes. A snapshot has no event times, so events from before the baseline are never reported.
  // Runs that could not be read are unknown, not empty: each one is seeded silently by its first successful read.
  baseline(task, snapshot) {
    if (!isLive(snapshot)) return false;
    const events = eventsOf(snapshot, task.scope);
    const unavailable = new Set(snapshot.observed?.unavailable ?? []);
    this.live.set(task, {
      unknown: new Set(runsOf(snapshot, task.scope).map(entry => entry.run.key).filter(key => unavailable.has(key))),
      unconfirmed: unconfirmedKeys(itemsOf(snapshot, task.scope)),
    });
    return this.#advance(task, events);
  }

  // Returns true when something changed and was queued for saving.
  onSnapshot(snapshot) {
    let changed = false;
    // A write that failed earlier is repeated here, so an unchanged system still reaches the disk.
    this.store.retry()?.catch(this.onError);
    this.announced = new Set();
    for (const task of this.store.tasks) {
      if (task.status !== 'active') continue;
      if (!isLive(snapshot)) this.interrupt(task);
      // A poll delivered at or after the deadline collects nothing; the timers then complete the task.
      else if (!this.#expired(task)) changed = this.#collect(task, snapshot) || changed;
      changed = this.#timers(task) || changed;
    }
    if (changed) this.store.save().catch(this.onError);
    return changed;
  }

  #expired(task) { return Boolean(task.deadline) && this.now() >= Date.parse(task.deadline); }

  #collect(task, snapshot) {
    if (!task.cursor.baselined || !this.live.has(task)) return this.baseline(task, snapshot);
    const events = eventsOf(snapshot, task.scope);
    const known = new Set(task.cursor.seen);
    const live = this.live.get(task);
    const { unknown } = live;
    for (const event of events) {
      const reported = !unknown.has(event.entry.run.key) && !live.unconfirmed.has(stageKey({ runKey: event.entry.run.key, stageId: event.stageId, kind: event.kind }));
      if (!known.has(event.key) && reported && task.events.includes(event.kind)) this.#record(task, event);
    }
    live.unconfirmed = stillUnconfirmed(live.unconfirmed, itemsOf(snapshot, task.scope));
    const unavailable = new Set(snapshot.observed?.unavailable ?? []);
    for (const key of unknown) if (!unavailable.has(key)) unknown.delete(key);
    return this.#advance(task, events);
  }

  // Moves the cursor past these events; keys no longer visible stay within the resolved horizon.
  #advance(task, events) {
    const { baselined, seen: before } = task.cursor;
    const known = new Set(before);
    const keys = events.map(event => event.key);
    const seen = pruned([...before, ...keys.filter(key => !known.has(key))], new Set(keys));
    task.cursor = { baselined: true, seen };
    return !baselined || seen.length !== before.length || seen.some((key, i) => key !== before[i]);
  }

  #record(task, event) {
    const { run } = event.entry;
    const result = { id: `${task.id}:${task.results.length}:${event.key}`, key: event.key, kind: event.kind, at: new Date(this.now()).toISOString(),
      runKey: run.key, runId: run.runId, instanceId: run.instanceId, runName: run.name ?? '', projectId: event.entry.projectId,
      stageId: event.stageId ?? null, stageName: event.stageName ?? null };
    task.results = [...task.results, result].slice(-MAX_RESULTS);
    task.unread += 1;
    this.announced.add(`${run.key}|${event.kind}`);
    this.onResult(task, result);
  }

  #timers(task) {
    const now = this.now();
    let changed = false;
    if (task.summaryAt && !task.summaryDue && !task.summaryDone && now >= Date.parse(task.summaryAt)) {
      task.summaryDue = true;
      changed = true;
      this.onSummaryDue(task);
    }
    if (task.deadline && now >= Date.parse(task.deadline)) {
      task.status = 'completed';
      changed = true;
    }
    return changed;
  }
}

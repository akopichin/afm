import { randomUUID } from 'node:crypto';
import { WatchStore } from './store.js';
import { EVENT_KINDS, isLive, WatchObserver } from './observer.js';

const SCOPES = ['all', 'project', 'run'];
const MAX_REPORTS = 20;

function timeOrNull(value, name) {
  if (value === undefined || value === null || value === '') return null;
  const at = Date.parse(value);
  if (Number.isNaN(at)) throw Error(`${name} must be a date and time.`);
  return new Date(at).toISOString();
}

// Validates a request and returns the stored shape; nothing here trusts the caller.
export function normalizeTask(request, now) {
  const scope = request.scope ?? { kind: 'all' };
  if (!SCOPES.includes(scope.kind)) throw Error('Scope must be all, project or run.');
  if (scope.kind === 'project' && !scope.projectId) throw Error('A project watch needs a project.');
  if (scope.kind === 'run' && !(scope.runId && scope.instanceId)) throw Error('A run watch needs the run id and instance id.');
  const events = request.events?.length ? [...new Set(request.events)] : ['question', 'approval', 'decision', 'failed', 'finished'];
  const bad = events.find(kind => !EVENT_KINDS.includes(kind));
  if (bad) throw Error(`Unknown event type: ${String(bad).slice(0, 40)}.`);
  const deadline = timeOrNull(request.deadline, 'The deadline');
  const summaryAt = timeOrNull(request.summaryAt, 'The summary time');
  if (deadline && Date.parse(deadline) <= now) throw Error('The deadline is already in the past.');
  if (summaryAt && Date.parse(summaryAt) <= now) throw Error('The summary time is already in the past.');
  if (deadline && summaryAt && Date.parse(summaryAt) > Date.parse(deadline)) throw Error('The summary time is after the deadline.');
  return { title: String(request.title ?? 'Watch').slice(0, 120) || 'Watch',
    scope: { kind: scope.kind, projectId: scope.projectId ?? null, runId: scope.runId ?? null, instanceId: scope.instanceId ?? null },
    events, deadline, summaryAt };
}

// What the window may see of a task: everything but the observer's private cursor.
export function taskView(task) {
  const { cursor, ...visible } = structuredClone(task);
  return visible;
}

// Background watch tasks: persisted definitions, one observer, read-only.
export class WatchService {
  constructor({ home, now = () => Date.now(), onResult, onSummaryDue = () => {}, onActiveChange = () => {}, onChange = () => {}, onError = () => {} }) {
    this.now = now;
    this.onActiveChange = onActiveChange;
    this.store = new WatchStore(home, onChange);
    this.onSummaryDue = onSummaryDue;
    this.observer = new WatchObserver({ store: this.store, now, onResult, onSummaryDue, onError });
  }

  // A summary that was due when Desktop quit is written once it is back.
  async load() {
    const tasks = await this.store.load();
    for (const task of tasks) if (task.summaryDue && !task.summaryDone && task.status !== 'stopped') this.onSummaryDue(task);
    return tasks;
  }
  get active() { return this.observer.active; }

  // Whether the latest poll made a watch announce this kind of event for this run. A silent baseline, a paused task and
  // an expired one announce nothing, so the notifier keeps those events.
  covers(kind, runKey) { return this.observer.announced.has(`${runKey}|${kind}`); }
  list() { return this.store.tasks.map(task => structuredClone(task)); }
  // Only a connected snapshot can be a baseline; offline polls still reach the timers.
  onSnapshot(snapshot) { if (isLive(snapshot)) this.latest = snapshot; return this.observer.onSnapshot(snapshot); }
  flush() { return this.store.flush(); }

  create(request) {
    const task = { id: randomUUID(), ...normalizeTask(request, this.now()), status: 'active', createdAt: new Date(this.now()).toISOString(),
      cursor: { baselined: false, seen: [] }, results: [], reports: [], unread: 0, summaryDue: false, summaryDone: false };
    // What is already pending right now is not news; only later changes become results.
    if (this.latest) this.observer.baseline(task, this.latest);
    this.store.add(task);
    return this.#changed(task);
  }

  // Pause, stop and resume keep every result; what happened during a pause is not reported.
  pause(id) { return this.#move(id, 'active', 'paused'); }
  resume(id) {
    const task = this.#need(id);
    if (task.status === 'paused') this.observer.interrupt(task);
    return this.#move(id, 'paused', 'active');
  }
  stop(id) {
    const task = this.#need(id);
    if (task.status === 'stopped' || task.status === 'completed') return structuredClone(task);
    task.status = 'stopped';
    return this.#changed(task);
  }

  // Stores the planned report id and a "started" mark before the provider is called. False means a report was already
  // started (Desktop quit during the reply) or the mark could not be stored: the plain report is written, never a second reply.
  async beginSummary(id) {
    const task = this.#need(id);
    if (task.summaryStarted) return false;
    task.summaryStarted = true;
    task.summaryId ??= randomUUID();
    try { await this.store.save(); } catch { return false; }
    return true;
  }

  // Takes back the started mark of a summary that stopped before the provider was asked, so a restart asks it normally.
  // The planned id stays. A failed write rejects and leaves the mark on disk: after a restart the plain report is written.
  async cancelSummary(id) {
    const task = this.store.get(id);
    if (!task || task.summaryDone || !task.summaryStarted) return;
    task.summaryStarted = false;
    await this.store.save();
  }

  // The scheduled report. It counts as unread like a result, and the summary is not asked for again.
  // A report id that is already stored changes nothing, so a repeated write cannot add a second unread.
  addReport(id, report) {
    const task = this.#need(id);
    if (task.reports?.some(item => item.id === report.id)) return structuredClone(task);
    task.reports = [...(task.reports ?? []), report].slice(-MAX_REPORTS);
    task.unread += 1;
    task.summaryDue = false;
    task.summaryDone = true;
    return this.#changed(task);
  }

  // What the assistant's watch tools call: a change is on disk before the model hears that it happened.
  tools() {
    const stored = async task => { await this.flush(); return task; };
    return {
      create: async request => stored(this.create(request)),
      list: async () => this.list(),
      pause: async id => stored(this.pause(id)),
      resume: async id => stored(this.resume(id)),
      stop: async id => stored(this.stop(id)),
    };
  }

  markRead(id) {
    const task = this.#need(id);
    if (task.unread === 0) return structuredClone(task);
    task.unread = 0;
    return this.#changed(task);
  }

  #need(id) {
    const task = this.store.get(id);
    if (!task) throw Error('That watch task no longer exists.');
    return task;
  }

  #move(id, from, to) {
    const task = this.#need(id);
    if (task.status === to) return structuredClone(task);
    if (task.status !== from) throw Error(`A ${task.status} task cannot become ${to}.`);
    task.status = to;
    return this.#changed(task);
  }

  // Callers that must know the change is stored wait for `flush()`; a failed write is retried on the next poll.
  #changed(task) {
    this.store.save().catch(this.observer.onError);
    this.onActiveChange(this.active);
    return structuredClone(task);
  }
}

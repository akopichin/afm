import { randomUUID } from 'node:crypto';

const MAX_REPORTS = 20;
const MINUTE = 60_000;
const iso = ms => new Date(ms).toISOString();
const nextSlot = (at, every, now) => iso(at + Math.max(1, Math.floor((now - at) / every) + 1) * every);

export function repeatingRequest(input, now = Date.now()) {
  const prompt = typeof input.prompt === 'string' ? input.prompt.trim() : '';
  if (!prompt || prompt.length > 8000) throw Error('Enter a prompt of 1–8000 characters.');
  const minutes = input.everyMinutes;
  if (!Number.isInteger(minutes) || minutes < 1 || minutes > 43200) throw Error('Choose an interval from 1 to 43200 minutes.');
  const delivery = input.delivery ?? 'tasks';
  if (!['tasks', 'telegram'].includes(delivery)) throw Error('Choose Tasks or Telegram for delivery.');
  const deadline = input.deadline ? Date.parse(input.deadline) : null;
  if (deadline !== null && (!Number.isFinite(deadline) || deadline <= now + minutes * MINUTE)) throw Error('The deadline must be after the first scheduled run.');
  return { title: String(input.title ?? '').trim().slice(0,120) || 'Repeating task', prompt, everyMs: minutes * MINUTE,
    delivery, deadline: deadline === null ? null : iso(deadline) };
}

export class RepeatingTasks {
  #running = new Map();
  #closed = false;
  #ticking = false;
  #timer;

  constructor({ store, execute, deliver, onError = () => {}, now = Date.now }) {
    Object.assign(this, { store, execute, deliver, onError, now });
  }

  async load() {
    const now = this.now();
    let changed = false;
    for (const task of this.store.tasks.filter(task => task.kind === 'repeating')) {
      if (task.inFlightAt) {
        this.#record(task, { at: task.inFlightAt, markdown: 'Desktop closed during this run. Its final outcome is unknown.', status: 'interrupted' });
        changed = true;
      }
      if (task.status === 'active' && Date.parse(task.nextAt) <= now) {
        task.nextAt = nextSlot(Date.parse(task.nextAt), task.everyMs, now);
        changed = true;
      }
    }
    if (changed) await this.store.save();
    this.wake();
  }

  async create(input, { scope, settings, providerName, chatId }) {
    if (this.#closed) throw Error('Desktop is closing.');
    const now = this.now();
    const spec = repeatingRequest(input, now);
    const task = { kind: 'repeating', id: randomUUID(), ...spec, scope, settings, providerName, chatId,
      status: 'active', createdAt: iso(now), nextAt: iso(now + spec.everyMs), events: [], results: [], reports: [], unread: 0 };
    this.store.add(task);
    await this.store.save();
    this.wake();
    return structuredClone(task);
  }

  async setModel(id, settings, providerName) {
    const task = this.store.get(id);
    if (task?.kind !== 'repeating' || !['active','paused'].includes(task.status)) throw Error('That repeating task is no longer active.');
    task.settings = { ...settings, permission: task.settings.permission };
    task.providerName = providerName;
    await this.store.save();
    return structuredClone(task);
  }

  wake() {
    clearTimeout(this.#timer);
    if (this.#closed) return;
    const active = this.store.tasks.filter(task => task.kind === 'repeating' && task.status === 'active');
    if (!active.length) return;
    const next = Math.min(...active.map(task => Math.min(Date.parse(task.nextAt), task.deadline ? Date.parse(task.deadline) : Infinity)));
    this.#timer = setTimeout(() => this.tick().catch(this.onError), Math.max(0, Math.min(next - this.now(), MINUTE)));
    this.#timer.unref?.();
  }

  async tick() {
    if (this.#closed || this.#ticking) return;
    this.#ticking = true;
    try {
      await this.store.retry();
      const now = this.now();
      for (const task of this.store.tasks) {
        if (this.#closed || task.kind !== 'repeating' || task.status !== 'active') continue;
        if (task.deadline && Date.parse(task.deadline) <= now) {
          task.status = 'completed';
          await this.store.save();
          continue;
        }
        const due = Date.parse(task.nextAt);
        if (due > now) continue;
        task.nextAt = nextSlot(due, task.everyMs, now);
        if (this.#running.has(task.id) || this.#running.size >= 2) {
          await this.store.save();
          continue;
        }
        const job = this.#run(task, Date.parse(task.nextAt) - task.everyMs)
          .catch(this.onError).finally(() => this.#running.delete(task.id));
        this.#running.set(task.id, job);
      }
    } finally { this.#ticking = false; this.wake(); }
  }

  async #run(task, slot) {
    const at = iso(slot);
    task.inFlightAt = at;
    try { await this.store.save(); }
    catch (error) { task.inFlightAt = null; throw error; }
    if (this.#closed || task.status !== 'active') {
      task.inFlightAt = null;
      await this.store.save();
      return;
    }
    let markdown, status = 'completed', deliveryStatus;
    try { markdown = await this.execute(structuredClone(task)); }
    catch (error) {
      markdown = String(error.message ?? error).slice(0, 2000);
      status = 'failed';
    }
    if (status === 'completed' && task.delivery === 'telegram') {
      if (this.#closed || task.status === 'stopped') deliveryStatus = 'cancelled';
      else {
        try { await this.deliver(task, markdown); deliveryStatus = 'sent'; }
        catch (error) { deliveryStatus = String(error.message ?? error).slice(0, 300); }
      }
    }
    this.#record(task, { at, markdown, status, deliveryStatus });
    await this.store.save();
  }

  #record(task, report) {
    task.reports = [...task.reports, { ...report, id: report.at, title: task.title, source: 'model' }].slice(-MAX_REPORTS);
    task.inFlightAt = null;
    task.unread += 1;
  }

  close() {
    this.#closed = true;
    clearTimeout(this.#timer);
    return Promise.allSettled(this.#running.values());
  }
}

import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';
import { stageKey, stillUnconfirmed, unconfirmedKeys } from './unconfirmed.js';

// Bound on resolved history; requests still pending are always kept, so the set can exceed it.
const SEEN_LIMIT = 500;
// More new requests than this in one snapshot become a single summary banner.
const BANNER_LIMIT = 5;
const DOCK_KINDS = new Set(['question', 'approval', 'decision']);

// Fixed text per kind: banners never carry question text, artifacts or secrets.
export const TEXT = {
  question: { title: 'AFM needs your answer', waiting: 'an answer' },
  approval: { title: 'AFM needs your approval', waiting: 'your approval' },
  decision: { title: 'AFM needs a decision', waiting: 'your decision' },
  failed: { title: 'An AFM stage failed', waiting: 'your attention' },
  finished: { title: 'An AFM run finished', waiting: 'nothing more' },
};

// Turns new attention items into one banner each. Pure main-process logic: Electron is injected as `show`.
export class Notifier {
  // `claimed(item, snapshot)` is true when a watch task reports the same event, which then owns the banner.
  // `visible(item)` tells whether the user already looks at that stage; `settings` is the notifications preference.
  constructor({ home, show, visible = () => false, claimed = () => false, settings, dock }) {
    this.file = path.join(desktopDir(home), 'notified.json');
    Object.assign(this, { show, visible, claimed, settings, dock });
    this.lock = new Mutex();
    this.seen = null;
    // Open stages whose request had no identity in the first snapshot; see unconfirmed.js.
    this.unconfirmed = new Set();
    this.dockPending = false;
  }

  get enabled() { return this.settings.enabled; }

  setSettings(settings, attention = []) {
    this.settings = settings;
    this.#updateDock(attention);
  }

  // Items of one kind and request are announced once, also across reconnects and restarts.
  #key(item) { return [item.runKey, item.stageId, item.kind, item.requestAt].join('|'); }

  onSnapshot(snapshot) {
    // Without the host there is no real attention list, so nothing is new and nothing is seeded.
    if (snapshot.host.error || snapshot.host.state !== 'connected') return Promise.resolve();
    return this.lock.run(async () => {
      this.#updateDock(snapshot.attention);
      const first = !this.seen;
      if (first) {
        const stored = await readJSON(this.file,{});
        this.seen = new Map(stored && typeof stored === 'object' && !Array.isArray(stored) ? Object.entries(stored) : []);
      }
      const identified = snapshot.attention.filter(item => item.requestAt);
      if (first) this.unconfirmed = unconfirmedKeys(snapshot.attention);
      const unseen = identified.filter(item => !this.seen.has(this.#key(item)));
      // The first identity of a question that was already open is remembered, not announced.
      const fresh = unseen.filter(item => !this.unconfirmed.has(stageKey(item)));
      this.unconfirmed = stillUnconfirmed(this.unconfirmed, snapshot.attention);
      for (const item of unseen) this.seen.set(this.#key(item), 1);
      if (!unseen.length && !first) return;
      // Only resolved history is evicted: a request still pending must not return as new after a reconnect.
      const pending = new Set(identified.map(found => this.#key(found)));
      for (const key of this.seen.keys()) {
        if (this.seen.size <= SEEN_LIMIT) break;
        if (!pending.has(key)) this.seen.delete(key);
      }
      await writeJSON(this.file, Object.fromEntries(this.seen));
      // Questions already pending when Desktop starts are in the queue, not a burst of banners.
      if (first) return;
      const wanted = fresh.filter(item => this.#wanted(item));
      // A watch owns the banner, not the Dock signal.
      if (wanted.some(item => DOCK_KINDS.has(item.kind))) this.dock?.bounce();
      const banners = wanted.filter(item => !this.claimed(item, snapshot));
      for (const item of banners.slice(0, BANNER_LIMIT)) this.#announce(item, snapshot);
      if (banners.length > BANNER_LIMIT) this.show({ title: 'AFM needs you', body: `${banners.length - BANNER_LIMIT} more requests are waiting.` });
    });
  }

  #updateDock(attention) {
    if (!this.dock) return;
    const pending = this.enabled && attention.some(item => DOCK_KINDS.has(item.kind) && this.settings.kinds[item.kind]);
    if (pending === this.dockPending) return;
    this.dock.setPending(pending);
    this.dockPending = pending;
  }

  #wanted(item) { return this.settings.enabled && this.settings.kinds[item.kind] && !this.visible(item); }

  #announce(item, snapshot) {
    const runs = snapshot.projects.flatMap(project => project.runs.map(run => ({ project, run })));
    const target = [...runs, ...snapshot.unassigned.map(run => ({ project: { name: path.basename(run.projectRoot ?? '') }, run }))]
      .find(({ run }) => run.key === item.runKey);
    const text = TEXT[item.kind];
    const where = target ? `${target.project.name} / ${target.run.name || target.run.runId}\n` : '';
    this.show({ title: text.title, body: `${where}Stage ${item.stageName} is waiting for ${text.waiting}.`, runKey: item.runKey, stageId: item.stageId });
  }
}

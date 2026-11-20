import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeJSON } from '../files.js';

const MAX_TASKS = 50;
export const MAX_RESULTS = 200;

// Watch tasks live in their own file, apart from the chat, so /clear and restarts never touch them.
// The in-memory list is the truth for readers; writes are queued and always save the latest list.
export class WatchStore {
  // `onChange` runs on every save, so a window showing the tasks can follow all of them, not only its own actions.
  constructor(home, onChange = () => {}) {
    this.onChange = onChange;
    this.file = path.join(desktopDir(home), 'watch-tasks.json');
    this.lock = new Mutex();
    this.tasks = [];
    this.pending = Promise.resolve();
    this.version = 0;
    this.writing = 0;
    // True while the file may be older than the list; a failed write leaves it set so it is retried.
    this.dirty = false;
  }

  async load() {
    const stored = await readJSON(this.file, {});
    this.tasks = Array.isArray(stored?.tasks) ? stored.tasks.filter(task => task && typeof task.id === 'string') : [];
    return this.tasks;
  }

  get(id) { return this.tasks.find(task => task.id === id) ?? null; }

  add(task) {
    if (this.tasks.length >= MAX_TASKS) {
      // Only finished work is dropped, oldest first; running tasks are never evicted.
      const finished = this.tasks.find(item => item.status === 'stopped' || item.status === 'completed');
      if (!finished) throw Error(`At most ${MAX_TASKS} watch tasks can exist. Stop one first.`);
      this.tasks = this.tasks.filter(item => item !== finished);
    }
    this.tasks.push(task);
    return task;
  }

  // Waits for every queued write and rejects if the last one failed, so Quit and tests see real storage state.
  flush() { return this.pending; }

  save() {
    const version = ++this.version;
    this.dirty = true;
    this.writing += 1;
    this.pending = this.lock.run(async () => {
      try {
        await writeJSON(this.file, { tasks: this.tasks });
        if (version === this.version) this.dirty = false;
      } finally {
        this.writing -= 1;
      }
    });
    this.pending.catch(() => {});
    this.onChange();
    return this.pending;
  }

  // Writes again after a failure; nothing happens while a write is running or when the file is current.
  retry() { return this.dirty && !this.writing ? this.save() : null; }
}

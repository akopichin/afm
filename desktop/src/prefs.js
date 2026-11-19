import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';
import { MODES } from './git-mode.js';

const DEFAULTS = { gitMode: 'current', stageBrowser: true };
const VALIDATORS = { gitMode: value => MODES.includes(value), stageBrowser: value => typeof value === 'boolean' };

// Desktop preferences. Only known keys with valid values are stored.
export class Prefs {
  constructor(home) { this.file = path.join(desktopDir(home), 'prefs.json'); this.lock = new Mutex(); }

  async get() { return { ...DEFAULTS, ...(await readJSON(this.file, {})) }; }

  set(changes) {
    return this.lock.run(async () => {
      const next = await this.get();
      for (const [key, value] of Object.entries(changes)) {
        if (!VALIDATORS[key]?.(value)) throw Error(`Invalid preference ${key}.`);
        next[key] = value;
      }
      await writeJSON(this.file, next);
      return next;
    });
  }
}

import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';
import { validVariable } from './environment.js';
import { MODES } from './git-mode.js';

const NOTIFICATION_KINDS = ['question', 'approval', 'decision', 'failed'];
const DEFAULTS = { gitMode: 'current', stageBrowser: true, environment: {shell: 'none', file: '', variables: {}},
  notifications: {enabled: true, kinds: {question: true, approval: true, decision: true, failed: false}} };
const isBoolean = value => typeof value === 'boolean';
const VALIDATORS = { gitMode: value => MODES.includes(value), stageBrowser: isBoolean,
  notifications: value => isBoolean(value?.enabled) && NOTIFICATION_KINDS.every(kind => isBoolean(value.kinds?.[kind])) };
// Only the known switches are stored, whatever else the caller sent.
const NORMALIZERS = { notifications: value => ({enabled: value.enabled, kinds: Object.fromEntries(NOTIFICATION_KINDS.map(kind => [kind, value.kinds[kind]]))}) };

// Desktop preferences. Only known keys with valid values are stored.
export class Prefs {
  constructor(home) { this.file = path.join(desktopDir(home), 'prefs.json'); this.lock = new Mutex(); }

  async get() { return { ...DEFAULTS, ...(await readJSON(this.file, {})) }; }

  async view() {
    const prefs = await this.get();
    const {shell, file, variables} = prefs.environment;
    return {...prefs, environment: {shell, file, names: Object.keys(variables).sort()}};
  }

  updateEnvironment({shell, file, name, value, remove} = {}) {
    return this.lock.run(async () => {
      const next = await this.get();
      const environment = {...next.environment, variables: {...next.environment.variables}};
      if (shell !== undefined) {
        if (!['none', 'zsh', 'bash'].includes(shell)) throw Error('Choose zsh, bash or no shell.');
        environment.shell = shell;
      }
      if (file !== undefined) {
        if (typeof file !== 'string' || file.includes('\0') || (file && !path.isAbsolute(file) && !file.startsWith('~/'))) throw Error('Enter an absolute or ~/ path to the environment file.');
        environment.file = file;
      }
      if (name !== undefined) {
        if (!validVariable(name) || typeof value !== 'string' || value.includes('\0')) throw Error('Enter a valid environment variable name and value.');
        environment.variables[name] = value;
      }
      if (validVariable(remove)) delete environment.variables[remove];
      await writeJSON(this.file, {...next, environment});
      return this.view();
    });
  }

  set(changes) {
    return this.lock.run(async () => {
      const next = await this.get();
      for (const [key, value] of Object.entries(changes)) {
        if (!VALIDATORS[key]?.(value)) throw Error(`Invalid preference ${key}.`);
        next[key] = NORMALIZERS[key]?.(value) ?? value;
      }
      await writeJSON(this.file, next);
      return next;
    });
  }
}

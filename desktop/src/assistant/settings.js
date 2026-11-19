import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeJSON } from '../files.js';

export const PERMISSIONS = ['ask', 'read', 'edit', 'yolo'];
export const KINDS = ['claude', 'codex', 'openai'];
const REASONING = ['default', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
const MODEL = /^[A-Za-z0-9][\w.:/@+-]{0,119}$/;

export class SettingsError extends Error {}

// Providers Desktop knows without configuration. Their executables are looked up, never stored.
export const BUILT_IN = [
  { id: 'claude', name: 'Claude Code', kind: 'claude', executable: 'claude' },
  { id: 'codex', name: 'Codex', kind: 'codex', executable: 'codex' },
];

const DEFAULTS = { provider: 'claude', model: '', reasoning: 'default', permission: 'ask', providers: [] };

// A credential is only ever a reference: the CLI's own login, an environment variable name or a key file path.
function credential(value) {
  const type = value?.type ?? 'cli';
  if (type === 'cli') return { type };
  if (type === 'env' && ENV_NAME.test(value.ref ?? '')) return { type, ref: value.ref };
  if (type === 'file' && path.isAbsolute(value.ref ?? '') && !value.ref.includes('\0')) return { type, ref: value.ref };
  throw new SettingsError('Credential needs a CLI login, an environment variable name or an absolute key file path.');
}

function endpoint(value) {
  let url;
  try { url = new URL(value); } catch { throw new SettingsError('Endpoint must be a URL.'); }
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if (url.username || url.password || !(url.protocol === 'https:' || (url.protocol === 'http:' && loopback)))
    throw new SettingsError('Endpoint must be https, or http on this machine, without credentials in the URL.');
  return url.href.replace(/\/$/, '');
}

export function validateProvider(input) {
  const name = String(input?.name ?? '').trim();
  if (!name || name.length > 60) throw new SettingsError('Provider needs a name.');
  if (!KINDS.includes(input.kind)) throw new SettingsError('Unknown provider kind.');
  const base = { id: input.id, name, kind: input.kind, credential: credential(input.credential) };
  if (input.kind === 'openai') {
    if (base.credential.type === 'cli') throw new SettingsError('An endpoint needs an environment variable or key file for its key.');
    return { ...base, endpoint: endpoint(input.endpoint) };
  }
  const executable = String(input.executable ?? '').trim();
  if (!path.isAbsolute(executable) || executable.includes('\0')) throw new SettingsError('Executable must be an absolute path.');
  return { ...base, executable };
}

export function validateSelection(changes) {
  const out = {};
  for (const [key, value] of Object.entries(changes)) {
    if (typeof value !== 'string') throw new SettingsError(`Invalid assistant setting ${key}.`);
    if (key === 'provider' && /^[a-z0-9][a-z0-9-]{0,40}$/.test(value)) out.provider = value;
    else if (key === 'model' && (value === '' || MODEL.test(value))) out.model = value;
    else if (key === 'reasoning' && REASONING.includes(value)) out.reasoning = value;
    else if (key === 'permission' && PERMISSIONS.includes(value)) out.permission = value;
    else throw new SettingsError(`Invalid assistant setting ${key}.`);
  }
  return out;
}

// Assistant choices, stored in Desktop's own directory. Only references to credentials are kept, never their values.
export class AssistantSettings {
  constructor(home) { this.file = path.join(desktopDir(home), 'assistant.json'); this.lock = new Mutex(); }

  async get() {
    const stored = await readJSON(this.file, {});
    const providers = [];
    for (const item of Array.isArray(stored.providers) ? stored.providers : []) {
      try { providers.push(validateProvider(item)); } catch { /* A hand-edited entry that no longer validates is ignored. */ }
    }
    const selection = {};
    for (const key of ['provider', 'model', 'reasoning', 'permission'].filter(name => typeof stored[name] === 'string')) {
      try { Object.assign(selection, validateSelection({ [key]: stored[key] })); } catch { /* Fall back to the default. */ }
    }
    return { ...DEFAULTS, ...selection, providers };
  }

  #update(change) {
    return this.lock.run(async () => {
      const next = change(await this.get());
      await writeJSON(this.file, next);
      return next;
    });
  }

  select(changes) {
    const valid = validateSelection(changes);
    // A model name belongs to one provider; switching providers drops it unless the caller names one.
    return this.#update(current => ({ ...current, ...(valid.provider && valid.provider !== current.provider && !('model' in valid) ? { model: '', reasoning: 'default' } : {}), ...valid }));
  }

  addProvider(input, id) {
    const provider = validateProvider({ ...input, id });
    return this.#update(current => {
      if (BUILT_IN.some(item => item.id === id) || current.providers.some(item => item.id === id)) throw new SettingsError('Provider id already exists.');
      return { ...current, providers: [...current.providers, provider] };
    });
  }

  removeProvider(id) {
    return this.#update(current => ({
      ...current, providers: current.providers.filter(item => item.id !== id),
      ...(current.provider === id ? { provider: DEFAULTS.provider, model: '', reasoning: 'default' } : {}),
    }));
  }
}

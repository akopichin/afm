import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readdir, readFile, realpath, stat } from 'node:fs/promises';
import { promisify } from 'node:util';
import path from 'node:path';
import { parse } from 'yaml';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';
import { declaredWorktrees, flowWorktreePolicy, configFilesFor } from './git-mode.js';
import { slug } from './serve-config.js';
import { retryWithSSHKey } from './ssh-key.js';

const run = promisify(execFile);
const GIT_SOURCE = /^(https?:\/\/|ssh:\/\/|git@|file:\/\/)/;

export function pipelineId(name, source) {
  return `desktop-${slug(name, 'pipeline')}-${createHash('sha256').update(source).digest('hex').slice(0, 6)}`;
}

// Pipeline sources the user configured: local folders and Git packages. Project flows are discovered
// per project. Pipelines are registered in serve.yaml because the host only starts what it lists.
export class Sources {
  constructor({ home, serveConfig, binary, environment = () => process.env, unlockSSH }) {
    this.home = home;
    this.serveConfig = serveConfig;
    this.binary = binary;
    this.environment = environment;
    this.unlockSSH = unlockSSH;
    this.file = path.join(desktopDir(home), 'sources.json');
    this.lock = new Mutex();
  }

  async #load() { return (await readJSON(this.file, { sources: [] })).sources ?? []; }

  list() { return this.#load(); }

  add({ location }) {
    return this.lock.run(async () => {
      const kind = GIT_SOURCE.test(location) ? 'git' : 'folder';
      const resolved = kind === 'folder' ? await realpath(location) : location;
      if (kind === 'folder' && !(await stat(resolved)).isDirectory()) throw Error('Source must be a folder.');
      const sources = await this.#load();
      const existing = sources.find(source => source.location === resolved);
      if (existing) return existing.error ? this.#refresh(existing) : existing;
      const source = { id: createHash('sha256').update(resolved).digest('hex').slice(0, 8), kind, location: resolved, entries: [], refreshedAt: null, error: null };
      await writeJSON(this.file, { sources: [...sources, source] });
      return this.#refresh(source);
    });
  }

  remove(id) {
    return this.lock.run(async () => {
      const sources = await this.#load();
      const source = sources.find(item => item.id === id);
      if (!source) return false;
      const retained = new Set(sources.filter(item => item.id !== id).flatMap(item => item.entries.map(entry => entry.id)));
      for (const entry of source.entries) if (!retained.has(entry.id)) await this.serveConfig.removePipeline(entry.id);
      await writeJSON(this.file, { sources: sources.filter(item => item.id !== id) });
      return true;
    });
  }

  refresh(id) {
    return this.lock.run(async () => {
      const source = (await this.#load()).find(item => item.id === id);
      if (!source) throw Error('Unknown source.');
      return this.#refresh(source);
    });
  }

  // Rescans one source, registers new pipelines and drops registrations that disappeared.
  // A failed scan keeps the previous inventory and records the error.
  async #refresh(source) {
    let entries = source.entries;
    let error = null;
    try {
      const next = source.kind === 'git' ? await this.#inspectGit(source) : await this.#scanFolder(source);
      for (const entry of next) await this.serveConfig.addPipeline({ id: entry.id, source: entry.source });
      const others = (await this.#load()).filter(item => item.id !== source.id);
      const retained = new Set([...next, ...others.flatMap(item => item.entries)].map(entry => entry.id));
      for (const old of source.entries) if (!retained.has(old.id)) await this.serveConfig.removePipeline(old.id);
      entries = next;
    } catch (failure) { error = failure.message; }
    const updated = { ...source, entries, error, refreshedAt: new Date().toISOString() };
    const sources = (await this.#load()).map(item => item.id === source.id ? updated : item);
    await writeJSON(this.file, { sources });
    return updated;
  }

  async #scanFolder(source) {
    const candidates = [source.location];
    for (const child of await readdir(source.location, { withFileTypes: true }))
      if (child.isDirectory() && !child.name.startsWith('.')) candidates.push(path.join(source.location, child.name));
    const entries = [];
    for (const dir of candidates) {
      const manifest = await readManifest(dir);
      if (!manifest) continue;
      const policy = await declaredWorktrees(path.join(dir, manifest.entrypoint));
      const flowName = await declaredName(path.join(dir, manifest.entrypoint));
      entries.push({ id: pipelineId(manifest.name, dir), name: manifest.name, version: manifest.version, source: dir, flowName, policy });
    }
    return entries;
  }

  async #inspectGit(source) {
    const [binary, env] = await Promise.all([this.binary(), this.environment()]);
    const options = { timeout: 120000, env: { ...env, HOME: this.home }, maxBuffer: 4 << 20 };
    let prompted = false;
    const json = async args => {
      try {
        const { stdout } = await retryWithSSHKey(() => run(binary, ['pipelines', ...args], options), {
          location: source.location, env: options.env,
          unlock: !prompted && this.unlockSSH ? async request => {
            prompted = true;
            return this.unlockSSH(request);
          } : null,
        });
        return JSON.parse(stdout);
      } catch (error) {
        if (!('stderr' in error) && prompted) throw error;
        const detail = error.stderr?.split(/\r?\n/).find(line => line.startsWith('Error:'));
        const message = detail?.slice(6).trim() || 'Could not read the pipeline repository.';
        const hint = /terminal prompts disabled/i.test(message)
          ? ' Configure a Git credential helper for this host or use an authenticated SSH URL.' : '';
        throw Error(message + hint);
      }
    };
    const { source: git, commit, directories } = await json(['catalog', source.location]);
    const entries = [];
    for (const dir of directories) {
      const base = git.location + (dir ? '//' + dir : '');
      const location = dir === (git.subdir || '') ? source.location : base + (git.ref ? '@' + git.ref : '');
      const info = await json(['inspect', base + '@' + commit, '--json']);
      const flow = path.join(info.snapshot.dir, info.package.entrypoint);
      entries.push({ id: pipelineId(info.package.name, location), name: info.package.name, version: info.package.version,
        source: location, flowName: await declaredName(flow), policy: await declaredWorktrees(flow), commit });
    }
    return entries;
  }
}

// The flow's declared name, which AFM uses for run and worktree names. An unreadable flow has none.
async function declaredName(file) {
  try { return parse(await readFile(file, 'utf8'))?.name || null; } catch { return null; }
}

async function readManifest(dir) {
  try {
    const manifest = parse(await readFile(path.join(dir, 'pipeline.yaml'), 'utf8'));
    return manifest?.schema_version === 2 && manifest.name && manifest.entrypoint ? manifest : null;
  } catch { return null; }
}

// Flows in the project's .afm/flows, with the worktree policy each one declares.
export async function projectFlows(home, folder) {
  const dir = path.join(folder, '.afm', 'flows');
  let files = [];
  try { files = (await readdir(dir, { withFileTypes: true })).filter(f => f.isFile() && /\.ya?ml$/.test(f.name)); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  const flows = [];
  for (const file of files.sort((a, b) => a.name.localeCompare(b.name))) {
    const name = await declaredName(path.join(dir, file.name)) || file.name.replace(/\.ya?ml$/, '');
    flows.push({ kind: 'Flow', file: file.name, name, policy: await flowWorktreePolicy(path.join(dir, file.name), configFilesFor(home, folder)) });
  }
  return flows;
}

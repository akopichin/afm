import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readdir, readFile, realpath, stat } from 'node:fs/promises';
import { promisify } from 'node:util';
import path from 'node:path';
import { parse } from 'yaml';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';
import { declaredWorktrees, flowWorktreePolicy, configFilesFor } from './git-mode.js';
import { slug } from './serve-config.js';

const run = promisify(execFile);
const GIT_SOURCE = /^(https?:\/\/|ssh:\/\/|git@|file:\/\/)/;

export function pipelineId(name, source) {
  return `desktop-${slug(name, 'pipeline')}-${createHash('sha256').update(source).digest('hex').slice(0, 6)}`;
}

// Pipeline sources the user configured: local folders and Git packages. Project flows are discovered
// per project. Pipelines are registered in serve.yaml because the host only starts what it lists.
export class Sources {
  constructor({ home, serveConfig, binary }) {
    this.home = home;
    this.serveConfig = serveConfig;
    this.binary = binary;
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
      if (existing) return existing;
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
      for (const entry of source.entries) await this.serveConfig.removePipeline(entry.id);
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
      entries = source.kind === 'git' ? await this.#inspectGit(source) : await this.#scanFolder(source);
      for (const entry of entries) await this.serveConfig.addPipeline({ id: entry.id, source: entry.source });
      for (const old of source.entries) if (!entries.some(entry => entry.id === old.id)) await this.serveConfig.removePipeline(old.id);
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
    const { stdout } = await run(await this.binary(), ['pipelines', 'inspect', source.location, '--json'],
      { timeout: 120000, env: { ...process.env, HOME: this.home }, maxBuffer: 4 << 20 });
    const info = JSON.parse(stdout);
    const policy = await declaredWorktrees(path.join(info.snapshot.dir, info.package.entrypoint));
    const flowName = await declaredName(path.join(info.snapshot.dir, info.package.entrypoint));
    return [{ id: pipelineId(info.package.name, source.location), name: info.package.name, version: info.package.version,
      source: source.location, flowName, policy, commit: info.source.commit ?? null }];
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

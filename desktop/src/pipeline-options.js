import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtemp, rm } from 'node:fs/promises';
import { promisify } from 'node:util';
import path from 'node:path';
import os from 'node:os';
import { Mutex, readText, writeAtomic } from './files.js';

const exec = promisify(execFile);
const emptyOptions = 'docker:\n  agents: {}\ncollaboration:\n  profiles: {}\n';
const revisionOf = content => createHash('sha256').update(content).digest('hex');

export class PipelineOptions {
  constructor({home, binary, projects, sources, serveConfig, environment}) {
    Object.assign(this, {home, binary, projects, sources, serveConfig, environment});
    this.lock = new Mutex();
  }

  async #entry(id, projectId) {
    const entries = (await this.sources.list()).flatMap(source => source.entries);
    const entry = entries.find(item => item.id === id);
    if (!entry) throw Error('Pipeline not found in Library.');
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$/.test(entry.name)) throw Error('This pipeline name cannot be used as a directory.');
    const file = path.join(this.home, '.afm', 'pipelines', entry.name, 'options.yaml');
    const project = projectId ? (await this.projects.get(projectId)).path : this.home;
    const registered = (await this.serveConfig.read()).pipelines.find(pipeline => pipeline.id === id);
    const previous = registered?.options || file;
    return {entry, file, previous, project, ownerFile: path.join(path.dirname(file), 'desktop-source'), collision:
      entries.some(item => item.name.toLowerCase() === entry.name.toLowerCase() && item.source !== entry.source)};
  }

  async #inspect(entry, project, options) {
    try {
      const args = ['--dir', project, 'pipelines', 'inspect', entry.source, '--json'];
      if (options) args.push('--pipeline-options', options);
      const {stdout} = await exec(await this.binary(), args, {
        cwd: project, env: await this.environment(), timeout: 45000, maxBuffer: 4 << 20,
      });
      const result = JSON.parse(stdout);
      return {roles: (result.roles || []).map(({id, command, client, model, reasoning, profile, auth_from, command_layer}) =>
        ({id, command, client, model, reasoning, profile, auth_from, command_layer})),
      issues: (result.requirements || []).filter(check => check.status === 'missing').map(check => check.detail)};
    } catch (error) {
      return {roles: [], issues: [], error: String(error.stderr || '').split(/\nUsage:/)[0].trim().slice(0, 1600)
        || 'AFM could not inspect the pipeline. Check its source and your environment settings.'};
    }
  }

  async get(id, projectId) {
    const spec = await this.#entry(id, projectId);
    const owner = await readText(spec.ownerFile);
    const conflict = spec.collision || Boolean(owner && owner.trim() !== spec.entry.source);
    const stored = !conflict || spec.previous !== spec.file ? await readText(spec.previous) : null;
    const result = await this.#inspect(spec.entry, spec.project, stored === null ? null : spec.previous);
    const text = stored ?? emptyOptions;
    return {id, name: spec.entry.name, file: spec.file, previous: spec.previous, text, revision: revisionOf(text),
      conflict, ...result};
  }

  save({id, projectId, text, revision}) {
    return this.lock.run(async () => {
      if (typeof text !== 'string' || !text.trim() || Buffer.byteLength(text) > 256 * 1024) throw Error('Enter a YAML document up to 256 KiB.');
      const spec = await this.#entry(id, projectId);
      const owner = await readText(spec.ownerFile);
      if (spec.collision || (owner && owner.trim() !== spec.entry.source)) throw Error('Another source shares this pipeline name. Its options have been preserved.');
      if (revision !== revisionOf(await readText(spec.previous) ?? emptyOptions)) throw Error('Options changed on disk. Close the editor and reopen it.');
      const temporary = await mkdtemp(path.join(os.tmpdir(), 'afm-options-'));
      try {
        const candidate = path.join(temporary, 'options.yaml');
        await writeAtomic(candidate, text);
        const result = await this.#inspect(spec.entry, spec.project, candidate);
        if (result.error) throw Error(result.error);
        await writeAtomic(spec.ownerFile, spec.entry.source + '\n');
        await writeAtomic(spec.file, text);
        await this.serveConfig.setPipelineOptions(id, spec.entry.source, spec.file);
        return {id, name: spec.entry.name, file: spec.file, previous: spec.file, text, revision: revisionOf(text), ...result};
      } finally {
        await rm(temporary, {recursive: true, force: true}).catch(() => {});
      }
    });
  }
}

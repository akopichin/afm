import path from 'node:path';
import { parseDocument } from 'yaml';
import { Mutex, readText, writeAtomic } from './files.js';

const ID = /^[a-z0-9-]+$/;
const lock = new Mutex();

export function serveConfigPath(home) { return path.join(home, '.afm', 'serve.yaml'); }

// Edits ~/.afm/serve.yaml through the YAML document model, so comments, ordering and
// entries Desktop does not know about survive every write.
export class ServeConfig {
  constructor(home) { this.file = serveConfigPath(home); }

  async #edit(change) {
    return lock.run(async () => {
      const text = await readText(this.file);
      const doc = parseDocument(text ?? '');
      if (doc.errors.length) throw Error('serve.yaml is not valid YAML: ' + doc.errors[0].message);
      if (doc.contents === null) doc.contents = doc.createNode({});
      const result = change(doc);
      if (result.changed) await writeAtomic(this.file, String(doc));
      return result.value;
    });
  }

  async read() {
    const text = await readText(this.file);
    const data = parseDocument(text ?? '').toJS() || {};
    return { projects: data.projects || [], pipelines: data.pipelines || [] };
  }

  // Returns the id of the existing entry for this path, or adds a new one. The id never collides.
  addProject(projectPath, preferredId) {
    return this.#edit(doc => {
      const entries = (doc.get('projects')?.toJSON()) || [];
      const existing = entries.find(entry => entry.path === projectPath);
      if (existing) return { changed: false, value: { id: existing.id, added: false } };
      const id = uniqueId(preferredId, entries.map(entry => entry.id));
      appendEntry(doc, 'projects', { id, path: projectPath });
      return { changed: true, value: { id, added: true } };
    });
  }

  removeProject(id) { return this.#remove('projects', id); }

  addPipeline({ id, source, options }) {
    return this.#edit(doc => {
      const entries = (doc.get('pipelines')?.toJSON()) || [];
      const existing = entries.find(entry => entry.id === id);
      const entry = { id, source, ...(options ? { options } : {}) };
      if (existing && existing.source === source && (options === undefined || existing.options === options)) return { changed: false, value: id };
      if (existing) throw Error(`Pipeline id ${id} is already used by another source.`);
      appendEntry(doc, 'pipelines', entry);
      return { changed: true, value: id };
    });
  }

  setPipelineOptions(id, source, options) {
    return this.#edit(doc => {
      const node = doc.get('pipelines')?.items.find(item => item.get('id') === id && item.get('source') === source);
      if (!node) throw Error('Pipeline is no longer registered in AFM.');
      if (node.get('options') === options) return {changed: false};
      node.set('options', options);
      return {changed: true};
    });
  }

  removePipeline(id) { return this.#remove('pipelines', id); }

  #remove(key, id) {
    return this.#edit(doc => {
      const list = doc.get(key);
      const index = list?.items?.findIndex(item => item.get('id') === id) ?? -1;
      if (index < 0) return { changed: false, value: false };
      list.delete(index);
      if (list.items.length === 0) doc.delete(key);
      return { changed: true, value: true };
    });
  }
}

export function slug(text, fallback = 'project') {
  const value = String(text).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  return ID.test(value) ? value : fallback;
}

function uniqueId(preferred, taken) {
  const base = slug(preferred);
  if (!taken.includes(base)) return base;
  for (let n = 2; ; n++) if (!taken.includes(`${base}-${n}`)) return `${base}-${n}`;
}

// `addIn` on a missing key would create a map, so a missing list is created as a sequence.
function appendEntry(doc, key, entry) {
  if (doc.get(key)) doc.addIn([key], doc.createNode(entry));
  else doc.set(key, doc.createNode([entry]));
}

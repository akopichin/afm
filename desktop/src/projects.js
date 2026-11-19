import { realpath, stat } from 'node:fs/promises';
import path from 'node:path';
import { desktopDir, Mutex, readJSON, writeJSON } from './files.js';

// Projects shown on Home. Each one is also registered in serve.yaml, the only list of folders the
// AFM host may start runs in. Entries the user added to serve.yaml by hand are never removed.
export class Projects {
  constructor({ home, serveConfig }) {
    this.serveConfig = serveConfig;
    this.file = path.join(desktopDir(home), 'projects.json');
    this.lock = new Mutex();
  }

  async list() { return (await readJSON(this.file, { projects: [] })).projects ?? []; }

  async get(id) {
    const project = (await this.list()).find(item => item.id === id);
    if (!project) throw Error('Unknown project.');
    return project;
  }

  // Adding the same folder twice, through a symlink or not, keeps one project.
  add(folder) {
    return this.lock.run(async () => {
      const resolved = await realpath(folder);
      if (!(await stat(resolved)).isDirectory()) throw Error('Project must be a folder.');
      const projects = await this.list();
      const known = projects.find(project => project.path === resolved);
      if (known) return known;
      const before = await this.serveConfig.read();
      const registered = !before.projects.some(entry => entry.path === resolved);
      const { id } = await this.serveConfig.addProject(resolved, path.basename(resolved));
      const project = { id, path: resolved, name: path.basename(resolved), registered, addedAt: new Date().toISOString() };
      await writeJSON(this.file, { projects: [...projects, project] });
      return project;
    });
  }

  // Removes the Home tile. The serve.yaml entry goes only when Desktop created it and nothing is running there.
  remove(id, { active = false } = {}) {
    return this.lock.run(async () => {
      const projects = await this.list();
      const project = projects.find(item => item.id === id);
      if (!project) return false;
      if (project.registered && !active) await this.serveConfig.removeProject(id);
      await writeJSON(this.file, { projects: projects.filter(item => item.id !== id) });
      return true;
    });
  }

  // Projects that runs or serve.yaml know about but Home does not show yet.
  async candidates(runs) {
    const known = new Set((await this.list()).map(project => project.path));
    const found = new Map();
    for (const entry of (await this.serveConfig.read()).projects) if (!known.has(entry.path)) found.set(entry.path, { path: entry.path, source: 'serve.yaml' });
    for (const run of runs) if (run.projectRoot && !known.has(run.projectRoot) && !found.has(run.projectRoot)) found.set(run.projectRoot, { path: run.projectRoot, source: 'run' });
    return [...found.values()];
  }
}

// Runs started in a worktree report the worktree folder, which lives under the project.
export function belongsTo(projectPath, runRoot) {
  if (!runRoot) return false;
  return runRoot === projectPath || runRoot.startsWith(path.join(projectPath, '.worktrees') + path.sep);
}

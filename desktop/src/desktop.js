import { Host } from './host.js';
import { inspectGit } from './git-mode.js';
import { Prefs } from './prefs.js';
import { Projects, belongsTo } from './projects.js';
import { Runs } from './runs.js';
import { ServeConfig } from './serve-config.js';
import { Sources, projectFlows } from './sources.js';

// Everything the main process knows about AFM projects and runs, built from one home directory.
export class Desktop {
  constructor({ home, binary, env, hostEnv }) {
    this.home = home;
    // The host's environment may be computed when the host is actually started.
    this.host = new Host(home, binary, hostEnv ? { env: async () => ({ ...(env ?? process.env), ...(await hostEnv()) }) } : env ? { env } : {});
    this.serveConfig = new ServeConfig(home);
    this.prefs = new Prefs(home);
    this.projects = new Projects({ home, serveConfig: this.serveConfig });
    this.sources = new Sources({ home, serveConfig: this.serveConfig, binary });
    this.runs = new Runs({ home, host: this.host, projects: this.projects, sources: this.sources, prefs: this.prefs, serveConfig: this.serveConfig });
  }

  // Home screen data: kept projects with their runs, runs without a project, and the host's state.
  // A host that is down still lets the project list show, with its runs empty.
  async overview() {
    const projects = await this.projects.list();
    let runs = [];
    let hostError = null;
    try { runs = await this.runs.list(); } catch (error) { hostError = { code: error.code ?? 'host_unreachable', message: error.message }; }
    const owned = new Set();
    const rows = projects.map(project => {
      const own = runs.filter(run => belongsTo(project.path, run.projectRoot));
      own.forEach(run => owned.add(run.key));
      return { ...project, runs: own };
    });
    const loose = runs.filter(run => !owned.has(run.key));
    return { host: { ...this.host.state, error: hostError }, projects: rows, unassigned: loose,
      candidates: await this.projects.candidates(runs) };
  }

  async projectDetails(id) { return this.folderDetails(await this.projects.get(id)); }

  // Flows, sources and Git state of a folder, whether or not it is on Home yet.
  async folderDetails(project) {
    const [flows, sources, prefs, git] = await Promise.all([projectFlows(this.home, project.path), this.sources.list(), this.prefs.view(), inspectGit(project.path)]);
    return { project, flows, sources, prefs, git };
  }

  close() { return this.host.close(); }
}

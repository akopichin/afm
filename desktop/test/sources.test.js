import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { afmBinary } from '../src/binary.js';
import { Desktop } from '../src/desktop.js';
import { desktopEnvironment } from '../src/environment.js';
import { ServeConfig } from '../src/serve-config.js';
import { LibraryLinks } from '../src/library-links.js';
import { Sources, projectFlows } from '../src/sources.js';
import { makeProject, tempHome } from './support.js';

async function writePackage(dir, name, flow = `name: ${name}-flow\nstages:\n  - id: a\n    script: echo hi\n`) {
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, 'pipeline.yaml'), `schema_version: 2\nname: ${name}\nversion: 1.0.0\nentrypoint: flow.yaml\n`);
  await writeFile(path.join(dir, 'flow.yaml'), flow);
}

test('folder sources list pipelines, register them for the host and follow refreshes', async t => {
  const { home } = await tempHome(t);
  const serveConfig = new ServeConfig(home);
  const sources = new Sources({ home, serveConfig, binary: afmBinary });
  const root = path.join(home, 'library');
  await writePackage(path.join(root, 'alpha'), 'alpha', 'name: alpha-flow\nworktrees: true\nstages: []\n');
  await writePackage(path.join(root, 'beta'), 'beta');
  await mkdir(path.join(root, 'not-a-package'));

  const source = await sources.add({ location: root });
  assert.deepEqual(source.entries.map(e => e.name).sort(), ['alpha', 'beta']);
  const alpha = source.entries.find(e => e.name === 'alpha');
  assert.equal(alpha.flowName, 'alpha-flow');
  assert.deepEqual(alpha.policy, { enabled: true, source: 'flow' });
  assert.equal((await serveConfig.read()).pipelines.length, 2);
  assert.equal((await sources.add({ location: root })).id, source.id, 'adding twice keeps one source');

  await rm(path.join(root, 'beta'), { recursive: true });
  await writePackage(path.join(root, 'gamma'), 'gamma');
  const refreshed = await sources.refresh(source.id);
  assert.deepEqual(refreshed.entries.map(e => e.name).sort(), ['alpha', 'gamma']);
  assert.deepEqual((await serveConfig.read()).pipelines.map(p => p.source).sort(), [path.join(root, 'alpha'), path.join(root, 'gamma')]);

  assert.equal(await sources.remove(source.id), true);
  assert.deepEqual((await serveConfig.read()).pipelines, []);
  assert.deepEqual(await sources.list(), []);
});

test('a failing refresh keeps the last inventory and reports the error', async t => {
  const { home } = await tempHome(t);
  const sources = new Sources({ home, serveConfig: new ServeConfig(home), binary: afmBinary });
  const root = path.join(home, 'lib');
  await writePackage(path.join(root, 'one'), 'one');
  const source = await sources.add({ location: root });
  await rm(root, { recursive: true });
  const failed = await sources.refresh(source.id);
  assert.equal(failed.entries.length, 1);
  assert.match(failed.error, /ENOENT/);
});

test('a Git package is inspected through the AFM CLI', { timeout: 60000 }, async t => {
  const { home } = await tempHome(t);
  const repo = path.join(home, 'remote');
  await writePackage(repo, 'from-git', 'name: git-flow\nstages: []\n');
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { stdio: 'ignore' });
  git('init', '-q', '-b', 'main'); git('config', 'user.email', 't@e.c'); git('config', 'user.name', 'T');
  git('add', '-A'); git('commit', '-q', '-m', 'p');
  const serveConfig = new ServeConfig(home);
  const sources = new Sources({ home, serveConfig, binary: afmBinary });
  const source = await sources.add({ location: 'file://' + repo });
  assert.equal(source.error, null, source.error);
  assert.equal(source.kind, 'git');
  assert.equal(source.entries[0].name, 'from-git');
  assert.equal(source.entries[0].flowName, 'git-flow');
  assert.equal((await serveConfig.read()).pipelines[0].source, 'file://' + repo);
});

test('a confirmed Git link uses the real Library registration and keeps one source', async t => {
  const { home, env } = await tempHome(t);
  const repo = path.join(home, 'remote');
  await writePackage(path.join(repo, 'packages', 'dev'), 'dev');
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { stdio: 'ignore' });
  git('init', '-q', '-b', 'main');
  git('add', '.');
  git('-c', 'user.name=T', '-c', 'user.email=t@example.com', 'commit', '-qm', 'packages');
  git('tag', 'v1');
  const remote = 'https://example.invalid/team/pipelines';
  const location = remote + '//packages/dev@v1';
  const config = new ServeConfig(home);
  execFileSync('git', ['config', '--global', `url.file://${repo}.insteadOf`, remote], { env });
  const sources = new Sources({ home, serveConfig: config, binary: afmBinary, environment: () => env });
  const links = new LibraryLinks(input => sources.add(input));
  const request = 'afm://library/install?' + new URLSearchParams({ source: location });
  assert.equal(links.receive(request), true);
  assert.deepEqual(await sources.list(), []);
  const first = await links.confirm(links.current().id);
  assert.equal(first.error, null, first.error);
  assert.equal(first.entries[0].name, 'dev');
  links.receive(request);
  assert.equal((await links.confirm(links.current().id)).id, first.id);
  assert.equal((await sources.list()).length, 1);
  assert.deepEqual((await config.read()).pipelines.map(pipeline => pipeline.source), [location]);
});

test('Git Library passes the configured environment to both catalog and inspect', async t => {
  const { home } = await tempHome(t);
  const snapshot = path.join(home, 'snapshot');
  const binary = path.join(home, 'afm-probe');
  const vars = path.join(home, 'library.env');
  await writePackage(snapshot, 'private-package');
  await writeFile(vars, 'SSH_AUTH_SOCK=/example/agent.sock\n');
  await writeFile(binary, `#!/usr/bin/env node
const expected = process.env.SSH_AUTH_SOCK === '/example/agent.sock';
if (!expected) {
  console.error("Error: could not read Username: terminal prompts disabled");
  process.exit(1);
}
const location = 'https://git.example.test/team/workflows';
const result = process.argv[3] === 'catalog'
  ? {source:{location},commit:'abc',directories:['']}
  : {snapshot:{dir:${JSON.stringify(snapshot)}},package:{name:'private-package',version:'1.0.0',entrypoint:'flow.yaml'}};
console.log(JSON.stringify(result));
`, { mode: 0o755 });
  let loads = 0;
  const desktop = new Desktop({ home, binary: async () => binary, environment: () => {
    loads++;
    return desktopEnvironment(desktop.prefs, home, {PATH:process.env.PATH, HOME:home, ZDOTDIR:home});
  } });
  const location = 'https://git.example.test/team/workflows';
  const failed = await desktop.sources.add({location});
  assert.match(failed.error, /Git credential helper/);
  await desktop.prefs.updateEnvironment({ file: vars });
  const inspected = await desktop.sources.refresh(failed.id);
  assert.equal(inspected.error, null);
  assert.equal(inspected.entries[0].name, 'private-package');
  assert.equal(loads, 2, 'catalog and inspect share one environment load per refresh');
  if (existsSync('/bin/zsh')) {
    await writeFile(path.join(home, '.zshrc'), 'export SSH_AUTH_SOCK=/example/agent.sock\n');
    await desktop.prefs.updateEnvironment({file:'', shell:'zsh'});
    const fromShell = await desktop.sources.refresh(failed.id);
    assert.equal(fromShell.error, null, fromShell.error);
    assert.equal(fromShell.entries[0].name, 'private-package');
    assert.equal(loads, 3);
  }
});

test('a repository library discovers nested packages at the selected ref and registers launchable sources', async t => {
  const { home } = await tempHome(t);
  const repo = path.join(home, 'library');
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { stdio: 'ignore' });
  await writePackage(path.join(repo, 'pipelines', 'dev'), 'dev');
  await writePackage(path.join(repo, 'pipelines', 'review'), 'review');
  git('init', '-q', '-b', 'main');
  git('add', '.');
  git('-c','user.name=T','-c','user.email=t@example.com','commit','-qm','packages');
  git('tag', 'v1');
  await rm(path.join(repo, 'pipelines', 'review'), {recursive:true});
  git('add', '-A');
  git('-c','user.name=T','-c','user.email=t@example.com','commit','-qm','remove review');
  const url = 'file://' + repo;
  const config = new ServeConfig(home);
  const sources = new Sources({ home, serveConfig: config, binary: afmBinary });
  const tagged = await sources.add({ location: url + '@v1' });
  assert.equal(tagged.error, null);
  assert.deepEqual(tagged.entries.map(e => e.source), [url + '//pipelines/dev@v1', url + '//pipelines/review@v1']);
  assert.equal(new Set(tagged.entries.map(e => e.commit)).size, 1);
  const current = await sources.add({ location: url });
  assert.deepEqual(current.entries.map(e => e.name), ['dev']);
  const single = await sources.add({ location: url + '//pipelines/dev' });
  assert.equal(current.entries[0].id, single.entries[0].id);
  await sources.remove(current.id);
  assert.ok((await config.read()).pipelines.some(entry => entry.id === single.entries[0].id));
});

test('project flows carry their declared names and policies', async t => {
  const { home } = await tempHome(t);
  const dir = await makeProject(home, 'p', { flows: { 'x.yaml': 'name: ex\nworktrees: false\nstages: []\n', 'y.yml': 'stages: []\n', 'note.txt': 'no' } });
  const flows = await projectFlows(home, dir);
  assert.deepEqual(flows.map(f => [f.file, f.name, f.policy?.enabled ?? null]), [['x.yaml', 'ex', false], ['y.yml', 'y', null]]);
  assert.deepEqual(await projectFlows(home, path.join(home, 'nothing')), []);
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { afmBinary } from '../src/binary.js';
import { ServeConfig } from '../src/serve-config.js';
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

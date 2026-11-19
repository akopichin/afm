import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { ServeConfig, slug } from '../src/serve-config.js';
import { tempHome } from './support.js';

test('editing serve.yaml keeps comments, order and unrelated entries', async t => {
  const { home } = await tempHome(t);
  const config = new ServeConfig(home);
  await writeFile(config.file, '# my hosts\nlimits:\n  max_running: 2 # keep low\nprojects:\n  - id: mine\n    path: /somewhere\nextra_key: kept\n');
  const added = await config.addProject('/tmp/new-one', 'New One');
  assert.deepEqual(added, { id: 'new-one', added: true });
  const text = await readFile(config.file, 'utf8');
  assert.match(text, /# my hosts/);
  assert.match(text, /max_running: 2 # keep low/);
  assert.match(text, /extra_key: kept/);
  assert.deepEqual((await config.read()).projects.map(p => p.id), ['mine', 'new-one']);
  await config.removeProject('new-one');
  assert.match(await readFile(config.file, 'utf8'), /id: mine/);
  assert.doesNotMatch(await readFile(config.file, 'utf8'), /new-one/);
});

test('adding the same path is idempotent and ids never collide', async t => {
  const { home } = await tempHome(t);
  const config = new ServeConfig(home);
  assert.equal((await config.addProject('/a/app', 'app')).id, 'app');
  assert.deepEqual(await config.addProject('/a/app', 'app'), { id: 'app', added: false });
  assert.equal((await config.addProject('/b/app', 'app')).id, 'app-2');
  const results = await Promise.all(['/c/x', '/d/x', '/e/x'].map(p => config.addProject(p, 'x')));
  assert.equal(new Set(results.map(r => r.id)).size, 3);
});

test('a missing file is created and an invalid one is left untouched', async t => {
  const { home } = await tempHome(t);
  const config = new ServeConfig(home);
  await config.addPipeline({ id: 'p1', source: '/pk' });
  assert.deepEqual((await config.read()).pipelines, [{ id: 'p1', source: '/pk' }]);
  await config.addPipeline({ id: 'p1', source: '/pk' });
  await assert.rejects(config.addPipeline({ id: 'p1', source: '/other' }), /already used/);
  await writeFile(config.file, 'projects: [unclosed');
  await assert.rejects(config.addProject('/x', 'x'), /not valid YAML/);
  assert.equal(await readFile(config.file, 'utf8'), 'projects: [unclosed');
});

test('ids follow the serve.yaml pattern', () => {
  assert.equal(slug('My Project!'), 'my-project');
  assert.equal(slug('***'), 'project');
});

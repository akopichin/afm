import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { readFile, writeFile } from 'node:fs/promises';
import { PipelineOptions } from '../src/pipeline-options.js';
import { ServeConfig } from '../src/serve-config.js';
import { tempHome } from './support.js';

test('pipeline options are validated and registered without losing settings on source refresh', async t => {
  const {home} = await tempHome(t);
  const entry = {id:'desktop-doc', name:'doc-flow', source:'https://example.com/pipelines'};
  const entries = [entry];
  const config = new ServeConfig(home);
  await config.addPipeline(entry);
  const stub = path.join(home, 'inspect');
  const template = '# package example\ndocker:\n  agents: {}\n';
  await writeFile(path.join(home, 'options.example.yaml'), template);
  await writeFile(stub, `#!/bin/sh
for arg in "$@"; do
  if [ -f "$arg" ] && /usr/bin/grep -q '^invalid' "$arg"; then
    echo 'Error: invalid option' >&2
    exit 1
  fi
done
echo '${JSON.stringify({snapshot:{dir:home}, roles:[{id:'writer',model:'example'}], requirements:[{status:'missing',detail:'Missing key'}]})}'
`, {mode:0o700});
  const editor = new PipelineOptions({home, serveConfig:config, binary:async()=>stub, environment:async()=>process.env,
    sources:{list:async()=>[{entries}]}, projects:{get:async()=>({path:home})}});
  const view = await editor.get(entry.id);
  assert.deepEqual(view.issues, ['Missing key']);
  assert.equal(view.text, template);
  assert.equal(view.template, 'options.example.yaml');
  await assert.rejects(editor.save({id:entry.id, revision:view.revision, text:'invalid: true'}), /invalid option/);
  const saved = await editor.save({id:entry.id, revision:view.revision, text:'docker:\n  agents: {}\n'});
  assert.equal(saved.file,path.join(home,'.afm','pipelines','doc-flow','options.yaml'));
  assert.equal(await readFile(saved.file,'utf8'),saved.text);
  const reopened = await editor.get(entry.id);
  assert.equal(reopened.text, saved.text);
  assert.equal(reopened.template, null);
  await config.addPipeline(entry);
  assert.equal((await config.read()).pipelines[0].options,saved.file);
  entries.push({id:'desktop-another', name:entry.name, source:'https://example.com/another'});
  assert.equal((await editor.get(entries[1].id)).conflict,true);
});

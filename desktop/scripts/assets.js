import { cp, mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
const source = path.resolve('../pkg/web/dashboard');
const output = '.generated/dashboard';
await mkdir(output, { recursive: true });
for (const file of ['skins/base', 'skins/graphite', 'assets', 'chat-composer.css', 'side-agent.css', 'favicon.svg']) {
  await cp(path.join(source, file), path.join(output, file), { recursive: true });
}
const dashboard = await readFile(path.join(source, 'index.html'), 'utf8');
const entry = dashboard.match(/src="\.\/(assets\/[^"]+\.js)"/)?.[1];
if (!entry) throw Error('Build the AFM dashboard assets before preparing Desktop.');
const fixture = await readFile('renderer/run.html', 'utf8');
await writeFile('.generated/run.html', fixture.replace('../.generated/dashboard/', './dashboard/')
  .replaceAll('../.generated/dashboard/', './dashboard/').replace(/assets\/index-[^"]+\.js/, entry)
  .replace('src="run-fixture.js"', 'src="../renderer/run-fixture.js"').replaceAll('"select.', '"../renderer/select.')
  .replace('"focus.css"', '"../renderer/focus.css"'));

const { build } = await import('esbuild');
const monaco = 'node_modules/monaco-editor';
const { outputFiles } = await build({
  entryPoints: [monaco + '/esm/vs/editor/editor.worker.js'],
  bundle: true, format: 'iife', minify: true, write: false,
});
await build({
  entryPoints: ['renderer/code-editor.js'],
  outfile: '.generated/editor/code-editor.js',
  bundle: true, format: 'iife', globalName: 'AFMCodeEditor', minify: true,
  define: { AFM_EDITOR_WORKER: JSON.stringify(outputFiles[0].text) },
  loader: {'.ttf': 'file'},
});
for (const notice of ['LICENSE', 'ThirdPartyNotices.txt']) {
  await cp(path.join(monaco, notice), path.join('.generated/editor', notice));
}

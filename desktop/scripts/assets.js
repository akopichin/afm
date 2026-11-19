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
  .replace('src="run-fixture.js"', 'src="../renderer/run-fixture.js"').replaceAll('"select.', '"../renderer/select.'));

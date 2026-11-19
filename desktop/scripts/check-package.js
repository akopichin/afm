import { statFile } from '@electron/asar';
import { execFileSync } from 'node:child_process';
import path from 'node:path';

const bundle = path.resolve('dist', 'AFM Desktop-' + process.platform + '-' + process.arch);
const resources = process.platform === 'darwin' ? path.join(bundle, 'AFM Desktop.app/Contents/Resources') : path.join(bundle, 'resources');
const binary = path.join(resources, process.platform === 'win32' ? 'afm.exe' : 'afm');
const archive = path.join(resources, 'app.asar');
for (const file of ['src/main.js', 'src/preload.cjs', 'src/dashboard-keys.cjs', 'renderer/index.html',
  'renderer/checkout.html', '.generated/run.html', '.generated/dashboard/skins/graphite/index.css',
  'node_modules/@modelcontextprotocol/sdk/package.json', 'node_modules/yaml/package.json']) {
  if (!statFile(archive, file).size) throw Error('Empty package entry: ' + file);
}
console.log(bundle, execFileSync(binary, ['--version'], {encoding: 'utf8'}).trim());

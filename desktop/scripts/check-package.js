import { statFile } from '@electron/asar';
import { verifySidecar } from '../src/sidecar.js';
import path from 'node:path';

const bundle = path.resolve('dist', 'AFM Desktop-' + process.platform + '-' + process.arch);
const resources = process.platform === 'darwin' ? path.join(bundle, 'AFM Desktop.app/Contents/Resources') : path.join(bundle, 'resources');
const binary = path.join(resources, process.platform === 'win32' ? 'afm.exe' : 'afm');
const archive = path.join(resources, 'app.asar');
for (const file of ['src/main.js', 'src/preload.cjs', 'src/dashboard-keys.cjs', 'src/workspace.js', 'src/protocol.js',
  'src/shortcuts.js', 'src/assistant/assistant.js', 'src/assistant/bridge.js', 'src/assistant/ipc.js', 'src/stage-browser/broker.js', 'src/stage-browser/companion-main.js', 'src/stage-browser/electron-page.js', 'renderer/assistant.js', 'renderer/index.html', 'renderer/inventory.js',
  'renderer/checkout.html', '.generated/run.html', '.generated/dashboard/skins/graphite/index.css',
  'node_modules/@modelcontextprotocol/sdk/package.json', 'node_modules/yaml/package.json']) {
  if (!statFile(archive, file).size) throw Error('Empty package entry: ' + file);
}
// The executable inside the package, not the one the build chose, must carry the stage browser hook and fit this machine.
const sidecar = await verifySidecar(binary);
console.log(bundle, sidecar.version, sidecar.platform + '/' + sidecar.arch);

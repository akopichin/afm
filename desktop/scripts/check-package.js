import { statFile } from '@electron/asar';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { readFile, mkdtemp, rm, stat } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { afmBinary } from '../src/binary.js';
import { verifySidecar } from '../src/sidecar.js';
import { writeMcpWrapper, mcpWrapperPath } from '../src/stage-browser/install.js';

const bundle = path.resolve('dist', 'AFM Desktop-' + process.platform + '-' + process.arch);
const resources = process.platform === 'darwin' ? path.join(bundle, 'AFM Desktop.app/Contents/Resources') : path.join(bundle, 'resources');
const binary = path.join(resources, process.platform === 'win32' ? 'afm.exe' : 'afm');
const archive = path.join(resources, 'app.asar');
for (const file of ['assets/afm-desktop.png', 'assets/afm-desktop-attention.png', 'src/main.js', 'src/preload.cjs', 'src/dashboard-keys.cjs', 'src/workspace.js', 'src/protocol.js', 'src/library-links.js',
  'src/shortcuts.js', 'src/assistant/assistant.js', 'src/assistant/bridge.js', 'src/assistant/ipc.js', 'src/stage-browser/broker.js', 'src/stage-browser/companion-main.js', 'src/stage-browser/electron-page.js', 'renderer/assistant.js', 'renderer/index.html', 'renderer/inventory.js',
  'renderer/checkout.html', '.generated/editor/code-editor.js', '.generated/editor/code-editor.css', '.generated/run.html', '.generated/dashboard/skins/graphite/index.css',
  'node_modules/@modelcontextprotocol/sdk/package.json', 'node_modules/yaml/package.json']) {
  if (!statFile(archive, file).size) throw Error('Empty package entry: ' + file);
}
if (process.platform === 'darwin') {
  const plist = path.join(bundle, 'AFM Desktop.app/Contents/Info.plist');
  const icon = execFileSync('/usr/libexec/PlistBuddy', ['-c', 'Print :CFBundleIconFile', plist], {encoding: 'utf8'}).trim();
  const expected = await readFile('assets/afm-desktop.icns');
  if (!expected.equals(await readFile(path.join(resources, icon)))) throw Error('The Mac package does not carry the AFM Desktop icon.');
  const scheme = execFileSync('/usr/libexec/PlistBuddy', ['-c', 'Print :CFBundleURLTypes:0:CFBundleURLSchemes:0', plist], { encoding: 'utf8' }).trim();
  if (scheme !== 'afm') throw Error('The Mac package does not declare the afm URL scheme.');
} else if (process.platform === 'linux') {
  const launcher = await stat(path.join(bundle, 'register-afm-links'));
  if (!(launcher.mode & 0o111)) throw Error('The Linux package is missing an executable AFM link registrar.');
}
// The executable inside the package, not the one the build chose, must carry the stage browser hook and fit this machine.
const sidecar = await verifySidecar(binary);
console.log(bundle, sidecar.version, sidecar.platform + '/' + sidecar.arch);

// What the tests resolve and what the package carries must be the same executable.
const digest = async file => createHash('sha256').update(await readFile(file)).digest('hex');
const [tested, packaged] = [await digest(await afmBinary()), await digest(binary)];
if (tested !== packaged) throw Error(`The package carries a different AFM (${packaged.slice(0, 12)}) than the tests used (${tested.slice(0, 12)}).`);
console.log('AFM sha256', packaged);

// The pinned chrome-devtools-mcp must start from the package alone: no copy on PATH, no Node or npm install.
const mcpScript = path.join(resources, 'app.asar.unpacked/node_modules/chrome-devtools-mcp/build/src/bin/chrome-devtools-mcp.js');
const electron = process.platform === 'darwin' ? path.join(bundle, 'AFM Desktop.app/Contents/MacOS/AFM Desktop')
  : path.join(bundle, process.platform === 'win32' ? 'AFM Desktop.exe' : 'AFM Desktop');
const home = await mkdtemp(path.join(os.tmpdir(), 'afm-desktop-mcp-'));
// The packaged files are always run (with this Node); the app's own runtime is run too where it can load. A Mac
// must run it; a Linux container without the GUI libraries cannot, and says so instead of passing silently.
async function mcpVersion(node, env) {
  await writeMcpWrapper(home, { node, script: mcpScript, env });
  return execFileSync(mcpWrapperPath(home), ['--version'], { encoding: 'utf8', env: { HOME: home, PATH: '/usr/bin:/bin' } }).trim();
}
try {
  const version = await mcpVersion(process.execPath, {});
  if (version !== '1.12.0') throw Error('Packaged chrome-devtools-mcp reports ' + version + ', expected the pinned 1.12.0.');
  console.log('chrome-devtools-mcp', version, '(packaged files, no external PATH)');
  try {
    console.log('chrome-devtools-mcp', await mcpVersion(electron, { ELECTRON_RUN_AS_NODE: '1' }), '(packaged runtime)');
  } catch (error) {
    if (process.platform === 'darwin' || !/shared libraries/.test(String(error.stderr))) throw error;
    console.log('packaged runtime not executed: this machine lacks the GUI libraries Electron loads (Mac check only)');
  }
} finally { await rm(home, { recursive: true, force: true }); }

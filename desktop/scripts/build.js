import { execFileSync } from 'node:child_process';
import { packager } from '@electron/packager';
import { copyFile, chmod, mkdir, readdir, access } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { afmBinary } from '../src/binary.js';
import { verifySidecar } from '../src/sidecar.js';

const resource = path.resolve('.generated/resources', process.platform === 'win32' ? 'afm.exe' : 'afm');
await mkdir(path.dirname(resource), { recursive: true });

// The AFM the tests resolve is the one that ships: the same lookup (override, staged sidecar, PATH), and it must
// match this OS and CPU and carry the stage browser hook, or the build stops.
const source = await afmBinary();
try { await verifySidecar(source, { checkVersion: false }); } catch (error) { throw Error(error.message + ' Run npm run sidecar to build the AFM of this branch.'); }
console.log('Bundling AFM from', source);
await copyFile(source, resource);
await chmod(resource, 0o755);

// Electron's zip is looked up locally first so the build needs no network: ELECTRON_ZIP_DIR, a copy kept in dist/,
// then Electron's own download cache. A zip found in the cache is kept in dist/ for runs with another HOME.
const electronVersion = (await import('electron/package.json', { with: { type: 'json' } })).default.version;
const zipName = `electron-v${electronVersion}-${process.platform}-${process.arch}.zip`;
const keptZips = path.resolve('dist', 'electron-zip');
async function findElectronZip() {
  const exists = file => access(file).then(() => true, () => false);
  const dirs = [process.env.ELECTRON_ZIP_DIR, keptZips].filter(Boolean);
  for (const dir of dirs) if (await exists(path.join(dir, zipName))) return dir;
  const cacheRoot = process.env.ELECTRON_CACHE || path.join(os.homedir(), '.cache', 'electron');
  for (const entry of await readdir(cacheRoot).catch(() => [])) {
    const candidate = path.join(cacheRoot, entry, zipName);
    if (await exists(candidate)) {
      await mkdir(keptZips, { recursive: true });
      await copyFile(candidate, path.join(keptZips, zipName));
      return keptZips;
    }
  }
  return undefined;
}
const electronZipDir = await findElectronZip();
const identity = process.env.AFM_DESKTOP_SIGN_IDENTITY;
const dirs = await packager({
  dir: '.', name: 'AFM Desktop', out: 'dist', overwrite: true, asar: { unpackDir: 'node_modules/chrome-devtools-mcp' },
  ...(process.platform === 'darwin' ? {icon: 'assets/afm-desktop.icns'} : {}),
  appBundleId: 'dev.afm.desktop', ...(electronZipDir ? { electronZipDir } : {}), osxSign: identity ? {identity, continueOnError: false} : undefined, extraResource: [resource],
  ignore: [/^\/\.generated\/resources/, /^\/(dist|test|scripts|workflow|homebrew)(\/|$)/]
});
if (process.platform === 'darwin') {
  for (const dir of dirs) {
    const bundle = path.join(dir, 'AFM Desktop.app');
    if (!identity) execFileSync('codesign', ['--force', '--deep', '--sign', '-', bundle], {stdio: 'inherit'});
    execFileSync('codesign', ['--verify', '--deep', bundle], {stdio: 'inherit'});
  }
}
console.log(dirs.join('\n'));

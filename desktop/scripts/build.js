import { execFileSync } from 'node:child_process';
import { packager } from '@electron/packager';
import { copyFile, chmod, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { verifySidecar } from '../src/sidecar.js';
import { access } from 'node:fs/promises';
import { constants } from 'node:fs';

const resource = path.resolve('.generated/resources', process.platform === 'win32' ? 'afm.exe' : 'afm');
await mkdir(path.dirname(resource), { recursive: true });

// The bundled AFM must be built for this OS and CPU from this branch. An explicit override that fails is an error,
// never skipped; the staged build and then PATH are tried in order.
const name = path.basename(resource);
const staged = path.resolve('dist', 'sidecars', `${process.platform}-${process.arch}`, name);
const onPath = (process.env.PATH || '').split(path.delimiter).filter(Boolean).map(dir => path.join(dir, name));
const explicit = process.env.AFM_DESKTOP_BINARY ? [path.resolve(process.env.AFM_DESKTOP_BINARY)] : null;
let source = null;
const refused = [];
for (const candidate of explicit ?? [staged, ...onPath]) {
  try { await access(candidate, constants.X_OK); } catch { continue; }
  try { await verifySidecar(candidate); source = candidate; break; } catch (error) { refused.push(error.message); if (explicit) break; }
}
if (!source) throw Error('No usable AFM executable for ' + process.platform + '/' + process.arch + '. ' + (refused.join(' ') || 'Run npm run sidecar first.'));
console.log('Bundling AFM from', source);
await copyFile(source, resource);
await chmod(resource, 0o755);
const identity = process.env.AFM_DESKTOP_SIGN_IDENTITY;
const dirs = await packager({
  dir: '.', name: 'AFM Desktop', out: 'dist', overwrite: true, asar: true,
  appBundleId: 'dev.afm.desktop', osxSign: identity ? {identity, continueOnError: false} : undefined, extraResource: [resource],
  ignore: [/^\/\.generated\/resources/, /^\/(dist|test|scripts|workflow)(\/|$)/]
});
if (process.platform === 'darwin') {
  for (const dir of dirs) {
    const bundle = path.join(dir, 'AFM Desktop.app');
    if (!identity) execFileSync('codesign', ['--force', '--deep', '--sign', '-', bundle], {stdio: 'inherit'});
    execFileSync('codesign', ['--verify', '--deep', bundle], {stdio: 'inherit'});
  }
}
console.log(dirs.join('\n'));

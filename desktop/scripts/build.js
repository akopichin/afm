import { execFileSync } from 'node:child_process';
import { packager } from '@electron/packager';
import { copyFile, chmod, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { afmBinary } from '../src/binary.js';
import { verifySidecar } from '../src/sidecar.js';

const resource = path.resolve('.generated/resources', process.platform === 'win32' ? 'afm.exe' : 'afm');
await mkdir(path.dirname(resource), { recursive: true });

// The AFM the tests resolve is the one that ships: the same lookup (override, staged sidecar, PATH), and it must
// match this OS and CPU and carry the stage browser hook, or the build stops.
const source = await afmBinary();
try { await verifySidecar(source); } catch (error) { throw Error(error.message + ' Run npm run sidecar to build the AFM of this branch.'); }
console.log('Bundling AFM from', source);
await copyFile(source, resource);
await chmod(resource, 0o755);
const identity = process.env.AFM_DESKTOP_SIGN_IDENTITY;
const dirs = await packager({
  dir: '.', name: 'AFM Desktop', out: 'dist', overwrite: true, asar: { unpackDir: 'node_modules/chrome-devtools-mcp' },
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

import { execFileSync } from 'node:child_process';
import { packager } from '@electron/packager';
import { copyFile, chmod, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { afmBinary } from '../src/binary.js';

const resource = path.resolve('.generated/resources', process.platform === 'win32' ? 'afm.exe' : 'afm');
await mkdir(path.dirname(resource), { recursive: true });
await copyFile(await afmBinary(), resource);
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

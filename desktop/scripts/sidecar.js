// Builds the AFM executable Desktop bundles, for this machine's OS and CPU, from the checked-out branch.
// The output is the one place the package build looks first: dist/sidecars/<platform>-<arch>/afm.
import { execFileSync } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { verifySidecar } from '../src/sidecar.js';

const repository = path.resolve('..');
const output = path.resolve('dist', 'sidecars', `${process.platform}-${process.arch}`, process.platform === 'win32' ? 'afm.exe' : 'afm');
await mkdir(path.dirname(output), { recursive: true });
const revision = execFileSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: repository, encoding: 'utf8' }).trim();
let version = '0.0.0';
try { version = execFileSync('git', ['describe', '--tags', '--abbrev=0'], { cwd: repository, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim().replace(/^v/, ''); } catch { /* No tag in this checkout. */ }
// GOWORK=off builds against the pinned modules, as a clean checkout does.
execFileSync('go', ['build', '-trimpath', '-ldflags', `-X main.version=${version}-desktop.${revision}`, '-o', output, './cmd/afm'],
  { cwd: repository, stdio: 'inherit', env: { ...process.env, GOWORK: process.env.GOWORK ?? 'off', CGO_ENABLED: '0' } });
console.log(output, JSON.stringify(await verifySidecar(output, { checkVersion: false })));

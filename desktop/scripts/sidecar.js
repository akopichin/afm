// Builds the AFM executable Desktop bundles from the checked-out branch.
// The output is the one place the package build looks first: dist/sidecars/<platform>-<arch>/afm.
import { execFileSync } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { verifySidecar } from '../src/sidecar.js';

const repository = path.resolve('..');
const goos = process.env.GOOS;
const goarch = process.env.GOARCH;
const platform = goos === 'windows' ? 'win32' : goos || process.platform;
const arch = goarch === 'amd64' ? 'x64' : goarch || process.arch;
const output = path.resolve('dist', 'sidecars', `${platform}-${arch}`, platform === 'win32' ? 'afm.exe' : 'afm');
await mkdir(path.dirname(output), { recursive: true });
const revision = execFileSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: repository, encoding: 'utf8' }).trim();
let version = '0.0.0';
try { version = execFileSync('git', ['describe', '--tags', '--match', 'v[0-9]*', '--abbrev=0'], { cwd: repository, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim().replace(/^v/, ''); } catch { /* No tag in this checkout. */ }
// GOWORK=off builds against the pinned modules, as a clean checkout does.
execFileSync('go', ['build', '-trimpath', '-ldflags', `-X main.version=${version}-desktop.${revision}`, '-o', output, './cmd/afm'],
  { cwd: repository, stdio: 'inherit', env: { ...process.env, GOWORK: process.env.GOWORK ?? 'off', CGO_ENABLED: '0' } });
console.log(output, JSON.stringify(await verifySidecar(output, { platform, arch, checkVersion: false })));

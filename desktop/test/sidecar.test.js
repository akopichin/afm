import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile, access } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { CAPABILITY, executableFormat, verifySidecar } from '../src/sidecar.js';

function header(kind, arch, tail = '') {
  const head = Buffer.alloc(64);
  if (kind === 'elf') { head.writeUInt32BE(0x7f454c46, 0); head[5] = 1; head.writeUInt16LE(arch === 'arm64' ? 0xb7 : 0x3e, 18); }
  if (kind === 'macho') { head.writeUInt32LE(0xfeedfacf, 0); head.writeUInt32LE(arch === 'arm64' ? 0x0100000c : 0x01000007, 4); }
  return Buffer.concat([head, Buffer.from(tail)]);
}

async function file(t, bytes) {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'sidecar-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const target = path.join(dir, 'afm');
  await writeFile(target, bytes, { mode: 0o755 });
  return target;
}

test('the bundled AFM must match the target OS and CPU and carry the stage browser hook', async t => {
  const mac = await file(t, header('macho', 'arm64', ` ${CAPABILITY} `));
  assert.deepEqual(await executableFormat(mac), { platform: 'darwin', arch: 'arm64' });
  assert.deepEqual(await verifySidecar(mac, { platform: 'darwin', arch: 'arm64', checkVersion: false }), { platform: 'darwin', arch: 'arm64', version: null }, 'inspect the package before macOS grants permission to run it');
  const linux = await file(t, header('elf', 'x64', CAPABILITY));
  await assert.rejects(verifySidecar(linux, { platform: 'darwin', arch: 'arm64' }), /linux\/x64, not darwin\/arm64/, 'a Linux binary never goes into the Mac app');
  await assert.rejects(verifySidecar(mac, { platform: 'darwin', arch: 'x64' }), /darwin\/arm64, not darwin\/x64/);
  const stale = await file(t, header('macho', 'arm64', 'afm version 3.1.11'));
  await assert.rejects(verifySidecar(stale, { platform: 'darwin', arch: 'arm64' }), /does not support AFM_CHROME_EXECUTABLE/, 'an AFM without the hook is stale');
  const script = await file(t, '#!/bin/sh\necho afm\n');
  await assert.rejects(verifySidecar(script), /not a native executable/);
});

test('a branch build of this machine is accepted, and an AFM from PATH without the hook is rejected', async t => {
  const staged = process.env.AFM_DESKTOP_BINARY || path.resolve('dist', 'sidecars', `${process.platform}-${process.arch}`, 'afm');
  const present = await access(staged).then(() => true, () => false);
  if (!present) return t.skip('no staged sidecar for this machine');
  assert.match((await verifySidecar(staged)).version, /\d+\.\d+/);
  for (const dir of (process.env.PATH || '').split(path.delimiter)) {
    const candidate = path.join(dir, 'afm');
    if (candidate === staged || !(await access(candidate).then(() => true, () => false))) continue;
    const verdict = await verifySidecar(candidate).then(() => 'usable', error => error.message);
    assert.match(verdict, /^usable$|does not support|built for/, candidate);
  }
});

import { execFileSync } from 'node:child_process';
import { open, readFile } from 'node:fs/promises';

// The string AFM compiled in when it learned to launch the stage browser named by this variable. An AFM
// without it ignores Desktop's companion, so `--version` alone cannot tell a usable sidecar from a stale one.
export const CAPABILITY = 'AFM_CHROME_EXECUTABLE';

const ELF_MACHINES = { 0x3e: 'x64', 0xb7: 'arm64' };
const MACHO_CPUS = { 0x01000007: 'x64', 0x0100000c: 'arm64' };

// Operating system and CPU of an executable, from its header. Null for anything this app does not ship.
export async function executableFormat(file) {
  const handle = await open(file, 'r');
  try {
    const head = Buffer.alloc(32);
    const { bytesRead } = await handle.read(head, 0, head.length, 0);
    if (bytesRead < 20) return null;
    if (head.readUInt32BE(0) === 0x7f454c46 && head[5] === 1) return { platform: 'linux', arch: ELF_MACHINES[head.readUInt16LE(18)] ?? null };
    if (head.readUInt32LE(0) === 0xfeedfacf) return { platform: 'darwin', arch: MACHO_CPUS[head.readUInt32LE(4)] ?? null };
    if (head.readUInt16BE(0) === 0x4d5a) return { platform: 'win32', arch: null };
    return null;
  } finally { await handle.close(); }
}

// Refuses an executable that is for another OS or CPU, or that lacks the stage browser hook.
// Runs it for its version only when it can run here. Returns what was verified.
export async function verifySidecar(file, { platform = process.platform, arch = process.arch } = {}) {
  const format = await executableFormat(file);
  if (!format) throw Error(`${file} is not a native executable.`);
  if (format.platform !== platform || (format.arch && format.arch !== arch))
    throw Error(`${file} is built for ${format.platform}/${format.arch ?? '?'}, not ${platform}/${arch}.`);
  if (!(await readFile(file)).includes(CAPABILITY))
    throw Error(`${file} does not support ${CAPABILITY}, so the stage browser would not reach Desktop. Build AFM from this branch (npm run sidecar).`);
  const runnable = platform === process.platform && arch === process.arch;
  const version = runnable ? execFileSync(file, ['--version'], { encoding: 'utf8' }).trim() : null;
  return { platform, arch, version };
}

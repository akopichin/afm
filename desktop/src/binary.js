import { access } from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';

export async function afmBinary(resources) {
  const filename = process.platform === 'win32' ? 'afm.exe' : 'afm';
  const candidates = resources ? [path.join(resources, filename)] : process.env.AFM_DESKTOP_BINARY
    ? [path.resolve(process.env.AFM_DESKTOP_BINARY)]
    : (process.env.PATH || '').split(path.delimiter).filter(Boolean).map(dir => path.join(dir, filename));
  for (const candidate of candidates) {
    try { await access(candidate, constants.X_OK); return candidate; } catch { /* Try the next PATH entry. */ }
  }
  throw Error('AFM executable missing. For development set AFM_DESKTOP_BINARY to its absolute path.');
}

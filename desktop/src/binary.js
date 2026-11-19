import { access } from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';

const filename = process.platform === 'win32' ? 'afm.exe' : 'afm';

// What `npm run sidecar` builds for this machine from this branch.
export const stagedSidecar = path.resolve(import.meta.dirname, '..', 'dist', 'sidecars', `${process.platform}-${process.arch}`, filename);

// Where to look, in order: the packaged copy; else AFM_DESKTOP_BINARY alone; else the staged sidecar, then PATH.
// The package build and the tests resolve it the same way, so what is tested is what ships.
export function afmCandidates(resources) {
  if (resources) return [path.join(resources, filename)];
  if (process.env.AFM_DESKTOP_BINARY) return [path.resolve(process.env.AFM_DESKTOP_BINARY)];
  return [stagedSidecar, ...(process.env.PATH || '').split(path.delimiter).filter(Boolean).map(dir => path.join(dir, filename))];
}

export async function afmBinary(resources) {
  const candidates = afmCandidates(resources);
  for (const candidate of candidates) {
    try { await access(candidate, constants.X_OK); return candidate; } catch { /* Try the next PATH entry. */ }
  }
  throw Error('AFM executable missing. For development set AFM_DESKTOP_BINARY to its absolute path.');
}

import { mkdir, readFile, rename, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { randomUUID } from 'node:crypto';

// Desktop keeps its own files under ~/.afm/desktop so shared AFM files stay untouched.
export function desktopDir(home) { return path.join(home, '.afm', 'desktop'); }

// Write through a temp file in the same directory so readers never see a partial file.
export async function writeAtomic(file, content, mode = 0o600) {
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  const temp = `${file}.${process.pid}.${randomUUID()}.tmp`;
  try {
    await writeFile(temp, content, { mode });
    await rename(temp, file);
  } catch (error) {
    await rm(temp, { force: true });
    throw error;
  }
}

export async function readText(file) {
  try { return await readFile(file, 'utf8'); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
}

export async function readJSON(file, fallback) {
  const text = await readText(file);
  if (text === null) return fallback;
  try { return JSON.parse(text); }
  catch { return fallback; }
}

export function writeJSON(file, value) { return writeAtomic(file, JSON.stringify(value, null, 2) + '\n'); }

// Serializes async read-modify-write cycles of one store inside this process.
export class Mutex {
  #tail = Promise.resolve();
  run(task) {
    const result = this.#tail.then(task);
    this.#tail = result.catch(() => {});
    return result;
  }
}

import path from 'node:path';
import { fileURLToPath } from 'node:url';

const LOOPBACK = new Set(['127.0.0.1', 'localhost', '[::1]']);
const SAME_ORIGIN = { 'ws:': 'http:', 'wss:': 'https:' };

// Origins of dashboards the user opened in this window. An origin leaves the set when its run does,
// so the set stays as small as the list of live runs.
export class DashboardOrigins {
  #runByOrigin = new Map();
  add(origin, runKey) { this.#runByOrigin.set(origin, runKey); }
  has(origin) { return this.#runByOrigin.has(origin); }
  retain(liveRunKeys) {
    for (const [origin, key] of this.#runByOrigin) if (!liveRunKeys.has(key)) this.#runByOrigin.delete(origin);
  }
  get size() { return this.#runByOrigin.size; }
}

// The network and file policy of the app session: the shell's own files, inline data, and the
// dashboards the user opened. Anything else, including other local ports, never leaves the process.
export function allowRequest(rawUrl, { appRoot, origins, allowGitHub = false }) {
  let url;
  try { url = new URL(rawUrl); } catch { return false; }
  if (url.protocol === 'data:' || url.protocol === 'blob:') return true;
  if (url.protocol === 'file:') {
    const file = path.resolve(fileURLToPath(url));
    return file === appRoot || file.startsWith(appRoot + path.sep);
  }
  if (allowGitHub && url.origin === 'https://api.github.com') return true;
  const http = SAME_ORIGIN[url.protocol] ?? url.protocol;
  if (http !== 'http:' || !LOOPBACK.has(url.hostname) || url.username || url.password) return false;
  return origins.has(`http://${url.host}`);
}

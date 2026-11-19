import { mkdtemp, mkdir, readFile, rm, writeFile, chmod } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Assistant } from '../src/assistant/assistant.js';
import { eventually } from './support.js';

const fake = fileURLToPath(new URL('./fake-provider.js', import.meta.url));
const bridgeScript = fileURLToPath(new URL('../src/assistant/bridge.js', import.meta.url));

// A disposable home with fake `claude` and `codex` executables on a private PATH.
export async function providerHome(t, vars = {}) {
  const home = await mkdtemp(path.join(os.tmpdir(), 'assistant-'));
  const bin = path.join(home, 'bin');
  await mkdir(bin);
  for (const name of ['claude', 'codex']) {
    await writeFile(path.join(bin, name), `#!/bin/sh\nexec "${process.execPath}" "${fake}" ${name} "$@"\n`);
    await chmod(path.join(bin, name), 0o755);
  }
  const record = path.join(home, 'record.jsonl');
  const env = { PATH: bin, HOME: home, FAKE_RECORD: record, ...vars };
  t.after(() => rm(home, { recursive: true, force: true }));
  const invocations = async () => (await readFile(record, 'utf8').catch(() => '')).split('\n').filter(Boolean).map(line => JSON.parse(line));
  return { home, bin, env, record, invocations };
}

export const stubContext = (extra = {}) => ({
  endpoint: async () => { throw Error('no host'); }, runs: async () => [], project: async () => { throw Error('unknown'); }, ...extra });

export function makeAssistant(t, ctx, options = {}) {
  const assistant = new Assistant({ home: ctx.home, env: ctx.env, context: stubContext(), bridge: { command: process.execPath, script: bridgeScript, env: {} }, grace: 300, systemDirs: [], ...options });
  const events = [];
  assistant.onEvent(event => events.push({ ...event, at: Date.now() }));
  t.after(() => assistant.close());
  return { assistant, events };
}

export const until = (events, type, what = type) => eventually(() => events.find(event => event.type === type), { timeout: 15000, what });
export const settled = events => eventually(() => events.find(event => ['done', 'error', 'cancelled'].includes(event.type)), { timeout: 15000, what: 'end of the reply' });

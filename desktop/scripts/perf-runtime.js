// Main-process workloads of DS-09: idle inventory, attention bursts, streamed chat and subscriber churn.
// Each prints one JSON line; budgets are asserted so a regression fails `npm run perf`.
import assert from 'node:assert/strict';
import { chmod, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { fileURLToPath } from 'node:url';
import { Assistant } from '../src/assistant/assistant.js';
import { Workspace } from '../src/workspace.js';

const fake = fileURLToPath(new URL('../test/fake-provider.js', import.meta.url));
const percentile = (samples, p) => [...samples].sort((a, b) => a - b)[Math.ceil(p * samples.length) - 1];
const report = (name, data) => console.log(JSON.stringify({ workload: name, node: process.version, platform: process.platform, arch: process.arch, ...data }));

function fixture({ projects = 100, runs = 1000, flagged = 0 } = {}) {
  const view = i => ({ key: `instance-${i}:${i}`, runId: String(i), instanceId: `instance-${i}`, name: `flow-${i}`, projectRoot: `/work/p${i % projects}`,
    apiUrl: `http://127.0.0.1:${20000 + i}`, state: 'running', attention: i < flagged });
  const all = Array.from({ length: runs }, (_, i) => view(i));
  const overview = { host: { state: 'connected', error: null }, unassigned: [], candidates: [],
    projects: Array.from({ length: projects }, (_, p) => ({ id: `p${p}`, name: `p${p}`, path: `/work/p${p}`, runs: all.filter((_, i) => i % projects === p) })) };
  return { overview, calls: { overview: 0 } };
}

// A run API that always reports one stage waiting for an answer and tracks how many reads are in flight.
function runApi() {
  const stats = { requests: 0, inflight: 0, peak: 0 };
  const request = async url => {
    stats.requests++;
    stats.peak = Math.max(stats.peak, ++stats.inflight);
    await new Promise(resolve => setImmediate(resolve));
    stats.inflight--;
    const port = Number(new URL(url).port) - 20000;
    const body = url.endsWith('/api/runtime') ? { run_id: String(port), instance_id: `instance-${port}` }
      : { run_id: String(port), stages: [{ id: 's', name: 's', status: 'awaiting_user_input' }] };
    return { ok: true, json: async () => body };
  };
  return { request, stats };
}

function workspaceFor(data, request) {
  const desktop = { overview: async () => { data.calls.overview++; return structuredClone(data.overview); } };
  return new Workspace({ desktop, request, interval: 3_600_000 });
}

async function idleInventory() {
  const data = fixture({ flagged: 20 });
  const { request, stats } = runApi();
  const workspace = workspaceFor(data, request);
  let updates = 0;
  workspace.onChange(() => updates++);
  await workspace.refresh();
  const first = { updates, requests: stats.requests };
  const samples = [];
  const requestsBefore = stats.requests;
  for (let i = 0; i < 55; i++) {
    const start = performance.now();
    await workspace.refresh();
    if (i >= 5) samples.push(performance.now() - start);
  }
  assert.equal(updates, first.updates, 'An identical poll notified the renderer');
  // Ten callers racing share one build.
  const before = data.calls.overview;
  await Promise.all(Array.from({ length: 10 }, () => workspace.refresh()));
  assert.equal(data.calls.overview - before, 1, 'Concurrent refreshes were not single-flight');
  workspace.close();
  const p95 = percentile(samples, 0.95);
  report('idle-inventory', { projects: 100, runs: 1000, flagged: 20, samples: samples.length, p50_ms: percentile(samples, 0.5), p95_ms: p95,
    renderer_updates_when_identical: 0, run_reads_per_poll: (stats.requests - requestsBefore) / 55 });
  assert.ok(p95 < 50, 'Idle poll exceeded its 50 ms p95 budget');
}

async function attentionBurst() {
  const data = fixture({ projects: 100, runs: 1000, flagged: 200 });
  const { request, stats } = runApi();
  const workspace = workspaceFor(data, request);
  const start = performance.now();
  const snapshot = await workspace.refresh();
  const first = performance.now() - start;
  assert.equal(snapshot.attention.length, 200);
  const order = snapshot.attention.map(item => item.id);
  const samples = [];
  for (let i = 0; i < 10; i++) {
    const t = performance.now();
    const next = await workspace.refresh();
    samples.push(performance.now() - t);
    assert.deepEqual(next.attention.map(item => item.id), order, 'Notice order changed between polls');
  }
  assert.ok(stats.peak <= 6, 'More than six run reads were in flight');
  assert.equal(stats.inflight, 0, 'Run reads were left outstanding');
  workspace.close();
  report('attention-burst', { flagged_runs: 200, first_ms: first, repeat_p95_ms: percentile(samples, 0.95), peak_inflight_requests: stats.peak, outstanding_after: stats.inflight });
  assert.ok(percentile(samples, 0.95) < 100, 'Attention burst exceeded its 100 ms p95 budget');
}

async function subscriberChurn() {
  const data = fixture({ flagged: 5 });
  const { request, stats } = runApi();
  const workspace = new Workspace({ desktop: { overview: async () => { data.calls.overview++; return structuredClone(data.overview); } }, request, interval: 5 });
  const start = performance.now();
  for (let i = 0; i < 100; i++) {
    const off = workspace.onChange(() => {});
    workspace.setVisible(true);
    workspace.setVisible(false);
    off();
  }
  await workspace.refresh().catch(() => {});
  const listeners = workspace.listeners.size;
  const callsAfterHide = data.calls.overview;
  await new Promise(resolve => setTimeout(resolve, 60));
  assert.equal(workspace.listeners.size, 0, 'Subscribers leaked after 100 switches');
  assert.equal(data.calls.overview, callsAfterHide, 'A hidden window kept polling');
  assert.equal(stats.inflight, 0, 'Requests were left outstanding');
  workspace.close();
  report('subscriber-churn', { switches: 100, elapsed_ms: performance.now() - start, subscribers_after: listeners, overview_calls: callsAfterHide, polls_while_hidden: 0, outstanding_requests: stats.inflight });
}

async function streamedChat() {
  const home = await mkdtemp(path.join(os.tmpdir(), 'perf-chat-'));
  try {
    const bin = path.join(home, 'bin');
    await mkdir(bin);
    for (const name of ['claude', 'codex']) {
      await writeFile(path.join(bin, name), `#!/bin/sh\nexec "${process.execPath}" "${fake}" ${name} "$@"\n`);
      await chmod(path.join(bin, name), 0o755);
    }
    const deltas = 2500;
    const env = { PATH: bin, HOME: home, FAKE_SCENARIO: 'flood', FAKE_DELTAS: String(deltas) };
    const assistant = new Assistant({ home, env, context: { endpoint: async () => { throw Error('no host'); }, runs: async () => [], project: async () => { throw Error('unknown'); } },
      bridge: { command: process.execPath, script: '', env: {} }, grace: 300, systemDirs: [] });
    const events = [];
    let ended;
    const finished = new Promise(resolve => { ended = resolve; });
    assistant.onEvent(event => {
      events.push({ type: event.type, bytes: event.text?.length ?? 0 });
      if (['done', 'error', 'cancelled'].includes(event.type)) ended(event);
    });
    const start = performance.now();
    await assistant.send({ text: 'flood' });
    const end = await finished;
    const elapsed = performance.now() - start;
    assert.equal(end.type, 'done', end.message);
    const sent = events.filter(event => event.type === 'delta');
    const text = end.reply.text;
    assert.ok(text.startsWith('word0 ') && text.endsWith(`word${deltas - 1} `), 'The streamed text lost or reordered deltas');
    report('streamed-chat', { provider_deltas: deltas, ipc_delta_messages: sent.length, reply_chars: text.length, elapsed_ms: elapsed });
    if (process.env.PERF_BASELINE) return;
    assert.ok(sent.length <= deltas / 5, 'Streamed deltas were not coalesced');
    await assistant.close();
  } finally { await rm(home, { recursive: true, force: true }); }
}

await idleInventory();
await attentionBurst();
await subscriberChurn();
await streamedChat();

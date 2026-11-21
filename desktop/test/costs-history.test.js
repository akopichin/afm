import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { CostsHistory, GAP_MS, MAX_SAMPLES, RETENTION_MS, SAMPLE_MS } from '../src/costs-history.js';
import { Costs } from '../src/costs.js';

const T0 = Date.parse('2026-11-20T10:00:00Z');
const MIN = 60000;
const FIELDS = ['elapsedMs', 'idleMs', 'workMs', 'tokens', 'usd'];

const entry = (key, elapsed, extra = {}) => ({ key, name: key, projectId: 'p', stale: false, startedAt: T0,
  elapsed: { ms: elapsed, since: null }, idle: { ms: 0, since: null }, stageWork: { ms: elapsed / 2, since: [] },
  tokens: elapsed, money: { usd: elapsed / 1000 }, ...extra });

// An entry whose every figure is given exactly.
const figures = (key, { elapsedMs, idleMs, workMs, tokens, usd }) => entry(key, elapsedMs,
  { idle: { ms: idleMs, since: null }, stageWork: { ms: workMs, since: [] }, tokens, money: { usd } });

async function home(t) {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'costs-history-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  return dir;
}

async function reload(dir) {
  const history = new CostsHistory(dir);
  await history.load();
  return history;
}

const growth = (history, field, from, to) => {
  const { value, gaps } = history.delta('i:r', field, from, to);
  return { value, gaps };
};

// A history file written by an earlier Desktop.
async function legacy(t, run, version = 1) {
  const dir = await home(t);
  await mkdir(path.join(dir, '.afm', 'desktop'), { recursive: true });
  await writeFile(path.join(dir, '.afm', 'desktop', 'costs-history.json'), JSON.stringify({ version, runs: { 'i:r': run } }));
  return reload(dir);
}

// A verified status as AFM sends it; `work` null is a started stage whose ledger is incomplete.
const statusAt = (minute, work) => ({ run_id: 'r', run_status: 'running', started_at: new Date(T0).toISOString(),
  elapsed_accumulated_ms: 1000 * (minute + 1), elapsed_since: null, idle_accumulated_ms: 0, idle_since: null,
  accounting: { health: 'ok', has_data: true, show_money: true },
  run_cost: { estimated_cost_usd: minute + 1, total_tokens: 100 * (minute + 1) },
  stages: [{ id: 'a', status: 'running', timing: work === null ? { started: true, complete: false } : { started: true, complete: true, work_ms: work } }] });

test('history survives a restart and a first sample is a baseline, not a change', async t => {
  const dir = await home(t);
  const first = new CostsHistory(dir);
  await first.record([entry('i:r', 1000)], T0);
  await first.record([entry('i:r', 3000)], T0 + MIN);
  const second = new CostsHistory(dir);
  await second.load();
  assert.equal(second.run('i:r').samples.length, 2);
  assert.equal(second.delta('i:r', 'tokens', T0 - MIN, T0 + 2 * MIN).value, 2000, 'the first value is only the starting point');
});

test('an unchanged run adds no sample', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('i:r', 1000)], T0);
  await history.record([entry('i:r', 1000)], T0 + MIN);
  assert.equal(history.run('i:r').samples.length, 1);
});

test('a gap is kept, its growth is counted in no period, and growth after it counts again', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('i:r', 1000)], T0);
  await history.record([entry('i:r', 5000)], T0 + 6 * MIN);
  await history.record([entry('i:r', 5600)], T0 + 7 * MIN);
  assert.equal(history.delta('i:r', 'tokens', T0, T0 + 10 * MIN).gaps, 1);
  assert.equal(history.delta('i:r', 'tokens', T0, T0 + 10 * MIN).value, 600);
  assert.equal(history.delta('i:r', 'tokens', T0 + 5 * MIN, T0 + 6 * MIN).value, 0, 'a period starting inside the gap gets none of it');
});

test('a known outage is remembered while it lasts and after the run answers again, across a reload', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  await history.record([entry('i:r', 1000)], T0);
  await history.record([entry('i:r', 1000, { stale: true })], T0 + MIN);
  assert.equal(history.delta('i:r', 'tokens', T0, T0 + 2 * MIN).gaps, 1, 'an outage still going on');
  assert.equal((await reload(dir)).delta('i:r', 'tokens', T0, T0 + 2 * MIN).gaps, 1, 'an outage still going on, after a restart');
  await history.markOutage(['i:r'], T0 + 2 * MIN);
  await history.record([entry('i:r', 3000)], T0 + 4 * MIN);
  const reloaded = new CostsHistory(dir);
  await reloaded.load();
  assert.equal(reloaded.delta('i:r', 'tokens', T0 + MIN, T0 + 2 * MIN).gaps, 1, 'a moment inside the outage');
  assert.equal(reloaded.delta('i:r', 'tokens', T0, T0 + 5 * MIN).value, 0, 'what changed during the outage is not invented');
});

test('every metric resets on its own without cancelling later growth', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('i:r', 9000)], T0);
  await history.record([entry('i:r', 9100, { tokens: 100, money: { usd: 0.1 } })], T0 + MIN);
  await history.record([entry('i:r', 9600, { tokens: 600, money: { usd: 0.6 } })], T0 + 2 * MIN);
  const tokens = history.delta('i:r', 'tokens', T0 - MIN, T0 + 3 * MIN);
  assert.equal(tokens.value, 500);
  assert.equal(tokens.gaps, 1);
  assert.equal(history.delta('i:r', 'usd', T0 - MIN, T0 + 3 * MIN).value, 0.5);
  const elapsed = history.delta('i:r', 'elapsedMs', T0 - MIN, T0 + 3 * MIN);
  assert.equal(elapsed.value, 600);
  assert.equal(elapsed.gaps, 0);
});

test('growth before and after a reset of any one figure survives thinning and a restart', async t => {
  for (const field of FIELDS) {
    const dir = await home(t);
    const history = new CostsHistory(dir);
    const at = minute => {
      const values = Object.fromEntries(FIELDS.map(name => [name, 1000 * (minute + 1)]));
      return figures('i:r', { ...values, [field]: [1000, 9000, 100, 600][minute] ?? 700 });
    };
    for (const minute of [0, 1, 2, 3]) await history.record([at(minute)], T0 + minute * MIN);
    const expected = name => (name === field ? { value: 8500, gaps: 1 } : { value: 3000, gaps: 0 });
    for (const name of FIELDS) assert.deepEqual(growth(history, name, T0, T0 + 3 * MIN), expected(name), `${field} reset, ${name}`);
    await history.record([at(70)], T0 + 70 * MIN);
    const reloaded = await reload(dir);
    for (const name of FIELDS) {
      assert.deepEqual(growth(history, name, T0, T0 + 3 * MIN), expected(name), `${field} reset, ${name}, thinned`);
      assert.deepEqual(growth(reloaded, name, T0, T0 + 3 * MIN), expected(name), `${field} reset, ${name}, reloaded`);
    }
  }
});

test('a figure that was unknown for a while counts no growth across it, and keeps the growth around it', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  const work = [800, 1000, null, 400, 900];
  for (const [minute, ms] of work.entries()) {
    await history.record([entry('i:r', 1000 * (minute + 1), { stageWork: ms === null ? null : { ms, since: [] } })], T0 + minute * MIN);
  }
  await history.record([entry('i:r', 70000, { stageWork: { ms: 2000, since: [] } })], T0 + 70 * MIN);
  for (const current of [history, await reload(dir)]) {
    assert.deepEqual(growth(current, 'workMs', T0, T0 + 4 * MIN), { value: 700, gaps: 1 }, 'never 400 - 1000');
    assert.deepEqual(growth(current, 'elapsedMs', T0, T0 + 4 * MIN), { value: 4000, gaps: 0 });
  }
  // A figure known only from some point on starts there, as any first value does; the snapshot without it is a gap.
  await history.record([entry('j:r', 1000, { stageWork: null })], T0);
  await history.record([entry('j:r', 2000, { stageWork: { ms: 100, since: [] } })], T0 + MIN);
  await history.record([entry('j:r', 3000, { stageWork: { ms: 300, since: [] } })], T0 + 2 * MIN);
  const { value, gaps } = history.delta('j:r', 'workMs', T0 - MIN, T0 + 3 * MIN);
  assert.deepEqual({ value, gaps }, { value: 200, gaps: 1 });
  assert.equal(history.delta('j:r', 'workMs', T0, T0 + 0.5 * MIN), null, 'before its first reading it is missing');
});

test('a figure AFM cannot report is a gap while it lasts and after it returns, across thinning and a restart', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  let now = T0;
  const costs = new Costs({ history, now: () => now });
  costs.setWatched(true);
  costs.setVisible(true);
  t.after(() => costs.close());
  const run = { key: 'i:r', runId: 'r', name: 'r', state: 'running' };
  const observe = async (minute, work) => {
    now = T0 + minute * MIN;
    await costs.observe(Costs.inventoryOf([{ run, projectId: 'p' }]), new Map([['i:r', statusAt(minute, work)]]));
  };
  for (const [minute, work] of [[0, 100], [1, 200], [2, null], [3, null], [4, null]]) await observe(minute, work);
  assert.equal(costs.view().runs[0].stageWork, null);
  const inside = current => growth(current, 'workMs', T0 + 2 * MIN, T0 + 3 * MIN);
  for (const current of [history, await reload(dir)]) {
    assert.deepEqual(inside(current), { value: 0, gaps: 1 }, 'a period inside it is not a complete zero');
    assert.deepEqual(growth(current, 'workMs', T0 + 4 * MIN, T0 + 9 * MIN), { value: 0, gaps: 1 }, 'it still goes on');
    assert.deepEqual(growth(current, 'workMs', T0, T0 + 3 * MIN), { value: 100, gaps: 1 }, 'growth before it counts');
    assert.deepEqual(growth(current, 'elapsedMs', T0 + 2 * MIN, T0 + 3 * MIN), { value: 1000, gaps: 0 }, 'other figures are unaffected');
  }
  for (const [minute, work] of [[5, 1000], [6, 1100], [7, 1200], [75, 2000]]) await observe(minute, work);
  assert.equal(history.run('i:r').samples.length, 7, 'readings inside the stretch and after it were thinned');
  for (const current of [history, await reload(dir)]) {
    assert.deepEqual(inside(current), { value: 0, gaps: 1 }, 'after it returned');
    assert.deepEqual(growth(current, 'workMs', T0, T0 + 7 * MIN), { value: 300, gaps: 1 }, 'never 1000 - 200');
    assert.deepEqual(growth(current, 'elapsedMs', T0 + 2 * MIN, T0 + 5 * MIN), { value: 3000, gaps: 0 });
    assert.deepEqual(growth(current, 'tokens', T0 + 2 * MIN, T0 + 5 * MIN), { value: 300, gaps: 0 });
  }
  // Hiding prices still removes all money; the stretch without Stage work keeps nothing about it.
  costs.setHidePrices(true);
  await observe(76, 2000);
  assert.doesNotMatch(await readFile(history.file, 'utf8'), /usd/);
  const reloaded = await reload(dir);
  assert.equal(reloaded.delta('i:r', 'usd', T0 - MIN, T0 + 80 * MIN), null);
  assert.deepEqual(inside(reloaded), { value: 0, gaps: 1 });
});

test('a stretch missed for several reasons at once is one gap', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('i:r', 1000)], T0);
  await history.record([entry('i:r', 2000, { stageWork: null })], T0 + MIN);
  await history.markOutage(['i:r'], T0 + 2 * MIN);
  assert.deepEqual(growth(history, 'workMs', T0 + 2 * MIN, T0 + 3 * MIN), { value: 0, gaps: 1 }, 'missing and unobserved, still going on');
  await history.record([entry('i:r', 5000, { stageWork: null })], T0 + 4 * MIN);
  await history.record([entry('i:r', 6000)], T0 + 5 * MIN);
  assert.deepEqual(growth(history, 'workMs', T0, T0 + 6 * MIN), { value: 0, gaps: 1 });
  assert.deepEqual(growth(history, 'elapsedMs', T0, T0 + 6 * MIN), { value: 2000, gaps: 1 });
});

test('the sample cap keeps every stretch without a figure that the kept readings show, across a restart', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  // Stage work is missing from every other snapshot, so each one is pinned and the cap has to drop the oldest.
  const count = MAX_SAMPLES + 4;
  const at = i => T0 + i * SAMPLE_MS;
  for (let i = 0; i < count; i++) {
    await history.record([entry('i:r', 1000 * (i + 1), { stageWork: i % 2 ? null : { ms: 100 * i, since: [] } })], at(i));
  }
  for (const current of [history, await reload(dir)]) {
    assert.equal(current.run('i:r').samples.length, MAX_SAMPLES);
    assert.deepEqual(growth(current, 'workMs', at(count - 4), at(count - 3)), { value: 0, gaps: 1 }, 'a kept stretch');
    assert.deepEqual(growth(current, 'workMs', at(count - 1), at(count + 9)), { value: 0, gaps: 1 }, 'the one still going on');
    assert.deepEqual(growth(current, 'elapsedMs', at(count - 4), at(count - 1)), { value: 3000, gaps: 0 });
    assert.equal(current.delta('i:r', 'workMs', T0 - MIN, at(count)).partial, true);
  }
});

test('more than two hundred outages keep every gap with the readings around it, across a restart', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  // Every 3 minutes: a reading after an outage, one more reading a minute later, then the next outage.
  for (let i = 0; i <= 201; i++) {
    const tokens = i * 100000;
    await history.record([entry('i:r', 1000 * (2 * i + 1), { tokens })], T0 + 3 * i * MIN);
    await history.record([entry('i:r', 1000 * (2 * i + 2), { tokens: tokens + 600 })], T0 + (3 * i + 1) * MIN);
    await history.markOutage(['i:r'], T0 + (3 * i + 2) * MIN);
  }
  for (const current of [history, await reload(dir)]) {
    assert.deepEqual(growth(current, 'tokens', T0 + MIN, T0 + 3 * MIN), { value: 0, gaps: 1 }, 'the first outage');
    assert.deepEqual(growth(current, 'tokens', T0 + 1.5 * MIN, T0 + 2.5 * MIN), { value: 0, gaps: 1 }, 'a moment inside it');
    assert.deepEqual(growth(current, 'tokens', T0, T0 + 2 * MIN), { value: 600, gaps: 1 }, 'a period ending inside it');
    assert.deepEqual(growth(current, 'tokens', T0 + 2 * MIN, T0 + 4 * MIN), { value: 600, gaps: 1 }, 'a period starting inside it');
    assert.deepEqual(growth(current, 'tokens', T0, T0 + 4 * MIN), { value: 1200, gaps: 1 }, 'growth after reconnecting counts');
  }
});

test('more than two hundred long silences each stay a gap, across a restart', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  const step = GAP_MS + MIN;
  for (let i = 0; i < 202; i++) await history.record([entry('i:r', 1000 * (i + 1))], T0 + i * step);
  const last = T0 + 201 * step;
  await history.record([entry('i:r', 1000 * 202 + 600)], last + MIN);
  for (const current of [history, await reload(dir)]) {
    assert.deepEqual(growth(current, 'tokens', T0, T0 + step), { value: 0, gaps: 1 }, 'the first silence');
    assert.deepEqual(growth(current, 'tokens', T0 + MIN, T0 + 2 * MIN), { value: 0, gaps: 1 }, 'a moment inside it');
    assert.deepEqual(growth(current, 'tokens', T0 + MIN, T0 + 2 * step), { value: 0, gaps: 2 }, 'a period starting inside it');
    assert.deepEqual(growth(current, 'tokens', last, last + MIN), { value: 600, gaps: 0 });
  }
});

test('when the sample cap drops the oldest readings, a period needing them is partial and nothing is invented', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  // Every reading follows a silence, so each one is kept by thinning and the cap has to drop the oldest.
  const step = GAP_MS + 1000;
  const count = MAX_SAMPLES + 3;
  for (let i = 0; i < count; i++) await history.record([entry('i:r', 1000 * (i + 1))], T0 + i * step);
  const end = T0 + count * step;
  for (const current of [history, await reload(dir)]) {
    const samples = current.run('i:r').samples;
    assert.equal(samples.length, MAX_SAMPLES);
    const whole = current.delta('i:r', 'tokens', T0 - MIN, end);
    assert.deepEqual({ value: whole.value, partial: whole.partial }, { value: 0, partial: true }, 'the run began inside, but its start is gone');
    const kept = current.delta('i:r', 'tokens', samples[0].t, end);
    assert.equal(kept.partial, false, 'what is still kept stays complete');
  }
});

test('a version-1 history that already lost gaps does not turn their growth into observed growth', async t => {
  // The earlier format kept the latest 200 gaps apart from its readings; the oldest gap was already gone.
  const samples = [{ t: T0, elapsedMs: 1000, idleMs: 0, workMs: 500, tokens: 1000, usd: 1, pin: true }];
  const gaps = [];
  for (let i = 1; i <= 201; i++) {
    samples.push({ t: T0 + 2 * i * MIN, elapsedMs: 1000 + i, idleMs: 0, workMs: 500, tokens: 1000 + i * 100000, usd: 1, pin: true });
    if (i > 1) gaps.push({ from: T0 + 2 * (i - 1) * MIN, to: T0 + 2 * i * MIN });
  }
  const last = samples.at(-1);
  samples.push({ t: last.t + MIN, elapsedMs: last.elapsedMs + 1, idleMs: 0, workMs: 500, tokens: last.tokens + 600, usd: 1 });
  const history = await legacy(t, { name: 'r', projectId: 'p', startedAt: T0, samples, gaps, outage: null });
  assert.deepEqual(growth(history, 'tokens', T0, T0 + 2 * MIN), { value: 0, gaps: 1 }, 'the lost gap');
  assert.deepEqual(growth(history, 'tokens', T0 + 4 * MIN, T0 + 6 * MIN), { value: 0, gaps: 1 }, 'a kept gap');
  assert.deepEqual(growth(history, 'tokens', last.t, last.t + MIN), { value: 600, gaps: 0 }, 'growth outside gaps still counts');
  assert.equal(history.delta('i:r', 'tokens', T0 - MIN, T0 + MIN).partial, true, 'what came before the kept readings is unknown');
});

test('a version-1 history that lost the reading before a reset marks that stretch unknown', async t => {
  const reading = (minute, tokens, extra = {}) => ({ t: T0 + minute * MIN, elapsedMs: 1000 * (minute + 1), idleMs: 0, workMs: 0, tokens, usd: 0, ...extra });
  // 9000 tokens at minute 1 were thinned away; only the reading after the reset was kept.
  const history = await legacy(t, { name: 'r', projectId: 'p', startedAt: T0, outage: null, gaps: [],
    samples: [reading(0, 1000), reading(2, 100, { breaks: ['tokens'], pin: true }), reading(3, 600)] });
  assert.deepEqual(growth(history, 'tokens', T0, T0 + 3 * MIN), { value: 500, gaps: 1 });
  assert.deepEqual(growth(history, 'tokens', T0 + 0.5 * MIN, T0 + MIN), { value: 0, gaps: 1 }, 'a moment in the lost stretch');
  assert.deepEqual(growth(history, 'elapsedMs', T0 + 2 * MIN, T0 + 3 * MIN), { value: 1000, gaps: 0 });
});

test('a version-1 history without per-figure resets never counts a drop as negative growth', async t => {
  // The first version marked only Elapsed resets (`reset`) and silences (`gap`), on the reading after them.
  const reading = (minute, tokens, extra = {}) => ({ t: T0 + minute * MIN, elapsedMs: 1000 * (minute + 1), idleMs: 0, workMs: 0, tokens, usd: 0, ...extra });
  const history = await legacy(t, { name: 'r', projectId: 'p', startedAt: T0,
    samples: [reading(0, 9000), reading(1, 100), reading(2, 600), reading(9, 5000, { gap: true }), reading(10, 5400)] });
  assert.deepEqual(growth(history, 'tokens', T0, T0 + 2 * MIN), { value: 500, gaps: 1 });
  assert.deepEqual(growth(history, 'tokens', T0 + 2 * MIN, T0 + 10 * MIN), { value: 400, gaps: 1 });
});

test('a version-2 history that marked a returning figure as a reset counts the stretch without it once', async t => {
  const reading = (minute, workMs, extra = {}) => ({ t: T0 + minute * MIN, elapsedMs: 1000 * (minute + 1), idleMs: 0, workMs, tokens: 100 * minute, ...extra });
  const history = await legacy(t, { name: 'r', projectId: 'p', startedAt: T0, outage: null, truncated: false,
    samples: [reading(0, 100), reading(1, 200, { pin: true }), reading(2, null, { pin: true }), reading(3, null, { pin: true }),
      reading(4, 1000, { breaks: ['workMs'], pin: true }), reading(5, 1100)] }, 2);
  assert.deepEqual(growth(history, 'workMs', T0 + 2 * MIN, T0 + 3 * MIN), { value: 0, gaps: 1 });
  assert.deepEqual(growth(history, 'workMs', T0, T0 + 5 * MIN), { value: 200, gaps: 1 });
  assert.deepEqual(growth(history, 'elapsedMs', T0, T0 + 5 * MIN), { value: 5000, gaps: 0 });
});

test('the same port or a new process alone does not join two histories', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('old:r', 5000)], T0);
  await history.record([entry('new:r', 100)], T0 + MIN);
  assert.deepEqual(history.keys().sort(), ['new:r', 'old:r']);
  assert.equal(history.run('old:r').samples.length, 1);
});

test('a period that starts before the history, with a run that began earlier, is partial', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('i:r', 1000, { startedAt: T0 - 3600000 })], T0);
  await history.record([entry('i:r', 2000, { startedAt: T0 - 3600000 })], T0 + MIN);
  assert.equal(history.delta('i:r', 'tokens', T0 - 600000, T0 + 2 * MIN).partial, true);
  assert.equal(history.delta('i:r', 'tokens', T0, T0 + 2 * MIN).partial, false, 'a snapshot exists at the period start');
  assert.equal(history.delta('i:r', 'tokens', T0 - 7200000, T0 + 2 * MIN).partial, false, 'the run began inside the period');
});

test('stale runs are not sampled and a run missing a figure keeps it null', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('i:r', 1000, { stale: true }), entry('j:r', 1000, { stageWork: null })], T0);
  assert.equal(history.run('i:r'), null);
  assert.equal(history.run('j:r').samples[0].workMs, null);
  assert.equal(history.delta('j:r', 'workMs', T0 - MIN, T0 + MIN), null);
});

test('old snapshots and surplus runs are dropped', async t => {
  const history = new CostsHistory(await home(t));
  await history.record([entry('old:r', 1000)], T0);
  await history.record([entry('new:r', 1000)], T0 + RETENTION_MS + MIN);
  assert.deepEqual(history.keys(), ['new:r']);
  const many = Array.from({ length: 80 }, (_, i) => entry(`k${i}:r`, 1000 + i));
  await history.record(many, T0 + RETENTION_MS + 2 * MIN);
  assert.ok(history.keys().length <= 50);
});

test('continuous polling for seven days stays bounded and keeps coverage and baselines', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  const polls = 7 * 24 * 3600 / 3;
  for (let i = 0; i <= polls; i += 10) await history.record([entry('i:r', 1000 + i * 3000)], T0 + i * 3000);
  const end = T0 + polls * 3000;
  const samples = history.run('i:r').samples;
  assert.ok(samples.length <= 2000, 'samples ' + samples.length);
  assert.ok(end - samples[0].t > 6.9 * 24 * 3600 * 1000, 'the oldest snapshot is still about seven days old');
  const day = history.delta('i:r', 'elapsedMs', end - 24 * 3600 * 1000, end);
  assert.equal(day.partial, false);
  assert.ok(Math.abs(day.value - 24 * 3600 * 1000) < 11 * 60 * 1000, 'the last 24 h cover a day of growth within bucket resolution');
  assert.equal(history.delta('i:r', 'elapsedMs', T0 - MIN, end).partial, false, 'the whole run is still covered');
  await history.record([entry('i:r', 1000 + (polls + 200) * 3000)], end + 10 * MIN);
  const reloaded = await reload(dir);
  assert.ok(reloaded.run('i:r').samples[0].t > T0, 'the oldest snapshot left the retention window');
  assert.equal(reloaded.delta('i:r', 'elapsedMs', T0 - MIN, end).partial, true, 'its start stays missing even though the run began inside');
  assert.equal(reloaded.delta('i:r', 'elapsedMs', end - 24 * 3600 * 1000, end).partial, false);
});

test('what AFM stops showing is removed from that run only', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  await history.record([entry('a:r', 1000), entry('b:r', 1000)], T0);
  // The price of a:r goes back once, so its readings carry a reset mark too.
  await history.record([entry('a:r', 1500, { money: { usd: 0.1 } }), entry('b:r', 1500)], T0 + MIN);
  await history.record([entry('a:r', 2000, { money: undefined }), entry('b:r', 2000)], T0 + 2 * MIN);
  assert.equal(history.delta('a:r', 'usd', T0 - MIN, T0 + 3 * MIN), null);
  assert.ok(history.delta('b:r', 'usd', T0 - MIN, T0 + 3 * MIN).value > 0);
  const reloaded = new CostsHistory(dir);
  await reloaded.load();
  assert.doesNotMatch(JSON.stringify(reloaded.run('a:r')), /usd/);
  assert.equal(reloaded.run('b:r').samples.some(sample => 'usd' in sample), true);
});

test('no money reaches disk while prices are hidden, and hiding removes what was stored', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  await history.record([entry('i:r', 1000)], T0);
  // A price reset and a silence leave marks next to the readings; none of them may keep anything about money.
  await history.record([entry('i:r', 2000, { money: { usd: 0.1 } })], T0 + MIN);
  await history.record([entry('i:r', 3000)], T0 + 10 * MIN);
  assert.match(await readFile(history.file, 'utf8'), /"usd"/);
  await history.dropMoney();
  assert.doesNotMatch(await readFile(history.file, 'utf8'), /usd/);
  await history.record([entry('i:r', 4000)], T0 + 11 * MIN, { hidePrices: true });
  await history.record([entry('k:r', 2000)], T0 + 11 * MIN, { hidePrices: true });
  assert.doesNotMatch(await readFile(history.file, 'utf8'), /usd/);
  assert.equal(history.delta('i:r', 'usd', T0 - MIN, T0 + 12 * MIN), null);
});

test('the value at a moment is that of the last snapshot at or before it, null where that snapshot lacks it', async t => {
  const dir = await home(t);
  const first = new CostsHistory(dir);
  await first.record([figures('i:r', { elapsedMs: 1000, idleMs: 0, workMs: 0, tokens: 0, usd: 0 })], T0);
  await first.record([figures('i:r', { elapsedMs: 2000, idleMs: 0, workMs: 500, tokens: 300, usd: 0.5 })], T0 + MIN);
  // Stage work becomes unknown in the last snapshot.
  await first.record([entry('i:r', 3000, { stageWork: null, tokens: 400, money: { usd: 0.7 } })], T0 + 2 * MIN);
  for (const history of [first, await reload(dir)]) {
    assert.equal(history.valueAt('i:r', 'tokens', T0 - 1), null, 'before the first snapshot');
    assert.equal(history.valueAt('other', 'tokens', T0 + MIN), null, 'a run never observed');
    assert.equal(history.valueAt('i:r', 'usd', T0), 0, 'a recorded zero is zero');
    assert.equal(history.valueAt('i:r', 'tokens', T0 + MIN + 1), 300, 'later snapshots do not count');
    assert.equal(history.valueAt('i:r', 'workMs', T0 + MIN), 500);
    assert.equal(history.valueAt('i:r', 'workMs', T0 + 3 * MIN), null, 'unknown at the last snapshot, not its earlier value');
    assert.equal(history.valueAt('i:r', 'usd', T0 + 3 * MIN), 0.7);
  }
});

test('the value at a moment follows a reset and a gap and stays at the last reading while a run is unobserved', async t => {
  const dir = await home(t);
  const history = new CostsHistory(dir);
  await history.record([entry('i:r', 5000)], T0);
  await history.record([entry('i:r', 1000)], T0 + MIN);
  assert.equal(history.valueAt('i:r', 'tokens', T0 + MIN), 1000, 'after a reset: the reading after it');
  await history.markOutage(['i:r'], T0 + 2 * MIN);
  assert.equal(history.valueAt('i:r', 'tokens', T0 + 10 * MIN), 1000, 'during an outage: the last reading');
  await history.record([entry('i:r', 4000)], T0 + 20 * MIN);
  const reloaded = await reload(dir);
  assert.equal(reloaded.valueAt('i:r', 'tokens', T0 + 20 * MIN), 4000, 'after the gap: the reading that closed it');
  assert.equal(reloaded.valueAt('i:r', 'tokens', T0 + 20 * MIN - 1), 1000);
  assert.equal(reloaded.valueAt('i:r', 'usd', T0 + MIN - 1), 5, 'before the reset');
  await reloaded.dropMoney();
  assert.equal(reloaded.valueAt('i:r', 'usd', T0 + 20 * MIN), null, 'no money once prices are hidden');
});

test('followed runs keep and grow their history under churn; a place frees only when a run is no longer followed', async t => {
  const history = new CostsHistory(await home(t));
  const runs = Array.from({ length: 80 }, (_, i) => `k${i}:r`);
  for (let tick = 1; tick <= 5; tick++) await history.record(runs.map(key => entry(key, tick * 1000)), T0 + tick * MIN);
  assert.deepEqual(history.keys().sort(), runs.slice(0, 50).sort(), 'the first 50 followed runs stay; later ones wait for a place');
  for (const key of history.keys()) assert.equal(history.run(key).samples.length, 5, `${key} accumulated every observation`);
  assert.equal(history.run('k60:r'), null);
  // k0 finishes (leaves the inventory): its place goes to the first run that waits, and the others are untouched.
  const rest = runs.filter(key => key !== 'k0:r');
  await history.record(rest.map(key => entry(key, 6000)), T0 + 6 * MIN);
  assert.equal(history.run('k0:r'), null);
  assert.ok(history.run('k50:r'), 'the waiting run took the free place');
  assert.equal(history.keys().length, 50);
  assert.equal(history.run('k1:r').samples.length, 6);
});

test('a stale or finished run gives up its place before any followed run, the oldest last snapshot first', async t => {
  const history = new CostsHistory(await home(t));
  const runs = Array.from({ length: 50 }, (_, i) => `k${i}:r`);
  await history.record(runs.map(key => entry(key, 1000)), T0);
  await history.record(runs.map(key => entry(key, 2000, key === 'k7:r' ? { stale: true } : {})), T0 + MIN);
  // k9 finishes, k7 is still stale: both are unfollowed, k7 has the older last snapshot.
  await history.record([...runs.filter(key => key !== 'k9:r'), 'new:r'].map(key => entry(key, 3000, key === 'k7:r' ? { stale: true } : {})), T0 + 2 * MIN);
  assert.ok(history.run('new:r'), 'a place was freed');
  assert.equal(history.run('k7:r'), null, 'the stale run with the oldest last snapshot gave up its place');
  assert.equal(history.run('k9:r').samples.length, 2, 'the finished run keeps its history until its place is needed');
  assert.equal(history.run('k1:r').samples.length, 3, 'followed runs are untouched');
  assert.equal(history.keys().length, 50);
});

test('the places survive a reload and prices stay private when runs wait or are admitted', async t => {
  const dir = await home(t);
  const first = new CostsHistory(dir);
  const runs = Array.from({ length: 60 }, (_, i) => `k${i}:r`);
  await first.record(runs.map(key => entry(key, 1000)), T0);
  const second = new CostsHistory(dir);
  await second.load();
  await second.record(runs.map(key => entry(key, 4000)), T0 + MIN, { hidePrices: true });
  assert.deepEqual(second.keys().sort(), runs.slice(0, 50).sort());
  assert.equal(second.run('k0:r').samples.length, 2);
  assert.doesNotMatch(await readFile(second.file, 'utf8'), /usd/);
});

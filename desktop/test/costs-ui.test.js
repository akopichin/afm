import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fakeNative, loadShell, projectRow, runRow, snapshotOf, tick } from './shell-support.js';
import { Costs } from '../src/costs.js';
import { CostsHistory } from '../src/costs-history.js';

const NOW = Date.now();
const entry = (id, extra = {}) => ({ key: `i-${id}:${id}`, name: 'flow-' + id, projectId: 'a', stale: false, staleSince: null, runStatus: 'running', startedAt: NOW - 3600000,
  elapsed: { ms: 3600000, since: null }, idle: { ms: 300000, since: null }, stageWork: { ms: 1800000, since: [] }, tokens: 1500000, price: 'visible', coverageIssues: 0,
  money: { usd: 12.5, coverage: 'full', unpriced: 0, unmetered: 0 }, stages: [{ id: 's1', name: 'Plan', status: 'done', workMs: 600000, tokens: 400000, usd: 3 }], overhead: { tokens: 50000, usd: 0.5 },
  analytics: null, models: [{ key: 'm', label: 'Sonnet 5.5', timeMs: 1000, tokens: 1200000, usd: 10 }], ...extra });
const view = (runs, hidePrices = false) => ({ hidePrices, runs });
const home = [projectRow('a', [runRow('ra'), runRow('rb')]), projectRow('b', [runRow('rc')])];

async function openCosts(t, runs = [entry('ra'), entry('rb')], options = {}) {
  const native = fakeNative(snapshotOf(home));
  native.costsView = view(runs, options.hidePrices);
  const shell = await loadShell(t, { demo: false, native });
  shell.get('finish-setup')?.click();
  await tick();
  shell.get('costs-nav').click();
  await tick();
  return { ...shell, native };
}
const text = shell => shell.get('screen').textContent;

test('the Costs button opens the screen, starts the watch and leaving stops it', async t => {
  const shell = await openCosts(t);
  assert.equal(shell.w.location.hash, '#costs');
  assert.deepEqual(shell.native.costsWatches, [true], 'Home already watches; Costs keeps the same watch');
  assert.match(text(shell), /Costs/);
  assert.equal(shell.d.querySelectorAll('[data-cost-row]').length, 2);
  assert.equal(shell.get('costs-nav').getAttribute('aria-current'), 'page');
  shell.get('home').click();
  await tick();
  assert.deepEqual(shell.native.costsWatches, [true], 'Home keeps the watch');
  assert.equal(shell.get('costs-nav').getAttribute('aria-current'), 'false');
  shell.w.shell.openProject(shell.w.shell.projects[0], shell.w.shell.projects[0].runs[0]);
  await tick();
  assert.deepEqual(shell.native.costsWatches, [true, false], 'a project page stops it');
});

test('totals add the runs and say what is missing instead of inventing numbers', async t => {
  const none = entry('rb', { price: 'unavailable', money: undefined, tokens: null });
  const shell = await openCosts(t, [entry('ra'), none]);
  const kpis = shell.d.querySelector('.cost-kpis').textContent;
  assert.match(kpis, /\$12\.50/);
  assert.match(kpis, /1\.50M/);
  assert.match(kpis, /1 runs without accounting/);
  const rows = [...shell.d.querySelectorAll('[data-cost-row]')];
  const missing = rows.find(row => row.textContent.includes('flow-rb'));
  assert.match(missing.textContent, /—/);
  assert.match(missing.textContent, /No accounting for this run/);
});

test('a real zero, hidden prices and a partial estimate read differently', async t => {
  const zero = entry('ra', { tokens: 0, money: { usd: 0, coverage: 'full', unpriced: 0, unmetered: 0 } });
  const afm = entry('rb', { price: 'afm-hidden', money: undefined });
  const partial = entry('rc', { projectId: 'b', money: { usd: 4, coverage: 'partial', unpriced: 2, unmetered: 0 } });
  const shell = await openCosts(t, [zero, afm, partial]);
  const row = name => [...shell.d.querySelectorAll('[data-cost-row]')].find(item => item.textContent.includes(name)).textContent;
  assert.match(row('flow-ra'), /\$0\.00/);
  assert.match(row('flow-ra'), /\b0\b/);
  assert.match(row('flow-rb'), /Hidden/);
  assert.match(row('flow-rb'), /hidden by AFM/);
  assert.match(row('flow-rc'), /\$4\.00/);
  assert.match(row('flow-rc'), /Partial estimate/);
});

test('Hide prices removes every price from the page and asks the main process', async t => {
  const shell = await openCosts(t);
  shell.get('cost-price-toggle').click();
  assert.deepEqual(JSON.parse(JSON.stringify(shell.native.prefChanges.at(-1))), { hidePrices: true });
  shell.native.publishCosts(view([entry('ra', { price: 'desktop-hidden', money: undefined })], true));
  const page = shell.get('screen').innerHTML;
  assert.doesNotMatch(page, /\$\d/);
  assert.doesNotMatch(page, /Est\. cost/);
  assert.equal(shell.d.querySelectorAll('thead th').length, 5);
  assert.equal(shell.get('cost-price-toggle').textContent, 'Show prices');
  // The dialog shows no price either.
  shell.d.querySelector('[data-cost-run]').click();
  assert.doesNotMatch(shell.get('run-cost-body').innerHTML, /\$\d/);
});

test('a stale run says so and its clocks stay where they stopped', async t => {
  const seen = NOW - 600000;
  const stale = entry('ra', { stale: true, staleSince: seen, elapsed: { ms: 0, since: seen - 3600000 } });
  const shell = await openCosts(t, [stale]);
  assert.match(text(shell), /Offline · figures as of/);
  const clock = shell.d.querySelector('[data-clock="elapsed"]');
  const before = clock.textContent;
  assert.equal(before, '1h 0m');
  await tick(1100);
  assert.equal(clock.textContent, before, 'a frozen clock does not move');
});

test('a running clock advances in place without rebuilding the table', async t => {
  const running = entry('ra', { elapsed: { ms: 0, since: Date.now() - 59500 } });
  const shell = await openCosts(t, [running]);
  const row = shell.d.querySelector('[data-cost-row]');
  const clock = shell.d.querySelector('[data-clock="elapsed"]');
  assert.equal(clock.textContent, '59s');
  await tick(1200);
  assert.equal(clock.textContent, '1m');
  assert.equal(shell.d.querySelector('[data-cost-row]'), row, 'same element');
});

test('selection, focus and the open details survive a stats update', async t => {
  const shell = await openCosts(t);
  shell.get('cost-project').value = 'b';
  shell.get('cost-project').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  assert.equal(shell.get('cost-project').value, 'b');
  shell.get('cost-project').value = 'all';
  shell.get('cost-project').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  const sort = shell.d.querySelector('[data-cost-sort="tokens"]');
  sort.focus();
  sort.click();
  assert.equal(shell.d.activeElement.dataset.costSort, 'tokens');
  shell.d.querySelector('[data-cost-run]').click();
  assert.equal(shell.get('run-cost').open, true);
  shell.native.publishCosts(view([entry('ra', { tokens: 9000000 }), entry('rb')]));
  assert.equal(shell.get('run-cost').open, true);
  assert.match(shell.get('run-cost-body').textContent, /9\.00M/);
  assert.equal(shell.d.activeElement.dataset.costSort, 'tokens', 'focus stayed on the sort button');
  assert.equal(shell.get('cost-project').value, 'all');
  shell.native.publishCosts(view([entry('rb')]));
  assert.equal(shell.get('run-cost').open, false, 'the dialog closes when its run is gone');
});

test('sorting puts runs without the figure last in both directions', async t => {
  const shell = await openCosts(t, [entry('ra', { tokens: null }), entry('rb', { tokens: 10 }), entry('rc', { tokens: 99, projectId: 'b' })]);
  const order = () => [...shell.d.querySelectorAll('[data-cost-row]')].map(row => row.dataset.costRow.split(':')[1]);
  shell.d.querySelector('[data-cost-sort="tokens"]').click();
  assert.deepEqual(order(), ['rc', 'rb', 'ra']);
  shell.d.querySelector('[data-cost-sort="tokens"]').click();
  assert.deepEqual(order(), ['rb', 'rc', 'ra']);
});

test('the project filter and the breakdown follow the collected data', async t => {
  const shell = await openCosts(t, [entry('ra'), entry('rc', { projectId: 'b', models: null })]);
  assert.match(shell.get('cost-breakdown').textContent, /Sonnet 5\.5/);
  assert.match(text(shell), /Models appear once a run reports its usage/);
  shell.get('cost-project').value = 'b';
  shell.get('cost-project').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  assert.equal(shell.d.querySelectorAll('[data-cost-row]').length, 1);
  shell.d.querySelector('[data-cost-group="project"]').click();
  assert.match(shell.get('cost-breakdown').textContent, /b/);
});

test('names are text, never markup, and a run opens in its project', async t => {
  const shell = await openCosts(t, [entry('ra', { name: '<img src=x onerror=alert(1)>' })]);
  assert.equal(shell.d.querySelector('.cost-home img'), null);
  assert.match(text(shell), /<img src=x onerror=alert\(1\)>/);
  shell.d.querySelector('[data-cost-run]').click();
  shell.d.querySelector('[data-cost-open]').click();
  assert.match(shell.w.location.hash, /^#a\//);
  assert.equal(shell.get('run-cost').open, false);
});

test('the demo shows deterministic sample data and can hide its prices', async t => {
  const { w, d, get } = await loadShell(t, { demo: true });
  w.location.hash = 'costs';
  await tick();
  assert.equal(d.querySelectorAll('[data-cost-row]').length, 2);
});

test('the demo history ends at the demo totals and starts with each run', async t => {
  const { w } = await loadShell(t, { demo: true });
  const to = Date.now(), data = w.eval(`costsDemoPeriod(${to - 24 * 3600000}, ${to}, 24)`);
  for (const entry of w.eval(`costsDemoView(${to})`).runs) {
    const run = data.runs[entry.key];
    assert.deepEqual({ ...run.anchors }, { cost: entry.price === 'visible' ? entry.money.usd : null, work: entry.stageWork.ms });
    for (const metric of ['cost', 'work']) {
      if (run.anchors[metric] === null) continue;
      const points = w.costsCumulative(run.anchors[metric], run.buckets[metric]);
      assert.ok(Math.abs(points.find(point => point !== null)) < 1e-6, `${entry.name} ${metric} starts at zero where the run started`);
      assert.equal(points.at(-1), run.anchors[metric]);
    }
  }
});

test('every live time figure advances on the clock, and a stale run stays frozen', async t => {
  const base = Date.now();
  const running = entry('ra', { elapsed: { ms: 0, since: base - 60000 }, idle: { ms: 0, since: base - 30000 }, stageWork: { ms: 0, since: [base - 60000] } });
  const stale = entry('rb', { stale: true, staleSince: base, elapsed: { ms: 0, since: base - 120000 }, idle: { ms: 0, since: null }, stageWork: { ms: 0, since: [] } });
  const shell = await openCosts(t, [running, stale]);
  const figure = selector => shell.d.querySelector(selector).textContent;
  const kpi = () => shell.d.querySelector('.cost-kpis').textContent;
  assert.match(kpi(), /3m/, 'elapsed total at the start: 1 + 2 minutes');
  shell.d.querySelector('[data-cost-row="i-ra:ra"] [data-cost-run]').click();
  const dialog = shell.get('run-cost-body');
  assert.match(dialog.textContent, /Elapsed1m/);
  shell.w.Date.now = () => base + 3600000;
  shell.w.tickCosts();
  assert.equal(figure('[data-cost-row="i-ra:ra"] [data-clock="elapsed"]'), '1h 1m');
  assert.equal(figure('[data-cost-row="i-ra:ra"] [data-clock="work"]'), '1h 1m');
  assert.equal(figure('[data-cost-row="i-ra:ra"] [data-clock="idle"]'), '1h 0m');
  assert.equal(figure('[data-clock="total-elapsed"]'), '1h 3m', 'running 1h 1m plus stale 2m');
  assert.equal(figure('[data-clock="total-idle"]'), '1h 0m');
  assert.match(dialog.textContent, /Elapsed1h 1m/);
  assert.match(dialog.textContent, /Stage work1h 1m/);
  assert.match(dialog.textContent, /Idle1h 0m/);
  assert.equal(figure('[data-cost-row="i-rb:rb"] [data-clock="elapsed"]'), '2m', 'the stale run did not move');
});

test('a run beyond the project tab limit opens from Costs', async t => {
  const runs = Array.from({ length: 21 }, (_, i) => runRow('r' + String(i).padStart(2, '0')));
  const native = fakeNative(snapshotOf([projectRow('a', runs)]));
  native.costsView = view(runs.map(run => entry(run.runId)));
  const shell = await loadShell(t, { demo: false, native });
  await tick();
  shell.get('costs-nav').click();
  await tick();
  assert.equal(shell.d.querySelectorAll('[data-cost-row]').length, 21);
  assert.equal(shell.w.shell.projects[0].runs.length, 20, 'the tab list keeps its limit');
  shell.d.querySelector('[data-cost-row="i-r00:r00"] [data-cost-run]').click();
  shell.d.querySelector('[data-cost-open]').click();
  assert.match(shell.w.location.hash, /^#a\/i-r00%3Ar00/);
  assert.equal(shell.get('run-cost').open, false);
  assert.equal(shell.w.shell.projects[0].runs.length, 21, 'only the opened run was added');
});

const slice = (value, extra = {}) => ({ value, partial: false, gaps: 0, ...extra });
const figures = (tokens, usd = 3) => ({ elapsed: slice(7200000), idle: slice(600000), work: slice(3600000), tokens: slice(tokens), usd: usd === null ? null : slice(usd) });
const buckets = (extra = {}) => ({ cost: Array.from({ length: 8 }, (_, i) => slice(i + 1)), tokens: Array.from({ length: 8 }, (_, i) => slice(1000 * (i + 1))),
  time: Array.from({ length: 8 }, (_, i) => slice(60000 * (i + 1))), ...extra });
const periodOf = (runs, perRun = {}) => ({ available: true, from: 0, to: 1, series: { from: 0, to: 1, buckets: 8 },
  runs: Object.fromEntries(Object.entries(runs).map(([key, value]) => [key, { ...value, buckets: buckets(perRun[key] ?? perRun.all ?? {}) }])) });

test('a chosen period shows the growth inside it, asks the history once per change and stops ticking', async t => {
  const shell = await openCosts(t, [entry('ra', { elapsed: { ms: 0, since: Date.now() - 1000 } })]);
  shell.native.periodResult = (from, to) => periodOf({ 'i-ra:ra': figures(250000) }, { all: { tokens: Array.from({ length: 8 }, (_, i) => (i === 3 ? null : slice(1000))) } });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  const [from, to, buckets] = shell.native.periodCalls.at(-1);
  assert.ok(Math.abs(to - from - 24 * 3600000) < 1000);
  assert.equal(buckets, 8);
  const row = shell.d.querySelector('[data-cost-row]').textContent;
  assert.match(row, /250k/, 'tokens are the growth, not the since-start total');
  assert.match(row, /2h 0m/);
  assert.equal(shell.d.querySelectorAll('[data-clock]').length, 0, 'period figures never tick');
  assert.equal(shell.d.querySelector('[data-cost-range="24"]').getAttribute('aria-pressed'), 'true');
  assert.equal(shell.d.querySelectorAll('.cost-point').length, 8);
  shell.d.querySelector('[data-cost-metric="tokens"]').click();
  await tick();
  const labels = [...shell.d.querySelectorAll('.cost-point')].map(point => point.getAttribute('aria-label'));
  assert.match(labels[3], /No data/);
  assert.match(labels[0], /1k/);
  shell.d.querySelector('.cost-point').focus();
  assert.match(shell.get('cost-chart-readout').textContent, /→/);
  shell.d.querySelector('[data-cost-range="all"]').click();
  await tick();
  assert.equal(shell.d.querySelectorAll('.cost-point').length, 0);
  assert.equal(shell.d.querySelectorAll('[data-clock]').length > 0, true, 'since start ticks again');
});

test('history that starts late or has gaps is said so, never shown as a complete period', async t => {
  const shell = await openCosts(t, [entry('ra'), entry('rb')]);
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': { ...figures(1000), tokens: slice(1000, { partial: true }) }, 'i-rb:rb': { ...figures(2000), tokens: slice(2000, { gaps: 1 }) } },
    { all: { tokens: Array.from({ length: 8 }, () => slice(10, { partial: true })) } });
  shell.d.querySelector('[data-cost-range="168"]').click();
  await tick();
  const page = text(shell);
  assert.match(page, /History starts after the period start/);
  assert.match(page, /Gap in history/);
  assert.match(shell.d.querySelector('.cost-kpis').textContent, /history starts after the period start/);
});

test('an older history answer never replaces a newer one', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  const waiting = [];
  shell.native.periodResult = (from, to) => new Promise(resolve => waiting.push(() => resolve(periodOf({ 'i-ra:ra': figures(to - from < 30 * 3600000 ? 111000 : 777000) }))));
  shell.d.querySelector('[data-cost-range="24"]').click();
  shell.d.querySelector('[data-cost-range="168"]').click();
  waiting[1]();
  await tick();
  waiting[0]();
  await tick();
  assert.match(shell.d.querySelector('[data-cost-row]').textContent, /777k/);
  assert.doesNotMatch(shell.d.querySelector('[data-cost-row]').textContent, /111k/);
});

test('with prices hidden a period shows no money and the chart falls back to time', async t => {
  const shell = await openCosts(t, [entry('ra', { price: 'desktop-hidden', money: undefined })], { hidePrices: true });
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': figures(5000, null) }, { all: { cost: null } });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  assert.doesNotMatch(shell.get('screen').innerHTML, /\$\d/);
  assert.equal(shell.d.querySelectorAll('.cost-point').length, 8);
  assert.match(shell.get('cost-chart-readout').closest('section').textContent, /Elapsed time per interval/);
});

test('models are not claimed for a period; the breakdown says so and groups by project', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': figures(5000) });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  assert.match(text(shell), /Models are reported for the whole run/);
  assert.doesNotMatch(shell.get('cost-breakdown').textContent, /Sonnet/);
});

const flat = value => Array.from({ length: 8 }, () => slice(value));
const labelsOf = shell => [...shell.d.querySelectorAll('.cost-point')].map(point => point.getAttribute('aria-label'));

test('the period chart sums only the runs the project filter keeps', async t => {
  const shell = await openCosts(t, [entry('ra'), entry('rc', { projectId: 'b' })]);
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': figures(1000, 1), 'i-rc:rc': figures(9000, 9) }, { 'i-ra:ra': { cost: flat(1) }, 'i-rc:rc': { cost: flat(9) } });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  assert.match(labelsOf(shell)[0], /\$10\.00/, 'all projects');
  shell.get('cost-project').value = 'a';
  shell.get('cost-project').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  assert.match(labelsOf(shell)[0], /\$1\.00/);
  assert.doesNotMatch(labelsOf(shell).join(' '), /\$10\.00/);
  assert.equal(shell.d.querySelectorAll('[data-cost-row]').length, 1);
});

test('money disappears from the chart as soon as a run stops showing prices, before any new history arrives', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': figures(1000, 1) }, { all: { cost: flat(1.25) } });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  assert.match(labelsOf(shell).join(' '), /\$1\.25/);
  shell.native.periodResult = () => new Promise(() => {});
  shell.native.publishCosts(view([entry('ra', { price: 'afm-hidden', money: undefined })]));
  assert.doesNotMatch(shell.get('screen').innerHTML, /\$\d/);
  assert.doesNotMatch(labelsOf(shell).join(' '), /\$/);
});

test('period details never mix in the since-start clocks', async t => {
  const base = Date.now();
  const shell = await openCosts(t, [entry('ra', { elapsed: { ms: 3600000, since: base - 1000 }, stageWork: { ms: 1800000, since: [base - 1000] } })]);
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': figures(1000, 1) });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  shell.d.querySelector('[data-cost-run]').click();
  assert.equal(shell.get('run-cost-body').querySelectorAll('[data-clock]').length, 0);
  const before = shell.get('run-cost-body').textContent;
  shell.w.Date.now = () => base + 36000000;
  shell.w.tickCosts();
  assert.equal(shell.get('run-cost-body').textContent, before);
  assert.match(shell.get('run-cost-body').textContent, /Elapsed2h 0m/);
});

test('missing period readings are no data, a recorded zero is zero', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': { elapsed: null, idle: null, work: null, tokens: null, usd: null } });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  const kpi = () => shell.d.querySelector('.cost-kpis').textContent;
  assert.match(kpi(), /Elapsed time—/);
  assert.match(kpi(), /Idle —/);
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': { elapsed: slice(0), idle: slice(0), work: slice(0), tokens: slice(0), usd: slice(0) } });
  shell.d.querySelector('[data-cost-range="168"]').click();
  await tick();
  assert.match(kpi(), /Elapsed time0s/);
  assert.match(kpi(), /Idle 0s/);
});

// What the history read reports each run ended with, as Costs.period does: no money for a run whose price is not shown.
const anchorsOf = (run, hidePrices = false) => ({ cost: !hidePrices && run.price === 'visible' ? run.money?.usd ?? null : null, work: run.stageWork?.ms ?? null });

async function openHome(t, runs, snapshot, options = {}) {
  const native = fakeNative(snapshot ?? snapshotOf(home));
  native.costsView = view(runs, options.hidePrices);
  native.periodResult = options.period ?? (() => periodOf(Object.fromEntries(runs.map(run => [run.key, { ...figures(1000, 1), anchors: anchorsOf(run, options.hidePrices) }])),
    { all: { cost: flat(1), work: flat(60000) } }));
  const shell = await loadShell(t, { demo: false, native });
  shell.get('finish-setup')?.click();
  await tick(30);
  return { ...shell, native };
}
const tileOf = (shell, id) => shell.d.querySelector(`[data-cost-project="${id}"]`);

test('Home tiles show one cumulative line per active run, the project total and a link to Costs', async t => {
  const shell = await openHome(t, [entry('ra'), entry('rb', { money: { usd: 7.5, coverage: 'full', unpriced: 0, unmetered: 0 } })]);
  const tile = tileOf(shell, 'a');
  assert.equal(tile.hidden, false);
  assert.equal(tile.previousElementSibling.hidden, true, 'the plain picture steps aside');
  assert.match(tile.textContent, /\$20\.00/, 'project total since start: 12.5 + 7.5');
  assert.match(tile.textContent, /flow-ra\s*\$12\.50/);
  assert.match(tile.textContent, /flow-rb\s*\$7\.50/);
  assert.equal(tile.querySelectorAll('polyline').length, 2);
  assert.equal(tileOf(shell, 'b').hidden, true, 'no active run in b: the plain picture stays');
  assert.equal(tileOf(shell, 'b').previousElementSibling.hidden, false);
  const link = shell.d.querySelector('[data-cost-link="a"]');
  assert.equal(link.hidden, false);
  assert.equal(shell.d.querySelector('[data-cost-link="b"]').hidden, true);
  link.click();
  await tick();
  assert.equal(shell.w.location.hash, '#costs');
  assert.equal(shell.get('cost-project').value, 'a');
  assert.match(shell.d.querySelector('.cost-kpis').textContent, /\$20\.00/, 'Home and Costs agree on the since-start total');
});

test('Home asks the history once for all tiles and never per tile', async t => {
  const runs = [entry('ra'), entry('rb'), entry('rc', { projectId: 'b' })];
  const shell = await openHome(t, runs);
  assert.equal(shell.native.periodCalls.length, 1);
  const [from, to, buckets] = shell.native.periodCalls[0];
  assert.ok(Math.abs(to - from - 24 * 3600000) < 1000);
  assert.equal(buckets, 24);
});

test('Home tiles fall back to Stage work when prices are hidden and show no money', async t => {
  const shell = await openHome(t, [entry('ra', { price: 'desktop-hidden', money: undefined })], undefined, { hidePrices: true });
  const tile = tileOf(shell, 'a');
  assert.match(tile.textContent, /Stage work · since start/);
  assert.match(tile.textContent, /30m/);
  assert.doesNotMatch(shell.get('screen').innerHTML, /\$\d/);
});

test('Home lines stop where the history is unknown and never invent earlier values', async t => {
  const period = () => periodOf({ 'i-ra:ra': { ...figures(1000, 1), anchors: { cost: 12.5, work: 1800000 } } },
    { all: { cost: [slice(1), slice(1), slice(1, { partial: true }), slice(1), slice(1), slice(1), slice(1), slice(1)] } });
  const shell = await openHome(t, [entry('ra')], undefined, { period });
  const points = tileOf(shell, 'a').querySelector('polyline').getAttribute('points').split(' ');
  assert.equal(points.length, 6, 'the partial slice and everything before it are empty: 5 slices after it + Now');
  const none = await openHome(t, [entry('ra')], undefined, { period: () => ({ available: false, runs: {}, series: null }) });
  assert.equal(tileOf(none, 'a').querySelector('polyline'), null, 'no history: only the current value, no line');
  assert.match(tileOf(none, 'a').textContent, /\$12\.50/);
});

test('identical cost data does not touch Home', async t => {
  const shell = await openHome(t, [entry('ra')]);
  let mutations = 0;
  new shell.w.MutationObserver(records => { mutations += records.length; }).observe(shell.d.body, { subtree: true, childList: true, attributes: true, characterData: true });
  for (let i = 0; i < 20; i++) shell.native.publishCosts(JSON.parse(JSON.stringify(view([entry('ra')]))));
  await tick(20);
  assert.equal(mutations, 0);
  assert.equal(shell.native.periodCalls.length, 1, 'the history is not re-read for every update');
});

test('active run rows on Home carry the since-start figure with its label', async t => {
  const shell = await openHome(t, [entry('ra'), entry('rb', { price: 'afm-hidden', money: undefined })]);
  const cell = key => shell.d.querySelector(`[data-cost-run-key="${key}"]`).textContent;
  assert.equal(cell('i-ra:ra'), '$12.50 · since start');
  assert.match(cell('i-rb:rb'), /Stage work 30m · since start/);
});

test('Home counts every run Costs observes, beyond the project tab limit', async t => {
  const rows = Array.from({ length: 21 }, (_, i) => runRow('r' + String(i).padStart(2, '0')));
  const runs = rows.map(row => entry(row.runId, { money: { usd: 1, coverage: 'full', unpriced: 0, unmetered: 0 } }));
  const shell = await openHome(t, runs, snapshotOf([projectRow('a', rows)]));
  assert.equal(shell.w.shell.projects[0].runs.length, 20, 'the tab list keeps its limit');
  const tile = tileOf(shell, 'a');
  assert.match(tile.textContent, /\$21\.00/);
  assert.equal(tile.querySelectorAll('.project-cost-legend span').length, 21);
  shell.d.querySelector('[data-cost-link="a"]').click();
  await tick();
  assert.match(shell.d.querySelector('.cost-kpis').textContent, /\$21\.00/, 'Costs agrees');
});

test('Home Stage work advances on the clock and a stale run stays where it stopped', async t => {
  const base = Date.now();
  const open = entry('ra', { price: 'desktop-desktop-hidden', money: undefined, stageWork: { ms: 0, since: [base - 1000] } });
  const stale = entry('rb', { stale: true, staleSince: base, stageWork: { ms: 0, since: [base - 1000] } });
  const shell = await openHome(t, [{ ...open, price: 'afm-hidden', money: undefined }, { ...stale, price: 'afm-hidden', money: undefined }]);
  const row = key => shell.d.querySelector(`[data-cost-run-key="${key}"]`).textContent;
  assert.match(row('i-ra:ra'), /Stage work 1s/);
  shell.w.Date.now = () => base + 120000;
  shell.w.tickCosts();
  assert.match(row('i-ra:ra'), /Stage work 2m/);
  assert.match(tileOf(shell, 'a').textContent, /2m/);
  assert.match(row('i-rb:rb'), /Stage work 1s/, 'the stale run did not move');
});

test('a new charge never rewrites earlier points of the Home line', async t => {
  const growth = Array.from({ length: 24 }, (_, i) => slice(i === 23 ? 1 : 0));
  const period = () => periodOf({ 'i-ra:ra': { ...figures(1000, 10), anchors: { cost: 10, work: 1800000 } } }, { all: { cost: growth } });
  const shell = await openHome(t, [entry('ra', { money: { usd: 10, coverage: 'full', unpriced: 0, unmetered: 0 } })], undefined, { period });
  const points = () => tileOf(shell, 'a').querySelector('polyline')?.getAttribute('points');
  const before = points();
  assert.ok(before);
  shell.native.publishCosts(view([entry('ra', { money: { usd: 20, coverage: 'full', unpriced: 0, unmetered: 0 } })]));
  await tick();
  assert.equal(points(), before, 'the line is anchored to the values of its own history read');
  assert.match(tileOf(shell, 'a').textContent, /\$20\.00/, 'the legend and total show the live value');
  assert.equal(shell.native.periodCalls.length, 1, 'no extra history read inside the cache window');
});

// Home fed by the real collector and history store, on a clock the test moves. Each minute of the hour before BASE the
// run cost $9, and $10 from a minute before BASE; its open Stage work started two hours before BASE. Reading its
// analytics waits for `gate`.
const BASE = Date.parse('2026-11-20T12:00:00Z');
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
async function observedHome(t, { hidePrices = false, ids = ['ra'] } = {}) {
  let now = BASE;
  const dir = await mkdtemp(path.join(os.tmpdir(), 'costs-home-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const history = new CostsHistory(dir);
  const gate = deferred(), asked = deferred();
  const inventory = ids.map(id => ({ run: runRow(id), projectId: 'a', readable: true }));
  const costs = new Costs({ history, hidePrices, now: () => now, request: async url => {
    if (!url.endsWith('/api/runtime')) return Response.json({ breakdown: { rows: [] } });
    asked.resolve();
    await gate.promise;
    return Response.json({ run_id: inventory[0].run.runId, instance_id: inventory[0].run.instanceId });
  } });
  costs.setVisible(true);
  costs.setWatched(true);
  t.after(() => { gate.resolve(); costs.close(); });
  const status = (usd, { analytics = false, showMoney = true } = {}) => ({ run_status: 'running', started_at: new Date(BASE - 2 * 3600000).toISOString(),
    elapsed_accumulated_ms: now - BASE + 2 * 3600000, elapsed_since: null, idle_accumulated_ms: 0, idle_since: null,
    stages: [{ id: 's', timing: { started: true, complete: true, work_ms: 0, open: { kind: 'work', since: new Date(BASE - 2 * 3600000).toISOString() } } }],
    accounting: { show_money: showMoney }, run_cost: { estimated_cost_usd: usd, total_tokens: 100, coverage: 'full' },
    capabilities: { usage_analytics: analytics }, analytics: { revision: 'r' + now } });
  // The first run costs `usd`; any other run always $5.
  const observe = (usd, options) => costs.observe(inventory, new Map(inventory.map(({ run }, i) => [run.key, i === 0 ? status(usd, options) : status(5)])));
  for (let minute = 61; minute >= 0; minute--) {
    now = BASE - minute * 60000;
    await observe(minute <= 1 ? 10 : 9);
  }
  const native = fakeNative(snapshotOf([projectRow('a', inventory.map(item => item.run))]));
  Object.assign(native, { costsView: costs.view(), costs: async () => costs.view(), periodResult: (...args) => costs.period(...args) });
  const shell = await loadShell(t, { demo: false, native });
  shell.w.Date.now = () => now;
  shell.get('finish-setup')?.click();
  await tick(30);
  shell.get('costs-nav').click();
  await tick();
  t.after(costs.onChange(update => native.publishCosts(update)));
  const goHome = async () => { shell.get('home').click(); await tick(30); };
  const line = (index = 0) => shell.w.eval('costsTileModel(projects[0])').lines[index];
  return { costs, history, key: inventory[0].run.key, gate, asked, native, shell, observe, goHome, line, setNow: value => { now = value; } };
}

test('a Home line opened while analytics are read still starts from the history it was measured with', async t => {
  const home = await observedHome(t);
  home.setNow(BASE + 60000);
  // The history already has $20, the page still has the $10 view: the view is sent once analytics are read.
  const pending = home.observe(20, { analytics: true });
  await home.asked.promise;
  try {
    assert.equal(home.native.costsView.runs[0].money.usd, 10);
    await home.goHome();
    assert.equal(home.line().points[23], 9, 'an hour ago the run had cost $9');
    home.gate.resolve();
    await pending;
    await tick();
    assert.equal(home.line().points[23], 9, 'the arriving view does not move the point');
    assert.equal(home.line().points[24], 20);
    assert.equal(home.line().total, 20, 'the legend is live');
  } finally { home.gate.resolve(); await pending; }
});

test('a Home line ends at the last recorded reading when a newer view is not in the history yet', async t => {
  const home = await observedHome(t);
  home.setNow(BASE + 10000);
  await home.observe(20);
  assert.equal(home.history.run(home.key).samples.at(-1).usd, 10, 'ten seconds after the last snapshot the reading is not stored');
  await home.goHome();
  const line = home.line();
  assert.equal(line.points[23], 9);
  assert.equal(line.points[24], 10);
  assert.equal(line.total, 20, 'the legend and total show the live value');
  assert.match(home.shell.d.querySelector('[data-cost-project="a"]').textContent, /\$20\.00/);
});

test('a running Stage work clock moves the Home total, never the recorded points', async t => {
  const home = await observedHome(t, { hidePrices: true });
  home.setNow(BASE + 10000);
  await home.goHome();
  const line = home.line();
  assert.equal(line.points[23], 3600000, 'an hour of Stage work an hour ago');
  assert.equal(line.points[24], 7200000, 'two hours at the last snapshot');
  assert.equal(line.total, 7210000, 'the total includes the ten seconds since');
});

test('a run that stops showing its price loses its Home money line at once, also in a project with priced runs', async t => {
  const home = await observedHome(t, { ids: ['ra', 'rb'] });
  await home.goHome();
  assert.equal(home.line(0).points[24], 10);
  const shapes = color => home.shell.d.querySelectorAll(`[data-cost-project="a"] polyline[stroke="${color}"], [data-cost-project="a"] circle[fill="${color}"]`).length;
  assert.ok(shapes('#8995ff') > 0);
  const reads = home.native.periodCalls.length;
  await home.observe(10, { showMoney: false });
  await tick();
  const model = home.shell.w.eval('costsTileModel(projects[0])');
  assert.equal(model.money, true, 'the other run still shows money');
  assert.equal(model.lines[0].total, null);
  assert.ok(model.lines[0].points.every(point => point === null), 'no cached price stays in the line');
  assert.equal(shapes('#8995ff'), 0, 'the run has no shape in the chart');
  assert.ok(shapes('#6dcaa5') > 0, 'the priced run keeps its line');
  assert.equal(home.native.periodCalls.length, reads, 'no new history read was needed');
});

test('a reset or a gap stops a Home line at the reading after it', async t => {
  const home = await observedHome(t);
  home.setNow(BASE + 60000);
  await home.observe(2);
  await home.goHome();
  assert.equal(home.line().points[24], 2);
  assert.equal(home.line().points[23], null, 'nothing is subtracted across the reset');
  await home.history.markOutage([home.key], BASE + 60000);
  home.setNow(BASE + 20 * 60000);
  await home.observe(30);
  home.shell.w.refreshCostsHome(true);
  await tick();
  assert.equal(home.line().points[24], 30);
  assert.equal(home.line().points[23], null, 'nothing is subtracted across the gap');
});

const asOfRun = (key, name, extra = {}) => ({ ...figures(1000, 1), meta: { name, projectId: 'a', startedAt: 0, finished: false },
  asOf: { elapsed: 3600000, idle: 60000, work: 1800000, tokens: 5000, usd: 2, observedAt: Date.now() - 90 * 60000, gap: false }, ...extra });
const slide = async (shell, value) => {
  const input = shell.get('cost-asof');
  input.value = String(value);
  input.dispatchEvent(new shell.w.Event('input', { bubbles: true }));
  input.dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  await tick();
};

test('As of: the slider asks the history once per release, shows the zoned moment and the real reading time apart, and Back to now returns', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  shell.native.periodResult = () => ({ available: true, from: 0, to: 1, series: null, runs: { 'i-ra:ra': asOfRun('i-ra:ra', 'flow-ra') } });
  assert.equal(shell.get('cost-asof-now').disabled, true);
  const calls = shell.native.periodCalls.length;
  const input = shell.get('cost-asof');
  for (const value of [150, 120, 100]) { input.value = String(value); input.dispatchEvent(new shell.w.Event('input', { bubbles: true })); }
  assert.equal(shell.native.periodCalls.length, calls, 'dragging asks nothing');
  assert.match(shell.get('cost-asof-label').textContent, /[A-Z]{3,}|GMT|UTC/, 'the zone is shown');
  await slide(shell, 100);
  assert.equal(shell.native.periodCalls.length, calls + 1, 'one request on release');
  const [from, to, buckets] = shell.native.periodCalls.at(-1);
  assert.equal(buckets, 0);
  assert.ok(Math.abs(Date.now() - 68 * 3600000 - to) < 5000);
  assert.match(text(shell), /Read at/);
  assert.match(shell.get('cost-asof-label').textContent, /latest reading used/);
  assert.equal(shell.d.querySelectorAll('[data-clock]').length, 0, 'a past moment never ticks');
  assert.match(shell.d.querySelector('[data-cost-row]').textContent, /5k/, 'since-start figures as of that moment');
  shell.get('cost-asof-now').click();
  await tick();
  assert.equal(shell.get('cost-asof').value, '168');
  assert.equal(shell.get('cost-asof-now').disabled, true);
  assert.equal(shell.d.querySelectorAll('[data-clock]').length > 0, true);
});

test('As of: a finished run appears in the past, a gap is labelled, and a moment before any reading says so', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  shell.native.periodResult = () => ({ available: true, from: 0, to: 1, series: null, runs: {
    'i-ra:ra': asOfRun('i-ra:ra', 'flow-ra', { asOf: { elapsed: 1, idle: 1, work: 1, tokens: 7, usd: 1, observedAt: Date.now() - 6 * 3600000, gap: true } }),
    'i-old:old': asOfRun('i-old:old', 'old-run', { meta: { name: 'old-run', projectId: 'a', startedAt: 0, finished: true } }) } });
  await slide(shell, 100);
  const rows = [...shell.d.querySelectorAll('[data-cost-row]')];
  assert.equal(rows.length, 2);
  const old = rows.find(row => row.textContent.includes('old-run'));
  assert.match(old.textContent, /Finished since/);
  assert.match(rows.find(row => row.textContent.includes('flow-ra')).textContent, /Gap in history/);
  shell.native.periodResult = () => ({ available: true, from: 0, to: 1, series: null, runs: {} });
  await slide(shell, 10);
  assert.equal(shell.d.querySelectorAll('[data-cost-row]').length, 0);
  assert.match(shell.get('cost-asof-label').textContent, /no readings at this moment/);
});

test('As of: hidden prices show no money for past runs either', async t => {
  const shell = await openCosts(t, [entry('ra', { price: 'desktop-hidden', money: undefined })], { hidePrices: true });
  shell.native.periodResult = () => ({ available: true, from: 0, to: 1, series: null, runs: { 'i-ra:ra': asOfRun('i-ra:ra', 'flow-ra', { asOf: { elapsed: 1, idle: 1, work: 1, tokens: 7, usd: null, observedAt: Date.now() - 3600000, gap: false } }) } });
  await slide(shell, 120);
  assert.doesNotMatch(shell.get('screen').innerHTML, /\$\d/);
});

test('Compare runs: two runs with different stages and a partial estimate, explicit missing markers, no misleading percentage', async t => {
  const a = entry('ra', { stages: [{ id: 'plan', name: 'Plan', status: 'done', workMs: 600000, tokens: 400000, usd: 3 }, { id: 'impl', name: 'Implement', status: 'done', workMs: 1200000, tokens: 800000, usd: 6 }], overhead: { tokens: 50000, usd: 0.5 } });
  const b = entry('rb', { tokens: 3000000, money: { usd: 25, coverage: 'partial', unpriced: 2, unmetered: 0 }, stages: [{ id: 'plan', name: 'Plan', status: 'done', workMs: 900000, tokens: 700000, usd: 5 }, { id: 'review', name: 'Review', status: 'done', workMs: 300000, tokens: 100000, usd: 1 }], overhead: null });
  const shell = await openCosts(t, [a, b]);
  assert.equal(shell.get('cost-compare').disabled, false);
  shell.get('cost-compare').click();
  assert.equal(shell.get('run-compare').open, true);
  shell.get('compare-a').value = 'i-ra:ra';
  shell.get('compare-b').value = 'i-rb:rb';
  shell.get('compare-b').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  const body = shell.get('compare-body');
  assert.match(body.textContent, /Not in this run/);
  assert.match(body.textContent, /No overhead recorded/);
  assert.match(body.textContent, /second run minus the first/i);
  assert.match(body.textContent, /not comparable/);
  const tokens = [...body.querySelectorAll('tbody tr')].find(row => row.textContent.startsWith('Tokens'));
  assert.match(tokens.textContent, /\+1\.50M \(\+100%\)/, 'tokens have a positive base');
  const cost = [...body.querySelectorAll('tbody tr')].find(row => row.textContent.startsWith('Est. cost'));
  assert.doesNotMatch(cost.textContent, /%/, 'no percentage across a partial estimate');
  shell.get('compare-a').value = shell.get('compare-b').value;
  shell.get('compare-a').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  assert.match(body.textContent, /Choose two different runs/);
  shell.get('close-compare').click();
  assert.equal(shell.get('run-compare').open, false);
});

test('Compare runs: with prices hidden the dialog has no money and a live update keeps the open selects', async t => {
  const shell = await openCosts(t, [entry('ra', { price: 'desktop-hidden', money: undefined }), entry('rb', { price: 'desktop-hidden', money: undefined })], { hidePrices: true });
  shell.get('cost-compare').click();
  assert.doesNotMatch(shell.get('run-compare').innerHTML, /\$\d|Est\. cost/);
  const select = shell.get('compare-a');
  select.focus();
  shell.native.publishCosts(view([entry('ra', { price: 'desktop-hidden', money: undefined }), entry('rb', { price: 'desktop-hidden', money: undefined })], true));
  assert.equal(shell.get('compare-a'), select, 'the same select element survives an unchanged update');
  shell.get('cost-project').value = 'b';
  shell.get('cost-project').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  assert.match(shell.get('compare-body').textContent, /Fewer than two runs/);
});

test('As of: past stage and overhead rows are unavailable, never today\'s numbers or a false "Not in this run"', async t => {
  const stages = [{ id: 'plan', name: 'Plan', status: 'done', workMs: 600000, tokens: 400000, usd: 8 }];
  const shell = await openCosts(t, [entry('ra', { stages, overhead: { tokens: 50000, usd: 7 } }), entry('rb', { stages: [], overhead: null })]);
  shell.native.periodResult = () => ({ available: true, from: 0, to: 1, series: null, runs: {
    'i-ra:ra': asOfRun('i-ra:ra', 'flow-ra'), 'i-old:old': asOfRun('i-old:old', 'old-run', { meta: { name: 'old-run', projectId: 'a', startedAt: 0, finished: true } }) } });
  await slide(shell, 100);
  shell.get('cost-compare').click();
  const body = shell.get('compare-body').textContent;
  assert.match(body, /not stored for a past moment/);
  assert.doesNotMatch(body, /Not in this run|No overhead recorded|\$8\.00|\$7\.00|Plan/);
  shell.get('close-compare').click();
  shell.d.querySelector('[data-cost-row="i-ra:ra"] [data-cost-run]').click();
  const details = shell.get('run-cost-body').textContent;
  assert.match(details, /not stored for a past moment/);
  assert.doesNotMatch(details, /Plan|\$8\.00|\$7\.00/);
  shell.get('close-cost').click();
  shell.d.querySelector('[data-cost-row="i-old:old"] [data-cost-run]').click();
  assert.equal(shell.get('run-cost').open, true, 'a finished run opens its (figures-only) details');
});

test('Historical estimates are never comparable: no percentage across them, even when the current coverage is full', async t => {
  const shell = await openCosts(t, [entry('ra'), entry('rb')]);
  shell.native.periodResult = () => ({ available: true, from: 0, to: 1, series: null, runs: {
    'i-ra:ra': asOfRun('i-ra:ra', 'flow-ra', { asOf: { elapsed: 1, idle: 1, work: 1, tokens: 5, usd: 2, observedAt: Date.now() - 3600000, gap: false } }),
    'i-rb:rb': asOfRun('i-rb:rb', 'flow-rb', { asOf: { elapsed: 1, idle: 1, work: 1, tokens: 5, usd: 1, observedAt: Date.now() - 3600000, gap: false } }) } });
  await slide(shell, 100);
  assert.match(text(shell), /Coverage not recorded/);
  shell.get('cost-compare').click();
  shell.get('compare-a').value = 'i-ra:ra';
  shell.get('compare-b').value = 'i-rb:rb';
  shell.get('compare-b').dispatchEvent(new shell.w.Event('change', { bubbles: true }));
  const cost = [...shell.get('compare-body').querySelectorAll('tbody tr')].find(row => row.textContent.startsWith('Est. cost'));
  assert.match(cost.textContent, /−\$1\.00/);
  assert.doesNotMatch(cost.textContent, /%/);
  assert.match(shell.get('compare-body').textContent, /not comparable/);
});

test('As of: a live update during a pointer drag leaves the slider alone and the screen catches up on release', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  const slider = shell.get('cost-asof');
  slider.dispatchEvent(new shell.w.Event('pointerdown', { bubbles: true }));
  shell.native.publishCosts(view([entry('ra', { tokens: 9000000 })]));
  assert.equal(shell.get('cost-asof'), slider, 'the dragged element was not recreated');
  shell.d.dispatchEvent(new shell.w.Event('pointerup', { bubbles: true }));
  await tick();
  assert.notEqual(shell.get('cost-asof'), slider, 'after release the screen is repainted');
  assert.match(text(shell), /9\.00M/);
});

test('live periods and Home lines re-read the history on the clock even when no cost update arrives', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  let elapsed = 1000;
  shell.native.periodResult = () => periodOf({ 'i-ra:ra': { ...figures(1000, 1), elapsed: slice(elapsed) } });
  shell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  const calls = shell.native.periodCalls.length;
  assert.match(shell.d.querySelector('[data-cost-row]').textContent, /1s/);
  elapsed = 3600000;
  const base = Date.now();
  shell.w.Date.now = () => base + 20000;
  shell.w.tickCosts();
  await tick();
  assert.equal(shell.native.periodCalls.length, calls + 1);
  assert.match(shell.d.querySelector('[data-cost-row]').textContent, /1h 0m/);
  shell.w.tickCosts();
  await tick();
  assert.equal(shell.native.periodCalls.length, calls + 1, 'throttled to 15 s');
});

test('a hidden window asks the history for nothing, and showing it refreshes at once (Costs and Home)', async t => {
  const hide = (shell, hidden) => {
    Object.defineProperty(shell.d, 'hidden', { configurable: true, get: () => hidden });
    shell.d.dispatchEvent(new shell.w.Event('visibilitychange'));
  };
  const costsShell = await openCosts(t, [entry('ra')]);
  costsShell.native.periodResult = () => periodOf({ 'i-ra:ra': figures(1000, 1) });
  costsShell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  let calls = costsShell.native.periodCalls.length;
  hide(costsShell, true);
  const base = Date.now();
  costsShell.w.Date.now = () => base + 60000;
  costsShell.w.tickCosts();
  await tick();
  assert.equal(costsShell.native.periodCalls.length, calls, 'Costs: no request while hidden');
  hide(costsShell, false);
  await tick();
  assert.equal(costsShell.native.periodCalls.length, calls + 1, 'Costs: refresh on show');

  const homeShell = await openHome(t, [entry('ra')]);
  calls = homeShell.native.periodCalls.length;
  hide(homeShell, true);
  const homeBase = Date.now();
  homeShell.w.Date.now = () => homeBase + 60000;
  homeShell.w.tickCosts();
  await tick();
  assert.equal(homeShell.native.periodCalls.length, calls, 'Home: no request while hidden');
  hide(homeShell, false);
  await tick();
  assert.equal(homeShell.native.periodCalls.length, calls + 1, 'Home: refresh on show');
});

test('a cost update delivered to a hidden window reads no history (Costs and Home), including a price flip; show resumes', async t => {
  const setHidden = (shell, hidden) => {
    Object.defineProperty(shell.d, 'hidden', { configurable: true, get: () => hidden });
    shell.d.dispatchEvent(new shell.w.Event('visibilitychange'));
  };
  const costsShell = await openCosts(t, [entry('ra')]);
  costsShell.native.periodResult = () => periodOf({ 'i-ra:ra': figures(1000, 1) });
  costsShell.d.querySelector('[data-cost-range="24"]').click();
  await tick();
  let calls = costsShell.native.periodCalls.length;
  setHidden(costsShell, true);
  costsShell.native.publishCosts(view([entry('ra', { tokens: 5000 })]));
  costsShell.native.publishCosts(view([entry('ra', { price: 'desktop-hidden', money: undefined })], true));
  await tick();
  assert.equal(costsShell.native.periodCalls.length, calls, 'Costs: update while hidden');
  setHidden(costsShell, false);
  await tick();
  assert.equal(costsShell.native.periodCalls.length, calls + 1);

  const homeShell = await openHome(t, [entry('ra')]);
  calls = homeShell.native.periodCalls.length;
  setHidden(homeShell, true);
  homeShell.native.publishCosts(view([entry('ra', { price: 'desktop-hidden', money: undefined })], true));
  await tick();
  assert.equal(homeShell.native.periodCalls.length, calls, 'Home: price flip while hidden');
  setHidden(homeShell, false);
  await tick();
  assert.equal(homeShell.native.periodCalls.length, calls + 1);
});

test('a fixed As of moment is not re-read on show, and one that never loaded is', async t => {
  const shell = await openCosts(t, [entry('ra')]);
  shell.native.periodResult = () => ({ available: true, from: 0, to: 1, series: null, runs: { 'i-ra:ra': asOfRun('i-ra:ra', 'flow-ra') } });
  await slide(shell, 100);
  const calls = shell.native.periodCalls.length;
  Object.defineProperty(shell.d, 'hidden', { configurable: true, get: () => false });
  shell.d.dispatchEvent(new shell.w.Event('visibilitychange'));
  await tick();
  assert.equal(shell.native.periodCalls.length, calls);
});

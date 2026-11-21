'use strict';
// The Costs screen: current cost, usage and time of the runs the main process observes. The main process owns
// the figures and the price rules; this page only formats them and keeps selection and focus steady.
const costsUi = Object.assign({ project: 'all', metric: 'cost', group: 'model', sort: 'cost', dir: -1, range: 'all', status: 'active' }, state.costs);
if (!['24', '168', 'all'].includes(costsUi.range)) costsUi.range = 'all';
if (costsUi.status !== 'finished') costsUi.status = 'active';
state.costs = costsUi;
const COSTS_METRICS = ['cost', 'tokens', 'time'];
let costsView = { hidePrices: false, runs: [] };
let costsWatching = false;
let costsDetail = null;
// Growth inside the chosen period comes from the history the main process keeps; it is requested per range and never ticks.
const COSTS_BUCKETS = 8;
const COSTS_RANGES = [['24', '24h'], ['168', '7d'], ['all', 'Since start']];
let costsPeriod = null;
let costsPeriodRequest = 0;
let costsPeriodAt = 0;
// As of: how many hours back the screen looks (0 = now) and the fixed instant that is; the history is local, so moving it asks once on release.
const COSTS_REWIND_HOURS = 168;
let costsRewind = 0;
let costsRewindAt = null;
let costsCompare = null;
let costsCompareControls = '';
let costsCompareBody = '';
const costsGrowthMode = () => costsUi.range !== 'all';
const costsPeriodMode = () => costsGrowthMode() || costsRewind > 0;
const costsFinished = () => costsUi.status === 'finished';
const costsPeriodData = () => (costsPeriod?.range === costsUi.range && costsPeriod.rewind === costsRewindAt && costsPeriod.status === costsUi.status ? costsPeriod.data : null);
const COSTS_NO_STAGES = 'Stage and overhead rows are not stored for a past moment: Back to now shows them.';
const costsZoned = ms => new Date(ms).toLocaleString('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' });

// Elapsed, Idle and Stage work arrive as a stored value plus the moments their clocks started; a stale run stops where it was last read.
function costsMs(figure, entry, now = Date.now()) {
  if (!figure) return null;
  const at = entry.stale && entry.staleSince ? entry.staleSince : now;
  const open = Array.isArray(figure.since) ? figure.since : figure.since === null ? [] : [figure.since];
  return figure.ms + open.reduce((sum, since) => sum + Math.max(0, at - since), 0);
}
function costsDuration(ms) {
  if (ms === null || ms === undefined) return '—';
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return ms < 1000 ? '0s' : Math.floor(ms / 1000) + 's';
  const days = Math.floor(minutes / 1440), hours = Math.floor(minutes % 1440 / 60), rest = minutes % 60;
  return [days ? days + 'd' : '', hours || days ? hours + 'h' : '', rest + 'm'].filter(Boolean).join(' ');
}
function costsTokens(value) {
  if (value === null || value === undefined) return '—';
  if (value >= 1e6) return (value / 1e6).toFixed(2) + 'M';
  return value >= 1e3 ? Math.round(value / 1e3) + 'k' : String(value);
}
const costsMoney = value => new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(value);

// How a figure should read: a real zero, no data, a price somebody chose to hide, or a partial estimate.
function costsPrice(entry, hidden) {
  if (hidden || entry.price === 'desktop-hidden') return { text: 'Hidden', note: 'Hidden in Desktop', kind: 'hidden' };
  if (entry.price === 'afm-hidden') return { text: 'Hidden', note: 'Prices are hidden by AFM for this run', kind: 'hidden' };
  if (entry.price === 'unavailable' || !entry.money) return { text: '—', note: 'No accounting for this run', kind: 'none' };
  const money = entry.money, partial = money.coverage !== 'full' || money.unpriced > 0 || money.unmetered > 0 || entry.coverageIssues > 0;
  return { text: costsMoney(money.usd), note: partial ? 'Partial estimate' : '', kind: partial ? 'partial' : 'ok', usd: money.usd };
}
// A period figure is the growth recorded inside the period; no readings at all is "no data", never zero.
function costsPeriodFigures(entry, hidden) {
  const run = costsPeriodData()?.runs?.[entry.key];
  const base = costsFinished() && !hidden ? { usd: 0 } : costsPrice(entry, hidden);
  const growth = costsGrowthMode();
  const value = figure => (figure ? figure.value : null);
  const flags = Object.values(run ?? {}).filter(item => item && typeof item.value === 'number');
  const usd = growth ? run?.usd?.value : run?.asOf?.usd ?? undefined;
  // History keeps amounts, not how complete the estimate was, so a recorded estimate is never presented as comparable.
  const price = base.usd === undefined ? base : usd !== undefined ? { ...base, usd, text: costsMoney(usd), kind: 'partial', note: 'Coverage not recorded' } : { text: '—', note: 'No history yet', kind: 'none' };
  const at = run?.asOf;
  const history = { loaded: Boolean(run), partial: growth && flags.some(f => f.partial), gaps: growth ? flags.some(f => f.gaps > 0) : Boolean(at?.gap), observedAt: at?.observedAt ?? null };
  if (growth) return { elapsed: value(run?.elapsed), idle: value(run?.idle), work: value(run?.work), tokens: entry.tokens === null && !costsFinished() ? null : value(run?.tokens), price, history };
  return { elapsed: at?.elapsed ?? null, idle: at?.idle ?? null, work: at?.work ?? null, tokens: at?.tokens ?? null, price, history };
}
// Figures since the run started, whatever period the Costs screen shows; Home always reads these.
function costsLiveFigures(entry, hidden, now) {
  return { elapsed: costsMs(entry.elapsed, entry, now), idle: costsMs(entry.idle, entry, now), work: costsMs(entry.stageWork, entry, now),
    tokens: entry.tokens, price: costsPrice(entry, hidden), history: { loaded: true, partial: false, gaps: false } };
}
function costsFigures(entry, hidden, now) {
  const wholeRun = costsFinished() && costsPeriodData()?.runs?.[entry.key]?.meta?.wholeRun;
  if (costsFinished() && !entry.recorded && (!costsGrowthMode() || wholeRun)) {
    const recorded = costsPeriodFigures(entry, hidden);
    const final = costsLiveFigures(entry, hidden, now);
    return { elapsed: final.elapsed ?? recorded.elapsed, idle: recorded.idle, work: final.work ?? recorded.work,
      tokens: final.tokens ?? recorded.tokens, price: recorded.price, history: recorded.history };
  }
  return costsPeriodMode() || (costsFinished() && entry.recorded) ? costsPeriodFigures(entry, hidden) : costsLiveFigures(entry, hidden, now);
}
function costsProjectName(entry) {
  return projects.find(p => p.id === entry.projectId)?.name ?? (entry.projectId ? entry.projectId : 'Other runs');
}
function costsRunLabel(entry) {
  if (entry.stale) return 'Offline · figures as of ' + new Date(entry.staleSince ?? Date.now()).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
  const run = projects.flatMap(p => p.runs).find(item => item.key === entry.key);
  return run ? label(run) : String(entry.runStatus ?? (entry.finished ? 'finished' : 'running')).replace(/_/g, ' ').replace(/^./, c => c.toUpperCase());
}
// A run Desktop followed then but no longer observes has no live entry; its row is built from what the history stored.
function costsPastEntry(key, meta) {
  return { key, name: meta.name, projectId: meta.projectId, stale: false, staleSince: null, runStatus: meta.status ?? 'finished', finished: true, recorded: true, startedAt: meta.startedAt, price: 'visible',
    money: { usd: 0, coverage: 'full', unpriced: 0, unmetered: 0 }, coverageIssues: 0, tokens: 0, elapsed: null, idle: null, stageWork: null, stages: [], overhead: null, models: null };
}
function costsEntries() {
  if (costsFinished()) return Object.entries(costsPeriodData()?.runs ?? {}).map(([key, run]) => {
    return run.final ?? costsPastEntry(key, run.meta);
  });
  const data = costsRewind > 0 ? costsPeriodData() : null;
  if (!data?.runs) return costsView.runs;
  return Object.entries(data.runs).map(([key, run]) => costsView.runs.find(entry => entry.key === key) ?? costsPastEntry(key, run.meta));
}
function costsRows() {
  const hidden = costsView.hidePrices, now = Date.now();
  return costsEntries().filter(entry => costsUi.project === 'all' || entry.projectId === costsUi.project).map(entry => ({ entry, figures: costsFigures(entry, hidden, now) }));
}
function costsSortValue(row) {
  const f = row.figures;
  return costsUi.sort === 'tokens' ? f.tokens : costsUi.sort === 'time' ? f.elapsed : f.price.usd;
}
function costsSorted(rows) {
  // A run without the figure always goes last, whatever the direction.
  return [...rows].sort((a, b) => {
    const x = costsSortValue(a), y = costsSortValue(b);
    if ((x ?? null) === null || (y ?? null) === null) return (x ?? null) === null ? ((y ?? null) === null ? 0 : 1) : -1;
    return costsUi.dir * (x - y);
  });
}
function costsTotals(rows) {
  const total = { usd: 0, priced: 0, unpriced: 0, partial: 0, tokens: 0, withTokens: 0, elapsed: 0, idle: 0, elapsedKnown: 0, idleKnown: 0, stale: 0, partialHistory: 0, gapHistory: 0 };
  for (const { entry, figures } of rows) {
    if (figures.price.usd !== undefined) { total.usd += figures.price.usd; total.priced++; if (figures.price.kind === 'partial') total.partial++; } else total.unpriced++;
    if (figures.tokens !== null) { total.tokens += figures.tokens; total.withTokens++; }
    // A run without the figure adds nothing and is not counted as zero.
    if (figures.elapsed !== null) { total.elapsed += figures.elapsed; total.elapsedKnown++; }
    if (figures.idle !== null) { total.idle += figures.idle; total.idleKnown++; }
    if (entry.stale) total.stale++;
    if (figures.history.partial) total.partialHistory++;
    if (figures.history.gaps) total.gapHistory++;
  }
  return total;
}
// Bars for the selected metric: models and projects from the collected rows, or one bar per run.
function costsBars(rows) {
  const metric = costsUi.metric, bars = new Map();
  const group = costsPeriodMode() && costsUi.group === 'model' && metric !== 'time' ? 'project' : costsUi.group;
  const add = (name, value) => { if (value === null || value === undefined) return; bars.set(name, (bars.get(name) ?? 0) + value); };
  let note = '';
  for (const { entry, figures } of rows) {
    if (group === 'project') add(costsProjectName(entry), metric === 'cost' ? figures.price.usd : metric === 'tokens' ? figures.tokens : figures.elapsed);
    else if (group === 'run' || metric === 'time') add(entry.name, metric === 'cost' ? figures.price.usd : metric === 'tokens' ? figures.tokens : figures.elapsed);
    else if (!entry.models) note = 'Models appear once a run reports its usage.';
    else for (const model of entry.models) add(model.label, metric === 'cost' ? model.usd : model.tokens);
  }
  if (group !== costsUi.group) note = 'Models are reported for the whole run: choose Since start and Back to now to see them.';
  const list = [...bars].map(([name, value]) => ({ name, value })).sort((a, b) => b.value - a.value);
  const sum = list.reduce((n, item) => n + item.value, 0);
  return { list: list.map(item => ({ ...item, ratio: sum ? item.value / sum : 0 })), note };
}
const costsValue = (metric, value) => metric === 'cost' ? costsMoney(value) : metric === 'tokens' ? costsTokens(value) : costsDuration(value);

function costsDemoView(now = Date.now()) {
  const ago = hours => now - hours * 3600000;
  const stage = (id, name, workMs, tokens, usd) => ({ id, name, status: 'done', workMs, tokens, usd });
  const entry = (key, name, projectId, started, tokens, usd, stages, models, extra = {}) => ({ key, name, projectId, stale: false, staleSince: null, runStatus: 'running', startedAt: ago(started),
    elapsed: { ms: started * 3600000, since: null }, idle: { ms: started * 600000, since: null }, stageWork: { ms: started * 3000000, since: [] }, tokens, price: 'visible', coverageIssues: 0,
    money: { usd, coverage: 'full', unpriced: 0, unmetered: 0 }, stages, overhead: { tokens: Math.round(tokens / 20), usd: usd / 20 }, analytics: null, models, ...extra });
  return { hidePrices: false, runs: [
    entry('demo:spider-1', 'checkout-flow', 'spider', 6, 1480000, 18.4, [stage('plan', 'Plan', 1800000, 210000, 2.6), stage('impl', 'Implement', 9000000, 1100000, 13.1)], [{ key: 'm1', label: 'Sonnet 5.5', timeMs: 5400000, tokens: 1200000, usd: 14.2 }, { key: 'm2', label: 'Opus 5.5', timeMs: 1800000, tokens: 280000, usd: 4.2 }]),
    entry('demo:afm-1', 'release-review', 'afm', 3, 820000, 0, [], [{ key: 'm3', label: 'Custom model', timeMs: 1200000, tokens: 820000 }], { price: 'afm-hidden', money: undefined }),
  ] };
}

// Each demo run grew evenly since it started, so its slices add up to the value it ends with, as recorded history would.
function costsDemoPeriod(from, to, buckets, { finished = false } = {}) {
  const step = (to - from) / buckets;
  const entries = costsDemoView(finished ? to - 2 * 3600000 : to).runs;
  if (finished) entries.splice(0, entries.length, { ...entries[0], key: 'demo:completed', name: 'storefront-release', runStatus: 'finished', finished: true });
  const runs = Object.fromEntries(entries.map(entry => {
    const end = finished ? to - 2 * 3600000 : to;
    const priced = entry.price === 'visible', usd = priced ? entry.money.usd : null, work = entry.stageWork.ms;
    // Nothing was recorded before the run started.
    const grew = (total, a, b) => (total === null || b <= entry.startedAt ? null
      : { value: total * Math.max(0, Math.min(b, end) - Math.max(a, entry.startedAt)) / (end - entry.startedAt), partial: false, gaps: 0 });
    const slices = total => Array.from({ length: buckets }, (_, i) => grew(total, from + i * step, from + (i + 1) * step));
    return [entry.key, { elapsed: grew(entry.elapsed.ms, from, to), idle: grew(entry.idle.ms, from, to), work: grew(work, from, to), tokens: grew(entry.tokens, from, to),
      usd: grew(usd, from, to), anchors: { cost: usd, work }, meta: { name: entry.name, projectId: entry.projectId, startedAt: entry.startedAt, finished, endedAt: finished ? end : null },
      final: finished ? entry : undefined,
      asOf: { elapsed: entry.elapsed.ms, idle: entry.idle.ms, work, tokens: entry.tokens, usd, observedAt: end - 60000, gap: false }, buckets: priced ? { cost: slices(usd), tokens: slices(entry.tokens), time: slices(entry.elapsed.ms), work: slices(work) } : { cost: null, tokens: slices(entry.tokens), time: slices(entry.elapsed.ms), work: slices(work) } }];
  }));
  return { available: true, from, to, runs, series: { from, to, buckets } };
}
// `value` and `rich` are trusted markup built here; `note` is plain text.
function costsKpi(title, value, note, extra = '', rich = '') {
  return `<div class="cost-card"${extra}><dt>${esc(title)}</dt><dd>${value}</dd><small>${rich || esc(note)}</small></div>`;
}
function costsRowHtml({ entry, figures }) {
  const price = figures.price;
  const live = !costsPeriodMode() && !costsFinished();
  const clock = (kind, text) => `<span${live ? ` data-clock="${kind}" data-key="${esc(entry.key)}"` : ''}>${text}</span>`;
  const work = figures.work === null ? 'Stage work unknown' : `Stage work ${clock('work', costsDuration(figures.work))}`;
  const historyNote = figures.history.partial ? '<br><small class="cost-price-note partial">History starts after the period start</small>' : figures.history.gaps ? '<br><small class="cost-price-note partial">Gap in history</small>' : '';
  const read = costsRewind > 0 && figures.history.observedAt ? `<br><small class="cost-price-note">Read at ${esc(costsStamp(figures.history.observedAt, true))}</small>` : '';
  const state = costsRewind > 0 && !costsFinished() ? (entry.finished ? 'Finished since' : 'Active') : costsRunLabel(entry);
  const priceCell = `${esc(price.text)}${price.note ? `<br><small class="cost-price-note ${price.kind === 'partial' ? 'partial' : ''}">${esc(price.note)}</small>` : ''}`;
  return `<tr data-cost-row="${esc(entry.key)}"><td><button type="button" class="cost-run-name" data-cost-run="${esc(entry.key)}"><strong>${esc(entry.name)}</strong><small>${esc(costsProjectName(entry))}</small></button></td>
    <td><span class="cost-state${entry.stale ? ' stale' : ''}">${esc(state)}</span>${read}</td>
    <td>${clock('elapsed', costsDuration(figures.elapsed))}<br><small class="cost-price-note">${work} · idle ${clock('idle', costsDuration(figures.idle))}</small>${historyNote}</td>
    <td title="${figures.tokens === null && !costsFinished() ? 'Accounting is off for this run' : ''}">${costsTokens(figures.tokens)}</td>
    ${costsView.hidePrices ? '' : `<td>${priceCell}</td>`}
    <td><button type="button" data-cost-run="${esc(entry.key)}" aria-label="Open cost details for ${esc(entry.name)}">↗</button></td></tr>`;
}
function costsFormatPoint(metric, value) { return value === null ? 'No data' : costsValue(metric, value); }
function costsStamp(ms, long) {
  return new Date(ms).toLocaleString('en-GB', long ? { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' } : { hour: '2-digit', minute: '2-digit' });
}
// The usage line over the chosen period, one point per slice; a slice with no readings breaks the line instead of drawing zero.
// The chart sums the slices of exactly the runs the screen shows, and takes money only from runs whose price is visible right now.
function costsSeries(rows, metric, count) {
  const data = costsPeriodData(), key = metric === 'cost' ? 'cost' : metric;
  return Array.from({ length: count }, (_, i) => {
    const parts = rows.map(({ entry, figures }) => (metric === 'cost' && figures.price.usd === undefined ? null : data.runs[entry.key]?.buckets?.[key]?.[i] ?? null)).filter(Boolean);
    return parts.length ? { value: parts.reduce((sum, part) => sum + part.value, 0), partial: parts.some(part => part.partial), gaps: parts.reduce((sum, part) => sum + part.gaps, 0) } : null;
  });
}
function costsCompletionSeries(rows, metric, count, data) {
  const points = Array(count).fill(null);
  for (const { entry } of rows) {
    const run = data.runs[entry.key];
    const ended = run.meta?.endedAt;
    const value = metric === 'tokens' ? run.final?.tokens : run.final?.elapsed?.ms;
    if (ended == null || value == null || ended < data.from || ended > data.to) continue;
    const i = Math.min(count - 1, Math.floor((ended - data.from) / (data.to - data.from) * count));
    points[i] = { value: (points[i]?.value ?? 0) + value };
  }
  return points;
}
function costsChart(hidden, rows) {
  if (!costsGrowthMode()) return '';
  const data = costsPeriodData();
  const metric = hidden && costsUi.metric === 'cost' ? 'time' : costsUi.metric;
  const count = Object.keys(data?.runs ?? {}).length ? data.series?.buckets ?? 0 : 0;
  let points = count ? costsSeries(rows, metric, count) : null;
  const byCompletion = costsFinished() && metric !== 'cost' && count && points && !points.some(Boolean);
  if (byCompletion) points = costsCompletionSeries(rows, metric, count, data);
  const title = costsUi.range === '24' ? 'Usage, last 24 hours' : 'Usage, last 7 days';
  if (!points || (costsFinished() && !points.some(Boolean))) return `<section class="cost-card cost-chart-card" aria-labelledby="cost-chart-title"><header class="cost-card-head"><div><h2 id="cost-chart-title">${title}</h2><p>${!data ? 'Loading history…' : costsFinished() ? 'Desktop recorded no intervals for this metric.' : 'History is not available.'}</p></div></header></section>`;
  const step = (data.to - data.from) / points.length, W = 640, H = 170, pad = 24;
  const high = Math.max(1e-9, ...points.map(p => p?.value ?? 0));
  const x = i => pad + (points.length === 1 ? 0 : i * (W - 2 * pad) / (points.length - 1));
  const y = value => H - pad - value / high * (H - 2 * pad);
  const runs = [];
  points.forEach((p, i) => { if (p) { if (!runs.at(-1)) runs.push([]); runs.at(-1).push(`${x(i).toFixed(1)},${y(p.value).toFixed(1)}`); } else runs.push(null); });
  const lines = runs.filter(Boolean).map(run => {
    if (run.length > 1) return `<polyline points="${run.join(' ')}" fill="none" stroke="var(--accent-primary)" stroke-width="2"/>`;
    const [cx, cy] = run[0].split(',');
    return `<circle cx="${cx}" cy="${cy}" r="3" fill="var(--accent-primary)"/>`;
  }).join('');
  const buttons = points.map((p, i) => {
    const label = `${costsStamp(data.from + i * step, costsUi.range !== '24')} → ${costsStamp(data.from + (i + 1) * step, costsUi.range !== '24')}: ${costsFormatPoint(metric, p?.value ?? null)}${p?.partial ? ' (history starts later)' : ''}${p?.gaps ? ' (gap in history)' : ''}`;
    return `<button type="button" class="cost-point" data-cost-point="${esc(label)}" aria-label="${esc(label)}" title="${esc(label)}" style="left:${(x(i) / W * 100).toFixed(2)}%;top:${p ? (y(p.value) / H * 100).toFixed(2) : 50}%"></button>`;
  }).join('');
  const partial = points.some(p => p?.partial), gaps = points.some(p => p?.gaps);
  return `<section class="cost-card cost-chart-card" aria-labelledby="cost-chart-title"><header class="cost-card-head"><div><h2 id="cost-chart-title">${title}</h2><p>${metric === 'time' ? 'Elapsed time' : metric === 'tokens' ? 'Recorded tokens' : 'Known estimated cost'} ${byCompletion ? 'for runs that finished in each interval' : 'per interval'}${partial ? ' · history starts after the period start' : ''}${gaps ? ' · gaps in history' : ''}</p></div></header>
    <div class="cost-plot"><svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" aria-hidden="true"><line x1="${pad}" x2="${W - pad}" y1="${H - pad}" y2="${H - pad}" stroke="var(--separator-subtle)"/>${lines}</svg><div class="cost-points" role="group" aria-label="Time intervals">${buttons}</div></div>
    <p id="cost-chart-readout" class="cost-muted" aria-live="polite">${esc(`${costsStamp(data.from, true)} – ${costsStamp(data.to, true)}`)}</p></section>`;
}
function costsSortHead(field, text, hidden) {
  if (hidden) return '';
  const sorted = costsUi.sort === field;
  return `<th scope="col" aria-sort="${sorted ? (costsUi.dir < 0 ? 'descending' : 'ascending') : 'none'}"><button type="button" data-cost-sort="${field}">${text} <span aria-hidden="true">${sorted ? (costsUi.dir < 0 ? '↓' : '↑') : ''}</span></button></th>`;
}
function renderCosts() {
  const hidden = costsView.hidePrices;
  if (hidden && costsUi.metric === 'cost') costsUi.metric = 'time';
  if (hidden && costsUi.sort === 'cost') costsUi.sort = 'time';
  const rows = costsRows(), total = costsTotals(rows), sorted = costsSorted(rows), bars = costsBars(rows);
  const title = costsFinished() ? 'Finished runs' : costsRewind > 0 ? 'Runs then' : 'Active runs';
  const liveClocks = !costsPeriodMode() && !costsFinished();
  const options = [{ id: 'all', name: 'All projects' }, ...projects.map(p => ({ id: p.id, name: p.name }))];
  const segment = (key, label, items) => `<div class="cost-segment compact" role="group" aria-label="${label}">${items.filter(([value]) => !(value === 'cost' && hidden)).map(([value, text]) => `<button type="button" data-cost-${key}="${value}" aria-pressed="${costsUi[key] === value}">${text}</button>`).join('')}</div>`;
  const noPrice = total.unpriced;
  const readings = rows.map(row => row.figures.history.observedAt).filter(Boolean);
  const asOfLine = costsRewind > 0 ? `${costsZoned(costsRewindAt)}${readings.length ? ` · latest reading used ${costsStamp(Math.max(...readings), true)}` : costsFinished() ? '' : ' · no readings at this moment'}` : 'Now';
  const asOfHtml = `<section class="cost-card cost-asof" aria-labelledby="cost-asof-title"><header class="cost-card-head"><div><h2 id="cost-asof-title">As of</h2><p id="cost-asof-label">${esc(asOfLine)}</p></div>
    <button type="button" class="action" id="cost-asof-now"${costsRewind > 0 ? '' : ' disabled'}>Back to now</button></header>
    <input type="range" id="cost-asof" min="0" max="${COSTS_REWIND_HOURS}" step="1" value="${COSTS_REWIND_HOURS - costsRewind}" aria-label="Look back up to 7 days" aria-valuetext="${esc(asOfLine)}">
    <div class="cost-asof-days" aria-hidden="true">${Array.from({ length: 8 }, (_, i) => `<span>${i === 7 ? 'Now' : new Date(Date.now() - (7 - i) * 86400000).toLocaleDateString('en-GB', { weekday: 'short' })}</span>`).join('')}</div></section>`;
  $('screen').innerHTML = `<div class="cost-home"><div class="cost-heading"><div><h1>Costs</h1><p>${costsGrowthMode() ? `Growth in the ${costsRewind > 0 ? `${costsUi.range === '24' ? '24 hours' : '7 days'} before the chosen moment` : `last ${costsUi.range === '24' ? '24 hours' : '7 days'}`} from Desktop's own observations.` : costsRewind > 0 ? 'Figures since each run started, as of the chosen moment.' : 'Figures are since each run started.'}</p></div>
    <div class="cost-filters">${segment('status', 'Run status', [['active', 'Active'], ['finished', 'Finished']])}<div class="cost-segment" role="group" aria-label="Date range">${COSTS_RANGES.map(([value, text]) => `<button type="button" data-cost-range="${value}" aria-pressed="${costsUi.range === value}">${text}</button>`).join('')}</div><label class="cost-select"><span class="sr-only">Project</span><select id="cost-project">${options.map(o => `<option value="${esc(o.id)}"${o.id === costsUi.project ? ' selected' : ''}>${esc(o.name)}</option>`).join('')}</select></label>
    <button type="button" class="action" id="cost-price-toggle" aria-pressed="${hidden}">${hidden ? 'Show prices' : 'Hide prices'}</button></div></div>
    ${asOfHtml}
    <dl class="cost-kpis" aria-live="polite">${hidden ? '' : costsKpi('Estimated cost', total.priced ? costsMoney(total.usd) : '—', total.priced ? ([total.partial || noPrice ? `${total.partial + noPrice} of ${rows.length} runs partial or without prices` : '', total.partialHistory ? `${total.partialHistory} runs: history starts after the period start` : ''].filter(Boolean).join(' · ') || 'List-price estimate') : 'No priced usage')}
    ${costsKpi('Tokens', total.withTokens ? costsTokens(total.tokens) : '—', total.withTokens ? (total.withTokens < rows.length ? `${rows.length - total.withTokens} runs without accounting` : 'Recorded input, cache and output') : 'No usage recorded')}
    ${costsKpi(title, String(rows.length), costsFinished() ? (costsRewind > 0 ? 'Finished by the chosen moment' : '') : costsRewind > 0 ? 'Followed at that moment' : total.stale ? `${total.stale} offline, figures paused` : 'Observed now')}
    ${costsKpi('Elapsed time', `<span${liveClocks ? ' data-clock="total-elapsed"' : ''}>${costsDuration(total.elapsedKnown ? total.elapsed : null)}</span>`, '', '', `Idle <span${liveClocks ? ' data-clock="total-idle"' : ''}>${costsDuration(total.idleKnown ? total.idle : null)}</span> · runs can overlap`)}</dl>
    ${costsChart(hidden, rows)}
    <section class="cost-card cost-chart-card cost-breakdown" aria-labelledby="cost-breakdown-title"><header class="cost-card-head"><div><h2 id="cost-breakdown-title">Breakdown</h2><p>${costsUi.metric === 'time' ? 'Elapsed time' : costsUi.metric === 'tokens' ? 'Recorded tokens' : 'Known estimated cost'} by ${costsUi.group === 'model' && costsUi.metric !== 'time' ? 'model' : costsUi.group === 'project' ? 'project' : 'run'}</p></div>
    <div class="cost-controls">${segment('metric', 'Metric', [['cost', 'Cost'], ['tokens', 'Tokens'], ['time', 'Time']])}${segment('group', 'Grouping', [['model', 'Model'], ['project', 'Project'], ['run', 'Run']])}</div></header>
    <ol id="cost-breakdown">${bars.list.map((bar, i) => `<li class="cost-bar"><div class="cost-bar-label"><span>${esc(bar.name)}</span><strong>${esc(costsValue(costsUi.metric, bar.value))} <small class="cost-muted">· ${Math.round(bar.ratio * 100)}%</small></strong></div><div class="cost-bar-track" aria-hidden="true"><i style="--bar-width:${bar.ratio * 100}%;--bar-color:${['#8995ff', '#6dcaa5', '#efb76c', '#b58cf1', '#63b7e9', '#f185a0'][i % 6]}"></i></div></li>`).join('') || `<li class="cost-muted">${esc(bars.note || 'No recorded usage yet.')}</li>`}</ol>${bars.list.length && bars.note ? `<p class="cost-muted">${esc(bars.note)}</p>` : ''}</section>
    <section class="cost-card cost-run-card" aria-labelledby="cost-runs-title"><header class="cost-card-head"><div><h2 id="cost-runs-title">${title}</h2><p>${rows.length} ${rows.length === 1 ? 'run' : 'runs'} · ${costsGrowthMode() ? 'in the period' : 'since start'}</p></div>
    <button type="button" class="action" id="cost-compare"${rows.length < 2 ? ' disabled' : ''}>Compare runs</button></header>
    <div class="cost-table-scroll"><table><thead><tr><th scope="col">Run</th><th scope="col">Status</th>${costsSortHead('time', 'Elapsed')}${costsSortHead('tokens', 'Tokens')}${costsSortHead('cost', 'Est. cost', hidden)}<th scope="col"><span class="sr-only">Run details</span></th></tr></thead>
    <tbody id="cost-rows">${sorted.map(costsRowHtml).join('') || `<tr><td colspan="${hidden ? 5 : 6}" class="cost-muted">${costsFinished() ? costsPeriodData() ? 'No finished runs match these filters.' : 'Loading finished runs…' : costsView.runs.length ? 'No active runs in this project.' : 'No active runs yet.'}</td></tr>`}</tbody></table></div></section></div>`;
  paintCostsDetail();
  paintCostsCompare();
}
// While the slider is held, a repaint would recreate it and drop the pointer; the screen catches up on release.
let costsSliding = false;
function paintCosts(fresh = false) {
  if (location.hash !== '#costs') return;
  if (costsSliding) return;
  if (fresh) refreshCostsPeriod();
  const saved = rememberFocus();
  renderCosts();
  restoreFocus(saved);
}
// The history behind a chosen period; a newer request or a changed range drops an older answer.
function refreshCostsPeriod(force = false) {
  // Every path (clock, costs:update, controls) ends here, so a hidden window never reads history; showing it resumes.
  if (document.hidden || (!costsPeriodMode() && !costsFinished()) || location.hash !== '#costs') return;
  // A past moment does not change while it is looked at, so a live update does not ask again.
  if (!force && costsRewindAt && costsPeriodData()) return;
  if (!force && costsFinished() && costsPeriodData() && Date.now() - costsPeriodAt < 15000) return;
  const { range, status } = costsUi, rewindAt = costsRewindAt, id = ++costsPeriodRequest;
  costsPeriodAt = Date.now();
  const to = rewindAt ?? Date.now(), from = to - (costsGrowthMode() ? Number(range) * 3600000 : 3600000), buckets = costsGrowthMode() ? COSTS_BUCKETS : 0;
  const options = costsFinished() ? { finished: true, growth: costsGrowthMode(), rewind: Boolean(rewindAt) } : undefined;
  const load = isDemo ? Promise.resolve(costsDemoPeriod(from, to, buckets, options)) : nativeAFM.costsPeriod(from, to, buckets, options);
  load.then(data => {
    if (id !== costsPeriodRequest || costsUi.range !== range || costsUi.status !== status || costsRewindAt !== rewindAt) return;
    costsPeriod = { range, status, rewind: rewindAt, data };
    paintCosts();
  }).catch(showNativeError);
}
// Open clocks advance in place, so a second never rebuilds the table or moves focus.
function tickCosts() {
  // A hidden window asks for nothing; showing it again refreshes at once (see visibilitychange below).
  if (document.hidden) return;
  // Unchanged status keeps costs:update quiet while the history keeps growing, so live periods ask again on their own, at most every 15 s.
  if (costsOnHome()) { refreshCostsHome(); paintHomeCosts(); return; }
  if (location.hash !== '#costs') return;
  if ((costsPeriodMode() || costsFinished()) && !costsRewindAt && !costsSliding && Date.now() - costsPeriodAt >= 15000) refreshCostsPeriod(true);
  if (costsFinished()) return;
  const now = Date.now(), totals = costsTotals(costsRows());
  for (const el of document.querySelectorAll('[data-clock]')) {
    const kind = el.dataset.clock, entry = costsView.runs.find(item => item.key === el.dataset.key);
    const ms = kind === 'total-elapsed' ? totals.elapsed : kind === 'total-idle' ? totals.idle
      : entry ? costsMs(kind === 'idle' ? entry.idle : kind === 'work' ? entry.stageWork : entry.elapsed, entry, now) : undefined;
    if (ms === undefined) continue;
    const text = costsDuration(ms);
    if (el.textContent !== text) el.textContent = text;
  }
}
function paintCostsDetail() {
  const dialog = $('run-cost'), entry = costsEntries().find(item => item.key === costsDetail);
  if (!entry) { if (dialog.open) dialog.close(); costsDetail = null; return; }
  const hidden = costsView.hidePrices, f = costsFigures(entry, hidden, Date.now());
  const money = value => hidden || entry.price !== 'visible' || value === undefined ? '' : `<td>${costsMoney(value)}</td>`;
  const showMoney = !hidden && entry.price === 'visible';
  const live = !costsPeriodMode() && !costsFinished();
  $('run-cost-project').textContent = costsProjectName(entry) + ' / Cost' + (costsGrowthMode() ? ` · last ${costsUi.range === '24' ? '24 hours' : '7 days'}` : '');
  $('run-cost-name').textContent = entry.name;
  $('run-cost-body').innerHTML = `<div class="cost-detail-totals">${showMoney || f.price.usd !== undefined ? `<div><small>Estimated cost</small><strong>${esc(f.price.text)}</strong></div>` : ''}
    <div><small>Elapsed</small><strong${live ? ` data-clock="elapsed" data-key="${esc(entry.key)}"` : ''}>${costsDuration(f.elapsed)}</strong></div><div><small>Stage work</small><strong${live && f.work !== null ? ` data-clock="work" data-key="${esc(entry.key)}"` : ''}>${costsDuration(f.work)}</strong></div><div><small>Idle</small><strong${live ? ` data-clock="idle" data-key="${esc(entry.key)}"` : ''}>${costsDuration(f.idle)}</strong></div>
    <div><small>Tokens</small><strong>${costsTokens(f.tokens)}</strong></div></div>
    ${costsRewind > 0 || costsGrowthMode() ? `<p class="cost-muted">${costsRewind > 0 ? COSTS_NO_STAGES : 'The totals above cover the period; the stage rows below are totals since the run started.'}</p>` : ''}
    ${costsRewind > 0 ? '' : `<table><thead><tr><th>Stage</th><th>Stage work</th><th>Tokens</th>${showMoney ? '<th>Est. cost</th>' : ''}</tr></thead><tbody>
    ${entry.stages.map(s => `<tr><td>${esc(s.name)}</td><td>${costsDuration(s.workMs)}</td><td>${costsTokens(s.tokens)}</td>${showMoney ? money(s.usd) || '<td>—</td>' : ''}</tr>`).join('')}
    ${entry.overhead ? `<tr><td>Run overhead</td><td>—</td><td>${costsTokens(entry.overhead.tokens)}</td>${showMoney ? money(entry.overhead.usd) || '<td>—</td>' : ''}</tr>` : ''}
    ${entry.stages.length || entry.overhead ? '' : `<tr><td colspan="${showMoney ? 4 : 3}" class="cost-muted">No stage usage recorded yet.</td></tr>`}</tbody></table>`}
    ${f.price.note && !hidden ? `<p class="cost-muted cost-detail-note">${esc(f.price.note)}</p>` : ''}
    <div class="actions"><button type="button" class="action" data-cost-open="${esc(entry.key)}">Open run</button></div>`;
  if (!dialog.open) dialog.showModal();
}
// The run itself opens in its project; its own Cost tab is inside the run's page. A run beyond the project's tab limit
// is pinned so the project route can show it.
function openCostRun(key) {
  const find = () => { for (const p of projects) { const run = p.runs.find(item => item.key === key); if (run) return { p, run }; } return null; };
  let found = find();
  if (!found && typeof lastSnapshot !== 'undefined' && lastSnapshot) { pinnedRunKey = key; applyWorkspace(lastSnapshot); found = find(); }
  if (!found) { showNativeError('That run is not in the project list yet.'); return; }
  $('run-cost').close();
  openProject(found.p, found.run, 'run');
}

// ---- Home: the cumulative lines on project tiles and the since-start figures on the active run rows ----
const COSTS_HOME_BUCKETS = 24;
const COSTS_COLORS = ['#8995ff', '#6dcaa5', '#efb76c', '#b58cf1', '#63b7e9', '#f185a0'];
let costsHome = null;
let costsHomeRequest = 0;
let costsHomeAt = 0;
let costsRoute = '';
const costsOnHome = () => location.hash === '' || location.hash === '#';
// One 24-hour history read feeds every tile; it is not repeated per tile and at most every 15 seconds.
function refreshCostsHome(force = false) {
  if (document.hidden || !costsOnHome()) return;
  const now = Date.now();
  if (!force && now - costsHomeAt < 15000) return;
  costsHomeAt = now;
  const id = ++costsHomeRequest, from = now - 24 * 3600000;
  const load = isDemo ? Promise.resolve(costsDemoPeriod(from, now, COSTS_HOME_BUCKETS)) : nativeAFM.costsPeriod(from, now, COSTS_HOME_BUCKETS);
  load.then(data => { if (id === costsHomeRequest) { costsHome = data; paintHomeCosts(); } }).catch(showNativeError);
}
// Value since the start at each of the 25 points of the last day: the run's last recorded value minus what grew after that
// point, both from one history read. Where growth is unknown, or history starts later, the earlier points stay empty.
function costsCumulative(end, growth) {
  const points = new Array(growth.length + 1).fill(null);
  if (end === null || end === undefined) return points;
  points[growth.length] = end;
  let value = end, known = true;
  for (let i = growth.length - 1; i >= 0; i--) {
    const slice = growth[i];
    if (!known || !slice || slice.partial || slice.gaps) { known = false; continue; }
    value -= slice.value;
    points[i] = Math.max(0, value);
  }
  return points;
}
function costsTileModel(p) {
  const hidden = costsView.hidePrices, now = Date.now();
  // Every run Costs observes for the project, not the tab list (which is limited), so Home and Costs add up the same runs.
  const items = costsView.runs.filter(entry => entry.projectId === p.id).map(entry => ({ entry, figures: costsLiveFigures(entry, hidden, now) }));
  if (!items.length) return null;
  const money = !hidden && items.some(({ figures }) => figures.price.usd !== undefined);
  const lines = items.map(({ entry, figures }, i) => {
    const total = money ? figures.price.usd ?? null : figures.work;
    const run = costsHome?.runs?.[entry.key];
    // The legend and total are live; the line is history and keeps no money of a run that no longer shows its price.
    const anchor = money && figures.price.usd === undefined ? null : run?.anchors?.[money ? 'cost' : 'work'] ?? null;
    const growth = run?.buckets?.[money ? 'cost' : 'work'];
    return { name: entry.name, color: COSTS_COLORS[i % COSTS_COLORS.length], total,
      label: money ? (figures.price.usd === undefined ? 'No price' : figures.price.text) : costsDuration(figures.work), partial: money && figures.price.kind === 'partial',
      points: growth ? costsCumulative(anchor, growth) : new Array(COSTS_HOME_BUCKETS + 1).fill(null) };
  });
  const known = lines.filter(line => line.total !== null);
  return { money, project: p.id, total: known.length ? known.reduce((sum, line) => sum + line.total, 0) : null, lines };
}
function costsTileHtml(model) {
  const W = 300, H = 64, pad = 4;
  const high = Math.max(1e-9, ...model.lines.flatMap(line => line.points.filter(point => point !== null)));
  const x = i => pad + i * (W - 2 * pad) / COSTS_HOME_BUCKETS, y = value => H - pad - value / high * (H - 2 * pad);
  const lines = model.lines.map(line => {
    const runs = [];
    line.points.forEach((point, i) => { if (point === null) runs.push(null); else { if (!runs.at(-1)) runs.push([]); runs.at(-1).push(`${x(i).toFixed(1)},${y(point).toFixed(1)}`); } });
    return runs.filter(Boolean).map(run => run.length > 1 ? `<polyline points="${run.join(' ')}" fill="none" stroke="${line.color}" stroke-width="2"/>` : `<circle cx="${run[0].split(',')[0]}" cy="${run[0].split(',')[1]}" r="2" fill="${line.color}"/>`).join('');
  }).join('');
  const total = model.total === null ? '—' : model.money ? costsMoney(model.total) : costsDuration(model.total);
  return `<div class="project-cost-top"><span>${model.money ? 'Est. cost' : 'Stage work'} · since start</span><strong>${esc(total)}</strong></div>
    <svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" aria-hidden="true">${lines}</svg>
    <div class="project-cost-axis"><span>−24h</span><span>Now</span></div>
    <div class="project-cost-legend">${model.lines.map(line => `<span><i style="background:${line.color}"></i>${esc(line.name)} <b>${esc(line.label)}</b>${line.partial ? ' <em>partial</em>' : ''}</span>`).join('')}</div>`;
}
// Only tiles and rows whose figures changed are touched, so focus and the scroll position of Home stay where they were.
function paintHomeCosts() {
  if (!costsOnHome()) return;
  for (const slot of document.querySelectorAll('[data-cost-project]')) {
    const p = projects.find(item => item.id === slot.dataset.costProject), plain = slot.previousElementSibling;
    const link = [...document.querySelectorAll('[data-cost-link]')].find(item => item.dataset.costLink === slot.dataset.costProject);
    const model = p && costsTileModel(p);
    const html = model ? costsTileHtml(model) : '';
    if (slot.dataset.sig !== html) { slot.innerHTML = html; slot.dataset.sig = html; }
    // Setting an attribute to the value it has still counts as a page change.
    const set = (el, hidden) => { if (el && el.hidden !== hidden) el.hidden = hidden; };
    set(slot, !model);
    set(plain, Boolean(model));
    set(link, !model);
  }
  for (const cell of document.querySelectorAll('[data-cost-run-key]')) {
    const entry = costsView.runs.find(item => item.key === cell.dataset.costRunKey);
    const f = entry && costsLiveFigures(entry, costsView.hidePrices, Date.now());
    const text = !f ? '' : f.price.usd !== undefined ? `${f.price.text} · since start` : f.work !== null ? `Stage work ${costsDuration(f.work)} · since start` : '';
    if (cell.textContent !== text) cell.textContent = text;
  }
}

// ---- Compare runs: two runs side by side, on the same basis as the table behind the dialog ----
const costsSigned = (diff, format) => (diff > 0 ? '+' : diff < 0 ? '−' : '') + format(Math.abs(diff));
// A percentage only where it means something: a positive base and, for money, two comparable estimates.
function costsDeltaCell(a, b, format, comparable = true) {
  if (a === null || a === undefined || b === null || b === undefined) return '—';
  const percent = comparable && a > 0 ? ` (${costsSigned(b - a, value => Math.round(value / a * 100) + '%')})` : '';
  return costsSigned(b - a, format) + percent;
}
function costsComparePair() {
  const rows = costsRows();
  const find = key => rows.find(row => row.entry.key === key);
  return { rows, first: find(costsCompare?.a), second: find(costsCompare?.b) };
}
function costsCompareStages(first, second) {
  const names = [...new Map([...first.entry.stages, ...second.entry.stages].map(stage => [stage.id, stage.name])).entries()];
  const money = !costsView.hidePrices;
  const cell = (entry, id) => {
    const stage = entry.stages.find(item => item.id === id);
    if (!stage) return '<td class="cost-muted">Not in this run</td>';
    const usd = money && entry.price === 'visible' && stage.usd !== undefined ? costsMoney(stage.usd) : '—';
    return `<td>${costsDuration(stage.workMs)} · ${costsTokens(stage.tokens)}${money ? ` · ${usd}` : ''}</td>`;
  };
  const overhead = entry => (entry.overhead ? `<td>${costsTokens(entry.overhead.tokens)}${money && entry.price === 'visible' && entry.overhead.usd !== undefined ? ` · ${costsMoney(entry.overhead.usd)}` : ''}</td>` : '<td class="cost-muted">No overhead recorded</td>');
  const body = names.map(([id, name]) => `<tr><th scope="row">${esc(name)}</th>${cell(first.entry, id)}${cell(second.entry, id)}</tr>`).join('');
  return `${body || '<tr><td colspan="3" class="cost-muted">No stage usage recorded yet.</td></tr>'}<tr><th scope="row">Run overhead</th>${overhead(first.entry)}${overhead(second.entry)}</tr>`;
}
function paintCostsCompare() {
  const dialog = $('run-compare');
  if (!costsCompare) { if (dialog.open) dialog.close(); return; }
  const { rows, first, second } = costsComparePair();
  const options = key => rows.map(({ entry }) => `<option value="${esc(entry.key)}"${entry.key === key ? ' selected' : ''}>${esc(entry.name)} · ${esc(costsProjectName(entry))}</option>`).join('');
  // A live update that changes nothing must not rebuild the selects under the user's hands.
  const controls = options(costsCompare.a) + '|' + options(costsCompare.b);
  if (controls !== costsCompareControls) { costsCompareControls = controls; $('compare-a').innerHTML = options(costsCompare.a); $('compare-b').innerHTML = options(costsCompare.b); }
  const hidden = costsView.hidePrices;
  if (!first || !second || first === second) {
    $('compare-body').textContent = rows.length < 2 ? 'Fewer than two runs are shown.' : 'Choose two different runs.';
    costsCompareBody = '';
  } else {
    const a = first.figures, b = second.figures;
    const basis = costsGrowthMode() ? 'growth in the chosen period' : costsRewind > 0 ? 'since start, as of the chosen moment' : 'since start';
    const comparablePrice = a.price.kind === 'ok' && b.price.kind === 'ok';
    const line = (name, x, y, format, comparable = true) => `<tr><th scope="row">${name}</th><td>${format(x)}</td><td>${format(y)}</td><td>${costsDeltaCell(x, y, format, comparable)}</td></tr>`;
    const html = `<p class="cost-muted">Figures: ${basis}. Delta is the second run minus the first.</p>
      <div class="cost-table-scroll"><table class="cost-compare-stage"><thead><tr><th scope="col">Figure</th><th scope="col">${esc(first.entry.name)}</th><th scope="col">${esc(second.entry.name)}</th><th scope="col">Delta</th></tr></thead><tbody>
      ${line('Elapsed', a.elapsed, b.elapsed, costsDuration)}${line('Stage work', a.work, b.work, costsDuration)}${line('Idle', a.idle, b.idle, costsDuration)}${line('Tokens', a.tokens, b.tokens, costsTokens)}
      ${hidden ? '' : line('Est. cost', a.price.usd, b.price.usd, value => (value === undefined || value === null ? '—' : costsMoney(value)), comparablePrice)}</tbody></table></div>
      ${hidden ? '' : `<p class="cost-muted">${comparablePrice ? '' : 'The estimates are not comparable (partial, hidden or missing): no percentage is shown. '}</p>`}
      <h3>Stages</h3>${costsRewind > 0 ? `<p class="cost-muted">${COSTS_NO_STAGES}</p>` : `<p class="cost-muted">Stage work · tokens${hidden ? '' : ' · est. cost'}, totals since each run started.</p>
      <div class="cost-table-scroll"><table class="cost-compare-stage"><thead><tr><th scope="col">Stage</th><th scope="col">${esc(first.entry.name)}</th><th scope="col">${esc(second.entry.name)}</th></tr></thead><tbody>${costsCompareStages(first, second)}</tbody></table></div>`}`;
    if (html !== costsCompareBody) { costsCompareBody = html; $('compare-body').innerHTML = html; }
  }
  if (!dialog.open) dialog.showModal();
}
function openCostsCompare() {
  const rows = costsSorted(costsRows());
  if (rows.length < 2) return;
  costsCompare = { a: rows[0].entry.key, b: rows[1].entry.key };
  paintCostsCompare();
}
function setCostsRewind(hours) {
  costsRewind = Math.max(0, Math.min(COSTS_REWIND_HOURS, hours));
  costsRewindAt = costsRewind > 0 ? Date.now() - costsRewind * 3600000 : null;
}
function syncCostsWatch() {
  const route = location.hash === '#costs' ? 'costs' : costsOnHome() ? 'home' : 'other';
  // Entering Home refreshes the history at once; the very first entry waits for the first figures, which list the runs to draw.
  if (route !== costsRoute) { costsRoute = route; if (route === 'home' && costsWatching) refreshCostsHome(true); }
  const want = route !== 'other';
  if (want === costsWatching) return;
  costsWatching = want;
  if (isDemo) return;
  nativeAFM.watchCosts(want).catch(showNativeError);
  if (want) nativeAFM.costs().then(view => { costsView = view; paintCosts(true); paintHomeCosts(); refreshCostsHome(true); }).catch(showNativeError);
}
document.addEventListener('workspace-change', syncCostsWatch);
document.addEventListener('click', event => {
  if (event.target.closest('#costs-nav')) { location.hash = 'costs'; return; }
  const link = event.target.closest('[data-cost-link]');
  if (link) { costsUi.project = link.dataset.costLink; save(); location.hash = 'costs'; return; }
  const button = event.target.closest('button');
  if (!button) return;
  const data = button.dataset;
  const set = (key, value) => { costsUi[key] = value; save(); paintCosts(); };
  if (data.costRun) { costsDetail = data.costRun; paintCostsDetail(); }
  else if (data.costOpen) openCostRun(data.costOpen);
  else if (button.id === 'cost-compare') openCostsCompare();
  else if (button.id === 'close-compare') $('run-compare').close();
  else if (button.id === 'cost-asof-now') { setCostsRewind(0); refreshCostsPeriod(true); paintCosts(); }
  else if (data.costMetric) set('metric', data.costMetric);
  else if (data.costStatus) { set('status', data.costStatus); refreshCostsPeriod(true); }
  else if (data.costRange) { costsUi.range = data.costRange; save(); refreshCostsPeriod(true); paintCosts(); }
  else if (data.costPoint !== undefined) { const readout = $('cost-chart-readout'); if (readout) readout.textContent = data.costPoint; }
  else if (data.costGroup) set('group', data.costGroup);
  else if (data.costSort) {
    if (costsUi.sort === data.costSort) costsUi.dir *= -1; else { costsUi.sort = data.costSort; costsUi.dir = -1; }
    save(); paintCosts();
  } else if (button.id === 'cost-price-toggle') {
    const hide = !costsView.hidePrices;
    if (isDemo) { costsView = { ...costsView, hidePrices: hide }; paintCosts(); }
    else nativeAFM.setPrefs({ hidePrices: hide }).catch(showNativeError);
  } else if (button.id === 'close-cost') $('run-cost').close();
});
const costsReadout = event => { const point = event.target.closest?.('[data-cost-point]'); const readout = point && $('cost-chart-readout'); if (readout) readout.textContent = point.dataset.costPoint; };
document.addEventListener('focusin', costsReadout);
document.addEventListener('pointerover', costsReadout);
// Dragging only moves the label; the history is asked once, when the slider is released or a key step ends.
document.addEventListener('pointerdown', event => { if (event.target.id === 'cost-asof') costsSliding = true; });
const costsReleased = () => { if (costsSliding) { costsSliding = false; refreshCostsPeriod(true); paintCosts(); } };
document.addEventListener('pointerup', costsReleased);
document.addEventListener('pointercancel', costsReleased);
document.addEventListener('input', event => {
  if (event.target.id !== 'cost-asof') return;
  setCostsRewind(COSTS_REWIND_HOURS - Number(event.target.value));
  const text = costsRewind > 0 ? costsZoned(costsRewindAt) : 'Now';
  $('cost-asof-label').textContent = text;
  event.target.setAttribute('aria-valuetext', text);
  $('cost-asof-now').disabled = costsRewind === 0;
});
document.addEventListener('change', event => {
  if (event.target.id === 'cost-asof') { refreshCostsPeriod(true); paintCosts(); return; }
  if (event.target.id === 'compare-a' || event.target.id === 'compare-b') {
    costsCompare = { a: $('compare-a').value, b: $('compare-b').value };
    paintCostsCompare();
    return;
  } if (event.target.id === 'cost-project') { costsUi.project = event.target.value; save(); paintCosts(); } });
$('run-cost').addEventListener('close', () => { costsDetail = null; });
$('run-compare').addEventListener('close', () => { costsCompare = null; costsCompareControls = ''; costsCompareBody = ''; });
if (isDemo) { costsView = costsDemoView(); document.addEventListener('workspace-change', () => { paintHomeCosts(); }); }
else if (window.nativeAFM?.onCosts) {
  const stop = nativeAFM.onCosts(view => {
    const flipped = view.hidePrices !== costsView.hidePrices;
    costsView = view;
    paintCosts(true); paintHomeCosts(); refreshCostsHome(flipped);
  });
  addEventListener('pagehide', stop);
}
document.addEventListener('visibilitychange', () => {
  if (document.hidden) return;
  if (costsOnHome()) refreshCostsHome(true);
  else if (location.hash === '#costs' && (costsPeriodMode() || costsFinished())) refreshCostsPeriod(!costsRewindAt);
});
setInterval(tickCosts, 1000);

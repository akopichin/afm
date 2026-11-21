'use strict';

const finishedReportPages = new WeakMap();
const reportOutcomes = { finished: 'Finished', failed: 'Failed', interrupted: 'Interrupted' };
const reportStatus = status => String(status ?? 'Unknown').replaceAll('_', ' ').replace(/^./, char => char.toUpperCase());

function finishedRunReport(run) {
  const existing = finishedReportPages.get(run);
  if (existing) return existing;

  const page = document.createElement('div');
  page.className = 'run-report';
  const outcome = reportOutcomes[run.status] ?? 'Process stopped';
  page.innerHTML = `<article class="run-report-page"><header class="run-report-head">
    <div><h1>${esc(run.flow)}</h1><p class="mono run-report-id" hidden></p></div>
    <span class="run-outcome" data-outcome="${esc(run.status ?? '')}">${outcome}</span>
  </header><div class="run-report-body" role="status"></div></article>`;
  finishedReportPages.set(run, page);
  const body = page.querySelector('.run-report-body');

  async function load() {
    body.setAttribute('role', 'status');
    body.textContent = 'Loading run report…';
    try {
      const report = await nativeAFM.runReport(run.rawId, run.instance);
      const title = report.run.flow || run.flow;
      const id = page.querySelector('.run-report-id');
      page.querySelector('h1').textContent = title;
      id.textContent = run.rawId;
      id.hidden = title === run.rawId;
      const status = page.querySelector('.run-outcome');
      if (reportOutcomes[report.run.status]) {
        status.dataset.outcome = report.run.status;
        status.textContent = reportOutcomes[report.run.status];
      }
      body.removeAttribute('role');
      body.innerHTML = renderFinishedReport(report);
    } catch (error) {
      body.setAttribute('role', 'status');
      body.innerHTML = '<p>Run report unavailable.</p><p class="muted run-report-error"></p><button class="action" type="button">Retry</button>';
      body.querySelector('.run-report-error').textContent = error.message;
      body.querySelector('button').addEventListener('click', load);
    }
  }
  load();
  return page;
}

function reportDate(value) {
  if (!value || value.startsWith('0001-')) return '';
  const date = new Date(value);
  return Number.isFinite(+date) ? date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '';
}

function renderFinishedReport(report) {
  const { run, stages, cost, analytics, diagnostics } = report;
  const done = stages.filter(stage => stage.status === 'done').length;
  const total = cost?.total;
  const agentTime = analytics?.totals?.agent_time;
  const metrics = [
    ['Elapsed', costsDuration(run.ended_at || run.elapsed_ms ? run.elapsed_ms : null)],
    ['Stages done', `${done} / ${stages.length}`],
  ];
  if (agentTime?.duration_ms != null) {
    metrics.push(['Agent time', costsDuration(agentTime.duration_ms) + (agentTime.unknown_jobs ? ' (partial)' : '')]);
  }
  if (total?.tokens) metrics.push(['Tokens', costsTokens(total.tokens.total) + (total.unmetered ? ' (partial)' : '')]);

  const started = reportDate(run.started_at), ended = reportDate(run.ended_at);
  const stageUsage = new Map((cost?.stages ?? []).map(row => [row.stage_id, row]));
  const models = analytics?.models ?? [];
  return `
    <dl class="run-report-metrics">${metrics.map(([label, value]) =>
      `<div><dt>${label}</dt><dd>${esc(value)}</dd></div>`).join('')}</dl>
    ${started || ended ? `<p class="run-report-dates muted">${[
      started && `Started ${esc(started)}`, ended && `Ended ${esc(ended)}`,
    ].filter(Boolean).join(' · ')}</p>` : ''}
    ${stages.length ? `<section class="run-report-section"><h2>Stages</h2><div class="run-report-table">
      <table aria-label="Stages"><thead><tr><th scope="col">Stage</th><th scope="col">Status</th>
        <th scope="col">Work</th><th scope="col">Wait</th><th scope="col">Paused</th>${stageUsage.size ? '<th scope="col">Tokens</th>' : ''}</tr></thead>
        <tbody>${stages.map(({ id, status, timing }) => {
          const wait = timing.approval_ms + timing.helpers_ms + timing.backoff_ms + timing.scheduling_ms;
          const usage = stageUsage.get(id);
          return `<tr><th scope="row">${esc(id)}</th><td data-stage-outcome="${esc(status)}">${esc(reportStatus(status))}</td>
            <td>${costsDuration(timing.work_ms)}</td><td>${costsDuration(Number.isFinite(wait) ? wait : null)}</td>
            <td>${costsDuration(timing.paused_ms)}</td>${stageUsage.size ? `<td>${costsTokens(usage?.tokens?.total)}${usage?.tokens && usage.unmetered ? ' (partial)' : ''}</td>` : ''}</tr>`;
        }).join('')}</tbody></table></div></section>` : ''}
    ${models.length ? `<section class="run-report-section"><h2>Models</h2><div class="run-report-table">
      <table aria-label="Models"><thead><tr><th scope="col">Model</th><th scope="col">Jobs</th><th scope="col">Agent time</th></tr></thead>
      <tbody>${models.map(row => `<tr><th scope="row">${esc(row.label)}</th><td>${esc(row.jobs)}</td>
        <td>${costsDuration(row.agent_time?.duration_ms)}${row.agent_time?.duration_ms != null && row.agent_time.unknown_jobs ? ' (partial)' : ''}</td></tr>`).join('')}</tbody></table></div></section>` : ''}
    ${diagnostics?.length ? `<details class="run-report-section run-report-notes"><summary>Data notes (${diagnostics.length})</summary>
      <ul>${diagnostics.map(item => `<li>${esc(item.message)}</li>`).join('')}</ul></details>` : ''}`;
}

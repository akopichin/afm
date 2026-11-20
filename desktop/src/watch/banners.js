import { TEXT } from '../notifier.js';

// More results than this in one poll become a single banner.
const BANNER_LIMIT = 5;
const short = text => String(text ?? '').replace(/[\u0000-\u001f\u007f]/g, ' ').slice(0, 80);

// Banner for one watch result: fixed text per kind plus names. Question text, artifacts and secrets never get here.
export function resultBanner(task, result) {
  const run = short(result.runName || result.runId);
  const text = TEXT[result.kind];
  const body = result.stageName ? `${run}\nStage ${short(result.stageName)} is waiting for ${text.waiting}.` : `${run}\n${result.kind === 'finished' ? 'The run finished.' : 'The run failed.'}`;
  return { title: text.title, body, runKey: result.stageId ? result.runKey : undefined, stageId: result.stageId ?? undefined, watchTaskId: task.id, itemId: result.id };
}

export function reportBanner(task, report) {
  return { title: 'AFM summary ready', body: short(report.title || task.title), watchTaskId: task.id, itemId: report.id };
}

// Collects the results of one poll and shows them together: a few banners, then one line for the rest.
export function createWatchBanners({ show, visible = () => false }) {
  let batch = [];
  const keys = new Set();
  const flush = () => {
    const shown = batch;
    batch = [];
    keys.clear();
    for (const banner of shown.slice(0, BANNER_LIMIT)) show(banner);
    if (shown.length > BANNER_LIMIT) show({ title: 'AFM watch', body: `${shown.length - BANNER_LIMIT} more results are waiting.` });
  };
  return {
    result(task, result) {
      // A run the user is already looking at needs no banner.
      if (visible(result)) return;
      // Several watches can record the same event; it is announced once.
      if (keys.has(result.key)) return;
      keys.add(result.key);
      if (!batch.length) queueMicrotask(flush);
      batch.push(resultBanner(task, result));
    },
    report(task, report) { show(reportBanner(task, report)); },
  };
}

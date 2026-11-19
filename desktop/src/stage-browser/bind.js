import { readRunStatus } from '../workspace.js';

const ENDED = new Set(['done', 'failed', 'hook_failed']);
const CANDIDATE_LIMIT = 6;

// AFM names the profile after the stage with unsafe characters replaced by "_".
const profileName = id => String(id).replace(/[^A-Za-z0-9.-]/g, '_');

function within(folder, root) {
  return folder === root || folder.startsWith(root.endsWith('/') ? root : root + '/');
}

// The companion is a child of its run's process, so only the run whose process id the host reports can own it.
// A host that reports no id (an older AFM) proves nothing, and then nothing is tied.
function ownsProcess(run, entry) {
  return !Number.isSafeInteger(entry.pid) || run.pid === entry.pid;
}

// Ties a stage browser to the run and stage that own it. The companion only knows its working folder and the
// stage's profile name, so the run must live in that folder and prove, through its own API, that it has this
// stage and has started it. An ambiguous or incomplete answer ties nothing; a tie is never moved to another run.
export function createInspector({ request = fetch } = {}) {
  return async function inspect(entry, runs) {
    const running = runs.filter(run => run.state === 'running');
    const candidates = entry.runKey
      ? runs.filter(run => run.key === entry.runKey)
      : running.filter(run => run.projectRoot && within(entry.cwd, run.projectRoot) && ownsProcess(run, entry));
    // Too many candidates cannot all be checked, so the answer would not be unique.
    if (!entry.runKey && candidates.length > CANDIDATE_LIMIT) return null;
    if (entry.runKey && !candidates.length) return { ended: true };
    if (entry.runKey && candidates[0].state !== 'running') return { ended: true };
    const found = [];
    for (const run of candidates) {
      let status;
      // A run that cannot answer may be the real owner, so another run answering proves nothing.
      try { status = await readRunStatus(run, request); } catch { if (!entry.runKey) return null; continue; }
      const stage = (status.stages ?? []).find(item => profileName(item.id) === entry.stagePart && item.status !== 'pending');
      if (stage) found.push({ runKey: run.key, stageId: stage.id, ended: ENDED.has(stage.status) });
    }
    return found.length === 1 ? found[0] : null;
  };
}

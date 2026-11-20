import { EVENT_KINDS } from '../watch/observer.js';

const NAMES = ['desktop_watch_create', 'desktop_watch_list', 'desktop_watch_pause', 'desktop_watch_resume', 'desktop_watch_stop'];
export const isWatchTool = name => NAMES.includes(name);

const taskId = { type: 'object', properties: { id: { type: 'string', description: 'Watch task id from desktop_watch_list.' } }, required: ['id'] };
const readOnly = { readOnlyHint: true };

// Background watch tools the provider sees next to the AFM tools. They only observe: nothing here starts, stops or changes a run.
export const WATCH_TOOLS = [
  { name: 'desktop_watch_create', annotations: readOnly,
    description: 'Watch AFM runs in the background and report when something needs attention or finishes. Observation only. '
      + 'Give absolute ISO 8601 times with a timezone offset. The watch starts now; earlier events are not reported.',
    inputSchema: { type: 'object', properties: {
      title: { type: 'string', description: 'Short name of the watch.' },
      scope: { type: 'object', description: 'Default is the conversation scope. Use projectId for one project, runId with instanceId for one run, or all for every project (all-projects conversations only).',
        properties: { all: { type: 'boolean' }, projectId: { type: 'string' }, runId: { type: 'string' }, instanceId: { type: 'string' } } },
      events: { type: 'array', items: { type: 'string', enum: EVENT_KINDS }, description: 'Default: all of them.' },
      deadline: { type: 'string', description: 'When the watch ends.' },
      summaryAt: { type: 'string', description: 'When to write a summary report (not after the deadline).' } } } },
  { name: 'desktop_watch_list', annotations: readOnly, description: 'List the background watches with their status and unread results.', inputSchema: { type: 'object', properties: {} } },
  { name: 'desktop_watch_pause', annotations: readOnly, description: 'Pause a watch. Results collected so far are kept.', inputSchema: taskId },
  { name: 'desktop_watch_resume', annotations: readOnly, description: 'Resume a paused watch. Events during the pause are not reported.', inputSchema: taskId },
  { name: 'desktop_watch_stop', annotations: readOnly, description: 'Stop a watch for good. Results collected so far are kept.', inputSchema: taskId },
];

const inside = (inner, outer, belongsTo) => {
  if (outer.kind === 'all') return true;
  if (outer.kind === 'run') return inner.kind === 'run' && inner.runId === outer.runId && inner.instanceId === outer.instanceId;
  return inner.kind === 'project' ? inner.projectId === outer.projectId : inner.kind === 'run' && belongsTo(outer.projectRoot, inner.projectRoot);
};

const brief = task => ({ id: task.id, title: task.title, status: task.status, scope: task.scope, events: task.events,
  deadline: task.deadline, summaryAt: task.summaryAt, results: task.results.length, unread: task.unread });

// Whether a stored watch lies inside the conversation's scope: all sees everything, a project its own project and runs,
// a run only itself. A run watch is placed in its project through `resolveScope`.
async function visibleTo(turnScope, task, { resolveScope, belongsTo }) {
  if (turnScope.kind === 'all') return true;
  const { kind, projectId, runId, instanceId } = task.scope;
  if (kind === 'all') return false;
  if (turnScope.kind === 'run') return kind === 'run' && runId === turnScope.runId && instanceId === turnScope.instanceId;
  if (kind === 'project') return projectId === turnScope.projectId;
  const run = await resolveScope({ runId, instanceId }).catch(() => null);
  return Boolean(run) && belongsTo(turnScope.projectRoot, run.projectRoot);
}

// Runs one watch tool for a turn. `resolveScope` turns a requested scope into a known project or run (or throws), and
// `belongsTo` says whether a project folder holds a run's folder. Everything is checked against the turn's own scope,
// and `authorized` is asked again right before a change, so a reply stopped meanwhile changes nothing.
export async function runWatchTool({ watch, tool, args, turnScope, resolveScope, belongsTo, scopeName, authorized }) {
  const helpers = { resolveScope, belongsTo };
  const stopped = () => Error('This reply was stopped, so nothing was changed.');
  if (tool === 'desktop_watch_list') {
    const tasks = [];
    for (const task of await watch.list()) if (await visibleTo(turnScope, task, helpers)) tasks.push(task);
    return { text: JSON.stringify({ tasks: tasks.map(brief) }) };
  }
  if (tool !== 'desktop_watch_create') {
    // A watch outside this conversation is reported as unknown, so its existence is not revealed.
    const found = (await watch.list()).find(task => task.id === String(args.id ?? ''));
    if (!found || !await visibleTo(turnScope, found, helpers)) throw Error('That watch task no longer exists.');
    if (!authorized()) throw stopped();
    return { text: JSON.stringify({ task: brief(await watch[tool.replace('desktop_watch_', '')](found.id)) }) };
  }
  const wanted = args.scope ?? {};
  const resolved = wanted.runId || wanted.projectId
    ? await resolveScope({ runId: wanted.runId, instanceId: wanted.instanceId, projectId: wanted.projectId })
    : wanted.all === true ? { kind: 'all' } : turnScope;
  if (!inside(resolved, turnScope, belongsTo)) throw Error(`That watch is outside this conversation's scope (${scopeName(turnScope)}).`);
  if (!authorized()) throw stopped();
  const task = await watch.create({ title: args.title, events: args.events, deadline: args.deadline, summaryAt: args.summaryAt,
    scope: { kind: resolved.kind, projectId: resolved.projectId, runId: resolved.runId, instanceId: resolved.instanceId } });
  return { text: JSON.stringify({ task: brief(task) }), card: { taskId: task.id, title: task.title, scope: resolved.kind === 'all' ? 'All projects' : resolved.name,
    events: task.events, deadline: task.deadline, summaryAt: task.summaryAt } };
}

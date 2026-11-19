import { belongsTo } from '../projects.js';

// What an AFM tool does, from the annotations the host publishes. A tool that does not say it only reads is a control tool.
export function toolKind(tool) {
  if (tool.annotations?.readOnlyHint) return 'read';
  return tool.annotations?.destructiveHint ? 'destructive' : 'control';
}

// Per permission mode: allowed outright, needs the user's approval, or refused.
const RULES = {
  read: { read: 'allow', control: 'deny', destructive: 'deny' },
  ask: { read: 'allow', control: 'ask', destructive: 'ask' },
  edit: { read: 'allow', control: 'allow', destructive: 'ask' },
  yolo: { read: 'allow', control: 'allow', destructive: 'allow' },
};

export const modeDecision = (mode, kind) => RULES[mode]?.[kind] ?? 'deny';

// Scope is {kind:'all'} or a project or run the user selected; main process resolves it, the renderer never supplies paths.
export function rowInScope(scope, row) {
  if (scope.kind === 'all') return true;
  if (scope.kind === 'run') return row.run_id === scope.runId && row.instance_id === scope.instanceId;
  return belongsTo(scope.projectRoot, row.project_root);
}

export const filterRuns = (scope, rows) => rows.filter(row => rowInScope(scope, row));

export const scopeLabel = scope => (scope.kind === 'all' ? 'All projects' : scope.name);

// A tool outside the scope is refused with the reason, so the model and the user both see why.
// Anything that does not name a run, and is not a known project-level tool, is refused when scope is narrow.
export function scopeViolation(scope, tool, args, rows) {
  if (scope.kind === 'all' || tool.name === 'afm_runs_list') return null;
  const where = `Scope is ${scopeLabel(scope)}.`;
  if (tool.name === 'afm_run_start') {
    if (scope.kind === 'run') return `${where} Starting a run needs a project scope or all projects.`;
    return args.project === scope.projectId ? null : `${where} That project is outside the scope.`;
  }
  if (tool.name === 'afm_run_attach') return `${where} Attaching a run needs the all-projects scope.`;
  const run = rows.find(row => row.run_id === args.run_id);
  if (!run) return `${where} The run is unknown or outside the scope.`;
  return rowInScope(scope, run) ? null : `${where} That run is outside the scope.`;
}

const hiddenFor = (scope, name) => scope.kind !== 'all' && (name === 'afm_run_attach' || (scope.kind === 'run' && name === 'afm_run_start'));

// Tools offered to the provider at all: control tools disappear in read-only mode.
export function visibleTools(tools, { mode, scope }) {
  return tools.filter(tool => !hiddenFor(scope, tool.name) && modeDecision(mode, toolKind(tool)) !== 'deny');
}

import { SettingsError } from './settings.js';

export const REASONING_LEVELS = { claude: ['low', 'medium', 'high', 'xhigh', 'max'], codex: ['minimal', 'low', 'medium', 'high', 'xhigh'], openai: [] };

const CLAUDE_MODES = { ask: ['default', 'manual'], read: ['plan'], edit: ['acceptEdits'] };

// The first mode name this CLI version lists, since releases renamed some of them.
const claudeMode = (caps, permission) => (CLAUDE_MODES[permission] ?? []).find(name => caps.permissionModes?.includes(name));

// Modes the installed CLI can enforce itself. A mode it cannot enforce is not offered rather than approximated.
export function supportedPermissions(kind, caps) {
  if (kind === 'openai') return ['read'];
  if (kind === 'claude') {
    if (!caps.print) return [];
    return ['ask', 'read', 'edit'].filter(mode => claudeMode(caps, mode)).concat(caps.skipPermissions || caps.permissionModes?.includes('bypassPermissions') ? ['yolo'] : []);
  }
  if (!caps.json || !caps.sandbox) return [];
  return ['read'].concat(caps.config ? ['ask', 'edit'] : [], caps.bypass ? ['yolo'] : []);
}

const toml = value => JSON.stringify(value);

export const PERMISSION_LABELS = { ask: 'Ask before actions', read: 'Read only', edit: 'Allow workspace edits', yolo: 'YOLO · Full access' };

// Turns the user's choices into this CLI's flags. Everything the CLI cannot do is refused, never silently relaxed.
// The prompt itself goes through stdin so it never appears in argv.
export function buildInvocation({ provider, settings, bridge, systemPrompt }) {
  const { capabilities: caps } = provider;
  const { permission, model, reasoning } = settings;
  if (!provider.permissions.includes(permission))
    throw new SettingsError(`${provider.name} cannot enforce "${PERMISSION_LABELS[permission]}" with the installed version. Choose another permission mode.`);
  if (reasoning !== 'default' && !provider.reasoning.includes(reasoning))
    throw new SettingsError(`${provider.name} does not support reasoning level "${reasoning}".`);
  if (model && caps.model === false) throw new SettingsError(`${provider.name} cannot select a model.`);
  const args = [];
  let flags;
  if (provider.kind === 'claude') {
    args.push('-p', '--output-format', 'stream-json', '--verbose', '--include-partial-messages');
    if (!caps.streaming) throw new SettingsError('The installed Claude Code cannot stream partial output.');
    if (model) args.push('--model', model);
    if (reasoning !== 'default') args.push('--effort', reasoning);
    if (permission === 'yolo') {
      flags = caps.skipPermissions ? ['--dangerously-skip-permissions'] : ['--permission-mode', 'bypassPermissions'];
    } else flags = ['--permission-mode', claudeMode(caps, permission)];
    args.push(...flags);
    if (bridge && caps.mcpConfig) {
      args.push('--mcp-config', bridge.configFile, '--strict-mcp-config');
      if (caps.allowedTools) args.push('--allowedTools', 'mcp__afm');
    }
    if (systemPrompt && caps.appendSystemPrompt) args.push('--append-system-prompt', systemPrompt);
  } else {
    args.push('exec', '--json');
    if (caps.skipGitCheck) args.push('--skip-git-repo-check');
    if (model) args.push('-m', model);
    if (reasoning !== 'default') args.push('-c', `model_reasoning_effort=${toml(reasoning)}`);
    flags = {
      ask: ['--sandbox', 'workspace-write', '-c', 'approval_policy="on-request"'],
      read: ['--sandbox', 'read-only'],
      edit: ['--sandbox', 'workspace-write', '-c', 'approval_policy="never"'],
      yolo: ['--dangerously-bypass-approvals-and-sandbox'],
    }[permission];
    args.push(...flags);
    if (bridge && caps.config) {
      args.push('-c', `mcp_servers.afm.command=${toml(bridge.command)}`, '-c', `mcp_servers.afm.args=${toml(bridge.args)}`);
      if (Object.keys(bridge.env).length) args.push('-c', `mcp_servers.afm.env={${Object.entries(bridge.env).map(([k, v]) => `${k}=${toml(v)}`).join(',')}}`);
    }
    args.push('-');
  }
  return { args, effective: { permission, label: PERMISSION_LABELS[permission], model: model || null, reasoning, flags: flags.join(' ') } };
}

// Flags shown to the user for a provider and mode before anything runs.
export function describeMode(provider, permission) {
  if (provider.kind === 'openai') return 'Chat only: no files and no AFM tools';
  try { return buildInvocation({ provider, settings: { permission, model: '', reasoning: 'default' }, bridge: null }).effective.flags; }
  catch (error) { return error.message; }
}

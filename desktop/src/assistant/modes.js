import { isAgentEndpoint } from './deep-agent.js';
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

// Whether this CLI can resume a session and still honour the chosen model, reasoning and permission mode.
// Anything it cannot is not approximated: the turn starts a fresh session instead.
export function canResume(provider, settings) {
  const { capabilities: caps } = provider;
  if (!caps?.resume) return false;
  if (provider.kind !== 'codex') return true;
  // `resume` already means the resume help lists --sandbox and --config (permission, reasoning and the AFM tools need them).
  const flags = caps.resumeFlags ?? {};
  if (settings.model && !flags.model) return false;
  return settings.permission !== 'yolo' || Boolean(flags.bypass);
}

export const PERMISSION_LABELS = { ask: 'Ask before actions', read: 'Read only', edit: 'Allow workspace edits', yolo: 'YOLO · Full access' };

// Turns the user's choices into this CLI's flags. Everything the CLI cannot do is refused, never silently relaxed.
// The prompt itself goes through stdin so it never appears in argv.
// `resume` is the provider's own id of the earlier session to continue, or null for a fresh one.
// `isolated` (Desktop's own replies) has no tools at all (MCP servers and native ones); a CLI that cannot do that is refused.
export function buildInvocation({ provider, settings, bridge, systemPrompt, resume = null, isolated = false, hasImages = false, imageFiles = [] }) {
  const { capabilities: caps } = provider;
  const { permission, model, reasoning } = settings;
  if (!provider.permissions.includes(permission))
    throw new SettingsError(`${provider.name} cannot enforce "${PERMISSION_LABELS[permission]}" with the installed version. Choose another permission mode.`);
  if (reasoning !== 'default' && !provider.reasoning.includes(reasoning))
    throw new SettingsError(`${provider.name} does not support reasoning level "${reasoning}".`);
  if (model && caps.model === false) throw new SettingsError(`${provider.name} cannot select a model.`);
  if (provider.kind === 'acp') {
    if (isolated) throw new SettingsError('This ACP agent cannot run a tool-free report.');
    if (!provider.localCodex && settings.acpMode && !provider.agentModes?.some(item => item.id === settings.acpMode))
      throw new SettingsError('This ACP agent does not offer the selected mode.');
    return { effective: { permission, label: provider.localCodex ? PERMISSION_LABELS[permission] : 'AFM · Ask before actions', flags: 'ACP agent permissions apply to its own tools',
      model: model || null, reasoning } };
  }
  const args = [];
  let flags;
  if (provider.kind === 'claude') {
    args.push('-p', '--output-format', 'stream-json', '--verbose', '--include-partial-messages');
    if (hasImages) args.push('--input-format', 'stream-json');
    if (!caps.streaming) throw new SettingsError('The installed Claude Code cannot stream partial output.');
    if (model) args.push('--model', model);
    if (reasoning !== 'default') args.push('--effort', reasoning);
    if (permission === 'yolo') {
      flags = caps.skipPermissions ? ['--dangerously-skip-permissions'] : ['--permission-mode', 'bypassPermissions'];
    } else flags = ['--permission-mode', claudeMode(caps, permission)];
    args.push(...flags);
    if (resume) args.push('--resume', resume);
    if (bridge && caps.mcpConfig) {
      args.push('--mcp-config', bridge.configFile, '--strict-mcp-config');
      if (caps.allowedTools) args.push('--allowedTools', 'mcp__afm');
    }
    else if (isolated) {
      // A report must not read files or run commands; if the CLI cannot switch its tools off, no model writes it.
      if (!caps.mcpConfig || !caps.tools) throw new SettingsError('The installed Claude Code cannot disable its tools for a report.');
      args.push('--strict-mcp-config', '--tools', '');
    }
    if (systemPrompt && caps.appendSystemPrompt) args.push('--append-system-prompt', systemPrompt);
  } else {
    // Codex's read-only sandbox does not switch off native reads or user-configured MCP servers.
    if (isolated) throw new SettingsError(`${provider.name} cannot disable its tools for a report.`);
    if (resume && !canResume(provider, settings)) throw new SettingsError(`${provider.name} cannot resume a session with these settings.`);
    const accepted = resume ? { ...caps, ...caps.resumeFlags } : caps;
    args.push('exec');
    if (resume) args.push('resume');
    args.push('--json');
    if (accepted.skipGitCheck) args.push('--skip-git-repo-check');
    if (model) args.push('-m', model);
    if (reasoning !== 'default') args.push('-c', `model_reasoning_effort=${toml(reasoning)}`);
    flags = {
      ask: ['--sandbox', 'workspace-write', '-c', 'approval_policy="on-request"'],
      read: ['--sandbox', 'read-only'],
      edit: ['--sandbox', 'workspace-write', '-c', 'approval_policy="never"'],
      yolo: ['--dangerously-bypass-approvals-and-sandbox'],
    }[permission];
    args.push(...flags);
    if (bridge && accepted.config) {
      args.push('-c', `mcp_servers.afm.command=${toml(bridge.command)}`, '-c', `mcp_servers.afm.args=${toml(bridge.args)}`);
      if (Object.keys(bridge.env).length) args.push('-c', `mcp_servers.afm.env={${Object.entries(bridge.env).map(([k, v]) => `${k}=${toml(v)}`).join(',')}}`);
    }
    for (const file of imageFiles) args.push(`--image=${file}`);
    if (resume) args.push(resume);
    args.push('-');
  }
  return { args, effective: { permission, label: PERMISSION_LABELS[permission], model: model || null, reasoning, flags: flags.join(' ') } };
}

// Flags shown to the user for a provider and mode before anything runs.
export function describeMode(provider, permission) {
  if (isAgentEndpoint(provider.kind)) return PERMISSION_LABELS[permission];
  if (provider.kind === 'openai') return 'Chat only: no files and no AFM tools';
  try { return buildInvocation({ provider, settings: { permission, model: '', reasoning: 'default' }, bridge: null }).effective.flags; }
  catch (error) { return error.message; }
}

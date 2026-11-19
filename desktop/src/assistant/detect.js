import { execFile } from 'node:child_process';
import { access, stat } from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';
import { REASONING_LEVELS, supportedPermissions } from './modes.js';

// A Finder-launched app gets a minimal PATH, so the usual install locations are searched as well.
export function searchDirs({ home, env = process.env, loginPath = '', systemDirs = ['/opt/homebrew/bin', '/usr/local/bin', '/usr/bin', '/bin'] }) {
  const wellKnown = ['.local/bin', '.claude/local', '.npm-global/bin', '.bun/bin', 'homebrew/bin', '.volta/bin', 'bin'].map(dir => path.join(home, dir)).concat(systemDirs);
  const all = [...(env.PATH ?? '').split(path.delimiter), ...loginPath.split(path.delimiter), ...wellKnown].filter(dir => path.isAbsolute(dir));
  return [...new Set(all)];
}

export async function findExecutable(name, dirs) {
  const file = process.platform === 'win32' && !name.endsWith('.exe') ? name + '.exe' : name;
  if (path.isAbsolute(file)) return access(file, constants.X_OK).then(() => file, () => null);
  for (const dir of dirs) {
    const candidate = path.join(dir, file);
    if (await access(candidate, constants.X_OK).then(() => true, () => false)) return candidate;
  }
  return null;
}

// Runs a short probe such as --help; a hung CLI is killed instead of blocking detection.
export function probe(executable, args, { env, timeout = 4000 } = {}) {
  return new Promise(resolve => {
    const child = execFile(executable, args, { env, timeout, maxBuffer: 1 << 20, windowsHide: true, killSignal: 'SIGKILL' },
      (error, stdout, stderr) => resolve({ code: error ? (typeof error.code === 'number' ? error.code : 1) : 0, stdout: String(stdout), stderr: String(stderr) }));
    child.stdin?.end();
  });
}

const hasFlag = (help, flag) => new RegExp(`(^|[\\s,])${flag}(?=[\\s,=<\\[]|$)`, 'm').test(help);

// The text after a flag up to the next flag, where option choices are listed.
function flagSection(help, flag) {
  const start = help.search(new RegExp(`(^|[\\s,])${flag}(?=[\\s,=<\\[]|$)`, 'm'));
  if (start < 0) return '';
  const rest = help.slice(start + flag.length + 1);
  const end = rest.search(/\n\s+(-\w, )?--\w/);
  return end < 0 ? rest : rest.slice(0, end);
}

// What the installed Claude Code CLI accepts, read from its own help text.
export function claudeCapabilities(help) {
  const modes = [...flagSection(help, '--permission-mode').matchAll(/"([A-Za-z]+)"/g)].map(match => match[1]);
  const effort = /\(([\w, ]+)\)/.exec(flagSection(help, '--effort'));
  return {
    print: hasFlag(help, '-p') || hasFlag(help, '--print'),
    streaming: help.includes('stream-json') && hasFlag(help, '--include-partial-messages'),
    permissionModes: modes,
    skipPermissions: hasFlag(help, '--dangerously-skip-permissions'),
    model: hasFlag(help, '--model'),
    effort: hasFlag(help, '--effort') ? (effort ? effort[1].split(',').map(item => item.trim()) : REASONING_LEVELS.claude.slice()) : null,
    mcpConfig: hasFlag(help, '--mcp-config') && hasFlag(help, '--strict-mcp-config'),
    allowedTools: hasFlag(help, '--allowedTools') || hasFlag(help, '--allowed-tools'),
    appendSystemPrompt: hasFlag(help, '--append-system-prompt'),
  };
}

export function codexCapabilities(help) {
  return {
    json: hasFlag(help, '--json'),
    sandbox: hasFlag(help, '--sandbox'),
    bypass: hasFlag(help, '--dangerously-bypass-approvals-and-sandbox'),
    config: hasFlag(help, '--config') || hasFlag(help, '-c'),
    model: hasFlag(help, '--model') || hasFlag(help, '-m'),
    skipGitCheck: hasFlag(help, '--skip-git-repo-check'),
  };
}

const ready = (via, detail) => ({ state: 'ready', via, detail });
const missing = detail => ({ state: 'missing', via: null, detail });

// Presence of a login is enough: token values are never read into Desktop's own state.
async function cliAuth(provider, executable, env, home) {
  const exists = file => stat(path.join(home, file)).then(() => true, () => false);
  if (provider.kind === 'claude') {
    if (env.CLAUDE_CODE_OAUTH_TOKEN) return ready('env', 'CLAUDE_CODE_OAUTH_TOKEN');
    if (env.ANTHROPIC_API_KEY) return ready('env', 'ANTHROPIC_API_KEY');
    const status = await probe(executable, ['auth', 'status'], { env });
    try { if (JSON.parse(status.stdout).loggedIn) return ready('cli-login', 'Existing Claude login'); } catch { /* Older CLI without auth status. */ }
    return await exists('.claude/.credentials.json') ? ready('cli-login', 'Existing Claude login') : missing('Run `claude` once to sign in, or set CLAUDE_CODE_OAUTH_TOKEN.');
  }
  if (env.OPENAI_API_KEY) return ready('env', 'OPENAI_API_KEY');
  const status = await probe(executable, ['login', 'status'], { env });
  if (status.code === 0 && !/not logged in/i.test(status.stdout + status.stderr)) return ready('cli-login', 'Existing Codex login');
  return await exists('.codex/auth.json') ? ready('cli-login', 'Existing Codex login') : missing('Run `codex login`, or set OPENAI_API_KEY.');
}

async function referencedAuth(reference, env) {
  if (reference.type === 'env') return env[reference.ref] ? ready('env', reference.ref) : missing(`Environment variable ${reference.ref} is not set.`);
  const found = await stat(reference.ref).then(file => file.isFile(), () => false);
  return found ? ready('file', reference.ref) : missing(`Key file ${reference.ref} is missing.`);
}

// One provider as the UI shows it: where it is, what it can do and whether it can sign in.
export async function detectProvider(provider, { home, env, dirs }) {
  const base = { id: provider.id, name: provider.name, kind: provider.kind, builtIn: !provider.credential, credential: provider.credential ?? { type: 'cli' },
    reasoning: REASONING_LEVELS[provider.kind] };
  if (provider.kind === 'openai') {
    const auth = await referencedAuth(provider.credential, env);
    return { ...base, endpoint: provider.endpoint, found: true, capabilities: {}, permissions: supportedPermissions('openai', {}), auth };
  }
  const executable = await findExecutable(provider.executable, dirs);
  if (!executable) return { ...base, found: false, capabilities: {}, permissions: [], auth: missing(`${provider.name} was not found. Install it or set its executable path.`) };
  const [version, help] = await Promise.all([
    probe(executable, ['--version'], { env }),
    probe(executable, provider.kind === 'codex' ? ['exec', '--help'] : ['--help'], { env }),
  ]);
  const capabilities = provider.kind === 'codex' ? codexCapabilities(help.stdout + help.stderr) : claudeCapabilities(help.stdout + help.stderr);
  const auth = provider.credential && provider.credential.type !== 'cli' ? await referencedAuth(provider.credential, env) : await cliAuth(provider, executable, env, home);
  // Reasoning is offered only when the CLI has a way to set it.
  const levels = provider.kind === 'claude' ? capabilities.effort ?? [] : capabilities.config ? REASONING_LEVELS.codex : [];
  return { ...base, executable, version: version.stdout.trim().split('\n')[0] || null, found: true, capabilities,
    permissions: supportedPermissions(provider.kind, capabilities), reasoning: levels, auth };
}

// The PATH the user's login shell would have, which a Finder-launched app does not inherit. Empty when it cannot be read.
export async function loginShellPath(env = process.env) {
  if (process.platform === 'win32' || !env.SHELL) return '';
  const result = await probe(env.SHELL, ['-ilc', 'printf "%s" "$PATH"'], { env, timeout: 3000 });
  return result.code === 0 ? result.stdout.trim().split('\n').pop() : '';
}

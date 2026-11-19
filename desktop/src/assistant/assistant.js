import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { desktopDir, Mutex, readJSON, writeJSON } from '../files.js';
import { ApprovalServer } from './approval.js';
import { detectProvider, searchDirs } from './detect.js';
import { runEndpoint } from './endpoint.js';
import { buildInvocation, describeMode, PERMISSION_LABELS } from './modes.js';
import { scopeLabel } from './policy.js';
import { runProcess } from './run.js';
import { AssistantSettings, BUILT_IN, SettingsError } from './settings.js';
import { parserFor } from './stream.js';

const MAX_MESSAGES = 60;
const MAX_TEXT = 8000;
// Text deltas reach the window in batches, so a fast model does not cost one IPC message per token.
const DELTA_FLUSH_MS = 40;
const MAX_TOOLS = 20;
const PROMPT_BUDGET = 24000;
const DETECTION_TTL_MS = 5 * 60 * 1000;
const APPROVAL_TIMEOUT_MS = 10 * 60 * 1000;
const SECRET_NAME = /TOKEN|KEY|SECRET|PASSWORD|CREDENTIAL/i;

export class AssistantError extends Error {
  constructor(code, message) { super(message); this.code = code; }
}

const clip = (text, limit) => (text.length > limit ? text.slice(0, limit) + '…' : text);

// Removes the values of secret-looking environment variables from text that may be shown to the user.
// `add` registers a credential resolved later, so it is hidden from everything the provider prints.
function redactor(env) {
  const secrets = Object.entries(env).filter(([name, value]) => SECRET_NAME.test(name) && value?.length >= 8).map(([, value]) => value);
  const redact = text => typeof text === 'string' ? secrets.reduce((out, secret) => out.split(secret).join('[hidden]'), text) : text;
  redact.add = value => { if (value?.length >= 8) secrets.push(value); };
  return redact;
}

function redactInput(redact, input) {
  if (input === undefined) return input;
  try { return JSON.parse(redact(JSON.stringify(input))); } catch { return '[hidden]'; }
}

// The Desktop-wide assistant. `context` is the app's view of AFM: {endpoint(), runs(), project(id)}.
// `bridge` says how a provider starts the AFM tool bridge: {command, script, env}.
export class Assistant {
  constructor({ home, context, bridge, env = process.env, loginPath = async () => '', systemDirs, maxActive = 3, grace }) {
    Object.assign(this, { home, context, bridge, baseEnv: env, loginPath, systemDirs, maxActive, grace });
    this.settings = new AssistantSettings(home);
    this.historyFile = path.join(desktopDir(home), 'assistant-history.json');
    this.historyLock = new Mutex();
    this.listeners = new Set();
    this.detections = new Map();
    this.active = new Map();
    this.approvals = new ApprovalServer(request => this.#askUser(request));
    this.pending = new Map();
  }

  onEvent(listener) { this.listeners.add(listener); return () => this.listeners.delete(listener); }
  #emit(event) {
    // Anything else about a reply must not overtake the text that came before it.
    if (event.type !== 'delta') this.#flushDelta(this.active.get(event.conversationId));
    for (const listener of this.listeners) listener(event);
  }

  #queueDelta(turn, text) {
    turn.pendingDelta = (turn.pendingDelta ?? '') + text;
    turn.deltaTimer ??= setTimeout(() => this.#flushDelta(turn), DELTA_FLUSH_MS);
  }

  #flushDelta(turn) {
    if (!turn) return;
    clearTimeout(turn.deltaTimer);
    turn.deltaTimer = null;
    if (!turn.pendingDelta) return;
    const text = turn.pendingDelta;
    turn.pendingDelta = '';
    this.#emit({ conversationId: turn.conversationId, turnId: turn.turnId, type: 'delta', text });
  }

  async #definitions() {
    const settings = await this.settings.get();
    return { settings, definitions: [...BUILT_IN, ...settings.providers] };
  }

  async #environment() {
    const dirs = searchDirs({ home: this.home, env: this.baseEnv, loginPath: await this.loginPath(), systemDirs: this.systemDirs });
    const env = { ...this.baseEnv, PATH: dirs.join(path.delimiter) };
    delete env.ELECTRON_RUN_AS_NODE;
    return { env, dirs };
  }

  async #detect(definition) {
    const { env, dirs } = await this.#environment();
    const info = await detectProvider(definition, { home: this.home, env, dirs });
    this.detections.set(definition.id, { at: Date.now(), info });
    return info;
  }

  async #provider(id, { fresh = false } = {}) {
    const { definitions } = await this.#definitions();
    const definition = definitions.find(item => item.id === id);
    if (!definition) throw new AssistantError('unknown_provider', 'The selected provider no longer exists. Choose another one.');
    const cached = this.detections.get(id);
    return !fresh && cached && Date.now() - cached.at < DETECTION_TTL_MS ? cached.info : this.#detect(definition);
  }

  // Settings and whatever has been detected so far. Detection itself runs on scan(), never here.
  async state() {
    const { settings, definitions } = await this.#definitions();
    const providers = definitions.map(definition => this.detections.get(definition.id)?.info ?? {
      id: definition.id, name: definition.name, kind: definition.kind, builtIn: !definition.credential, credential: definition.credential ?? { type: 'cli' },
      endpoint: definition.endpoint, found: null, permissions: Object.keys(PERMISSION_LABELS), reasoning: [], capabilities: {}, auth: { state: 'unknown', via: null, detail: 'Not checked yet.' },
    });
    const selected = providers.find(item => item.id === settings.provider);
    const effective = selected ? { provider: selected.name, permission: settings.permission, label: PERMISSION_LABELS[settings.permission], flags: describeMode(selected, settings.permission) } : null;
    return { settings, providers: providers.map(item => ({ ...item, modes: Object.fromEntries(item.found === null ? [] : item.permissions.map(mode => [mode, describeMode(item, mode)])) })), effective, scanned: this.detections.size > 0 };
  }

  async scan() {
    const { definitions } = await this.#definitions();
    await Promise.all(definitions.map(definition => this.#detect(definition)));
    return this.state();
  }

  async configure(changes) { await this.settings.select(changes); return this.state(); }

  async addProvider(input) {
    await this.settings.addProvider(input, 'custom-' + randomUUID().slice(0, 8));
    return this.scan();
  }

  async removeProvider(id) { await this.settings.removeProvider(id); this.detections.delete(id); return this.state(); }

  // Setup check without a model call: the executable, what it supports and whether its credential resolves.
  async testProvider(id) {
    const info = await this.#provider(id, { fresh: true });
    return { id, found: info.found, version: info.version ?? null, permissions: info.permissions, auth: info.auth, ok: info.found && info.auth.state === 'ready' };
  }

  // ---- conversations ----

  async history(conversationId = 'main') { return (await readJSON(this.historyFile, { conversations: {} })).conversations[conversationId] ?? []; }

  #saveMessages(conversationId, change) {
    return this.historyLock.run(async () => {
      const all = await readJSON(this.historyFile, { conversations: {} });
      const next = change(all.conversations[conversationId] ?? []).slice(-MAX_MESSAGES);
      await writeJSON(this.historyFile, { conversations: { ...all.conversations, [conversationId]: next } });
      return next;
    });
  }

  async clear(conversationId = 'main') {
    if (this.active.has(conversationId)) throw new AssistantError('busy', 'Stop the running reply before clearing the chat.');
    await this.#saveMessages(conversationId, () => []);
  }

  async #resolveScope(input) {
    if (input?.runId) {
      const row = (await this.context.runs()).find(item => item.run_id === input.runId && item.instance_id === input.instanceId);
      if (!row) throw new AssistantError('scope_unknown', 'The selected run is no longer known to the AFM host.');
      return { kind: 'run', runId: row.run_id, instanceId: row.instance_id, projectRoot: row.project_root, name: row.run_name || row.run_id };
    }
    if (input?.projectId) {
      const project = await this.context.project(input.projectId).catch(() => null);
      if (!project) throw new AssistantError('scope_unknown', 'The selected project is not registered with Desktop.');
      return { kind: 'project', projectId: project.id, projectRoot: project.path, name: project.name };
    }
    return { kind: 'all' };
  }

  // Older turns are dropped first; the provider gets only what fits the budget.
  #transcript(messages, text) {
    const lines = messages.filter(item => item.status !== 'error' && item.text).map(item => `${item.role === 'user' ? 'User' : 'Assistant'}: ${clip(item.text, 2000)}`);
    let used = text.length;
    const kept = [];
    for (const line of lines.reverse()) {
      if (used + line.length > PROMPT_BUDGET) break;
      used += line.length;
      kept.unshift(line);
    }
    return kept;
  }

  #systemPrompt(scope, effective, hasTools) {
    return [`You are the AFM Desktop assistant, working for the user on their machine.`,
      `Scope: ${scopeLabel(scope)}${scope.kind === 'all' ? '' : ` (${scope.projectRoot})`}. Permission mode: ${effective.label}.`,
      hasTools ? 'Use the afm_* tools to inspect or control AFM runs. They only work inside the scope and mode above; report any tool error to the user as it is.'
        : 'AFM tools are not available in this conversation; say so if the user asks you to inspect or control runs.',
      'Answer briefly in the user\'s language.'].join('\n');
  }

  // Starts a reply and returns at once; progress arrives through onEvent.
  async send({ conversationId = 'main', text, scope: scopeInput } = {}) {
    text = String(text ?? '').trim();
    if (!text) throw new AssistantError('invalid', 'Write a message first.');
    if (text.length > MAX_TEXT) throw new AssistantError('invalid', `Messages are limited to ${MAX_TEXT} characters.`);
    if (this.active.has(conversationId)) throw new AssistantError('busy', 'The assistant is still answering. Stop it or wait.');
    if (this.active.size >= this.maxActive) throw new AssistantError('too_many', 'Too many replies are running at once.');
    const turn = { turnId: randomUUID(), conversationId, cancelled: false };
    this.active.set(conversationId, turn);
    try {
      const settings = await this.settings.get();
      const provider = await this.#provider(settings.provider);
      if (!provider.found) throw new AssistantError('provider_missing', provider.auth.detail);
      if (provider.auth.state !== 'ready') throw new AssistantError('missing_credentials', `${provider.name} is not signed in. ${provider.auth.detail}`);
      const scope = await this.#resolveScope(scopeInput);
      const prior = await this.history(conversationId);
      Object.assign(turn, { settings, provider, scope, prior, text });
      const effective = provider.kind === 'openai' ? { permission: 'read', label: PERMISSION_LABELS.read, flags: 'chat only', model: settings.model || null, reasoning: 'default' } : this.#invocation(turn).effective;
      turn.effective = effective;
    } catch (error) { this.active.delete(conversationId); throw error; }
    const user = { id: randomUUID(), role: 'user', text, context: scopeLabel(turn.scope), at: new Date().toISOString() };
    turn.reply = { id: randomUUID(), role: 'assistant', text: '', context: user.context, status: 'streaming', tools: [], effective: { provider: turn.provider.name, ...turn.effective }, at: user.at };
    await this.#saveMessages(conversationId, messages => [...messages, user]);
    this.#emit({ type: 'start', conversationId, turnId: turn.turnId, message: user, reply: turn.reply });
    turn.finished = this.#run(turn).catch(error => this.#finish(turn, 'error', error.message));
    return { turnId: turn.turnId, effective: turn.effective, scope: scopeLabel(turn.scope) };
  }

  #invocation(turn, bridge = null) {
    return buildInvocation({ provider: turn.provider, settings: turn.settings, bridge, systemPrompt: this.#systemPrompt(turn.scope, { label: PERMISSION_LABELS[turn.settings.permission] }, Boolean(bridge)) });
  }

  async #run(turn) {
    const { provider, settings, scope, prior, text } = turn;
    const { env } = await this.#environment();
    const redact = redactor(env);
    turn.redact = redact;
    const onEvent = event => this.#onProviderEvent(turn, event, redact);
    let tempDir;
    try {
      if (provider.kind === 'openai') {
        const messages = [{ role: 'system', content: this.#systemPrompt(scope, { label: PERMISSION_LABELS.read }, false) },
          ...this.#transcript(prior, text).map(line => ({ role: line.startsWith('User: ') ? 'user' : 'assistant', content: line.replace(/^(User|Assistant): /, '') })), { role: 'user', content: text }];
        if (!settings.model) throw new AssistantError('model_required', 'Choose a model for this endpoint first.');
        const apiKey = await this.#secret(provider);
        redact.add(apiKey);
        if (turn.cancelled) return this.#finish(turn, 'cancelled');
        turn.process = runEndpoint({ endpoint: provider.endpoint, apiKey, model: settings.model, messages, onEvent });
      } else {
        tempDir = await mkdtemp(path.join(os.tmpdir(), 'afm-turn-'));
        const bridge = await this.#prepareBridge(turn, tempDir);
        const { args } = this.#invocation(turn, bridge);
        const cwd = scope.kind === 'all' ? this.home : scope.projectRoot;
        const prompt = [...this.#transcript(prior, text), `User: ${text}`].join('\n\n');
        const credential = await this.#providerCredential(provider);
        Object.values(credential).forEach(redact.add);
        Object.assign(env, credential);
        if (turn.cancelled) return this.#finish(turn, 'cancelled');
        turn.process = runProcess({ command: provider.executable, args, cwd, env, input: prompt, parser: parserFor(provider.kind), onEvent, grace: this.grace });
      }
      const outcome = await turn.process.done;
      if (turn.cancelled) return this.#finish(turn, 'cancelled');
      if (outcome.error) return this.#finish(turn, 'error', redact(`${provider.name} could not run: ${outcome.error.message}`));
      if (turn.failure) return this.#finish(turn, 'error', turn.failure);
      if (outcome.code) return this.#finish(turn, 'error', redact(`${provider.name} exited with code ${outcome.code}.${outcome.stderr ? ' ' + clip(outcome.stderr.trim(), 400) : ''}`));
      if (!turn.reply.text && !turn.reply.tools.length) return this.#finish(turn, 'error', `${provider.name} ended without a reply.`);
      return this.#finish(turn, 'done');
    } catch (error) {
      return this.#finish(turn, 'error', redact(error.message));
    } finally {
      if (tempDir) await rm(tempDir, { recursive: true, force: true });
    }
  }

  // The tool bridge is a separate stdio server per turn, bound to this turn's scope and permission mode.
  async #prepareBridge(turn, dir) {
    let endpoint;
    try { endpoint = await this.context.endpoint(); }
    catch (error) {
      this.#emit({ type: 'notice', conversationId: turn.conversationId, turnId: turn.turnId, text: 'AFM host is unavailable, so AFM tools are off for this reply.' });
      return null;
    }
    const policyFile = path.join(dir, 'policy.json');
    const policy = { endpoint, mode: turn.settings.permission, scope: turn.scope, turnId: turn.turnId, approvalSocket: await this.approvals.listen() };
    await writeFile(policyFile, JSON.stringify(policy), { mode: 0o600 });
    const spec = { command: this.bridge.command, args: [this.bridge.script, '--policy', policyFile], env: this.bridge.env ?? {} };
    const configFile = path.join(dir, 'mcp.json');
    await writeFile(configFile, JSON.stringify({ mcpServers: { afm: { type: 'stdio', ...spec } } }), { mode: 0o600 });
    return { ...spec, configFile };
  }

  // A key file or environment variable the user configured is read here and passed to the provider's environment
  // under the variable that provider's CLI expects; it beats a standard variable already present.
  async #providerCredential(provider) {
    const { type } = provider.credential;
    if (type !== 'file' && type !== 'env') return {};
    const value = await this.#secret(provider);
    return value ? { [provider.kind === 'codex' ? 'OPENAI_API_KEY' : 'ANTHROPIC_API_KEY']: value } : {};
  }

  async #secret(provider) {
    if (provider.credential.type === 'file') return (await readFile(provider.credential.ref, 'utf8')).trim();
    return this.baseEnv[provider.credential.ref] ?? '';
  }

  #onProviderEvent(turn, event, redact) {
    const base = { conversationId: turn.conversationId, turnId: turn.turnId };
    const { reply } = turn;
    if (turn.cancelled) return;
    if (event.type === 'delta') {
      const text = redact(event.text);
      reply.text += text;
      this.#queueDelta(turn, text);
    } else if (event.type === 'tool') {
      if (reply.tools.length < MAX_TOOLS) reply.tools.push({ id: event.id, name: event.name, input: redactInput(redact, event.input), status: 'running' });
      this.#emit({ ...base, type: 'tool', tool: { id: event.id, name: event.name, input: redactInput(redact, event.input) } });
    } else if (event.type === 'tool_result') {
      const tool = reply.tools.find(item => item.id === event.id);
      if (tool) Object.assign(tool, { status: event.error ? 'error' : 'done', text: redact(event.text) });
      this.#emit({ ...base, type: 'tool_result', id: event.id, error: event.error, text: redact(event.text) });
    } else if (event.type === 'result') {
      if (!event.ok) turn.failure = redact(event.error || 'The provider reported an error.');
      else if (!reply.text && event.text) this.#onProviderEvent(turn, { type: 'delta', text: event.text }, redact);
    }
  }

  async #finish(turn, status, message) {
    if (turn.settled) return;
    turn.settled = true;
    this.#flushDelta(turn);
    this.#denyApprovals(turn.turnId);
    const { reply } = turn;
    Object.assign(reply, { status, error: message ?? null, text: clip(turn.redact?.(reply.text) ?? reply.text, MAX_TEXT * 4) });
    await this.#saveMessages(turn.conversationId, messages => [...messages, reply]);
    this.active.delete(turn.conversationId);
    this.#emit({ type: status === 'done' ? 'done' : status, conversationId: turn.conversationId, turnId: turn.turnId, message: status === 'error' ? message : undefined, reply });
  }

  cancel(conversationId = 'main') {
    const turn = this.active.get(conversationId);
    if (!turn) return false;
    turn.cancelled = true;
    this.#denyApprovals(turn.turnId);
    // Before the process exists, #run notices the flag and finishes the turn itself.
    turn.process?.cancel();
    return true;
  }

  // ---- approvals ----

  #askUser({ turnId, tool, kind, scope, summary }) {
    const turn = [...this.active.values()].find(item => item.turnId === turnId);
    if (!turn || turn.cancelled) return false;
    const approvalId = randomUUID();
    return new Promise(resolve => {
      const timer = setTimeout(() => this.approve(approvalId, false), APPROVAL_TIMEOUT_MS);
      this.pending.set(approvalId, { turnId, conversationId: turn.conversationId, resolve: value => { clearTimeout(timer); resolve(value); } });
      this.#emit({ type: 'approval', conversationId: turn.conversationId, turnId, approvalId, tool, kind, scope, summary });
    });
  }

  approve(approvalId, approved) {
    const pending = this.pending.get(approvalId);
    if (!pending) return false;
    this.pending.delete(approvalId);
    pending.resolve(approved === true);
    this.#emit({ type: 'approval_done', conversationId: pending.conversationId, turnId: pending.turnId, approvalId, approved: approved === true });
    return true;
  }

  #denyApprovals(turnId) {
    for (const [id, pending] of this.pending) if (pending.turnId === turnId) this.approve(id, false);
  }

  async close() {
    const turns = [...this.active.values()];
    for (const turn of turns) this.cancel(turn.conversationId);
    await Promise.all(turns.map(turn => turn.finished));
    this.approvals.close();
  }
}

export { SettingsError };

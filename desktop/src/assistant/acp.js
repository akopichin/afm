import { connectRemoteAcp } from './remote-acp.js';
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Readable, Writable } from 'node:stream';
import { ClientApp, ndJsonStream, PROTOCOL_VERSION } from '@agentclientprotocol/sdk';

const setup = () => ({ signal: AbortSignal.timeout(12000) });
const cancelled = { outcome: { outcome: 'cancelled' } };
const optionValues = option => option?.options?.flatMap(item => item.options ?? [item]) ?? [];
const selector = (options, category) => options?.find(option => option.type === 'select' && option.category === category);

function childEnvironment(env, credential) {
  const names = ['PATH', 'HOME', 'USER', 'LOGNAME', 'TMPDIR', 'TMP', 'TEMP', 'SHELL', 'LANG', 'TERM',
    'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_CACHE_HOME', 'CODEX_HOME'];
  if (credential?.type === 'env') names.push(credential.ref);
  return Object.fromEntries(names.filter(name => env[name] !== undefined).map(name => [name, env[name]]));
}

export const codexAcpModes = { read: 'read-only', ask: 'read-only', edit: 'workspace-write', yolo: 'agent-full-access' };

function launch({ executable, args, credential, localCodex, endpoint, remoteCwd }, { env, cwd, onUpdate, approve, token, bridge, confirmWorkspace }) {
  const app = new ClientApp();
  app.onNotification('session/update', ({ params: { sessionId, update } }) => onUpdate?.(sessionId, update));
  app.onRequest('session/request_permission', ({ params }) => approve?.(params) ?? cancelled);
  if (endpoint) return connectRemoteAcp(app, { endpoint, remoteCwd }, token, confirmWorkspace);
  const posix = process.platform !== 'win32';
  const childEnv = childEnvironment(env, credential);
  if (localCodex) {
    Object.assign(childEnv, { CODEX_PATH: localCodex, ELECTRON_RUN_AS_NODE: '1' });
    // The Desktop bridge applies scope and asks for approval itself.
    if (bridge) childEnv.CODEX_CONFIG = JSON.stringify({ mcp_servers: { afm: {
      command: bridge.command, args: bridge.args, env: bridge.env, default_tools_approval_mode: 'approve',
    } } });
  }
  const child = spawn(executable, args, { cwd, env: childEnv, stdio: ['pipe', 'pipe', 'ignore'], detached: posix, windowsHide: true });
  child.on('error', () => {});
  child.stdin.on('error', () => {});
  const exited = new Promise(resolve => child.once('exit', resolve).once('error', resolve));
  const stream = ndJsonStream(Writable.toWeb(child.stdin), Readable.toWeb(child.stdout));
  const connection = app.connect(stream);
  child.once('exit', () => connection.close());
  const signal = (name, group) => {
    if (!child.pid) return;
    try { process.kill(posix && group ? -child.pid : child.pid, name); } catch { /* Already exited. */ }
  };
  let stopped;
  const stop = (group = true) => (stopped ??= (async () => {
    connection.close();
    signal('SIGTERM', group);
    const timer = setTimeout(() => signal('SIGKILL', group), 1000);
    try { await exited; } finally { clearTimeout(timer); }
  })());
  return { agent: connection.agent, stop };
}

async function initialize(agent) {
  const info = await agent.request('initialize', { protocolVersion: PROTOCOL_VERSION, clientCapabilities: {},
    clientInfo: { name: 'afm-desktop', version: '1' } }, setup());
  if (info.protocolVersion !== PROTOCOL_VERSION) throw Error('The agent did not accept ACP v1.');
  return info;
}

export async function probeAcp(provider, env, token, { confirmWorkspace = false } = {}) {
  const cwd = provider.remoteCwd ?? await mkdtemp(path.join(os.tmpdir(), 'afm-acp-'));
  let process;
  try {
    process = launch(provider, { env, cwd, token, confirmWorkspace });
    if (confirmWorkspace) await process.ready;
    const info = await initialize(process.agent);
    const version = [info.agentInfo?.version, 'ACP v' + info.protocolVersion].filter(Boolean).join(' · ');
    const caps = { image: info.agentCapabilities?.promptCapabilities?.image === true, resume: info.agentCapabilities?.loadSession === true };
    let session;
    try { session = await process.agent.request('session/new', { cwd, mcpServers: [] }, setup()); }
    catch (error) {
      if (error.code !== -32000) throw error;
      return { version, capabilities: caps, authRequired: true, models: [], reasoning: [] };
    }
    const models = selector(session.configOptions, 'model');
    const effort = selector(session.configOptions, 'thought_level');
    return { version, capabilities: { ...caps, model: Boolean(models || session.models) },
      models: models ? optionValues(models).map(option => ({ id: option.value, label: option.name, default: option.value === models.currentValue }))
        : (session.models?.availableModels ?? []).map(model => ({ id: model.modelId, label: model.name, default: model.modelId === session.models.currentModelId })),
      reasoning: optionValues(effort).map(option => option.value),
      agentModes: optionValues(selector(session.configOptions, 'mode')).map(option => ({ id: option.value, label: option.name })) };
  } finally { await process?.stop(); if (!provider.remoteCwd) await rm(cwd, { recursive: true, force: true }); }
}

function toolResult(update, onEvent) {
  if (!['completed', 'failed'].includes(update.status)) return;
  const text = update.content?.filter(item => item.type === 'content' && item.content.type === 'text').map(item => item.content.text).join('\n');
  onEvent({ type: 'tool_result', id: update.toolCallId, error: update.status === 'failed', text: text ?? '' });
}

export function runAcp({ provider, settings, cwd, env, bridge, token, prompt, images, resume, onEvent, approve }) {
  let sessionId, prompting = false;
  const tools = new Set();
  const { agent, stop, ready } = launch(provider, { cwd, env, token, bridge, confirmWorkspace: Boolean(provider.workspaceRevision), approve: async ({ sessionId: session, toolCall, options }) => {
    if (!prompting || session !== sessionId) return cancelled;
    const allow = options.find(option => option.kind === 'allow_once');
    const deny = options.find(option => option.kind === 'reject_once');
    const selected = allow && settings.permission !== 'read' && (provider.localCodex && settings.permission === 'yolo' || await approve(toolCall)) ? allow : deny;
    return selected ? { outcome: { outcome: 'selected', optionId: selected.optionId } } : cancelled;
  }, onUpdate: (session, update) => {
    if (!prompting || session !== sessionId) return;
    if (update.sessionUpdate === 'agent_message_chunk' && update.content.type === 'text') onEvent({ type: 'delta', text: update.content.text });
    if (update.sessionUpdate === 'tool_call') {
      tools.add(update.toolCallId);
      onEvent({ type: 'tool', id: update.toolCallId, name: update.title, input: update.rawInput ?? {}, native: update.kind === 'execute' });
    }
    if (tools.has(update.toolCallId) && ['tool_call', 'tool_call_update'].includes(update.sessionUpdate)) toolResult(update, onEvent);
  } });
  const done = (async () => {
    try {
      await ready;
      const info = await initialize(agent);
      if (images.length && !info.agentCapabilities?.promptCapabilities?.image) throw Error('This ACP agent does not accept images.');
      const mcpServers = bridge && !provider.endpoint && !provider.localCodex ? [{ name: 'afm', command: bridge.command, args: bridge.args,
        env: Object.entries(bridge.env).map(([name, value]) => ({ name, value })) }] : [];
      const session = resume ? await agent.request('session/load', { sessionId: resume, cwd, mcpServers }, setup()).catch(error => {
        if (error.code === -32002) throw Error('Unknown session.');
        throw error;
      }) : await agent.request('session/new', { cwd, mcpServers }, setup());
      sessionId = resume ?? session.sessionId;
      onEvent({ type: 'session', id: sessionId });
      let options = session?.configOptions;
      for (const [category, value] of [['model', settings.model], ['thought_level', settings.reasoning], ['mode', provider.localCodex ? codexAcpModes[settings.permission] : settings.acpMode]]) {
        if (!value || (category === 'thought_level' && value === 'default')) continue;
        const config = selector(options, category);
        if (!config && category === 'model' && session?.models?.availableModels.some(item => item.modelId === value)) {
          await agent.request('session/set_model', { sessionId, modelId: value }, setup());
          continue;
        }
        if (!optionValues(config).some(item => item.value === value)) throw Error('The ACP agent does not offer that ' + ({ model: 'model.', thought_level: 'reasoning level.', mode: 'mode.' })[category]);
        if (config.currentValue !== value) {
          const result = await agent.request('session/set_config_option', { sessionId, configId: config.id, value }, setup());
          options = result.configOptions;
        }
      }
      prompting = true;
      const result = await agent.request('session/prompt', { sessionId, prompt: [{ type: 'text', text: prompt },
        ...images.map(({ type, data }) => ({ type: 'image', mimeType: type, data }))] });
      if (result.stopReason === 'refusal') throw Error('The ACP agent declined the request.');
      if (result.stopReason === 'cancelled') throw Error('The ACP agent cancelled the reply.');
      return { code: 0 };
    } catch (error) { return { error }; }
    finally { await stop(); }
  })();
  return { done, cancel() {
    if (!prompting) return stop();
    const timer = setTimeout(stop, 100);
    return agent.notify('session/cancel', { sessionId }).catch(() => {}).finally(() => {
      clearTimeout(timer);
      return stop();
    });
  }, yield: () => stop(false) };
}

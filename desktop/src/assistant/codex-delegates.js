import { glob, open } from 'node:fs/promises';
import path from 'node:path';

const THREAD_ID = /^[\da-f]{8}(?:-[\da-f]{4}){3}-[\da-f]{12}$/i;
const MAX_BACKLOG = 1024 * 1024;
const OPAQUE_MESSAGE = /^\s*gAAAAA[A-Za-z0-9_-]{60,}={0,2}\s*$/;

function rolloutTool(item) {
  if (typeof item?.call_id !== 'string') return null;
  if (['function_call_output', 'custom_tool_call_output'].includes(item.type))
    return { id: item.call_id, status: item.is_error ? 'error' : 'done' };
  if (!['function_call', 'custom_tool_call'].includes(item.type) || typeof item.name !== 'string') return null;
  let input = item.arguments ?? item.input;
  if (item.name === 'send_message') {
    try { input = JSON.parse(input); } catch { return null; }
    if (typeof input?.message !== 'string' || !input.message.trim() || OPAQUE_MESSAGE.test(input.message)) return null;
    if (input.target === '/root') input.target = 'Main';
  }
  return { id: item.call_id, name: item.name, input, status: 'running' };
}

export function codexRolloutDelegate(line, startedAt, sessionId, child = false) {
  let row;
  try { row = JSON.parse(line); } catch { return null; }
  if (!(Date.parse(row?.timestamp) >= startedAt)) return null;
  const payload = row.payload;
  if (child && row.type === 'response_item') {
    const tool = rolloutTool(payload);
    return tool ? { type: 'delegate', id: sessionId, tool } : null;
  }
  if (row.type !== 'event_msg') return null;
  if (child && payload?.type === 'task_started') return { type: 'delegate', id: sessionId, status: 'running' };
  if (child && payload?.type === 'task_complete') return { type: 'delegate', id: sessionId, status: 'done' };
  if (child && payload?.type === 'turn_aborted') return { type: 'delegate', id: sessionId, status: 'interrupted' };
  const item = payload?.item;
  if (child && payload?.type === 'item_completed' && item?.type === 'Reasoning' && Array.isArray(item.summary_text)) {
    const text = item.summary_text.join('\n');
    if (text) return { type: 'delegate', id: sessionId, channel: 'reasoning', delta: text + '\n\n' };
  }
  if (item?.type !== 'SubAgentActivity' || !item.agent_thread_id || item.agent_thread_id === sessionId) return null;
  const name = item.agent_path?.split('/').filter(Boolean).at(-1);
  const status = { started: 'running', completed: 'done', interrupted: 'interrupted', failed: 'error' }[item.kind];
  if (!status || item.agent_path === '/root') return null;
  return { type: 'delegate', id: item.agent_thread_id, name, status };
}

// Codex exec's JSON stream omits SubAgentActivity events that the same thread records locally.
export function watchCodexDelegates({ home, startedAt, onEvent }) {
  let sessionId, timer, pending;
  const logs = new Map();
  const chunk = Buffer.alloc(64 * 1024);
  const track = id => {
    if (THREAD_ID.test(id) && !logs.has(id)) logs.set(id, { buffer: '', decoder: new TextDecoder() });
  };
  async function scanLog(id, log) {
    if (!log.file) {
      for await (const entry of glob(`*/*/*/rollout-*-${id}.jsonl`, { cwd: home })) {
        log.file = path.join(home, entry);
        break;
      }
    }
    if (!log.file) return;
    const file = await open(log.file, 'r');
    try {
      const { size } = await file.stat();
      log.offset ??= Math.max(0, size - MAX_BACKLOG);
      while (log.offset < size) {
        const { bytesRead } = await file.read(chunk, 0, Math.min(chunk.length, size - log.offset), log.offset);
        if (!bytesRead) break;
        log.offset += bytesRead;
        log.buffer += log.decoder.decode(chunk.subarray(0, bytesRead), { stream: true });
        const lines = log.buffer.split('\n');
        log.buffer = lines.pop();
        for (const line of lines) {
          const event = codexRolloutDelegate(line, startedAt, id, id !== sessionId);
          if (!event || (id !== sessionId && event.id === sessionId)) continue;
          track(event.id);
          onEvent(event);
        }
      }
      if (log.buffer.length > MAX_BACKLOG) log.buffer = '';
    } finally {
      await file.close();
    }
  }
  async function scan() {
    for (const [id, log] of logs) await scanLog(id, log).catch(() => {});
  }
  const poll = () => pending ??= scan().finally(() => { pending = undefined; });
  return {
    start(id) {
      if (sessionId || !THREAD_ID.test(id)) return;
      sessionId = id;
      track(id);
      timer = setInterval(poll, 750);
      void poll();
    },
    child(id) { if (sessionId) track(id); },
    async stop() {
      if (!sessionId) return;
      clearInterval(timer);
      await pending;
      await poll();
    },
  };
}

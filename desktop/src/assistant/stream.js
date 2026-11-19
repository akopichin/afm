const TOOL_PREFIX = /^mcp__afm__/;
const clip = (text, limit = 600) => (text.length > limit ? text.slice(0, limit) + '…' : text);

function textOf(content) {
  if (typeof content === 'string') return content;
  return Array.isArray(content) ? content.map(part => part?.text ?? '').join('') : '';
}

// Each parser turns one line of provider output into zero or more events:
// {type:'delta',text} {type:'tool',id,name,input} {type:'tool_result',id,error,text} {type:'result',ok,text,error}.
// Lines that are not JSON or not understood are ignored, so a newer CLI does not break the chat.
export class ClaudeParser {
  #streamed = false;
  parse(line) {
    let event;
    try { event = JSON.parse(line); } catch { return []; }
    if (event.type === 'stream_event') {
      const inner = event.event;
      if (inner?.type === 'message_start') this.#streamed = false;
      if (inner?.type === 'content_block_delta' && inner.delta?.type === 'text_delta' && inner.delta.text) {
        this.#streamed = true;
        return [{ type: 'delta', text: inner.delta.text }];
      }
      return [];
    }
    if (event.type === 'assistant') {
      const out = [];
      for (const part of event.message?.content ?? []) {
        if (part.type === 'text' && part.text && !this.#streamed) out.push({ type: 'delta', text: part.text });
        if (part.type === 'tool_use') out.push({ type: 'tool', id: part.id, name: String(part.name).replace(TOOL_PREFIX, ''), input: clip(JSON.stringify(part.input ?? {})) });
      }
      this.#streamed = false;
      return out;
    }
    if (event.type === 'user') {
      return (event.message?.content ?? []).filter(part => part?.type === 'tool_result')
        .map(part => ({ type: 'tool_result', id: part.tool_use_id, error: Boolean(part.is_error), text: clip(textOf(part.content)) }));
    }
    if (event.type === 'result') {
      const failed = event.is_error || (event.subtype && event.subtype !== 'success');
      return [{ type: 'result', ok: !failed, text: textOf(event.result), error: failed ? textOf(event.result) || String(event.subtype ?? 'failed') : null }];
    }
    return [];
  }
}

export class CodexParser {
  #messages = 0;
  parse(line) {
    let event;
    try { event = JSON.parse(line); } catch { return []; }
    const item = event.item;
    if (event.type === 'item.completed' && item?.type === 'agent_message' && item.text) {
      // Codex reports whole messages; separate them so they read as paragraphs.
      return [{ type: 'delta', text: (this.#messages++ ? '\n\n' : '') + item.text }];
    }
    if (item?.type === 'mcp_tool_call') {
      const name = String(item.tool ?? 'tool').replace(TOOL_PREFIX, '');
      if (event.type === 'item.started') return [{ type: 'tool', id: item.id, name, input: clip(JSON.stringify(item.arguments ?? {})) }];
      if (event.type === 'item.completed') {
        const error = item.status === 'failed' || Boolean(item.error);
        return [{ type: 'tool_result', id: item.id, error, text: clip(item.error?.message ?? textOf(item.result?.content)) }];
      }
    }
    if (event.type === 'turn.failed') return [{ type: 'result', ok: false, text: '', error: event.error?.message ?? 'The turn failed.' }];
    if (event.type === 'error') return [{ type: 'result', ok: false, text: '', error: event.message ?? 'The provider reported an error.' }];
    if (event.type === 'turn.completed') return [{ type: 'result', ok: true, text: '', error: null }];
    return [];
  }
}

export const parserFor = kind => (kind === 'codex' ? new CodexParser() : new ClaudeParser());

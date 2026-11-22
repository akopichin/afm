export const memoryTool = {
  name: 'desktop_search_conversations',
  description: 'Search saved Desktop conversations within the current project. Accepts a text fragment and optional dates. Use an empty query when searching by date.',
  annotations: { readOnlyHint: true },
  inputSchema: { type: 'object', properties: {
    query: { type: 'string', maxLength: 300 },
    after: { type: 'string', description: 'ISO 8601 lower bound, inclusive' },
    before: { type: 'string', description: 'ISO 8601 upper bound, exclusive' },
  } },
};

export function searchMessages(rows, scope, args) {
  const fragment = (typeof args.query === 'string' ? args.query : '').slice(0, 300).trim().toLowerCase();
  const boundary = (value, fallback) => {
    if (value === undefined) return fallback;
    const ms = Date.parse(value);
    if (!Number.isFinite(ms)) throw Error('Use an ISO date for the time range.');
    return ms;
  };
  const after = boundary(args.after, -Infinity), before = boundary(args.before, Infinity);
  const results = [];
  for (const { conversationId, name, messages } of rows) {
    for (const message of messages) {
      if (typeof message.text !== 'string' || !message.text || !message.text.toLowerCase().includes(fragment)) continue;
      if (scope.kind !== 'all' && message.projectRoot !== scope.projectRoot) continue;
      if (scope.kind === 'run' && (message.runId !== scope.runId || message.instanceId !== scope.instanceId)) continue;
      const at = Date.parse(message.at);
      if (!Number.isFinite(at) || at < after || at >= before) continue;
      const index = fragment ? message.text.toLowerCase().indexOf(fragment) : 0;
      const start = Math.max(0, index - 150);
      const excerpt = message.text.slice(start, start + 450);
      results.push({ conversationId, name, messageId: message.id, at: message.at, role: message.role, excerpt });
    }
  }
  return results.sort((a,b) => a.at < b.at ? 1 : a.at > b.at ? -1 : 0).slice(0, 20);
}

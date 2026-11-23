function conversationId(value = 'main') {
  if (value === 'main' || (typeof value === 'string' && /^[a-f0-9-]{36}$/.test(value))) return value;
  throw new Error('Invalid conversation ID.');
}

// Renderer-facing assistant calls. Every argument is picked field by field, so the page cannot pass anything else through.
export function registerAssistantIpc(handle, assistant, publish) {
  handle('assistant:state', () => assistant.state());
  handle('assistant:scan', () => assistant.scan());
  handle('assistant:discover-provider', id => assistant.discoverProvider(String(id)));
  handle('assistant:configure', changes => assistant.configure({ ...changes }));
  handle('assistant:add-provider', input => assistant.addProvider({
    token: typeof input?.token === 'string' ? input.token : '', name: input?.name, kind: input?.kind, executable: input?.executable, endpoint: input?.endpoint, remoteCwd: input?.remoteCwd, args: input?.args,
    credential: input?.credential ? { type: input.credential.type, ref: input.credential.ref } : undefined }));
  handle('assistant:remove-provider', id => assistant.removeProvider(String(id)));
  handle('assistant:update-provider-workspace', (id, cwd) => assistant.updateProviderWorkspace(String(id), String(cwd ?? '')));
  handle('assistant:test-provider', id => assistant.testProvider(String(id)));
  handle('assistant:test-model', (force, draft) => assistant.testModel(force === true, draft == null ? undefined : {
    provider: draft.provider, model: draft.model, reasoning: draft.reasoning,
  }));
  handle('assistant:mcp-list', () => assistant.mcp.list());
  handle('assistant:mcp-save', input => assistant.mcp.save({
    id: input?.id, name: input?.name, transport: input?.transport, projectId: input?.projectId,
    url: input?.url, command: input?.command, args: input?.args, env: input?.env,
    tokenEnv: input?.tokenEnv, tokenFile: input?.tokenFile, token: input?.token, removeToken: input?.removeToken === true,
  }));
  handle('assistant:mcp-import', (text, projectId) => assistant.mcp.importOpenCode(text, projectId));
  handle('assistant:mcp-remove', id => assistant.mcp.remove(String(id)));
  handle('assistant:mcp-test', id => assistant.testMcpServer(String(id)));
  handle('assistant:send', request => assistant.send({
    conversationId: conversationId(request?.conversationId), text: request?.text, images: request?.images,
    scope: { projectId: request?.scope?.projectId, runId: request?.scope?.runId, instanceId: request?.scope?.instanceId } }));
  handle('assistant:follow-up', (turnId, queued) => assistant.setFollowUp(String(turnId), queued === true));
  handle('assistant:cancel', id => assistant.cancel(conversationId(id)));
  handle('assistant:approve', (approvalId, approved) => assistant.approve(String(approvalId), approved === true));
  handle('assistant:history', id => assistant.history(conversationId(id)));
  handle('assistant:clear', id => assistant.clear(conversationId(id)));
  handle('assistant:backlog', (id, text, scope) => assistant.backlogCommand(conversationId(id), String(text), {
    projectId: scope?.projectId, runId: scope?.runId, instanceId: scope?.instanceId,
  }));
  handle('assistant:backlog-plans', (scope, completed) => assistant.backlogPlans({
    projectId: scope?.projectId, runId: scope?.runId, instanceId: scope?.instanceId,
  }, completed === true));
  handle('assistant:conversations', () => assistant.conversations());
  handle('assistant:new-conversation', () => assistant.createConversation());
  // Replies Desktop writes for itself (watch reports) have their own conversations and stay out of the chat.
  return assistant.onEvent(event => { if (event.conversationId === 'main' || /^[a-f0-9-]{36}$/.test(event.conversationId)) publish(event); });
}

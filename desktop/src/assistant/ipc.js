// Renderer-facing assistant calls. Every argument is picked field by field, so the page cannot pass anything else through.
export function registerAssistantIpc(handle, assistant, publish) {
  handle('assistant:state', () => assistant.state());
  handle('assistant:scan', () => assistant.scan());
  handle('assistant:configure', changes => assistant.configure({ ...changes }));
  handle('assistant:add-provider', input => assistant.addProvider({
    name: input?.name, kind: input?.kind, executable: input?.executable, endpoint: input?.endpoint,
    credential: input?.credential ? { type: input.credential.type, ref: input.credential.ref } : undefined }));
  handle('assistant:remove-provider', id => assistant.removeProvider(String(id)));
  handle('assistant:test-provider', id => assistant.testProvider(String(id)));
  handle('assistant:send', request => assistant.send({
    conversationId: 'main', text: request?.text, images: request?.images,
    scope: { projectId: request?.scope?.projectId, runId: request?.scope?.runId, instanceId: request?.scope?.instanceId } }));
  handle('assistant:cancel', () => assistant.cancel('main'));
  handle('assistant:approve', (approvalId, approved) => assistant.approve(String(approvalId), approved === true));
  handle('assistant:history', () => assistant.history('main'));
  handle('assistant:clear', () => assistant.clear('main'));
  // Replies Desktop writes for itself (watch reports) have their own conversations and stay out of the chat.
  return assistant.onEvent(event => { if (event.conversationId === 'main') publish(event); });
}

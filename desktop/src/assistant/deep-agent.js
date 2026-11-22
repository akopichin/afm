import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';

export const isAgentEndpoint = kind => kind === 'openai-agent' || kind === 'anthropic-agent';
const anthropicBase = endpoint => endpoint.replace(/\/v1$/, '');
const noRedirect = (input, init) => fetch(input, { ...init, redirect: 'error' });
function anthropicFetch(apiKey) {
  return (input, init) => {
    const headers = new Headers(init?.headers ?? input?.headers);
    headers.delete('authorization');
    headers.set('x-api-key', apiKey);
    return noRedirect(input, { ...init, headers });
  };
}

export async function endpointModels(provider, apiKey, signal = AbortSignal.timeout(4000)) {
  const anthropic = provider.kind === 'anthropic-agent';
  const url = anthropic ? anthropicBase(provider.endpoint) + '/v1/models' : provider.endpoint + '/models';
  const headers = anthropic ? { 'x-api-key': apiKey, 'anthropic-version': '2023-06-01' } : { authorization: 'Bearer ' + apiKey };
  const response = await noRedirect(url, { headers, signal });
  if ([401, 403].includes(response.status)) throw Error('The endpoint rejected the API key.');
  if ([404, 405, 501].includes(response.status)) return [];
  if (!response.ok) throw Error(`The model catalog returned HTTP ${response.status}.`);
  const body = await response.json();
  return (Array.isArray(body.data) ? body.data : []).filter(item => typeof item?.id === 'string')
    .map(item => ({ id: item.id, label: item.display_name || item.id }));
}

export function runDeepAgent({ provider, apiKey, modelId, systemPrompt, messages, bridge, onEvent }) {
  const controller = new AbortController();
  const done = (async () => {
    let client;
    try {
      const [{ createDeepAgent }, { DynamicStructuredTool }] = await Promise.all([
        import('deepagents'), import('@langchain/core/tools'),
      ]);
      const model = provider.kind === 'anthropic-agent'
        ? new (await import('@langchain/anthropic')).ChatAnthropic({
          model: modelId, apiKey, anthropicApiUrl: anthropicBase(provider.endpoint), clientOptions: { fetch: anthropicFetch(apiKey) },
          maxRetries: 0,
        })
        : new (await import('@langchain/openai')).ChatOpenAI({
          model: modelId, apiKey, configuration: { baseURL: provider.endpoint, fetch: noRedirect },
          useResponsesApi: false, maxRetries: 0,
        });
      const tools = [];
      if (bridge) {
        client = new Client({ name: 'afm-desktop-deep-agent', version: '1' });
        controller.signal.throwIfAborted();
        await client.connect(new StdioClientTransport({ command: bridge.command, args: bridge.args, env: bridge.env }));
        const { tools: specs } = await client.listTools(undefined, { signal: controller.signal });
        for (const spec of specs) {
          tools.push(new DynamicStructuredTool({ name: spec.name, description: spec.description ?? spec.name,
            schema: spec.inputSchema, func: async args => {
              const result = await client.callTool({ name: spec.name, arguments: args }, undefined, {
                signal: controller.signal, timeout: 601_000,
              });
              if (result.isError) throw Error(result.content?.map(item => item.text).filter(Boolean).join('\n') || 'Tool failed.');
              return JSON.stringify(result);
            } }));
        }
      }
      const agent = createDeepAgent({ model, systemPrompt, tools });
      const calls = new Set();
      const stream = await agent.streamEvents({ messages }, { version: 'v2', signal: controller.signal });
      for await (const event of stream) {
        if (event.event === 'on_chat_model_stream' && event.metadata?.langgraph_node === 'model_request'
          && !event.metadata?.langgraph_checkpoint_ns?.includes('|')) {
          const { content } = event.data.chunk;
          const text = typeof content === 'string' ? content : content?.filter(block => block.type === 'text').map(block => block.text).join('');
          if (text) onEvent({ type: 'delta', text });
        } else if (event.event === 'on_tool_start') {
          calls.add(event.run_id);
          onEvent({ type: 'tool', id: event.run_id, name: event.name, input: event.data.input, native: true });
        } else if (event.event === 'on_tool_end' && calls.delete(event.run_id)) {
          const output = event.data.output;
          onEvent({ type: 'tool_result', id: event.run_id, error: output?.status === 'error',
            text: typeof output?.content === 'string' ? output.content : JSON.stringify(output?.content ?? '') });
        }
      }
      return { code: 0 };
    } catch (error) {
      return controller.signal.aborted ? { cancelled: true } : { error };
    } finally {
      await client?.close();
    }
  })();
  return { done, cancel: () => controller.abort() };
}

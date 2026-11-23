import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { isDelegationTool } from './stream.js';
import { deepMcpTools } from './deep-mcp.js';

import { anthropicBase, noRedirect } from './model-catalog.js';

function anthropicFetch(apiKey) {
  return (input, init) => {
    const headers = new Headers(init?.headers ?? input?.headers);
    headers.delete('authorization');
    headers.set('x-api-key', apiKey);
    return noRedirect(input, { ...init, headers });
  };
}

function parentDelegate(delegates, namespace) {
  while (namespace?.includes('|')) {
    namespace = namespace.slice(0, namespace.lastIndexOf('|'));
    if (delegates.has(namespace)) return delegates.get(namespace);
  }
}

async function endpointModel({ provider, apiKey, modelId, reasoning, maxTokens }) {
  const effort = reasoning && reasoning !== 'default' ? reasoning : undefined;
  if (provider.kind === 'anthropic-agent') {
    const { ChatAnthropic } = await import('@langchain/anthropic');
    return new ChatAnthropic({
      model: modelId, apiKey, anthropicApiUrl: anthropicBase(provider.endpoint), clientOptions: { fetch: anthropicFetch(apiKey) },
      maxRetries: 0, maxTokens, ...(effort ? { outputConfig: { effort } } : {}),
    });
  }
  const { ChatOpenAI } = await import('@langchain/openai');
  return new ChatOpenAI({
    model: modelId, apiKey, configuration: { baseURL: provider.endpoint, fetch: noRedirect },
    useResponsesApi: false, maxRetries: 0, maxTokens,
    // Compatible gateways can expose reasoning models under IDs the OpenAI client does not recognize.
    ...(effort ? { modelKwargs: { reasoning_effort: effort } } : {}),
  });
}

function replyText(content) {
  return typeof content === 'string' ? content : (content ?? []).filter(block => block.type === 'text').map(block => block.text).join('');
}

export async function testEndpointModel(selection, prompt, signal) {
  const model = await endpointModel({ ...selection, maxTokens: 128 });
  const stream = await model.stream([{ role: 'user', content: prompt }], { signal });
  let received = false;
  for await (const chunk of stream) {
    received ||= Boolean(replyText(chunk.content).trim());
  }
  signal.throwIfAborted();
  if (!received) throw Error('The endpoint returned no model response.');
}

export function runDeepAgent({ provider, apiKey, modelId, reasoning, systemPrompt, messages, bridge, mcp, onEvent }) {
  const controller = new AbortController();
  const done = (async () => {
    let client, external;
    try {
      const [{ createDeepAgent }, { DynamicStructuredTool }] = await Promise.all([
        import('deepagents'), import('@langchain/core/tools'),
      ]);
      const model = await endpointModel({ provider, apiKey, modelId, reasoning });
      const tools = [];
      if (mcp?.servers.length) {
        external = deepMcpTools(DynamicStructuredTool, mcp, controller.signal);
        tools.push(...external.tools);
      }
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
      const calls = new Map();
      const delegates = new Map();
      const stream = await agent.streamEvents({ messages }, { version: 'v2', signal: controller.signal });
      for await (const event of stream) {
        const namespace = event.metadata?.langgraph_checkpoint_ns;
        if (event.event === 'on_chat_model_stream' && event.metadata?.langgraph_node === 'model_request') {
          const { content } = event.data.chunk;
          if (namespace?.includes('|')) {
            const owner = parentDelegate(delegates, namespace);
            const delta = Array.isArray(content) && content.map(block => block.type === 'thinking' ? block.thinking
              : block.type === 'reasoning' ? block.reasoning : '').join('');
            if (owner && delta) onEvent({ type: 'delegate', id: owner, ownerTool: true, channel: 'reasoning', delta });
          } else {
            const text = replyText(content);
            if (text) onEvent({ type: 'delta', text });
          }
        } else if (event.event === 'on_tool_start') {
          // JS stream events identify nested graphs by checkpoint namespace.
          const owner = parentDelegate(delegates, namespace);
          const createsDelegate = isDelegationTool(event.name);
          calls.set(event.run_id, { owner, main: !owner || createsDelegate, namespace });
          if (createsDelegate && namespace) delegates.set(namespace, event.run_id);
          if (owner) onEvent({ type: 'delegate', id: owner, ownerTool: true, tool: {
            id: event.run_id, name: event.name, input: event.data.input, status: 'running', native: true,
          } });
          if (!owner || createsDelegate)
            onEvent({ type: 'tool', id: event.run_id, name: event.name, input: event.data.input, native: true });
        } else if (['on_tool_end', 'on_tool_error'].includes(event.event) && calls.has(event.run_id)) {
          const { owner, main, namespace } = calls.get(event.run_id);
          calls.delete(event.run_id);
          if (delegates.get(namespace) === event.run_id) delegates.delete(namespace);
          const output = event.data.output;
          const error = event.event === 'on_tool_error' || output?.status === 'error';
          if (owner) onEvent({ type: 'delegate', id: owner, ownerTool: true, tool: {
            id: event.run_id, status: error ? 'error' : 'done', native: true,
          } });
          if (main) onEvent({ type: 'tool_result', id: event.run_id, error,
            text: typeof output?.content === 'string' ? output.content : JSON.stringify(output?.content ?? '') });
        }
      }
      return { code: 0 };
    } catch (error) {
      return controller.signal.aborted ? { cancelled: true } : { error };
    } finally {
      await Promise.all([external?.close(), client?.close()]);
    }
  })();
  return { done, cancel: () => controller.abort() };
}

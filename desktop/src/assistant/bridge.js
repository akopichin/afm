import { readFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';
import { localURL } from '../host.js';
import { askApproval } from './approval.js';
import { filterRuns, modeDecision, scopeLabel, scopeViolation, toolKind, visibleTools } from './policy.js';
import { PERMISSION_LABELS } from './modes.js';

const refusal = text => ({ isError: true, content: [{ type: 'text', text }] });
const clip = (text, limit = 300) => (text.length > limit ? text.slice(0, limit) + '…' : text);

// The AFM tools the provider sees: the host's own tools, filtered by the user's permission mode and the
// selected scope, with approval asked for what the mode does not allow outright. `host` and `approve` are injected.
export function createBridge({ host, policy, approve }) {
  const server = new Server({ name: 'afm-desktop-assistant', version: '0.0.1' }, { capabilities: { tools: {} } });
  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: visibleTools(await host.listTools(), policy) }));
  server.setRequestHandler(CallToolRequestSchema, async ({ params }) => {
    const args = params.arguments ?? {};
    const tool = (await host.listTools()).find(item => item.name === params.name);
    if (!tool) return refusal(`${params.name}: not_found: Unknown AFM tool.`);
    const kind = toolKind(tool);
    const violation = scopeViolation(policy.scope, tool, args, tool.name === 'afm_runs_list' ? [] : await runRows(host));
    if (violation) return refusal(`${tool.name}: out_of_scope: ${violation}`);
    const decision = modeDecision(policy.mode, kind);
    if (decision === 'deny') return refusal(`${tool.name}: not_allowed: "${PERMISSION_LABELS[policy.mode]}" mode does not allow this. Ask the user to change the permission mode.`);
    if (decision === 'ask' && !await approve({ turnId: policy.turnId, tool: tool.name, kind, scope: scopeLabel(policy.scope), summary: clip(JSON.stringify(args)) }))
      return refusal(`${tool.name}: declined: The user did not approve this action.`);
    const result = await host.callTool(tool.name, args);
    return tool.name === 'afm_runs_list' ? scopedList(result, policy.scope) : result;
  });
  return server;
}

async function runRows(host) {
  const result = await host.callTool('afm_runs_list', {});
  return result.structuredContent?.runs ?? JSON.parse(result.content[0].text).runs;
}

function scopedList(result, scope) {
  const runs = filterRuns(scope, result.structuredContent?.runs ?? JSON.parse(result.content[0].text).runs);
  return { ...result, structuredContent: { runs }, content: [{ type: 'text', text: JSON.stringify({ runs }) }] };
}

// Entry point of the stdio server the provider starts. It reads its policy from the file named on its command line.
async function main() {
  const policy = JSON.parse(await readFile(process.argv[process.argv.indexOf('--policy') + 1], 'utf8'));
  const client = new Client({ name: 'afm-desktop-assistant', version: '0.0.1' });
  await client.connect(new StreamableHTTPClientTransport(localURL(policy.endpoint), { requestInit: { redirect: 'error' } }));
  const host = {
    listTools: async () => (await client.listTools()).tools,
    callTool: (name, args) => client.callTool({ name, arguments: args }),
  };
  await createBridge({ host, policy, approve: request => askApproval(policy.approvalSocket, request) }).connect(new StdioServerTransport());
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { process.stderr.write(String(error?.message ?? error) + '\n'); process.exit(1); });
}

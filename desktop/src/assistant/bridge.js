import { setTimeout as delay } from 'node:timers/promises';
import { readFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';
import { localURL } from '../host.js';
import { askApproval, askLocal } from './approval.js';
import { isWatchTool, SCHEDULE_TOOL, TASK_TOOL, WATCH_TOOLS } from './watch-tools.js';
import { filterRuns, modeDecision, scopeLabel, scopeViolation, toolKind, visibleTools } from './policy.js';
import { PERMISSION_LABELS } from './modes.js';

const refusal = text => ({ isError: true, content: [{ type: 'text', text }] });
const RUN_WAIT = 'afm_run_wait';
const PAUSE = 'desktop_wait';
const pauseTool = { name: PAUSE, description: 'Wait between status checks of a background job. Send queued user messages through at this checkpoint.',
  annotations: { readOnlyHint: true }, inputSchema: { type: 'object', properties: { seconds: { type: 'integer', minimum: 1, maximum: 60 } }, required: ['seconds'] } };
const clip = (text, limit = 300) => (text.length > limit ? text.slice(0, limit) + '…' : text);

// The AFM tools the provider sees: the host's own tools, filtered by the user's permission mode and the
// selected scope, with approval asked for what the mode does not allow outright. `host` and `approve` are injected.
// `local` answers the Desktop-side watch tools; without it (or with `policy.watch` off) those tools are not offered.
export function createBridge({ host, policy, approve, local = null }) {
  const watching = Boolean(policy.watch && local);
  const scheduling = Boolean(watching && policy.schedule);
  const server = new Server({ name: 'afm-desktop-assistant', version: '0.0.1' }, { capabilities: { tools: {} } });
  const steering = policy.steering && local;
  const waits = new Set();
  let busy = 0, parked, handingOff = false;
  const offerHandoff = () => {
    if (busy || !waits.size || parked || handingOff) return;
    const offer = parked = new AbortController();
    let retry = false;
    local({ op: 'park', turnId: policy.turnId }, 65_000, offer.signal).then(async ({ queued }) => {
      if (!queued || offer.signal.aborted || busy || !waits.size) return;
      handingOff = true;
      // Block subsequent calls before claiming the handoff. A call already in flight got to finish first.
      const { accepted } = await local({ op: 'yield', turnId: policy.turnId });
      if (accepted) for (const wait of waits) wait.abort();
      else { handingOff = false; retry = true; }
    }).catch(() => {
      if (parked === offer) handingOff = false;
    }).finally(() => {
      if (parked !== offer) return;
      parked = undefined;
      if (retry) offerHandoff();
    });
  };
  const waitFor = async (signal, operation) => {
    const controller = new AbortController();
    waits.add(controller);
    offerHandoff();
    try { return await operation(AbortSignal.any([signal, controller.signal])); }
    finally {
      waits.delete(controller);
      if (!waits.size) parked?.abort();
    }
  };
  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: [...visibleTools(await host.listTools(), policy),
    ...(watching ? WATCH_TOOLS : []), ...(scheduling ? [TASK_TOOL] : []), ...(steering ? [pauseTool] : [])] }));
  const call = async (params, signal, ready) => {
    const args = params.arguments ?? {};
    if (params.name === PAUSE && steering) {
      if (!Number.isInteger(args.seconds) || args.seconds < 1 || args.seconds > 60) return refusal('desktop_wait: seconds must be an integer from 1 to 60.');
      return waitFor(signal, async abort => {
        ready();
        await delay(args.seconds * 1000, undefined, { signal: abort });
        return { content: [{ type: 'text', text: 'Wait elapsed. Check the background job again.' }] };
      });
    }
    if (isWatchTool(params.name)) {
      if (!watching || (params.name === SCHEDULE_TOOL && !scheduling)) return refusal(`${params.name}: not_found: Unknown AFM tool.`);
      try {
        const answer = await local({ op: 'watch', turnId: policy.turnId, tool: params.name, args }, params.name === SCHEDULE_TOOL ? 601_000 : undefined);
        return { content: [{ type: 'text', text: answer.text }] };
      } catch (error) { return refusal(`${params.name}: rejected: ${error.message}`); }
    }
    const tool = (await host.listTools()).find(item => item.name === params.name);
    if (!tool) return refusal(`${params.name}: not_found: Unknown AFM tool.`);
    const kind = toolKind(tool);
    const violation = scopeViolation(policy.scope, tool, args, tool.name === 'afm_runs_list' ? [] : await runRows(host));
    if (violation) return refusal(`${tool.name}: out_of_scope: ${violation}`);
    const decision = modeDecision(policy.mode, kind);
    if (decision === 'deny') return refusal(`${tool.name}: not_allowed: "${PERMISSION_LABELS[policy.mode]}" mode does not allow this. Ask the user to change the permission mode.`);
    if (decision === 'ask' && !await approve({ turnId: policy.turnId, tool: tool.name, kind, scope: scopeLabel(policy.scope), summary: clip(JSON.stringify(args)) }))
      return refusal(`${tool.name}: declined: The user did not approve this action.`);
    if (tool.name === RUN_WAIT && steering && kind === 'read') {
      return waitFor(signal, abort => {
        ready();
        return host.callTool(tool.name, args, abort);
      });
    }
    const result = await host.callTool(tool.name, args, signal);
    return tool.name === 'afm_runs_list' ? scopedList(result, policy.scope) : result;
  };
  server.setRequestHandler(CallToolRequestSchema, async ({ params }, { signal }) => {
    if (handingOff) return refusal('Desktop is handing off to a queued user message.');
    if (!steering) return call(params, signal, () => {});
    busy++;
    parked?.abort();
    parked = undefined;
    let counted = true;
    const ready = () => {
      if (!counted) return;
      counted = false;
      busy--;
      offerHandoff();
    };
    try { return await call(params, signal, ready); }
    finally { ready(); }
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
  let client;
  let host = { listTools: async () => [], callTool: async () => refusal('AFM host is unavailable.') };
  // Without a host endpoint only the Desktop-side watch tools are served.
  if (policy.endpoint) {
    client = new Client({ name: 'afm-desktop-assistant', version: '0.0.1' });
    await client.connect(new StreamableHTTPClientTransport(localURL(policy.endpoint), { requestInit: { redirect: 'error' } }));
    host = {
      listTools: async () => (await client.listTools()).tools,
      callTool: (name, args, signal) => client.callTool({ name, arguments: args }, undefined, { signal, timeout: name === RUN_WAIT ? 65_000 : 60_000 }),
    };
  }
  const bridge = createBridge({ host, policy, approve: request => askApproval(policy.approvalSocket, request),
    local: (request, timeout, signal) => askLocal(policy.approvalSocket, request, timeout, signal) });
  // The provider may yield without signalling its children. EOF ends this turn's bridge.
  process.stdin.once('end', () => {
    bridge.close().catch(() => {});
    client?.close().catch(() => {});
  });
  await bridge.connect(new StdioServerTransport());
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { process.stderr.write(String(error?.message ?? error) + '\n'); process.exit(1); });
}

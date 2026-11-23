// A deterministic stand-in for the claude and codex CLIs. Behaviour comes from FAKE_* variables, and every
// real invocation records its argv, working directory and stdin so tests can inspect them.
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';

const [kind, ...args] = process.argv.slice(2);
const env = process.env;
const out = value => process.stdout.write(JSON.stringify(value) + '\n');
if (args[0] === 'app-server') {
  const { createInterface } = await import('node:readline');
  for await (const line of createInterface({input: process.stdin})) {
    const msg = JSON.parse(line);
    if (msg.method === 'initialize') out({id:msg.id, result:{}});
    if (msg.method === 'model/list') out({id:msg.id, result:{ data:[{model:'gpt-test', displayName:'Test model', isDefault:true,
      ...(env.FAKE_CODEX_MODEL ? {supportedReasoningEfforts:[{reasoningEffort:'low'},{reasoningEffort:'high'}], defaultReasoningEffort:'low'} : {})}], nextCursor:null }});
    if (msg.method === 'config/read') out({id:msg.id, result:{ config:{ model:env.FAKE_CODEX_MODEL ?? null,
      model_reasoning_effort:env.FAKE_CODEX_MODEL ? 'medium' : null } }});
  }
  process.exit(0);
}
const old = env.FAKE_CAPS === 'old';
const noResume = old || env.FAKE_NO_RESUME === '1';

const CLAUDE_HELP = `Usage: claude [options] [command] [prompt]
  -p, --print                           Print response and exit
  --output-format <format>              Output format (choices: "text", "json", "stream-json")
  --input-format <format>               Input format (choices: "text", "stream-json")
  --include-partial-messages            Include partial message chunks as they arrive
  --permission-mode <mode>              Permission mode to use for the session
                                        (choices: "acceptEdits", "auto",
                                        "bypassPermissions", "manual",
                                        "dontAsk", "plan")
  --dangerously-skip-permissions        Bypass all permission checks.
  --model <model>                       Model for the current session.
${old ? '' : '  --effort <level>                      Effort level for the current session\n                                        (low, medium, high, xhigh, max)\n'}  --mcp-config <configs...>             Load MCP servers from JSON files
  --strict-mcp-config                   Only use MCP servers from --mcp-config
  --tools <tools...>                    Specify the list of available tools
  --allowedTools, --allowed-tools <tools...>
                                        Tools to allow
  --append-system-prompt <prompt>       Append a system prompt
${noResume ? '' : '  -r, --resume [value]                  Resume a conversation by session ID\n'}`;
const CODEX_HELP = `Usage: codex exec [OPTIONS] [PROMPT]
  -c, --config <key=value>              Override a configuration value
  -m, --model <MODEL>                   Model the agent should use
  -s, --sandbox <SANDBOX_MODE>          Select the sandbox policy
${old ? '' : '      --dangerously-bypass-approvals-and-sandbox\n                                        Skip all confirmation prompts\n'}      --skip-git-repo-check             Allow running outside a Git repo
      --json                            Print events to stdout as JSONL
      --image <FILE>                    Attach an image
`;

// `codex exec resume` lists fewer flags than `exec`; FAKE_RESUME_STRICT=1 makes it list the fewest. The fake refuses what its help omits.
const CODEX_RESUME_HELP = `Usage: codex exec resume [OPTIONS] [SESSION_ID] [PROMPT]
  -c, --config <key=value>              Override a configuration value
  -s, --sandbox <SANDBOX_MODE>          Select the sandbox policy
      --json                            Print events to stdout as JSONL
      --image <FILE>                    Attach an image
${env.FAKE_RESUME_STRICT === '1' ? '' : '  -m, --model <MODEL>                   Model the agent should use\n      --skip-git-repo-check             Allow running outside a Git repo\n      --dangerously-bypass-approvals-and-sandbox\n                                        Skip all confirmation prompts\n'}`;
const loggedIn = env.FAKE_AUTH !== 'none';
if (args.includes('--version')) { console.log('9.9.9 (fake)'); process.exit(0); }
if (kind === 'claude' && args[0] === 'auth') { out({ loggedIn }); process.exit(0); }
if (kind === 'codex' && args[0] === 'login') { console.log(loggedIn ? 'Logged in using ChatGPT' : 'Not logged in'); process.exit(loggedIn ? 0 : 1); }
if (kind === 'codex' && args[0] === 'exec' && args[1] === 'resume' && args.includes('--help')) { if (noResume) process.exit(2); process.stdout.write(CODEX_RESUME_HELP); process.exit(0); }
if (args.includes('--help')) { process.stdout.write(kind === 'codex' ? CODEX_HELP : CLAUDE_HELP); process.exit(0); }

const stdin = await new Promise(resolve => { let data = ''; process.stdin.setEncoding('utf8'); process.stdin.on('data', chunk => { data += chunk; }); process.stdin.on('end', () => resolve(data)); });
if (env.FAKE_RECORD) {
  const images = args.filter(arg => arg.startsWith('--image=')).map(arg => readFileSync(arg.slice('--image='.length), 'base64'));
  appendFileSync(env.FAKE_RECORD, JSON.stringify({ kind, args, cwd: process.cwd(), stdin, ...(images.length ? { images } : {}),
    hasSecret: Object.values(env).includes(env.FAKE_SECRET ?? '\0'), secretVars: Object.keys(env).filter(name => env[name] === env.FAKE_SECRET) }) + '\n');
}

// A resumed session keeps its id; a fresh one gets a new one (FAKE_SESSION fixes it for a test).
const resumed = kind === 'codex' ? (args[0] === 'exec' && args[1] === 'resume' ? args.at(-2) : null) : (args.includes('--resume') ? args[args.indexOf('--resume') + 1] : null);
const SID = resumed ?? env.FAKE_SESSION ?? 'fake-' + Math.random().toString(36).slice(2, 10);
if (kind === 'codex' && resumed) {
  const unknown = args.slice(2).find(arg => arg.startsWith('-') && arg !== '-' && !CODEX_RESUME_HELP.includes(arg));
  if (unknown) { process.stderr.write(`error: unexpected argument '${unknown}' found\n`); process.exit(2); }
}
if (resumed && env.FAKE_EXPIRED === '1') {
  process.stderr.write(kind === 'codex' ? `Error: no rollout found for thread id ${resumed}\n` : `No conversation found with session ID: ${resumed}\n`);
  process.exit(1);
}
if (kind === 'codex') out({ type: 'thread.started', thread_id: SID });
else out({ type: 'system', subtype: 'init', session_id: SID });
const claudeDelta = text => out({ type: 'stream_event', session_id: SID, event: { type: 'content_block_delta', delta: { type: 'text_delta', text } } });
const codexMessage = (id, text) => out({ type: 'item.completed', item: { id, type: 'agent_message', text } });
const say = (kind === 'codex' ? text => codexMessage('m' + Math.random(), text) : claudeDelta);
const finish = (ok = true, text = '') => kind === 'codex' ? out(ok ? { type: 'turn.completed' } : { type: 'turn.failed', error: { message: text } })
  : out({ type: 'result', session_id: SID, subtype: ok ? 'success' : 'error', is_error: !ok, result: text });

// The AFM tool bridge named in the provider's own config, as the real CLI would start it.
function bridgeSpec(name = 'afm') {
  if (kind === 'claude') {
    const file = args[args.indexOf('--mcp-config') + 1];
    return args.includes('--mcp-config') ? JSON.parse(readFileSync(file, 'utf8')).mcpServers[name] : undefined;
  }
  const prefix = `mcp_servers.${name}.`;
  const config = key => {
    const flag = args.find(item => item.startsWith(prefix + key + '='));
    return flag && JSON.parse(flag.slice(prefix.length + key.length + 1));
  };
  const command = config('command');
  return command && { command, args: config('args') };
}

const scenario = env.FAKE_SCENARIO ?? 'stream';
const pause = Number(env.FAKE_PAUSE ?? 120);
if (scenario === 'external-mcp') {
  const spec = bridgeSpec('external');
  if (!spec) {
    say('No external server');
  } else {
    const { Client } = await import('@modelcontextprotocol/sdk/client/index.js');
    const { StdioClientTransport } = await import('@modelcontextprotocol/sdk/client/stdio.js');
    const client = new Client({ name: 'fixture', version: '1' });
    await client.connect(new StdioClientTransport(spec));
    try {
      const { tools } = await client.listTools();
      const result = await client.callTool({ name: tools[0].name, arguments: { query: 'live' } });
      say(result.content[0].text);
    } finally { await client.close(); }
  }
  finish();
} else if (scenario === 'stream') {
  if (kind === 'claude') out({ type: 'stream_event', event: { type: 'message_start' } });
  for (const part of ['Hel', 'lo ', 'world']) { say(part); await delay(pause); }
  if (kind === 'claude') out({ type: 'assistant', message: { content: [{ type: 'text', text: 'Hello world' }] } });
  finish();
} else if (scenario === 'flood') {
  // Many small deltas with no pause: the shape of a fast model, used by the performance script.
  if (kind === 'claude') out({ type: 'stream_event', event: { type: 'message_start' } });
  for (let i = 0; i < Number(env.FAKE_DELTAS ?? 5000); i++) say('word' + i + ' ');
  finish();
} else if (scenario === 'native-fail') {
  // Does real work (a command or a tool) and only then fails, even with a message that looks like a rejected session.
  if (env.FAKE_MARKER) appendFileSync(env.FAKE_MARKER, 'ran\n');
  if (kind === 'codex') out({ type: 'item.completed', item: { id: 'c1', type: 'command_execution', command: 'touch x', status: 'completed' } });
  else out({ type: 'assistant', session_id: SID, message: { content: [{ type: 'tool_use', id: 't1', name: 'Bash', input: { command: 'touch x' } }] } });
  finish(false, resumed ? `No conversation found with session ID: ${resumed}` : 'boom');
  process.exit(1);
} else if (scenario === 'echo-session') {
  // Says its session id, then ends on the start of it, which only the end of the reply shows to be harmless.
  say('Session: ' + SID + ' ends ' + SID.slice(0, -1)); finish();
} else if (scenario === 'split-session') {
  // The id arrives in two deltas, further apart than Desktop's delta batching interval.
  const half = Math.ceil(SID.length / 2);
  say('Session: ' + SID.slice(0, half)); await delay(100);
  if (env.FAKE_HANG_AFTER_SPLIT === '1') setInterval(() => {}, 1000);
  else { say(SID.slice(half) + ' done'); finish(); }
} else if (scenario === 'split-around') {
  // The id is split around a tool call or, with FAKE_BETWEEN=approval, around a request for the user's approval
  // whose summary names the id and FAKE_SECRET, as the bridge does for a tool's arguments.
  const half = Math.ceil(SID.length / 2);
  say('Session: ' + SID.slice(0, half));
  if (env.FAKE_BETWEEN === 'approval') {
    const { askApproval } = await import('../src/assistant/approval.js');
    const spec = bridgeSpec();
    const policy = JSON.parse(readFileSync(spec.args[spec.args.indexOf('--policy') + 1], 'utf8'));
    const approved = await askApproval(policy.approvalSocket, { turnId: policy.turnId, tool: 'afm_run_cancel', kind: 'destructive', scope: 'All projects', summary: JSON.stringify({ note: `${SID} ${env.FAKE_SECRET}` }) });
    say(SID.slice(half) + (approved ? ' approved' : ' denied'));
  } else {
    if (kind === 'codex') {
      out({ type: 'item.started', item: { id: 'x1', type: 'mcp_tool_call', tool: 'afm_runs_list', arguments: {} } });
      out({ type: 'item.completed', item: { id: 'x1', type: 'mcp_tool_call', tool: 'afm_runs_list', result: { content: [{ type: 'text', text: 'none' }] } } });
    } else {
      out({ type: 'assistant', session_id: SID, message: { content: [{ type: 'tool_use', id: 'x1', name: 'mcp__afm__afm_runs_list', input: {} }] } });
      out({ type: 'user', session_id: SID, message: { content: [{ type: 'tool_result', tool_use_id: 'x1', is_error: false, content: 'none' }] } });
    }
    await delay(100);
    // FAKE_AFTER replaces the rest of the id with text that turns out not to be one ('' says nothing more).
    const after = env.FAKE_AFTER ?? SID.slice(half) + ' done';
    if (after) say(after);
  }
  finish();
} else if (scenario === 'session-prefix') {
  // The whole reply is the start of the session id; Claude repeats it in its result, as the real CLI does.
  const text = SID.slice(0, Number(env.FAKE_PREFIX ?? 1));
  say(text); finish(true, text);
} else if (scenario === 'leak-session') {
  // Names the session id (and FAKE_MENTION, an earlier one) in a tool call, its result, text and the error.
  const ids = [SID, env.FAKE_MENTION].filter(Boolean).join(' ');
  if (kind === 'codex') {
    out({ type: 'item.started', item: { id: 'x1', type: 'mcp_tool_call', tool: 'afm_runs_list', arguments: { note: ids } } });
    out({ type: 'item.completed', item: { id: 'x1', type: 'mcp_tool_call', tool: 'afm_runs_list', result: { content: [{ type: 'text', text: 'seen ' + ids }] } } });
  } else {
    out({ type: 'assistant', session_id: SID, message: { content: [{ type: 'tool_use', id: 'x1', name: 'mcp__afm__afm_runs_list', input: { note: ids } }] } });
    out({ type: 'user', session_id: SID, message: { content: [{ type: 'tool_result', tool_use_id: 'x1', is_error: false, content: 'seen ' + ids }] } });
  }
  say('Text ' + ids);
  finish(false, 'failed in ' + ids);
  process.exit(1);
} else if (scenario === 'stderr-session') {
  process.stderr.write('crashed in ' + [SID, env.FAKE_MENTION].filter(Boolean).join(' ') + '\n');
  process.exit(3);
} else if (scenario === 'fail') {
  process.stderr.write('boom ' + (env.FAKE_SECRET ?? '') + '\n');
  process.exit(3);
} else if (scenario === 'not-logged-in') {
  finish(false, 'Not logged in · Please run /login');
  process.exit(1);
} else if (scenario === 'hang') {
  say('started');
  process.on('SIGTERM', () => { if (env.FAKE_TERM_FILE) writeFileSync(env.FAKE_TERM_FILE, 'term'); process.exit(143); });
  setInterval(() => {}, 1000);
} else if (scenario === 'bridge') {
  // Acts like the provider does: starts the AFM tool bridge named in its own config and calls tools through it.
  const { Client } = await import('@modelcontextprotocol/sdk/client/index.js');
  const { StdioClientTransport } = await import('@modelcontextprotocol/sdk/client/stdio.js');
  const spec = bridgeSpec();
  const client = new Client({ name: 'fake-provider', version: '1' });
  await client.connect(new StdioClientTransport({ command: spec.command, args: spec.args, env: { ...env, ...(spec.env ?? {}) } }));
  const names = (await client.listTools()).tools.map(tool => tool.name);
  say('tools: ' + names.filter(name => /^(afm_run(s_list|_start|_cancel|_attach)|desktop_watch_\w+)$/.test(name)).sort().join(',') + '\n');
  if (env.FAKE_NATIVE_UNTIL) {
    const id = 'native-command';
    const command = type => out({ type, item: { id, type: 'command_execution' } });
    if (kind === 'codex') command('item.started');
    else out({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: 'Bash', input: {} }] } });
    const check = setInterval(() => {
      if (!existsSync(env.FAKE_NATIVE_UNTIL)) return;
      clearInterval(check);
      if (kind === 'codex') command('item.completed');
      else out({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, content: 'Done' }] } });
    }, 20);
    check.unref();
  }
  let n = 0;
  for (const call of JSON.parse(env.FAKE_CALLS ?? '[]')) {
    const id = 'call-' + (++n);
    if (kind === 'claude') out({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: 'mcp__afm__' + call.name, input: call.args }] } });
    const result = await client.callTool({ name: call.name, arguments: call.args });
    const text = result.content?.[0]?.text ?? '';
    if (kind === 'claude') out({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, is_error: Boolean(result.isError), content: text }] } });
    say(`${call.name}:${result.isError ? 'error' : 'ok'}:${text.replace(/\s*\n\s*/g, ' ')}\n`);
  }
  await client.close();
  finish();
}

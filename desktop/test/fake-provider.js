// A deterministic stand-in for the claude and codex CLIs. Behaviour comes from FAKE_* variables, and every
// real invocation records its argv, working directory and stdin so tests can inspect them.
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';

const [kind, ...args] = process.argv.slice(2);
const env = process.env;
const out = value => process.stdout.write(JSON.stringify(value) + '\n');
if (args[0] === 'app-server') {
  const { createInterface } = await import('node:readline');
  for await (const line of createInterface({input: process.stdin})) {
    const msg = JSON.parse(line);
    if (msg.method === 'initialize') out({id:msg.id, result:{}});
    if (msg.method === 'model/list') out({id:msg.id, result:{ data:[{model:'gpt-test', displayName:'Test model', isDefault:true}], nextCursor:null }});
  }
  process.exit(0);
}
const old = env.FAKE_CAPS === 'old';

const CLAUDE_HELP = `Usage: claude [options] [command] [prompt]
  -p, --print                           Print response and exit
  --output-format <format>              Output format (choices: "text", "json", "stream-json")
  --include-partial-messages            Include partial message chunks as they arrive
  --permission-mode <mode>              Permission mode to use for the session
                                        (choices: "acceptEdits", "auto",
                                        "bypassPermissions", "manual",
                                        "dontAsk", "plan")
  --dangerously-skip-permissions        Bypass all permission checks.
  --model <model>                       Model for the current session.
${old ? '' : '  --effort <level>                      Effort level for the current session\n                                        (low, medium, high, xhigh, max)\n'}  --mcp-config <configs...>             Load MCP servers from JSON files
  --strict-mcp-config                   Only use MCP servers from --mcp-config
  --allowedTools, --allowed-tools <tools...>
                                        Tools to allow
  --append-system-prompt <prompt>       Append a system prompt
`;
const CODEX_HELP = `Usage: codex exec [OPTIONS] [PROMPT]
  -c, --config <key=value>              Override a configuration value
  -m, --model <MODEL>                   Model the agent should use
  -s, --sandbox <SANDBOX_MODE>          Select the sandbox policy
${old ? '' : '      --dangerously-bypass-approvals-and-sandbox\n                                        Skip all confirmation prompts\n'}      --skip-git-repo-check             Allow running outside a Git repo
      --json                            Print events to stdout as JSONL
`;

const loggedIn = env.FAKE_AUTH !== 'none';
if (args.includes('--version')) { console.log('9.9.9 (fake)'); process.exit(0); }
if (kind === 'claude' && args[0] === 'auth') { out({ loggedIn }); process.exit(0); }
if (kind === 'codex' && args[0] === 'login') { console.log(loggedIn ? 'Logged in using ChatGPT' : 'Not logged in'); process.exit(loggedIn ? 0 : 1); }
if (args.includes('--help')) { process.stdout.write(kind === 'codex' ? CODEX_HELP : CLAUDE_HELP); process.exit(0); }

const stdin = await new Promise(resolve => { let data = ''; process.stdin.setEncoding('utf8'); process.stdin.on('data', chunk => { data += chunk; }); process.stdin.on('end', () => resolve(data)); });
if (env.FAKE_RECORD) appendFileSync(env.FAKE_RECORD, JSON.stringify({ kind, args, cwd: process.cwd(), stdin, hasSecret: Object.values(env).includes(env.FAKE_SECRET ?? '\0'), secretVars: Object.keys(env).filter(name => env[name] === env.FAKE_SECRET) }) + '\n');

const claudeDelta = text => out({ type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'text_delta', text } } });
const codexMessage = (id, text) => out({ type: 'item.completed', item: { id, type: 'agent_message', text } });
const say = (kind === 'codex' ? text => codexMessage('m' + Math.random(), text) : claudeDelta);
const finish = (ok = true, text = '') => kind === 'codex' ? out(ok ? { type: 'turn.completed' } : { type: 'turn.failed', error: { message: text } })
  : out({ type: 'result', subtype: ok ? 'success' : 'error', is_error: !ok, result: text });

const scenario = env.FAKE_SCENARIO ?? 'stream';
const pause = Number(env.FAKE_PAUSE ?? 120);
if (scenario === 'stream') {
  if (kind === 'claude') out({ type: 'stream_event', event: { type: 'message_start' } });
  for (const part of ['Hel', 'lo ', 'world']) { say(part); await delay(pause); }
  if (kind === 'claude') out({ type: 'assistant', message: { content: [{ type: 'text', text: 'Hello world' }] } });
  finish();
} else if (scenario === 'flood') {
  // Many small deltas with no pause: the shape of a fast model, used by the performance script.
  if (kind === 'claude') out({ type: 'stream_event', event: { type: 'message_start' } });
  for (let i = 0; i < Number(env.FAKE_DELTAS ?? 5000); i++) say('word' + i + ' ');
  finish();
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
  let spec;
  if (kind === 'claude') spec = JSON.parse(readFileSync(args[args.indexOf('--mcp-config') + 1], 'utf8')).mcpServers.afm;
  else {
    const config = key => JSON.parse(args.find(item => item.startsWith(`mcp_servers.afm.${key}=`)).slice(`mcp_servers.afm.${key}=`.length));
    spec = { command: config('command'), args: config('args') };
  }
  const client = new Client({ name: 'fake-provider', version: '1' });
  await client.connect(new StdioClientTransport({ command: spec.command, args: spec.args, env: { ...env, ...(spec.env ?? {}) } }));
  const names = (await client.listTools()).tools.map(tool => tool.name);
  say('tools: ' + names.filter(name => /^afm_run(s_list|_start|_cancel|_attach)$/.test(name)).sort().join(',') + '\n');
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

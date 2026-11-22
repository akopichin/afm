import { spawn } from 'node:child_process';
import { timingSafeEqual } from 'node:crypto';
import { realpathSync, statSync } from 'node:fs';
import { createServer } from 'node:http';
import path from 'node:path';
import readline from 'node:readline';
import { WebSocketServer } from 'ws';

const defaultWorkspace = realpathSync(process.env.ACP_WORKSPACE || '/workspace');
const workspaceRoot = realpathSync(process.env.ACP_WORKSPACE_ROOT || defaultWorkspace);
const token = process.env.ACP_BRIDGE_TOKEN || '';
const port = Number(process.env.ACP_PORT ?? 8765);
const host = process.env.ACP_LISTEN || '127.0.0.1';
if (!token && !['127.0.0.1', '::1', 'localhost'].includes(host)) {
  throw Error('Set ACP_BRIDGE_TOKEN before listening on a non-loopback interface.');
}
const { ACP_BRIDGE_TOKEN: _, ...environment } = process.env;
const server = createServer((_req, res) => res.writeHead(404).end());
const wss = new WebSocketServer({ noServer: true, maxPayload: 32 * 1024 * 1024, perMessageDeflate: false });
wss.on('headers', (headers, req) => {
  if (req.headers['x-afm-acp-workspace']) headers.push('X-AFM-ACP-Workspace: ' + req.headers['x-afm-acp-workspace']);
});
server.on('upgrade', (req, socket, head) => {
  if (req.url !== '/acp') return socket.end('HTTP/1.1 404 Not Found\r\nConnection: close\r\n\r\n');
  if (token) {
    const expected = Buffer.from('Bearer ' + token), actual = Buffer.from(req.headers.authorization || '');
    if (expected.length !== actual.length || !timingSafeEqual(expected, actual))
      return socket.end('HTTP/1.1 401 Unauthorized\r\nConnection: close\r\n\r\n');
  }
  let workspace;
  try {
    const requested = req.headers['x-afm-acp-workspace'];
    const directory = requested ? Buffer.from(requested, 'base64url').toString('utf8') : defaultWorkspace;
    if (!path.isAbsolute(directory)) throw Error('Workspace must be absolute');
    workspace = realpathSync(directory);
    const relative = path.relative(workspaceRoot, workspace);
    if (relative === '..' || relative.startsWith('..' + path.sep) || !statSync(workspace).isDirectory()) throw Error('Workspace outside the bridge root');
  } catch {
    return socket.end('HTTP/1.1 400 Invalid Workspace\r\nConnection: close\r\n\r\n');
  }
  wss.handleUpgrade(req, socket, head, ws => wss.emit('connection', ws, workspace));
});
wss.on('connection', (ws, workspace) => {
  const child = spawn(process.env.ACP_AGENT || 'claude-agent-acp', process.argv.slice(2), {
    cwd: workspace, env: environment, stdio: ['pipe', 'pipe', 'inherit'],
  });
  const output = readline.createInterface({ input: child.stdout });
  const close = () => {
    output.close();
    child.stdin.end();
    const timer = setTimeout(() => child.kill('SIGKILL'), 2000);
    timer.unref();
    child.once('exit', () => clearTimeout(timer));
  };
  child.on('error', () => ws.close(1011, 'Agent startup failed'));
  child.stdin.on('error', () => ws.close(1011, 'Agent disconnected'));
  child.once('close', () => ws.close(1000));
  ws.on('error', () => ws.terminate());
  ws.once('close', close);
  output.on('line', line => {
    if (ws.readyState !== ws.OPEN) return;
    if (ws.bufferedAmount > 32 * 1024 * 1024) ws.terminate();
    else ws.send(line);
  });
  ws.on('message', (frame, binary) => {
    if (binary) return ws.close(1003, 'Expected JSON text');
    let message;
    try { message = JSON.parse(frame.toString()); }
    catch { return ws.close(1007, 'Invalid JSON'); }
    if (!message || Array.isArray(message) || message.jsonrpc !== '2.0') return ws.close(1007, 'Invalid ACP message');
    if (['session/new', 'session/load', 'session/resume', 'session/fork'].includes(message.method)) {
      message.params = { ...message.params, cwd: workspace, mcpServers: [] };
    }
    if (child.stdin.writableLength > 32 * 1024 * 1024) ws.terminate();
    else child.stdin.write(JSON.stringify(message) + '\n');
  });
});
server.listen(port, host, () => console.log(`ACP bridge listening on port ${server.address().port}`));

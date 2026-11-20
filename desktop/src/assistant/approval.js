import net from 'node:net';
import { mkdtempSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const MAX_LINE = 16 * 1024;

// Approvals travel over a socket in a private temp directory: the provider's MCP bridge asks, the app window answers.
export class ApprovalServer {
  #server;
  #dir;
  constructor(onRequest) { this.onRequest = onRequest; }

  async listen() {
    if (this.address) return this.address;
    this.#dir = mkdtempSync(path.join(process.platform === 'darwin' ? '/tmp' : os.tmpdir(), 'afm-a-'));
    this.address = process.platform === 'win32' ? `\\\\.\\pipe\\afm-desktop-${path.basename(this.#dir)}` : path.join(this.#dir, 's');
    this.#server = net.createServer(socket => {
      let buffer = '';
      socket.setEncoding('utf8');
      socket.on('error', () => {});
      socket.on('data', async chunk => {
        buffer += chunk;
        if (buffer.length > MAX_LINE) return socket.destroy();
        const end = buffer.indexOf('\n');
        if (end < 0) return;
        let reply = { approved: false };
        try {
          const answer = await this.onRequest(JSON.parse(buffer.slice(0, end)), socket);
          // A local request (watch tools) answers with data, an approval with a yes or no.
          reply = answer !== null && typeof answer === 'object' ? { result: answer } : { approved: answer === true };
        } catch (error) { reply = { approved: false, error: String(error?.message ?? error).slice(0, 500) }; }
        socket.end(JSON.stringify(reply) + '\n');
      });
    });
    await new Promise((resolve, reject) => { this.#server.once('error', reject); this.#server.listen(this.address, resolve); });
    return this.address;
  }

  close() {
    this.#server?.close();
    this.#server = null;
    if (this.#dir) rmSync(this.#dir, { recursive: true, force: true });
    this.#dir = this.address = null;
  }
}

// Bridge side, for requests the app answers with data. Rejects with the app's reason, or when nobody answers.
export function askLocal(address, request, timeout = 30 * 1000) {
  return new Promise((resolve, reject) => {
    const socket = net.connect(address);
    let buffer = '';
    const finish = (error, value) => { clearTimeout(timer); socket.destroy(); error ? reject(error) : resolve(value); };
    const timer = setTimeout(() => finish(Error('Desktop did not answer in time.')), timeout);
    socket.setEncoding('utf8');
    socket.on('connect', () => socket.write(JSON.stringify(request) + '\n'));
    socket.on('data', chunk => {
      buffer += chunk;
      if (!buffer.includes('\n')) return;
      try {
        const reply = JSON.parse(buffer);
        if (reply.error) finish(Error(reply.error));
        else finish(null, reply.result);
      } catch { finish(Error('Desktop sent an unreadable answer.')); }
    });
    socket.on('error', () => finish(Error('Desktop is not reachable.')));
    socket.on('close', () => finish(Error('Desktop closed the connection.')));
  });
}

// Bridge side. No answer, a broken socket or a timeout all mean "not approved".
export function askApproval(address, request, timeout = 10 * 60 * 1000) {
  return new Promise(resolve => {
    const socket = net.connect(address);
    let buffer = '';
    const finish = approved => { clearTimeout(timer); socket.destroy(); resolve(approved); };
    const timer = setTimeout(() => finish(false), timeout);
    socket.setEncoding('utf8');
    socket.on('connect', () => socket.write(JSON.stringify(request) + '\n'));
    socket.on('data', chunk => {
      buffer += chunk;
      if (!buffer.includes('\n')) return;
      try { finish(JSON.parse(buffer).approved === true); } catch { finish(false); }
    });
    socket.on('error', () => finish(false));
    socket.on('close', () => finish(false));
  });
}

import WebSocket from 'ws';
import { ndJsonStream } from '@agentclientprotocol/sdk';

const MAX_FRAME = 32 * 1024 * 1024;

export function connectRemoteAcp(app, { endpoint, remoteCwd }, token, confirmWorkspace) {
  const workspace = remoteCwd ? Buffer.from(remoteCwd).toString('base64url') : '';
  const workspaceHeader = 'x-afm-acp-workspace';
  const socket = new WebSocket(endpoint, {
    headers: { ...(token ? { Authorization: 'Bearer ' + token } : {}), ...(workspace ? { [workspaceHeader]: workspace } : {}) },
    handshakeTimeout: 10_000,
    followRedirects: false,
    maxPayload: MAX_FRAME,
    perMessageDeflate: false,
  });
  const ready = new Promise((resolve, reject) => {
    if (confirmWorkspace) socket.once('upgrade', response => {
      if (response.headers[workspaceHeader] !== workspace) reject(Error('The ACP bridge did not confirm the requested workspace. Update the bridge first.'));
    });
    socket.once('open', resolve);
    socket.once('error', error => reject(confirmWorkspace && error.message === 'Unexpected server response: 400'
      ? Error('The workspace must be an existing directory inside the ACP bridge workspace root.') : error));
    socket.once('close', () => reject(Error('ACP bridge disconnected.')));
  });
  ready.catch(() => {});
  const input = new ReadableStream({
    start(controller) {
      socket.on('message', (data, binary) => {
        if (binary || controller.desiredSize <= 0) socket.terminate();
        else controller.enqueue(Buffer.from(data.toString() + '\n'));
      });
      socket.once('close', () => { try { controller.close(); } catch {} });
    },
    cancel() { socket.terminate(); },
  }, { highWaterMark: 16 });
  const output = new WritableStream({
    async write(chunk) {
      await ready;
      await new Promise((resolve, reject) => {
        socket.send(Buffer.from(chunk).toString().trimEnd(), error => error ? reject(error) : resolve());
      });
    },
  });
  const connection = app.connect(ndJsonStream(output, input));
  socket.on('error', () => connection.close());
  socket.on('close', () => connection.close());
  const stop = async () => {
    connection.close();
    socket.terminate();
  };
  return { agent: connection.agent, ready, stop };
}

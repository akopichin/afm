import net from 'node:net';
import { readFileSync } from 'node:fs';
import { StringDecoder } from 'node:string_decoder';
import { lineFramer, pipeFramer } from './wire.js';

// Version of the broker handshake. The Desktop broker refuses a companion that speaks another one.
export const PROTOCOL = 1;

// AFM starts the "Chrome" executable with its CDP pipe on fd 3 (requests) and fd 4 (replies) and a private
// profile directory named <epoch>__<stage>. This process is that executable: it forwards the pipe to the
// Desktop broker, which backs the stage with a page inside the app.
export function parseLaunch(argv) {
  const option = name => argv.find(arg => arg.startsWith(name + '='))?.slice(name.length + 1);
  const profile = option('--user-data-dir');
  const start = argv.findLast(arg => /^https?:\/\//.test(arg));
  return { broker: option('--desktop-broker'), profile, url: start ?? 'about:blank' };
}

export function readBroker(file) {
  const info = JSON.parse(readFileSync(file, 'utf8'));
  if (typeof info.socket !== 'string' || typeof info.token !== 'string') throw Error('Unreadable Desktop broker file.');
  return info;
}

// Bridges the pipe to the broker until either side ends. Resolves with the process exit code.
export function bridge({ launch, broker, input, output, cwd = process.cwd(), pid = process.ppid }) {
  return new Promise(resolve => {
    const socket = net.connect(broker.socket);
    const replies = lineFramer(), requests = pipeFramer();
    const fromSocket = new StringDecoder('utf8'), fromPipe = new StringDecoder('utf8');
    let ready = false, finished = false;
    const finish = code => {
      if (finished) return;
      finished = true;
      socket.destroy();
      input.destroy();
      output.destroy();
      resolve(code);
    };
    socket.on('connect', () => socket.write(JSON.stringify({ hello: { protocol: PROTOCOL, token: broker.token, profile: launch.profile, url: launch.url, cwd, pid } }) + '\n'));
    socket.on('data', chunk => {
      try {
        for (const line of replies.push(fromSocket.write(chunk))) {
          if (!ready) {
            const answer = JSON.parse(line);
            if (!answer.ready) { process.stderr.write('AFM Desktop refused the stage browser: ' + (answer.error ?? 'unknown') + '\n'); return finish(1); }
            ready = true;
            input.on('data', data => {
              try { for (const message of requests.push(fromPipe.write(data))) socket.write(message + '\n'); }
              catch { finish(1); }
            });
          } else output.write(line + '\0');
        }
      } catch { finish(1); }
    });
    socket.on('error', error => { process.stderr.write('AFM Desktop is not reachable: ' + error.message + '\n'); finish(1); });
    socket.on('close', () => finish(ready ? 0 : 1));
    input.on('end', () => finish(0));
    input.on('error', () => finish(1));
    output.on('error', () => finish(1));
  });
}

import { connect } from 'node:net';

const [address] = process.argv.slice(2);
const socket = connect(address);
const timer = setTimeout(() => socket.destroy(), 90000);
socket.on('error', () => {});
socket.on('connect', () => socket.write('unlock\n'));
let response = '';
socket.setEncoding('utf8');
socket.on('data', chunk => {
  response += chunk;
  if (response.length > 32768) socket.destroy();
});
socket.on('close', hadError => {
  clearTimeout(timer);
  process.exitCode = 1;
  if (hadError || response.length > 32768) return;
  try {
    const secret = JSON.parse(response);
    if (typeof secret === 'string' && !/[\r\n\0]/.test(secret)) {
      process.stdout.write(secret + '\n');
      process.exitCode = 0;
    }
  } catch { /* A cancelled or interrupted prompt has no answer. */ }
});

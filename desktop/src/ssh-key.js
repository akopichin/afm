import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { createServer } from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { writeLauncherScript } from './launcher-script.js';

export const sshAuthFailure = (location, message) =>
  /^(?:ssh:\/\/|git@)/.test(location)
  && /permission denied \([^)]*publickey|agent refused operation|could not read passphrase/i.test(message);

export async function retryWithSSHKey(operation, {location, env, unlock}) {
  try { return await operation(); }
  catch (error) {
    if (!unlock || !sshAuthFailure(location, error.stderr || '')) throw error;
    if (!await unlock({location, env})) throw Error('SSH key unlock cancelled.');
    return operation();
  }
}

// Only the local key-loading process receives the answer. Git keeps using the user's SSH agent and host policy.
export async function unlockSSHKey({ key, env, launcher, ask, command = 'ssh-add' }) {
  const directory = await mkdtemp(path.join(process.platform === 'darwin' ? '/tmp' : os.tmpdir(), 'afm-ssh-'));
  const address = path.join(directory, 's');
  const pending = new Set();
  let cancelled = false;
  const server = createServer(socket => {
    pending.add(socket);
    socket.on('error', () => {});
    socket.on('close', () => pending.delete(socket));
    socket.setTimeout(90000, () => socket.destroy());
    let request = '';
    socket.setEncoding('utf8');
    socket.on('data', async chunk => {
      request += chunk;
      if (request.length > 16) { socket.destroy(); return; }
      if (!request.endsWith('\n')) return;
      socket.removeAllListeners('data');
      if (request !== 'unlock\n') { socket.destroy(); return; }
      let answer = null;
      try { answer = await ask(key); } catch { /* Closed windows cancel the prompt. */ }
      if (typeof answer !== 'string' || answer.length > 4096 || /[\r\n\0]/.test(answer)) {
        cancelled = true;
        answer = null;
      }
      socket.end(JSON.stringify(answer));
    });
  });
  try {
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(address, resolve);
    });
    const wrapper = await writeLauncherScript(path.join(directory, 'askpass'), launcher, [address]);
    const code = await new Promise(resolve => {
      const child = spawn(command, [key], {
        stdio: ['ignore', 'ignore', 'ignore'], windowsHide: true,
        env: { ...env, SSH_ASKPASS: wrapper, SSH_ASKPASS_REQUIRE: 'force' },
      });
      const timer = setTimeout(() => child.kill(), 100000);
      child.once('error', () => { clearTimeout(timer); resolve(null); });
      child.once('close', status => { clearTimeout(timer); resolve(status); });
    });
    if (cancelled) return false;
    if (code !== 0) throw Error('Could not unlock the SSH key. Check the selected key, its passphrase, and that your SSH agent is running.');
    return true;
  } finally {
    for (const socket of pending) socket.destroy();
    if (server.listening) await new Promise(resolve => server.close(resolve));
    await rm(directory, { recursive:true, force:true });
  }
}

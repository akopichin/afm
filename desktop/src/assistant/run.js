import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';

const STDERR_LIMIT = 4096;
const KILL_GRACE_MS = 2000;

// Runs one provider process and feeds each output line through `parser`. Cancelling stops the whole process group,
// so a provider's own children do not outlive the turn.
export function runProcess({ command, args, cwd, env, input, parser, onEvent, grace = KILL_GRACE_MS }) {
  const posix = process.platform !== 'win32';
  const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'], detached: posix, windowsHide: true });
  let cancelled = false, yielding = false, stderr = '', spawnError = null, killTimer;
  child.on('error', error => { spawnError = error; });
  child.stdin.on('error', () => {});
  child.stdin.end(input);
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', chunk => { stderr = (stderr + chunk).slice(-STDERR_LIMIT); });
  const lines = createInterface({ input: child.stdout });
  lines.on('line', line => { for (const event of parser.parse(line)) onEvent(event); });
  const signal = (name, group) => {
    if (!child.pid) return;
    try { process.kill(posix && group ? -child.pid : child.pid, name); } catch { /* Already gone. */ }
  };
  const stop = group => {
    if (cancelled) return;
    cancelled = true;
    signal('SIGTERM', group);
    killTimer = setTimeout(() => signal('SIGKILL', group), grace);
  };
  let linesClosed = false;
  lines.on('close', () => { linesClosed = true; });
  const done = new Promise(resolve => {
    child.on('exit', (code, exitSignal) => {
      if (!yielding) return;
      // A command may still hold one of the inherited pipes after the provider exits.
      clearTimeout(killTimer);
      child.stdout.unref?.();
      child.stderr.unref?.();
      resolve({ code, signal: exitSignal, cancelled, stderr, error: spawnError });
    });
    child.on('close', (code, exitSignal) => {
      clearTimeout(killTimer);
      const finish = () => resolve({ code, signal: exitSignal, cancelled, stderr, error: spawnError });
      // Output buffered before exit is delivered before the turn is reported finished.
      if (linesClosed) finish(); else lines.once('close', finish);
    });
  });
  return {
    done,
    cancel: () => stop(true),
    // Yielding a provider must leave commands it already launched alone.
    yield() { yielding = true; stop(false); },
  };
}

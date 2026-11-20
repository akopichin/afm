import path from 'node:path';
import { desktopDir } from '../files.js';
import { writeLauncherScript } from '../launcher-script.js';

export const wrapperPath = home => path.join(desktopDir(home), 'bin', 'afm-stage-chrome');

// AFM launches the "Chrome" executable named by AFM_CHROME_EXECUTABLE. This shell wrapper starts the companion
// with the app's own runtime (no Node install needed) and hands it the broker file; it holds no secret.
export const writeWrapper = (home, launcher, broker) => writeLauncherScript(wrapperPath(home), launcher, ['--desktop-broker=' + broker]);

// AFM looks for `chrome-devtools-mcp` on PATH and never installs it; this wrapper runs the copy shipped in the
// app with the app's own runtime, so a clean Mac needs no Node or npm.
export const mcpWrapperPath = home => path.join(desktopDir(home), 'bin', 'chrome-devtools-mcp');
export const writeMcpWrapper = (home, mcp) => writeLauncherScript(mcpWrapperPath(home), mcp);

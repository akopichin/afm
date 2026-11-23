import { randomUUID } from 'node:crypto';
import { readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { parse } from 'yaml';
import { desktopDir, Mutex, readJSON, writeJSON } from '../files.js';
import { belongsTo } from '../projects.js';
import { findExecutable } from './detect.js';

const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
const SECRET_ENV = /TOKEN|PASSWORD|SECRET|CREDENTIAL|(?:^|_)KEY(?:_|$)/i;
const UUID = /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/;
const LOCAL_HOSTS = ['localhost', '127.0.0.1', '[::1]'];
const untested = { state: 'untested' };

function filePath(value) {
  return typeof value === 'string' && !value.includes('\0') && (path.isAbsolute(value) || value.startsWith('~/'));
}

function environmentEntry(item) {
  const { name, ref, value, file } = item ?? {};
  if (typeof name !== 'string' || !ENV_NAME.test(name)) throw Error('Enter a valid environment variable name.');
  if (filePath(file)) return { name, file };
  if (typeof ref === 'string' && ENV_NAME.test(ref)) return { name, ref };
  if (typeof value === 'string' && value.length <= 4096 && !value.includes('\0')) {
    if (SECRET_ENV.test(name)) throw Error(`Use a file or Desktop environment variable for ${name}.`);
    return { name, value };
  }
  throw Error(`Enter an environment value, file, or Desktop variable for ${name}.`);
}

function validate(input) {
  const name = String(input.name ?? '').trim();
  if (!/^[a-zA-Z0-9_-]{1,64}$/.test(name) || name.toLowerCase() === 'afm')
    throw Error('Use 1–64 letters, numbers, hyphens or underscores. AFM is reserved.');
  if (input.projectId && typeof input.projectId !== 'string') throw Error('Choose a registered project.');
  const tokenFile = input.tokenFile || '';
  if (tokenFile && !filePath(tokenFile)) throw Error('Enter an absolute token file path or a path beginning with ~/.');
  const base = { name, transport: input.transport, projectId: input.projectId || null, tokenFile };
  if (input.transport === 'http') {
    let url;
    try { url = new URL(input.url); } catch { throw Error('Enter the MCP server URL.'); }
    if (url.username || url.password || url.hash || url.search ||
      !(url.protocol === 'https:' || (url.protocol === 'http:' && LOCAL_HOSTS.includes(url.hostname))))
      throw Error('Use HTTPS, or HTTP on loopback. Enter credentials in the token field.');
    return { ...base, url: url.href };
  }
  if (input.transport !== 'stdio') throw Error('Select an MCP transport.');
  const command = String(input.command ?? '').trim();
  if ((!path.isAbsolute(command) && !/^[\w.-]+$/.test(command)) || ['.', '..'].includes(command) || command.includes('\0'))
    throw Error('Enter an executable name from PATH or an absolute path.');
  const { args = [], env = [], tokenEnv = '' } = input;
  if (!Array.isArray(args) || args.length > 64 || args.some(arg => typeof arg !== 'string' || arg.length > 4096 || arg.includes('\0')))
    throw Error('Enter up to 64 arguments, one per line.');
  if (!Array.isArray(env) || env.length > 64) throw Error('Enter up to 64 environment variables.');
  const variables = env.map(environmentEntry);
  if (typeof tokenEnv !== 'string' || (tokenEnv && !ENV_NAME.test(tokenEnv))) throw Error('Enter a valid token environment variable name.');
  const names = new Set(variables.map(item => item.name));
  if (names.size !== env.length || names.has(tokenEnv)) throw Error('Use each environment variable name once.');
  if (tokenFile && !tokenEnv) throw Error('Enter the environment variable that receives the token.');
  return { ...base, command, args, env: variables, tokenEnv };
}

const placeholder = /^\{YOUR_[A-Z0-9_]+\}$/i;
function openCodeServers(text, projectId) {
  let data;
  if (typeof text !== 'string' || text.length > 128 * 1024) throw Error('Paste an OpenCode MCP configuration up to 128 KB.');
  try { data = parse(text, { maxAliasCount: 0 }); } catch { throw Error('Could not parse the OpenCode configuration.'); }
  if (!data?.mcp || typeof data.mcp !== 'object' || Array.isArray(data.mcp))
    throw Error('The configuration must contain an mcp object.');
  const servers = [];
  for (const [name, entry] of Object.entries(data.mcp)) {
    if (entry?.enabled === false) continue;
    if (entry?.type !== 'local' || !Array.isArray(entry.command) || !entry.command.length)
      throw Error(`Import expects an OpenCode local MCP server for "${name}".`);
    const [command, ...args] = entry.command;
    const env = Object.entries(entry.environment ?? {}).map(([name, value]) => {
      if (typeof value !== 'string') throw Error(`Enter a string for ${name}.`);
      if (placeholder.test(value)) return { name, ref: name };
      const file = /^\{file:(.+)\}$/.exec(value);
      if (file) return { name, file: file[1] };
      const ref = /^\{env:(.+)\}$/.exec(value);
      return ref ? { name, ref: ref[1] } : { name, value };
    });
    servers.push(validate({ name, transport: 'stdio', projectId, command, args, env }));
  }
  if (!servers.length) throw Error('No enabled local MCP servers found.');
  return servers;
}

export class McpServers {
  constructor({ home, secrets, project }) {
    Object.assign(this, { home, secrets, project });
    this.file = path.join(desktopDir(home), 'mcp.json');
    this.lock = new Mutex();
    this.statuses = new Map();
  }

  async #read() {
    const stored = await readJSON(this.file, []);
    const servers = [];
    for (const item of Array.isArray(stored) ? stored : []) {
      if (!UUID.test(item?.id) || !UUID.test(item.revision)) continue;
      const { id, revision, tokenRef } = item;
      if (tokenRef !== null && !(typeof tokenRef === 'string' && tokenRef.startsWith('mcp-') && UUID.test(tokenRef.slice(4)))) continue;
      try { servers.push({ ...validate(item), id, revision, tokenRef }); } catch { /* Ignore invalid hand-edited entries. */ }
    }
    return servers;
  }

  async list() {
    return (await this.#read()).map(({ tokenRef, revision, ...server }) => ({
      ...server, hasToken: Boolean(tokenRef), status: this.statuses.get(revision)?.status ?? untested,
    }));
  }

  save(input) {
    return this.lock.run(async () => {
      const server = validate(input);
      if (server.projectId) await this.project(server.projectId);
      const servers = await this.#read();
      const previous = servers.find(item => item.id === input.id);
      if (input.id && !previous) throw Error('This MCP server was removed.');
      if (servers.some(item => item.id !== input.id && item.name.toLowerCase() === server.name.toLowerCase()))
        throw Error('This MCP server name is already in use.');
      const token = input.token ?? '';
      if (typeof token !== 'string' || token.length > 4096 || token.includes('\0') ||
        (token && server.transport === 'http' && !/^[A-Za-z0-9._~+/-]+=*$/.test(token))) throw Error('Enter a valid MCP token.');
      const destinationChanged = previous && (previous.transport !== server.transport || previous.url !== server.url ||
        previous.command !== server.command || JSON.stringify(previous.args) !== JSON.stringify(server.args) || previous.tokenEnv !== server.tokenEnv);
      const existingRef = input.removeToken || server.tokenFile ? null : previous?.tokenRef;
      if (token && server.tokenFile) throw Error('Choose either a pasted token or a token file.');
      if (existingRef && destinationChanged && !token) throw Error('Enter the token again or remove it when changing the server destination.');
      if ((token || existingRef) && server.transport === 'stdio' && !server.tokenEnv)
        throw Error('Enter the environment variable that receives the token.');
      const revision = randomUUID();
      const tokenRef = token ? 'mcp-' + revision : existingRef ?? null;
      if (token) {
        if (!this.secrets) throw Error('Secure storage is unavailable.');
        try { await this.secrets.set(tokenRef, token); } catch { throw Error('Could not save the token in secure storage.'); }
      }
      const next = { ...server, id: previous?.id ?? randomUUID(), revision, tokenRef };
      const updated = previous ? servers.map(item => item.id === next.id ? next : item) : [...servers, next];
      try { await writeJSON(this.file, updated); }
      catch (error) {
        if (token) await this.secrets.remove(tokenRef).catch(() => {});
        throw error;
      }
      if (previous) {
        this.statuses.delete(previous.revision);
        if (previous.tokenRef && previous.tokenRef !== tokenRef) await this.secrets.remove(previous.tokenRef);
      }
      return this.list();
    });
  }

  importOpenCode(text, projectId) {
    return this.lock.run(async () => {
      const incoming = openCodeServers(text, projectId);
      if (projectId) await this.project(projectId);
      const servers = await this.#read();
      const names = new Set(servers.map(item => item.name.toLowerCase()));
      for (const server of incoming) {
        const name = server.name.toLowerCase();
        if (names.has(name)) throw Error(`MCP server "${server.name}" already exists.`);
        names.add(name);
      }
      await writeJSON(this.file, [...servers, ...incoming.map(server => ({ ...server, id: randomUUID(), revision: randomUUID(), tokenRef: null }))]);
      return this.list();
    });
  }

  remove(id) {
    return this.lock.run(async () => {
      const servers = await this.#read();
      const server = servers.find(item => item.id === id);
      if (server) {
        await writeJSON(this.file, servers.filter(item => item.id !== id));
        this.statuses.delete(server.revision);
        if (server.tokenRef) await this.secrets.remove(server.tokenRef);
      }
      return this.list();
    });
  }

  async #readSecret(file, label) {
    try {
      const resolved = file.startsWith('~/') ? path.join(this.home, file.slice(2)) : file;
      const info = await stat(resolved);
      if (!info.isFile() || info.size > 8192) throw Error();
      const value = (await readFile(resolved, 'utf8')).trim();
      if (!value || value.length > 4096 || value.includes('\0')) throw Error();
      return value;
    } catch { throw Error(`Could not read ${label} from its file.`); }
  }

  async #resolve(server, env) {
    let token;
    if (server.tokenRef) {
      try { token = await this.secrets.get(server.tokenRef); }
      catch { throw Error('Could not read the saved token.'); }
    }
    if (server.tokenFile) token = await this.#readSecret(server.tokenFile, 'the token');
    if (server.transport === 'http') {
      if (token && !/^[A-Za-z0-9._~+/-]+=*$/.test(token)) throw Error('The token is invalid for HTTP Bearer authentication.');
      return { config: { type: 'http', name: server.name, url: server.url,
        headers: token ? [{ name: 'Authorization', value: 'Bearer ' + token }] : [] }, secrets: token ? [token] : [] };
    }
    const secrets = token ? [token] : [];
    const variables = [];
    for (const { name, ref, file, value } of server.env) {
      if (ref && (!Object.hasOwn(env, ref) || typeof env[ref] !== 'string')) throw Error(`Environment variable ${ref} is missing.`);
      const resolved = file ? await this.#readSecret(file, name) : ref ? env[ref] : value;
      if ((file || ref) && resolved) secrets.push(resolved);
      variables.push({ name, value: resolved });
    }
    const command = path.isAbsolute(server.command) ? server.command : await findExecutable(server.command, (env.PATH ?? '').split(path.delimiter).filter(dir => path.isAbsolute(dir)));
    if (!command) throw Error('The MCP executable was not found in PATH.');
    if (token) variables.push({ name: server.tokenEnv, value: token });
    return { config: { name: server.name, command, args: server.args, env: variables }, secrets };
  }

  async forScope(scope, env) {
    return this.lock.run(async () => {
      const configs = [], secrets = [], notices = [], revisions = [];
      let usesEnvironment = false, hasFileCredentials = false;
      for (const server of await this.#read()) {
        if (server.projectId && server.projectId !== scope.projectId) {
          if (scope.kind !== 'run') continue;
          const project = await this.project(server.projectId).catch(() => null);
          if (!project || !belongsTo(project.path, scope.projectRoot)) continue;
        }
        try {
          const resolved = await this.#resolve(server, env);
          configs.push(resolved.config);
          secrets.push(...resolved.secrets);
          revisions.push(server.revision);
          usesEnvironment ||= Boolean(server.env?.some(item => item.ref) || server.command && !path.isAbsolute(server.command));
          hasFileCredentials ||= Boolean(server.tokenFile || server.env?.some(item => item.file));
        } catch (error) { notices.push(`MCP server "${server.name}" was skipped. ${error.message}`); }
      }
      return { configs, secrets, notices, revisions, usesEnvironment, hasFileCredentials };
    });
  }

  async test(id, env) {
    const { pending } = await this.lock.run(async () => {
      const server = (await this.#read()).find(item => item.id === id);
      if (!server) throw Error('This MCP server was removed.');
      const existing = this.statuses.get(server.revision);
      if (existing?.pending) return existing;
      const checking = { status: { state: 'checking' } };
      this.statuses.set(server.revision, checking);
      // Capture the credential while the configuration is locked so an edit cannot remove it mid-read.
      const prepared = await this.#resolve(server, env).catch(error => ({ error: error.message }));
      checking.pending = (async () => {
        let status;
        try {
          if (prepared.error) throw Error(prepared.error);
          const cwd = server.projectId ? (await this.project(server.projectId)).path : this.home;
          status = await checkConnection(prepared.config, env, cwd);
        } catch { status = { state: 'error', detail: prepared.error ?? 'The MCP check could not start.' }; }
        checking.status = { ...status, checkedAt: new Date().toISOString() };
        checking.pending = null;
      })();
      return checking;
    });
    await pending;
    return this.list();
  }
}

async function checkConnection(config, env, cwd) {
  const signal = AbortSignal.timeout(config.type === 'http' ? 8000 : 30000);
  const transport = config.type === 'http'
    ? new StreamableHTTPClientTransport(new URL(config.url), {
      requestInit: { headers: Object.fromEntries(config.headers.map(({ name, value }) => [name, value])), redirect: 'error' },
      fetch: (url, init) => fetch(url, { ...init, signal: init?.signal ? AbortSignal.any([init.signal, signal]) : signal }),
    })
    : new StdioClientTransport({ command: config.command, args: config.args, cwd, stderr: 'ignore',
      env: { ...(env.PATH ? { PATH: env.PATH } : {}), ...Object.fromEntries(config.env.map(({ name, value }) => [name, value])) } });
  const client = new Client({ name: 'afm-desktop', version: '1' });
  try {
    await client.connect(transport, { signal });
    return { state: 'connected' };
  } catch (error) {
    const detail = signal.aborted ? 'The MCP handshake timed out.'
      : [401, 403].includes(error.code) ? 'The server rejected authentication.'
        : 'The MCP handshake failed. Check the server settings and make sure it is reachable.';
    return { state: 'error', detail };
  } finally {
    if (transport instanceof StreamableHTTPClientTransport && transport.sessionId) await transport.terminateSession().catch(() => {});
    await client.close().catch(() => {});
  }
}

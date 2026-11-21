import { execFile } from 'node:child_process';
import { lstat, realpath } from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';

const execute = promisify(execFile);
const MAX_FILES = 1000;
const MAX_PATCH = 384 * 1024;
const sha = value => typeof value === 'string' && /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(value);
const within = (root, candidate) => candidate === root || candidate.startsWith(root + path.sep);
const safePath = name => typeof name === 'string' && name.length <= 4096 && !name.includes('\0') &&
  name.split('/').every(part => part && part !== '.' && part !== '..' && part.toLowerCase() !== '.git');

async function git(root, args, { limit = 512 * 1024, differences = false, config = [] } = {}) {
  const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('GIT_')));
  Object.assign(env, { GIT_OPTIONAL_LOCKS: '0', GIT_LITERAL_PATHSPECS: '1', LC_ALL: 'C' });
  const command = ['--no-pager', '-C', root, '-c', 'core.fsmonitor=false', '-c', 'core.quotePath=false', ...config, ...args];
  try {
    return (await execute('git', command, { env, timeout: 5000, maxBuffer: limit })).stdout;
  } catch (error) {
    if (differences && error.code === 1) return error.stdout;
    if (error.code === 'ERR_CHILD_PROCESS_STDIO_MAXBUFFER') throw Error('Git output exceeds the viewer limit.');
    throw Error('Git could not read the run checkout.');
  }
}

async function withoutFilters(root) {
  const keys = await git(root, ['config', '--null', '--name-only', '--get-regexp', '^filter\\..*\\.(clean|smudge|process|required)$'], {differences:true});
  return [...new Set(keys.split('\0').filter(Boolean))].flatMap(key => ['-c', key + (key.endsWith('.required') ? '=false' : '=')]);
}

async function ref(root, name) {
  const result = await git(root, ['rev-parse', '--verify', name + '^{commit}']).catch(() => '');
  return sha(result.trim()) ? result.trim() : null;
}

export async function captureRunGit(plan) {
  const { git: initial, effective, project } = plan;
  if (!initial.git || !sha(initial.head) || !['branch', 'worktree'].includes(effective.mode)) return null;
  if (effective.mode === 'branch') return { root: initial.root, base: await ref(project.path, 'HEAD'), dirty: initial.dirty };
  const candidate = path.join(project.path, '.worktrees', effective.worktree);
  const checkout = await realpath(candidate).catch(() => null);
  if (checkout && checkout === candidate) {
    const common = await git(checkout, ['rev-parse', '--path-format=absolute', '--git-common-dir']).catch(() => '');
    const parentCommon = await git(project.path, ['rev-parse', '--path-format=absolute', '--git-common-dir']).catch(() => '');
    if (common && common === parentCommon) {
      const base = await ref(checkout, 'HEAD');
      const changes = await withoutFilters(checkout).then(config => git(checkout, ['status', '--porcelain', '-z'], {config})).catch(() => '?');
      return { root: initial.root, checkout, base, dirty: changes.length > 0 };
    }
    return null;
  }
  return checkout ? null : { root: initial.root, checkout: candidate, base: await ref(project.path, 'HEAD'), dirty: false };
}

const diffFlags = ['diff', '--no-color', '--no-ext-diff', '--no-textconv', '--no-renames', '--ignore-submodules=all'];
const unixPath = name => name.split('/').join(path.sep);

export async function listRunChanges({ root, projectRoot, branch, kind, start, mode }) {
  const checkout = await realpath(root).catch(() => null);
  const project = await realpath(projectRoot).catch(() => null);
  if (!checkout || !project || !within(project, checkout)) throw Error('The saved run checkout is unavailable.');
  const top = (await git(checkout, ['rev-parse', '--show-toplevel'])).trim();
  if (await realpath(top) !== checkout) throw Error('The saved run checkout is not a Git root.');
  const common = await git(checkout, ['rev-parse', '--path-format=absolute', '--git-common-dir']);
  const projectCommon = await git(project, ['rev-parse', '--path-format=absolute', '--git-common-dir']);
  if (common !== projectCommon) throw Error('The saved checkout belongs to a different Git repository.');

  const head = await ref(checkout, 'HEAD');
  const currentBranch = (await git(checkout, ['branch', '--show-current'])).trim();
  const tip = branch ? await ref(checkout, 'refs/heads/' + branch) : head;
  if (!tip) throw Error('The run branch is unavailable.');
  const working = currentBranch === (branch ?? '') && head === tip;
  const targetRef = await ref(checkout, 'refs/heads/main') ?? await ref(checkout, 'refs/remotes/origin/main');
  const baseStart = start && sha(start.base) && await ref(checkout, start.base);
  const mainBase = targetRef && (await git(checkout, ['merge-base', tip, targetRef]).catch(() => '')).trim();
  const choices = { start: Boolean(baseStart), main: sha(mainBase) };
  const comparison = (mode === 'start' || mode === 'main') && choices[mode] ? mode : choices.start ? 'start' : 'main';
  if (!choices[comparison]) throw Error('No run-start commit or main merge base is available.');
  const base = comparison === 'start' ? baseStart : mainBase;
  const range = working ? [base] : [base, tip];
  const config = working ? await withoutFilters(checkout) : [];
  const [tracked, untracked] = await Promise.all([
    git(checkout, [...diffFlags, '--name-status', '-z', ...range, '--'], {config}),
    working ? git(checkout, ['ls-files', '--others', '--exclude-standard', '-z', '--']) : '',
  ]);
  const entries = new Map();
  const records = tracked.split('\0');
  for (let i = 0; i + 1 < records.length; i += 2) {
    const [status, name] = [records[i], records[i + 1]];
    if (safePath(name)) entries.set(name, { path: name, status: status[0] });
  }
  for (const name of untracked.split('\0')) {
    if (safePath(name) && !entries.has(name)) entries.set(name, { path: name, status: 'A', untracked: true });
  }
  const files = [...entries.values()].sort((a, b) => a.path.localeCompare(b.path));
  return { checkout, branch: branch ?? currentBranch, head, tip, working, kind, base, comparison, choices,
    dirty: Boolean(start?.dirty), truncated: files.length > MAX_FILES, files: files.slice(0, MAX_FILES) };
}

export async function readRunPatch(snapshot, name) {
  const entry = snapshot.files.find(file => file.path === name);
  if (!entry || !safePath(name)) throw Error('Select a file from the changed-files list.');
  const { checkout, working, head, base, tip, branch } = snapshot;
  if (working && (await ref(checkout, 'HEAD') !== head || (await git(checkout, ['branch', '--show-current'])).trim() !== branch)) {
    throw Error('The checkout changed. Refresh the file list.');
  }
  let patch;
  if (entry.untracked) {
    const file = path.join(checkout, unixPath(name));
    const resolved = await realpath(file).catch(() => null);
    const stat = resolved === file && within(checkout, resolved) ? await lstat(file) : null;
    if (!stat?.isFile()) {
      throw Error('This new file is unavailable or is not a regular file.');
    }
    if (stat.size > 8 * 1024 * 1024) throw Error('This file exceeds the viewer size limit.');
    patch = await git(checkout, [...diffFlags, '--no-index', '--', '/dev/null', name], { limit: MAX_PATCH, differences: true });
  } else {
    const config = working ? await withoutFilters(checkout) : [];
    patch = await git(checkout, [...diffFlags, '--unified=3', '--output-indicator-new=+', '--output-indicator-old=-',
      '--output-indicator-context= ', ...(working ? [base] : [base, tip]), '--', name], { limit: MAX_PATCH, config });
  }
  return { path: name, patch };
}

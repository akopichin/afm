import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { parse } from 'yaml';

const run = promisify(execFile);
export const MODES = ['current', 'branch', 'worktree'];

// Same rules as AFM's worktree name validation: one safe path component that is also a branch name.
export function validWorktreeName(name) {
  return typeof name === 'string' && /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name)
    && !name.includes('..') && !name.endsWith('.') && !name.endsWith('.lock');
}

async function git(folder, ...args) {
  const { stdout } = await run('git', ['-C', folder, ...args], { env: { ...process.env, LC_ALL: 'C' } });
  return stdout.trim();
}

export async function inspectGit(folder) {
  try {
    if (await git(folder, 'rev-parse', '--is-inside-work-tree') !== 'true') return { git: false };
    // A repository root is required: AFM puts worktrees under the project root.
    const top = await git(folder, 'rev-parse', '--show-toplevel');
    const status = await git(folder, 'status', '--porcelain');
    let head = null;
    try { head = await git(folder, 'rev-parse', 'HEAD'); } catch { /* A repository without commits has no HEAD. */ }
    const branch = await git(folder, 'branch', '--show-current');
    return { git: true, root: top, branch: branch || null, detached: !branch, head, dirty: status !== '' };
  } catch { return { git: false }; }
}

// What the flow file itself declares; null when it has no opinion.
export async function declaredWorktrees(flowFile) {
  const flow = await readYAML(flowFile);
  return typeof flow?.worktrees === 'boolean' ? { enabled: flow.worktrees, source: 'flow' } : null;
}

// AFM's order: the flow's own `worktrees:`, then AFM_WORKTREES, then project and global config.
export async function effectivePolicy(declared, configFiles = [], env = process.env) {
  if (declared) return declared;
  const fromEnv = env.AFM_WORKTREES;
  if (fromEnv) return { enabled: fromEnv === '1' || fromEnv === 'true', source: 'env' };
  for (const file of configFiles) {
    const enabled = (await readYAML(file))?.worktrees?.enabled;
    if (typeof enabled === 'boolean') return { enabled, source: 'config' };
  }
  return null;
}

export async function flowWorktreePolicy(flowFile, configFiles = [], env = process.env) {
  return effectivePolicy(await declaredWorktrees(flowFile), configFiles, env);
}

async function readYAML(file) {
  try { return parse(await readFile(file, 'utf8')); }
  catch { return null; }
}

// Chooses the launch mode and explains it. An explicit choice beats the flow's policy, which beats
// the Desktop preference. A flow that declares worktrees cannot be launched in the current checkout:
// the AFM CLI has no switch that turns the declared policy off, so those choices are refused here.
export function resolveMode({ gitInfo, policy, preference = 'current', explicit, flowName, branchName, worktreeName }) {
  if (!gitInfo.git) {
    if (explicit && explicit !== 'current') return blocked('This folder is not a Git repository.');
    return { mode: 'current', source: 'folder', blocked: null, branch: null, worktree: null };
  }
  const mode = explicit || (policy?.enabled ? 'worktree' : policy ? 'current' : preference);
  const source = explicit ? 'explicit' : policy ? policy.source : 'preference';
  if (policy?.enabled && mode !== 'worktree') {
    return blocked('This flow declares worktrees: true, so it always runs in its own worktree.');
  }
  if (mode !== 'current' && !gitInfo.head) return blocked('The repository has no commits yet, so it cannot branch.');
  if (mode === 'worktree') {
    const name = worktreeName || flowName;
    if (!validWorktreeName(name)) return blocked('Choose a worktree name using letters, digits, dots, dashes or underscores.');
    return { mode, source, blocked: null, branch: name, worktree: name };
  }
  if (mode === 'branch') {
    if (!validWorktreeName(branchName)) return blocked('Choose a branch name using letters, digits, dots, dashes or underscores.');
    return { mode, source, blocked: null, branch: branchName, worktree: null };
  }
  if (mode !== 'current') return blocked('Unknown Git mode.');
  return { mode, source, blocked: null, branch: gitInfo.branch, worktree: null };
}

function blocked(reason) { return { mode: null, source: null, blocked: reason, branch: null, worktree: null }; }

// Creates the branch in place. `switch -c` carries uncommitted changes along and refuses an existing
// branch, so nothing the user has can be discarded. Returns a function that undoes the switch.
export async function prepareBranch(folder, name) {
  const before = await inspectGit(folder);
  if (!before.git) throw coded('not_git', 'This folder is not a Git repository.');
  if (before.detached) throw coded('detached_head', 'The checkout is on a detached HEAD; check out a branch first.');
  if (!before.head) throw coded('no_commits', 'The repository has no commits yet.');
  try { await git(folder, 'rev-parse', '--verify', '--quiet', `refs/heads/${name}`); throw coded('branch_exists', `Branch ${name} already exists.`); }
  catch (error) { if (error.code === 'branch_exists') throw error; }
  try { await git(folder, 'switch', '-c', name); }
  catch (error) { throw coded('branch_failed', 'Git could not create the branch: ' + (error.stderr || error.message).trim()); }
  return async function undo() {
    const now = await inspectGit(folder);
    // Only move back while the new branch is still empty, so a run's commits are never orphaned.
    if (now.branch === name && now.head === before.head) {
      await git(folder, 'switch', before.branch);
      await git(folder, 'branch', '-d', name);
    }
  };
}

function coded(code, message) { return Object.assign(Error(message), { code }); }

export function configFilesFor(home, folder) {
  return [path.join(folder, '.afm', 'config.yaml'), path.join(home, '.afm', 'config.yaml')];
}

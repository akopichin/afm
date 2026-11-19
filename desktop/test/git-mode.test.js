import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { flowWorktreePolicy, inspectGit, prepareBranch, resolveMode, validWorktreeName } from '../src/git-mode.js';
import { makeProject, tempHome } from './support.js';

const git = (dir, ...args) => execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8' }).trim();
const repo = { git: true, branch: 'main', head: 'abc', dirty: false };

test('mode selection: explicit beats flow policy beats preference', () => {
  const pick = input => resolveMode({ gitInfo: repo, flowName: 'feat', branchName: 'b1', ...input });
  assert.equal(pick({ preference: 'branch' }).mode, 'branch');
  assert.equal(pick({ preference: 'branch' }).source, 'preference');
  assert.deepEqual([pick({ policy: { enabled: true, source: 'flow' }, preference: 'current' }).mode, pick({ policy: { enabled: true, source: 'flow' } }).source], ['worktree', 'flow']);
  assert.equal(pick({ policy: { enabled: false, source: 'flow' }, preference: 'worktree' }).mode, 'current');
  assert.equal(pick({ policy: { enabled: false, source: 'flow' }, explicit: 'worktree' }).source, 'explicit');
  assert.equal(pick({ explicit: 'current', preference: 'worktree' }).mode, 'current');
  const forced = pick({ policy: { enabled: true, source: 'flow' }, explicit: 'current' });
  assert.match(forced.blocked, /always runs in its own worktree/);
});

test('mode selection handles non-git folders, bad names and empty repositories', () => {
  const plain = resolveMode({ gitInfo: { git: false }, preference: 'worktree', flowName: 'f' });
  assert.equal(plain.mode, 'current');
  assert.match(resolveMode({ gitInfo: { git: false }, explicit: 'branch', branchName: 'x' }).blocked, /not a Git/);
  assert.match(resolveMode({ gitInfo: repo, explicit: 'branch', branchName: '../x' }).blocked, /branch name/);
  assert.match(resolveMode({ gitInfo: repo, explicit: 'worktree', flowName: 'two words' }).blocked, /worktree name/);
  assert.match(resolveMode({ gitInfo: { git: true, head: null }, explicit: 'branch', branchName: 'x' }).blocked, /no commits/);
  assert.equal(resolveMode({ gitInfo: repo, explicit: 'worktree', worktreeName: 'mine', flowName: 'two words' }).worktree, 'mine');
  for (const bad of ['', '.x', '-x', 'a..b', 'a.lock', 'a.']) assert.equal(validWorktreeName(bad), false, bad);
});

test('flow policy reads the flow before project and global config', async t => {
  const { home } = await tempHome(t);
  const dir = await makeProject(home, 'p', { flows: { 'a.yaml': 'name: a\nworktrees: true\nstages: []\n', 'b.yaml': 'name: b\nstages: []\n' } });
  const configs = [path.join(dir, '.afm', 'config.yaml'), path.join(home, '.afm', 'config.yaml')];
  assert.deepEqual(await flowWorktreePolicy(path.join(dir, '.afm', 'flows', 'a.yaml'), configs), { enabled: true, source: 'flow' });
  assert.equal(await flowWorktreePolicy(path.join(dir, '.afm', 'flows', 'b.yaml'), configs), null);
  await writeFile(configs[0], 'worktrees:\n  enabled: true\n');
  assert.deepEqual(await flowWorktreePolicy(path.join(dir, '.afm', 'flows', 'b.yaml'), configs), { enabled: true, source: 'config' });
});

test('a new branch carries uncommitted work and undo restores the original branch', async t => {
  const { home } = await tempHome(t);
  const dir = await makeProject(home, 'p', { git: true });
  await writeFile(path.join(dir, 'README.md'), 'edited\n');
  await writeFile(path.join(dir, 'new.txt'), 'untracked\n');
  assert.equal((await inspectGit(dir)).dirty, true);
  const undo = await prepareBranch(dir, 'feature-x');
  assert.equal(git(dir, 'branch', '--show-current'), 'feature-x');
  assert.equal(await readFile(path.join(dir, 'README.md'), 'utf8'), 'edited\n');
  await undo();
  assert.equal(git(dir, 'branch', '--show-current'), 'main');
  assert.equal(await readFile(path.join(dir, 'README.md'), 'utf8'), 'edited\n');
  assert.equal(await readFile(path.join(dir, 'new.txt'), 'utf8'), 'untracked\n');
  assert.equal(git(dir, 'branch', '--list', 'feature-x'), '');
});

test('branch conflicts and unsafe checkouts change nothing', async t => {
  const { home } = await tempHome(t);
  const dir = await makeProject(home, 'p', { git: true });
  git(dir, 'branch', 'taken');
  await assert.rejects(prepareBranch(dir, 'taken'), { code: 'branch_exists' });
  assert.equal(git(dir, 'branch', '--show-current'), 'main');
  git(dir, 'checkout', '-q', '--detach');
  await assert.rejects(prepareBranch(dir, 'fresh'), { code: 'detached_head' });
  const plain = await makeProject(home, 'plain');
  await assert.rejects(prepareBranch(plain, 'x'), { code: 'not_git' });
});

test('undo keeps a branch that already has commits', async t => {
  const { home } = await tempHome(t);
  const dir = await makeProject(home, 'p', { git: true });
  const undo = await prepareBranch(dir, 'work');
  await writeFile(path.join(dir, 'c.txt'), 'c');
  git(dir, 'add', 'c.txt'); git(dir, 'commit', '-q', '-m', 'c');
  await undo();
  assert.equal(git(dir, 'branch', '--show-current'), 'work');
});

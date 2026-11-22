import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile, copyFile, chmod } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { LibraryLinks, parseLibraryLink } from '../src/library-links.js';
import { fakeNative, loadShell, snapshotOf, tick } from './shell-support.js';

const repository = 'https://github.com/example/pipelines';
const linkTo = source => 'afm://library/install?' + new URLSearchParams({ source });

test('a Library link decodes one Git source and separates the AFM package selectors', () => {
  const source = repository + '//pipelines/dev@release/v1';
  assert.deepEqual(parseLibraryLink(linkTo(source)), { source, repository, subdir: 'pipelines/dev', ref: 'release/v1' });
  assert.equal(parseLibraryLink(linkTo('github.com/example/pipelines')).source, repository);
  assert.equal(parseLibraryLink(linkTo('git@example.com:team/repo.git//pipeline@main')).repository, 'git@example.com:team/repo.git');
  assert.equal(parseLibraryLink(linkTo('ssh://git@example.com:2222/team/repo.git')).repository, 'ssh://git@example.com:2222/team/repo.git');
});

test('invalid Library links never disclose the supplied credentials or local path', () => {
  const rejected = ['/tmp/work', 'file:///private/work', 'http://example.com/flow', 'https://token@example.com/work',
    'https://user:token@example.com/work', 'ssh://git:token@example.com/work', 'ssh://git:@example.com/work',
    'https://example.com/work?token=secret', 'https://example.com/work//../elsewhere', 'https://example.com/work@-ref',
    'https://example.com/work\nnext', 'https://example.com/work%0d', 'https://example.com/work@',
    'https://example.com@'];
  for (const source of rejected) {
    assert.throws(() => parseLibraryLink(linkTo(source)), error => !error.message.includes(source));
  }
  for (const link of [linkTo(repository) + '&source=extra', linkTo(repository) + '&extra=x',
    'afm://library/other?' + new URLSearchParams({ source: repository }), linkTo(repository) + '#fragment',
    'afm://library/install?source=' + 'a'.repeat(8192)]) assert.throws(() => parseLibraryLink(link));
});

test('confirmation binds the exact reviewed source, deduplicates in-flight calls and permits retry', async () => {
  const calls = [];
  const outcome = Promise.withResolvers();
  const links = new LibraryLinks(async input => {
    calls.push(input);
    return calls.length === 1 ? outcome.promise : { error: null, entries: [{name:'Dev'}] };
  });
  assert.equal(links.receive('electron'), false);
  links.receive(linkTo(repository));
  const { id } = links.current();
  links.receive(linkTo(repository));
  assert.equal(links.current().id, id);
  assert.deepEqual(calls, []);
  await assert.rejects(links.confirm('wrong'), /no longer available/);
  const first = links.confirm(id), duplicate = links.confirm(id);
  outcome.resolve({ error: 'Repository unreachable' });
  assert.equal((await first).error, 'Repository unreachable');
  await duplicate;
  assert.equal(calls.length, 1);
  assert.equal(links.current().id, id);
  await links.confirm(id);
  assert.deepEqual(calls, [{ location: repository }, { location: repository }]);
  assert.equal(links.current(), null);
  links.receive(linkTo('file:///private/work'));
  await assert.rejects(links.confirm(links.current().id), /no longer available/);
  links.dismiss(links.current().id);
  assert.equal(links.current(), null);
});

test('a cold link opens Library for review, retries failures and renders the registered pipeline', async t => {
  const calls = [];
  let entries = [];
  const native = fakeNative(snapshotOf([]));
  const review = { id: 'cold', ...parseLibraryLink(linkTo(repository + '//dev@v1')) };
  Object.assign(native, {
    libraryLink: async () => review,
    onLibraryLink: callback => { native.openLink = callback; return () => {}; },
    sources: async () => entries,
    confirmLibraryLink: async id => {
      calls.push(id);
      if (calls.length === 1) return { error: 'Could not read the repository.' };
      entries = [{ id: 'source', location: repository, entries: [{ id: 'dev', name: 'Dev pipeline' }] }];
      return { entries: entries[0].entries, error: null };
    },
    dismissLibraryLink: async id => { calls.push(id); },
  });
  const shell = await loadShell(t, { demo: false, native });
  await tick();
  assert.equal(shell.get('settings').open, true);
  const reviewPanel = () => shell.get('library-link-title').parentElement.textContent;
  for (const value of [repository, 'dev', 'v1']) assert.ok(reviewPanel().includes(value));
  assert.deepEqual(calls, []);
  shell.get('library-link-confirm').click();
  await tick();
  assert.match(reviewPanel(), /Could not read the repository/);
  assert.equal(shell.get('library-link-confirm').textContent, 'Retry');
  shell.get('library-link-confirm').click();
  await tick();
  assert.equal(shell.get('library-link-title'), null);
  assert.match(shell.get('settings-body').textContent, /Dev pipeline/);
  native.openLink({ id: 'warm', ...parseLibraryLink(linkTo(repository)) });
  assert.ok(shell.get('library-link-confirm'));
  shell.get('library-link-dismiss').click();
  await tick();
  assert.equal(shell.get('library-link-title'), null);
  assert.deepEqual(calls, ['cold', 'cold', 'warm']);
});

test('Library shows pipeline sources and keeps the last successful refresh visible through a retry', async t => {
  const firstDate = '2026-11-20T09:31:00.000Z';
  const nextDate = '2026-11-22T11:12:00.000Z';
  const source = { id: 'one', location: repository, lastSuccessfulRefreshAt: firstDate, entries: [{ id: 'dev', name: 'Dev pipeline' }] };
  const other = { id: 'two', location: '/work/other-library', entries: [{ id: 'review', name: 'Review pipeline' }] };
  let inventory = [source, other];
  let pending = Promise.withResolvers();
  const calls = [];
  const native = {
    ...fakeNative(snapshotOf([])),
    sources: async () => inventory,
    refreshSource: id => { calls.push(id); return pending.promise; },
  };
  const shell = await loadShell(t, { demo: false, native });
  const query = selector => shell.d.querySelector(selector);
  const refresh = () => query('[data-refresh="one"]');
  const row = () => refresh().parentElement;
  shell.get('settings-toggle').click();
  query('[data-panel="library"]').click();
  await tick();
  assert.equal(row().querySelector('time').dateTime, firstDate);
  assert.equal(query('[data-refresh="two"]').parentElement.querySelector('time'), null);
  assert.equal(query('[data-pipeline-options="dev"]').parentElement.querySelector('small').textContent, 'Source: pipelines');
  assert.equal(query('[data-pipeline-options="review"]').parentElement.querySelector('small').textContent, 'Source: other-library');

  refresh().click();
  assert.equal(refresh().disabled, true);
  assert.equal(refresh().getAttribute('aria-busy'), 'true');
  assert.match(row().textContent, /Refreshing/);
  assert.equal(query('[data-refresh="two"]').disabled, false);
  query('[data-panel="runs"]').click();
  query('[data-panel="library"]').click();
  assert.equal(refresh().disabled, true);
  refresh().click();
  assert.deepEqual(calls, ['one']);

  inventory = [{ ...source, error: 'Repository unavailable' }, other];
  pending.resolve(inventory[0]);
  await tick();
  assert.equal(refresh().disabled, false);
  assert.equal(refresh().hasAttribute('aria-busy'), false);
  assert.match(row().textContent, /Repository unavailable/);
  assert.equal(row().querySelector('time').dateTime, firstDate);

  pending = Promise.withResolvers();
  refresh().click();
  pending.reject(Error('Refresh request failed'));
  await tick();
  assert.equal(refresh().disabled, false);
  assert.match(shell.get('settings-body').textContent, /Refresh request failed/);

  pending = Promise.withResolvers();
  refresh().click();
  inventory = [{ ...source, lastSuccessfulRefreshAt: nextDate }, other];
  pending.resolve(inventory[0]);
  await tick();
  assert.equal(refresh().disabled, false);
  assert.equal(row().querySelector('time').dateTime, nextDate);
  assert.doesNotMatch(shell.get('settings-body').textContent, /Repository unavailable|Refresh request failed/);
  assert.deepEqual(calls, ['one', 'one', 'one']);
});

test('the Linux registration command writes an encoded launcher and requests the AFM MIME handler', async t => {
  const temp = await mkdtemp(path.join(os.tmpdir(), 'lib-link-'));
  t.after(() => rm(temp, { recursive: true, force: true }));
  const application = path.join(temp, 'AFM bundle 100%'), data = path.join(temp, 'data'), bin = path.join(temp, 'bin');
  await mkdir(application);
  await mkdir(bin);
  const script = path.join(application, 'register-afm-links'), calls = path.join(temp, 'calls');
  await copyFile(new URL('../scripts/register-afm-links', import.meta.url), script);
  await chmod(script, 0o755);
  await writeFile(path.join(application, 'AFM Desktop'), '#!/bin/sh\n', { mode: 0o755 });
  await writeFile(path.join(bin, 'xdg-mime'), '#!/bin/sh\nprintf "%s\\n" "$@" > "$AFM_TEST_CALLS"\n', { mode: 0o755 });
  execFileSync(script, [], { env: { ...process.env, PATH: bin + path.delimiter + process.env.PATH, XDG_DATA_HOME: data, AFM_TEST_CALLS: calls } });
  const entry = await readFile(path.join(data, 'applications/dev.afm.desktop.desktop'), 'utf8');
  assert.match(entry, /Exec=".*AFM bundle 100%%\/AFM Desktop" %u/);
  assert.match(entry, /MimeType=x-scheme-handler\/afm;/);
  assert.deepEqual((await readFile(calls, 'utf8')).trim().split('\n'), ['default', 'dev.afm.desktop.desktop', 'x-scheme-handler/afm']);
});

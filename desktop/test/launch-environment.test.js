import test from 'node:test';
import assert from 'node:assert/strict';
import { access, mkdir, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';
import { Prefs } from '../src/prefs.js';
import { desktopEnvironment } from '../src/environment.js';
import { Runs } from '../src/runs.js';
import { tempHome } from './support.js';

test('manual Desktop environment values override the file and values never appear in preferences returned to the UI', async t => {
  const {home} = await tempHome(t);
  const prefs = new Prefs(home);
  const file = path.join(home, 'test.env');
  await writeFile(file, 'SETUP_TOKEN=from-file\nFROM_FILE="with spaces"\n');
  await prefs.updateEnvironment({file});
  const view = await prefs.updateEnvironment({name: 'SETUP_TOKEN', value: 'manual'});
  assert.deepEqual(view.environment.names, ['SETUP_TOKEN']);
  assert.equal(view.environment.variables, undefined);
  assert.deepEqual(await desktopEnvironment(prefs, home, {SETUP_TOKEN: 'inherited'}),
    {SETUP_TOKEN: 'manual', FROM_FILE: 'with spaces'});
  await prefs.updateEnvironment({remove: 'SETUP_TOKEN'});
  assert.equal((await desktopEnvironment(prefs, home)).SETUP_TOKEN, 'from-file');
});

for (const shell of ['zsh', 'bash']) {
  test(shell + ' import reads exported variables even when the startup file prints a banner', async t => {
    const executable = '/bin/' + shell;
    if (!await access(executable, constants.X_OK).then(() => true, () => false)) return t.skip('Requires ' + shell);
    const {home} = await tempHome(t);
    const prefs = new Prefs(home);
    await writeFile(path.join(home, '.' + shell + 'rc'), 'echo banner\nexport AFM_SHELL_TEST=imported\nAFM_UNEXPORTED_TEST=local\n');
    await prefs.updateEnvironment({shell});
    const env = await desktopEnvironment(prefs, home, {PATH: '/usr/bin:/bin', SHELL: executable, HOME: home, ZDOTDIR: home});
    assert.equal(env.AFM_SHELL_TEST, 'imported');
    assert.equal(env.AFM_UNEXPORTED_TEST, undefined);
  });
}

test('an accepted launch that fails before readiness exposes missing variable names without forwarding log content', async t => {
  const {home} = await tempHome(t);
  const id = 'a'.repeat(64);
  const dir = path.join(home, '.afm', 'serve');
  await mkdir(path.join(dir, 'launches'), {recursive: true});
  await mkdir(path.join(dir, 'logs'), {recursive: true});
  await writeFile(path.join(dir, 'launches', id + '.json'), JSON.stringify({state: 'failed'}));
  await writeFile(path.join(dir, 'logs', id + '.log'),
    'Error: startup failed\n  - secret:writer: secret not found: env:MY_TOKEN (checked secrets file and process env)\nprivate diagnostic');
  const result = await new Runs({home}).waitForLaunch({launch_id: id, state: 'starting'});
  assert.equal(result.state, 'failed');
  assert.match(result.message, /Missing environment variables: MY_TOKEN/);
  assert.doesNotMatch(result.message, /private diagnostic/);
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeNative, loadShell, projectRow, runRow, snapshotOf, tick } from './shell-support.js';

const ra = runRow('ra'), rb = runRow('rb');
const browser = (run, extra = {}) => ({ runKey: run.key, stageId: 'live-run', handle: 'handle-' + run.runId, url: 'http://127.0.0.1:18080/', title: 'Checkout', pages: 1, ...extra });
const snapshot = browsers => snapshotOf([projectRow('a', [ra]), projectRow('b', [rb])], [], { stageBrowsers: browsers });

async function openShell(t, browsers) {
  const native = fakeNative(snapshot(browsers));
  native.layouts = [];
  native.liveLayout = async (bounds, stage) => { native.layouts.push([bounds && Object.keys(bounds).join(), stage]); };
  const shell = await loadShell(t, { demo: false, native });
  shell.get('finish-setup')?.click();
  await tick();
  return { ...shell, native };
}
const liveTab = d => d.querySelector('button.tab[data-view="live"]');

test('only a run with a stage browser has a Live tab, named after its stage', async t => {
  const { w, d } = await openShell(t, [browser(ra)]);
  w.shell.openProject(w.shell.projects[0], w.shell.projects[0].runs[0], 'run');
  await tick();
  assert.match(liveTab(d).textContent, /Live · live-run/);
  w.shell.openProject(w.shell.projects[1], w.shell.projects[1].runs[0], 'run');
  await tick();
  assert.equal(liveTab(d), null, 'another project\'s run has no browser');
});

test('the Live tab places the stage\'s own view, hides it when the user leaves and when the stage ends', async t => {
  const { w, d, native } = await openShell(t, [browser(ra)]);
  const [project] = w.shell.projects;
  w.shell.openProject(project, project.runs[0], 'run');
  await tick();
  liveTab(d).click();
  await tick(30);
  assert.equal(d.getElementById('native-live-tab').dataset.stageBrowser, 'handle-ra');
  assert.match(d.querySelector('.livebar').textContent, /live-run.*Checkout/);
  assert.deepEqual(native.layouts.at(-1), ['x,y,width,height', 'handle-ra'], 'the shell places the view the stage owns');

  d.querySelector('button.tab:not([data-view])').click();
  await tick(30);
  assert.deepEqual(native.layouts.at(-1), [null, 'handle-ra'], 'leaving hides that stage\'s view');

  liveTab(d).click();
  await tick(30);
  native.publish(snapshot([]));
  await tick(30);
  assert.equal(liveTab(d), null, 'the tab goes with the stage');
  assert.equal(d.getElementById('native-live-tab'), null);
  assert.deepEqual(native.layouts.at(-1), [null, 'handle-ra'], 'its view is hidden when the stage ends');
});

test('a navigation in the stage updates the tab without replacing the run', async t => {
  const { w, d, native } = await openShell(t, [browser(ra)]);
  const [project] = w.shell.projects;
  const run = project.runs[0];
  w.shell.openProject(project, run, 'run');
  await tick();
  native.publish(snapshot([browser(ra, { url: 'http://127.0.0.1:18080/cart', title: 'Cart' })]));
  await tick();
  assert.equal(w.shell.projects[0].runs[0].liveStage.title, 'Cart');
  native.publish(snapshot([browser(ra, { url: 'http://127.0.0.1:18080/cart', title: 'Cart' })]));
  assert.equal(w.shell.projects[0].runs[0].liveStage.title, 'Cart', 'an unchanged snapshot is a no-op');
});

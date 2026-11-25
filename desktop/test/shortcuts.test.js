import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { JSDOM } from 'jsdom';
import { fakeNative, loadShell, primaryMods, projectRow, runRow, snapshotOf, tick } from './shell-support.js';
import { dashboardShortcutMessage } from '../src/shortcuts.js';

const PLATFORMS = ['MacIntel', 'Linux x86_64'];

test('Electron reserves only the Desktop navigation bindings from the run view', () => {
  const key = (input, platform = 'darwin') => dashboardShortcutMessage({type: 'keyDown', ...input}, platform);
  for (const [code, name] of [['KeyH','home'], ['KeyJ','next'], ['KeyK','previous'], ['KeyM','model'], ['KeyD','changes']]) {
    assert.deepEqual(key({meta:true, code}), {navigate:name});
  }
  assert.deepEqual(key({alt:true, code:'KeyA'}), {navigate:'assistant'});
  assert.deepEqual(key({meta:true, key:'3'}), {navigate:3});
  assert.equal(key({meta:true, shift:true, code:'KeyK'}), 'switch-project');
  assert.deepEqual(key({meta:true, shift:true, code:'KeyC'}), {navigate:'connect-run'});
  assert.deepEqual(key({meta:true, key:','}), {navigate:'settings'});
  assert.equal(key({control:true, shift:true, code:'KeyK'}, 'linux'), 'switch-project');
  assert.deepEqual(key({control:true, shift:true, code:'KeyC'}, 'linux'), {navigate:'connect-run'});
  for (const input of [{control:true,code:'KeyK'}, {meta:true,key:'f'}, {alt:true,code:'KeyM'},
    {meta:true,code:'KeyK',isAutoRepeat:true}, {meta:true,code:'KeyH',isComposing:true},
    {meta:true,key:'0'}, {meta:true,key:'2',shift:true}]) assert.equal(key(input), null);
});

for (const platform of PLATFORMS) {
  test(`accepted Desktop keyboard routes work through the real shell with a focused run (${platform})`, async t => {
    const { w, d, get, key } = await loadShell(t, { platform });
    const primary = primaryMods(platform);
    get('finish-setup').click();
    key('Comma', ',', primary); assert.ok(get('settings').open);
    key('Comma', ',', primary); assert.equal(get('settings').open,false);
    key('KeyA', 'a', { altKey: true }); assert.equal(d.activeElement.id, 'chat-input');
    key('KeyA', 'a', { altKey: true }); assert.equal(get('assistant').hidden, true);
    key('KeyM', 'm', primary); assert.ok(get('quick-model').open);
    get('quick-model').close();
    key('KeyK', 'k', { ...primary, shiftKey: true }); assert.ok(get('picker').open);
    key('KeyK', 'k', { ...primary, shiftKey: true }); assert.equal(get('picker').open, false, 'the picker shortcut toggles');
    key('Enter', 'Enter', { ...primary, shiftKey: true }); assert.ok(get('launch').open); get('launch').close();
    key('KeyJ', 'j', primary); await tick(); assert.match(w.location.hash, /afm\/review/);
    key('KeyJ', 'j', primary); await tick(); assert.match(w.location.hash, /delivery-check/);
    key('KeyK', 'k', primary); await tick(); assert.match(w.location.hash, /afm\/review/);
    const frame = d.querySelector('#dashboard-host iframe:not([hidden])');
    const fromRun = data => w.dispatchEvent(new w.MessageEvent('message', { source: frame.contentWindow, data }));
    fromRun({ type: 'stage-focus', active: true }); assert.ok(d.body.classList.contains('desktop-focus'));
    fromRun({ navigate: 'assistant' }); assert.equal(get('assistant').hidden, false);
    assert.equal(d.body.classList.contains('desktop-focus'), false);
    fromRun({ navigate: 'home' }); await tick(); assert.equal(w.location.hash, '');
  });

  test(`numbered shortcuts select run tabs across views (${platform})`, async t => {
    const first = runRow('first');
    const live = { runKey: first.key, stageId: 'stage', handle: 'live-first' };
    const native = fakeNative(snapshotOf([projectRow('a', [
      first,
      runRow('second', { state: 'finished', status: 'finished', can: { open: false, report: true } }),
      runRow('third'),
    ])], [], { stageBrowsers: [live] }));
    native.runReport = async () => ({ run: { status: 'finished', elapsed_ms: 2000 }, stages: [] });
    let focusRequests = 0;
    native.focusShell = async () => { focusRequests++; };
    native.onLiveShortcut = listener => { native.emitLiveShortcut = listener; };
    const { w, d, get, key } = await loadShell(t, { demo: false, platform, native });
    const primary = primaryMods(platform);
    get('finish-setup').click();
    await tick();
    assert.equal(key('Digit1', '1', primary).defaultPrevented, false, 'Home has no run tabs');
    d.querySelector('#screen .tile').click();
    await tick();
    const tabs = [...d.querySelectorAll('.run-tabs button:not([data-view])')];
    assert.deepEqual(tabs.map(tab => tab.getAttribute('aria-keyshortcuts')),
      [1, 2, 3].map(number => `${platform.startsWith('Mac') ? 'Meta' : 'Control'}+${number}`));
    const selected = () => d.querySelector('.run-tabs .active').textContent;
    assert.equal(key('Digit1', '1', primary).defaultPrevented, true);
    await tick();
    assert.equal(selected(), 'flow-first');
    assert.equal(key('Digit1', '1', primary).defaultPrevented, true);
    assert.equal(focusRequests, 1, 'the current tab keeps its native focus');

    native.emitDashboard(first.key, { navigate: 2 });
    await tick();
    assert.equal(selected(), 'flow-second');
    assert.match(get('content').textContent, /Elapsed2s/);
    assert.equal(focusRequests, 2, 'leaving a native dashboard asks the shell to take focus');
    native.emitDashboard(first.key, { navigate: 3 });
    await tick();
    assert.equal(selected(), 'flow-second', 'hidden dashboards cannot switch tabs');
    assert.equal(focusRequests, 2);

    assert.equal(key('Digit1', '1', primary).defaultPrevented, true);
    await tick();
    assert.equal(selected(), 'flow-first', 'shortcuts continue from the finished report');
    native.emitDashboard(first.key, { navigate: 3 });
    await tick();
    assert.equal(selected(), 'flow-third');
    d.querySelector('.run-tabs [data-view="live"]').click();
    await tick();
    native.emitLiveShortcut(3);
    await tick();
    assert.equal(selected(), 'flow-third');
    assert.equal(key('Digit9', '9', primary).defaultPrevented, false);
    assert.equal(key('Digit1', '1', { ...primary, repeat: true }).defaultPrevented, false);
    assert.equal(key('Digit1', '1', { ...primary, isComposing: true }).defaultPrevented, false);
    assert.equal(key('Digit1', '1', { ...primary, shiftKey: true }).defaultPrevented, false);
    get('quick').click();
    assert.equal(key('Digit1', '1', primary).defaultPrevented, false, 'a dialog owns the keyboard');
    await tick();
    assert.equal(selected(), 'flow-third');
    assert.match(w.location.hash, /third/);
  });

  test(`shortcuts keep text editing, composition, modals and the other platform's keys intact (${platform})`, async t => {
    const { d, get, key } = await loadShell(t, { platform });
    const primary = primaryMods(platform), other = platform.startsWith('Mac') ? { ctrlKey: true } : { metaKey: true };
    get('finish-setup').click();
    assert.equal(key('KeyK', 'k', other).defaultPrevented, false, 'the other platform modifier is not our shortcut');
    assert.equal(get('picker').open, false);
    assert.equal(key('Enter', 'Enter', { ...other, shiftKey: true }).defaultPrevented, false);
    assert.equal(key('KeyJ', 'j', { ...primary, shiftKey: true }).defaultPrevented, false);
    assert.equal(key('KeyJ', 'j', { altKey: true, shiftKey: true }).defaultPrevented, false);
    assert.equal(key('KeyA', 'a', { altKey: true, isComposing: true }).defaultPrevented, false);
    assert.equal(key('KeyA', 'a', { altKey: true, repeat: true }).defaultPrevented, false);
    key('KeyA', 'a', { altKey: true });
    const input = get('chat-input');
    assert.equal(key('KeyF', 'f', {}, input).defaultPrevented, false, 'typing F in a field is text');
    assert.equal(key('KeyK', 'k', other, input).defaultPrevented, false, 'Control-K keeps its meaning in a Mac text field');
    key('KeyK', 'k', { ...primary, shiftKey: true }); assert.ok(get('picker').open);
    key('KeyA', 'a', { altKey: true }); assert.equal(get('assistant').hidden, false, 'a modal owns the keyboard');
    key('KeyM', 'm', primary); assert.equal(get('quick-model').open, false);
    get('picker').close();
  });
}

// The page-embedded copy of the shortcuts, run inside a frame the way Electron injects it.
async function loadFrame(t, platform) {
  const top = new JSDOM('<body><iframe></iframe></body>', { url: 'http://desktop.test/', runScripts: 'outside-only' });
  t.after(() => top.window.close());
  const frame = top.window.document.querySelector('iframe').contentWindow;
  Object.defineProperty(frame.navigator, 'platform', { value: platform, configurable: true });
  frame.document.body.innerHTML = '<div id="root"></div>';
  frame.eval(await readFile(new URL('../src/dashboard-keys.cjs', import.meta.url), 'utf8'));
  frame.dispatchEvent(new frame.Event('DOMContentLoaded'));
  const posted = [];
  // Messages cross a jsdom window boundary; compare them as plain data.
  top.window.addEventListener('message', event => posted.push(JSON.parse(JSON.stringify(event.data))));
  const press = (code, key, mods = {}) => {
    const event = new frame.KeyboardEvent('keydown', { code, key, bubbles: true, cancelable: true, ...mods });
    frame.document.dispatchEvent(event);
    return event;
  };
  return { posted, press, top };
}

for (const platform of PLATFORMS) {
  test(`shortcuts reach the shell from dashboard content (${platform})`, async t => {
    const { posted, press, top } = await loadFrame(t, platform);
    const primary = primaryMods(platform), other = platform.startsWith('Mac') ? { ctrlKey: true } : { metaKey: true };
    for (const [code, name] of [['KeyH', 'home'], ['KeyJ', 'next'], ['KeyK', 'previous'], ['KeyM', 'model']]) {
      assert.equal(press(code, code.slice(3).toLowerCase(), primary).defaultPrevented, true);
      await tick(5);
      assert.deepEqual(posted.pop(), { navigate: name });
    }
    press('Digit2', '2', primary); await tick(5); assert.deepEqual(posted.pop(), { navigate: 2 });
    press('KeyK', 'k', { ...primary, shiftKey: true }); await tick(5); assert.equal(posted.pop(), 'switch-project');
    press('Enter', 'Enter', { ...primary, shiftKey: true }); await tick(5); assert.equal(posted.pop(), 'new-run');
    for (const event of [press('KeyK', 'k', other), press('KeyJ', 'j', { ...primary, shiftKey: true }), press('Enter', 'Enter', primary),
      press('KeyJ', 'j', { altKey: true, shiftKey: true }), press('KeyJ', 'j', { altKey: true, isComposing: true }), press('KeyJ', 'j', { altKey: true, repeat: true })])
      assert.equal(event.defaultPrevented, false);
    await tick(5);
    assert.deepEqual(posted, []);
    top.window.document.querySelector('iframe').contentDocument.body.insertAdjacentHTML('beforeend', '<dialog open></dialog>');
    press('KeyJ', 'j', primary); await tick(5);
    assert.deepEqual(posted, [], 'a dashboard modal owns the keyboard');
  });
}

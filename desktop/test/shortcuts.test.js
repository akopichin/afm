import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { JSDOM } from 'jsdom';
import { loadShell, primaryMods, tick } from './shell-support.js';

const PLATFORMS = ['MacIntel', 'Linux x86_64'];

for (const platform of PLATFORMS) {
  test(`accepted Desktop keyboard routes work through the real shell with a focused run (${platform})`, async t => {
    const { w, d, get, key } = await loadShell(t, { platform });
    const primary = primaryMods(platform);
    get('finish-setup').click();
    key('Comma', ',', primary); assert.ok(get('settings').open);
    key('Comma', ',', primary); assert.equal(get('settings').open,false);
    key('KeyA', 'a', { altKey: true }); assert.equal(d.activeElement.id, 'chat-input');
    key('KeyA', 'a', { altKey: true }); assert.equal(get('assistant').hidden, true);
    key('KeyM', 'm', { altKey: true }); assert.ok(get('quick-model').open);
    get('quick-model').close();
    key('KeyK', 'k', primary); assert.ok(get('picker').open);
    key('KeyK', 'k', primary); assert.equal(get('picker').open, false, 'the picker shortcut toggles');
    key('Enter', 'Enter', { ...primary, shiftKey: true }); assert.ok(get('launch').open); get('launch').close();
    key('KeyJ', 'j', { altKey: true }); await tick(); assert.match(w.location.hash, /afm\/review/);
    key('KeyJ', 'j', { altKey: true }); await tick(); assert.match(w.location.hash, /delivery-check/);
    key('KeyK', 'k', { altKey: true }); await tick(); assert.match(w.location.hash, /afm\/review/);
    const frame = d.querySelector('#dashboard-host iframe:not([hidden])');
    const fromRun = data => w.dispatchEvent(new w.MessageEvent('message', { source: frame.contentWindow, data }));
    fromRun({ type: 'stage-focus', active: true }); assert.ok(d.body.classList.contains('desktop-focus'));
    fromRun({ navigate: 'assistant' }); assert.equal(get('assistant').hidden, false);
    assert.equal(d.body.classList.contains('desktop-focus'), false);
    fromRun({ navigate: 'home' }); await tick(); assert.equal(w.location.hash, '');
  });

  test(`shortcuts keep text editing, composition, modals and the other platform's keys intact (${platform})`, async t => {
    const { d, get, key } = await loadShell(t, { platform });
    const primary = primaryMods(platform), other = platform.startsWith('Mac') ? { ctrlKey: true } : { metaKey: true };
    get('finish-setup').click();
    assert.equal(key('KeyK', 'k', other).defaultPrevented, false, 'the other platform modifier is not our shortcut');
    assert.equal(get('picker').open, false);
    assert.equal(key('Enter', 'Enter', { ...other, shiftKey: true }).defaultPrevented, false);
    assert.equal(key('KeyK', 'k', { ...primary, shiftKey: true }).defaultPrevented, false);
    assert.equal(key('KeyJ', 'j', { altKey: true, shiftKey: true }).defaultPrevented, false);
    assert.equal(key('KeyA', 'a', { altKey: true, isComposing: true }).defaultPrevented, false);
    assert.equal(key('KeyA', 'a', { altKey: true, repeat: true }).defaultPrevented, false);
    key('KeyA', 'a', { altKey: true });
    const input = get('chat-input');
    assert.equal(key('KeyF', 'f', {}, input).defaultPrevented, false, 'typing F in a field is text');
    assert.equal(key('KeyK', 'k', other, input).defaultPrevented, false, 'Control-K keeps its meaning in a Mac text field');
    key('KeyK', 'k', primary); assert.ok(get('picker').open);
    key('KeyA', 'a', { altKey: true }); assert.equal(get('assistant').hidden, false, 'a modal owns the keyboard');
    key('KeyM', 'm', { altKey: true }); assert.equal(get('quick-model').open, false);
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
    for (const [code, name] of [['KeyH', 'home'], ['KeyJ', 'next'], ['KeyK', 'previous'], ['KeyA', 'assistant'], ['KeyM', 'model']]) {
      assert.equal(press(code, code.slice(3).toLowerCase(), { altKey: true }).defaultPrevented, true);
      await tick(5);
      assert.deepEqual(posted.pop(), { navigate: name });
    }
    press('KeyK', 'k', primary); await tick(5); assert.equal(posted.pop(), 'switch-project');
    press('Enter', 'Enter', { ...primary, shiftKey: true }); await tick(5); assert.equal(posted.pop(), 'new-run');
    for (const event of [press('KeyK', 'k', other), press('KeyK', 'k', { ...primary, shiftKey: true }), press('Enter', 'Enter', primary),
      press('KeyJ', 'j', { altKey: true, shiftKey: true }), press('KeyJ', 'j', { altKey: true, isComposing: true }), press('KeyJ', 'j', { altKey: true, repeat: true })])
      assert.equal(event.defaultPrevented, false);
    await tick(5);
    assert.deepEqual(posted, []);
    top.window.document.querySelector('iframe').contentDocument.body.insertAdjacentHTML('beforeend', '<dialog open></dialog>');
    press('KeyJ', 'j', { altKey: true }); await tick(5);
    assert.deepEqual(posted, [], 'a dashboard modal owns the keyboard');
  });
}

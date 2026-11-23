import test from 'node:test';
import assert from 'node:assert/strict';
import { fakeAssistant, fakeNative, loadShell, projectRow, snapshotOf } from './shell-support.js';
import { eventually } from './support.js';

test('MCP settings save a project HTTP server and show its connection status', async t => {
  const assistant = fakeAssistant();
  let saved;
  const publicServer = () => ({ id: 'test', name: saved.name, transport: saved.transport, projectId: saved.projectId, url: saved.url, hasToken: true });
  Object.assign(assistant, {
    mcpList: async () => [],
    mcpSave: async input => {
      saved = input;
      return [{ ...publicServer(), status: { state: 'untested' } }];
    },
    mcpTest: async id => {
      assert.equal(id, 'test');
      return [{ ...publicServer(), status: { state: 'connected' } }];
    },
  });
  const native = { ...fakeNative(snapshotOf([projectRow('project', [])])), assistant };
  const { d, get } = await loadShell(t, { demo: false, native });
  get('finish-setup')?.click();
  get('settings-toggle').click();
  d.querySelector('[data-panel="mcp"]').click();
  const add = await eventually(() => d.querySelector('[data-mcp-add]:enabled'));
  add.click();
  const form = d.querySelector('.mcp-editor form');
  form.elements.name.value = 'fixture';
  form.elements.transport.value = 'http';
  form.elements.transport.onchange();
  assert.equal(d.querySelector('[data-mcp-stdio]').hidden, true);
  form.elements.projectId.value = 'project';
  form.elements.url.value = 'https://example.com/mcp';
  form.elements.token.value = 'input-token';
  form.requestSubmit();
  const testConnection = await eventually(() => d.querySelector('[data-action="test"]:enabled'));
  assert.equal(saved.projectId, 'project');
  assert.equal(saved.token, 'input-token');
  assert(d.querySelector('.mcp-status[data-state="untested"] .dot[aria-hidden="true"]'));
  testConnection.click();
  const connected = await eventually(() => d.querySelector('.mcp-status[data-state="connected"]'));
  assert.match(connected.textContent, /Connected/);
  assert(connected.querySelector('.dot[aria-hidden="true"]'));
  d.querySelector('[data-action="edit"]').click();
  assert.equal(d.querySelector('input[name="token"]').value, '');
});

test('MCP uses the shared editor, saves its text and disposes it on tab switch', async t => {
  const assistant = fakeAssistant();
  const server = { id: 'fixture', name: 'gitlab', transport: 'stdio', command: 'npx', args: ['-y'],
    env: [{ name: 'GITLAB_PERSONAL_ACCESS_TOKEN', ref: 'GITLAB_PERSONAL_ACCESS_TOKEN' }], status: { state: 'untested' } };
  let saved;
  assistant.mcpList = async () => [server];
  assistant.mcpSave = async input => { saved = input; return [server]; };
  const { w, d, get } = await loadShell(t, { demo: false, native: { ...fakeNative(snapshotOf([])), assistant } });
  const editors = new Map();
  w.AFMCodeEditor = {
    createCodeEditor: (_surface, { text, label, language }) => {
      let change;
      const model = {
        text, language, getValue: () => model.text, focus: () => {}, onChange: callback => { change = callback; },
        set: value => { model.text = value; change(); },
        dispose: () => { model.disposed = true; },
      };
      editors.set(label, model);
      return model;
    },
  };
  get('finish-setup')?.click();
  get('settings-toggle').click();
  d.querySelector('[data-panel="mcp"]').click();
  (await eventually(() => d.querySelector('[data-action="edit"]:enabled'))).click();
  const environment = await eventually(() => editors.get('Environment variables'));
  assert.equal(editors.get('Arguments').text, '-y');
  assert.equal(environment.text, 'GITLAB_PERSONAL_ACCESS_TOKEN');
  assert(d.querySelector('textarea[name="env"]').hidden);
  environment.set('GITLAB_PERSONAL_ACCESS_TOKEN=@~/key');
  d.querySelector('.mcp-editor form').requestSubmit();
  await eventually(() => environment.disposed);
  assert.equal(saved.env[0].file, '~/key');

  d.querySelector('[data-mcp-import]').click();
  const configuration = await eventually(() => editors.get('OpenCode configuration'));
  assert.equal(configuration.language, 'yaml');
  d.querySelector('[data-panel="appearance"]').click();
  assert(configuration.disposed);
});

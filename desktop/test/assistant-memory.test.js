import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Assistant } from '../src/assistant/assistant.js';
import { AssistantBacklog } from '../src/assistant/backlog.js';

test('Desktop persists separate conversation histories and project backlog numbers', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-assistant-memory-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const context = { project: async id => ({ id, name: 'Project', path: '/workspace/project' }) };
  const assistant = new Assistant({ home, context });
  const first = await assistant.createConversation();
  const second = await assistant.createConversation();
  const scope = { projectRoot: '/workspace/project' };
  const backlog = new AssistantBacklog(home);
  const source = { conversationId: first.id, messageId: 'user-message' };
  const entry = { title: 'Scope the plan', plan: 'Create and validate a plan.', acceptance: 'The plan has a stable number.' };
  const [a, b] = await Promise.all([backlog.add(scope, entry, source, () => true), backlog.add(scope, entry, source, () => true)]);
  assert.equal(a.number, 1);
  assert.equal(b.number, a.number);

  const resumed = new Assistant({ home, context });
  assert.equal((await resumed.conversations()).length, 3);
  const project = { projectId: 'project' };
  const [, open] = await resumed.backlogCommand(second.id, '/backlog list', project);
  assert.match(open.text, /#1 · Scope the plan/);
  await resumed.backlogCommand(second.id, '/backlog done 1', project);
  const [, complete] = await resumed.backlogCommand(first.id, '/backlog list done', project);
  assert.match(complete.text, /#1 · Scope the plan/);
  await resumed.clear(second.id);
  assert.equal((await resumed.history(second.id)).length, 0);
  assert.equal((await resumed.history(first.id)).length, 2);
});

test('All projects can save a title-only draft and list it from any conversation', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-assistant-backlog-all-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const scope = { kind: 'all' };
  const backlog = new AssistantBacklog(home);
  const saved = await backlog.add(scope, { title: 'test-plan' }, { conversationId: 'main', messageId: 'u1' }, () => true);
  assert.equal(saved.plan, '');
  const assistant = new Assistant({ home, context: {} });
  const chat = await assistant.createConversation();
  const [, answer] = await assistant.backlogCommand(chat.id, '/backlog list', {});
  assert.match(answer.text, /#1 · test-plan/);
  assert.deepEqual(await backlog.plans({ kind: 'project', projectRoot: '/workspace/project' }), []);
});


test('deletion and clearing affect the requested status and never recycle plan numbers', async t => {
  const home = await mkdtemp(path.join(os.tmpdir(), 'afm-assistant-backlog-delete-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const backlog = new AssistantBacklog(home);
  const scope = { kind: 'all' };
  const add = title => backlog.add(scope, { title }, { conversationId: 'main', messageId: title }, () => true);
  await add('first');
  await add('second');
  await add('third');
  await backlog.command(scope, '/backlog done 2');
  await backlog.command(scope, '/backlog done 3');
  assert.match(await backlog.command(scope, '/backlog delete 2'), /No open plan/);
  assert.match(await backlog.command(scope, '/backlog done delete 2'), /Deleted #2/);
  assert.match(await backlog.command(scope, '/backlog done clear'), /Removed 1 completed plan/);
  assert.deepEqual(await backlog.plans(scope), [{ number: 1, title: 'first' }]);
  assert.deepEqual(await backlog.plans(scope, true), []);
  assert.match(await backlog.command(scope, '/backlog delete 1'), /Deleted #1/);
  assert.equal((await add('fourth')).number, 4);
});

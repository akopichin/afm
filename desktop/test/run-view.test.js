import test from 'node:test';
import assert from 'node:assert/strict';
import { runView } from '../src/runs.js';

const row = { run_id: 'r1', instance_id: 'i1', run_name: 'flow', project_root: '/p', mode: 'dashboard', api_url: 'http://127.0.0.1:9',
  ownership: 'managed', liveness: 'active', run_status: 'running', actions: ['cancel', 'report'], launch: { launch_id: 'L1', state: 'ready' } };

test('run states describe the process honestly and operations follow the host actions', () => {
  const view = overrides => runView({ ...row, ...overrides }, { L1: {} });
  assert.deepEqual([view({}).state, view({}).origin, view({}).can.open, view({}).can.cancel], ['running', 'desktop', true, true]);
  assert.equal(view({ run_status: 'finished', actions: ['release', 'report'] }).state, 'finished');
  assert.deepEqual([view({ run_status: 'finished', actions: ['release'] }).can.release, view({ run_status: 'finished', actions: ['release'] }).can.open], [true, true]);
  assert.deepEqual([view({ mode: 'core' }).state, view({ mode: 'core' }).can.open], ['api-only', false]);
  assert.deepEqual([view({ liveness: 'unreachable' }).state, view({ liveness: 'unreachable' }).can.open], ['offline', false]);
  assert.equal(view({ liveness: 'stale' }).state, 'offline');
  assert.equal(view({ liveness: 'exited' }).state, 'finished');
  assert.deepEqual([view({ liveness: 'identity_mismatch' }).state, view({ liveness: 'identity_mismatch' }).can.open], ['mismatch', false]);
  assert.equal(view({ ownership: 'discovered' }).can.open, false);
  assert.deepEqual([runView({ ...row, ownership: 'attached', launch: undefined, actions: ['report'] }).origin, runView({ ...row, ownership: 'attached', actions: ['report'] }).can.cancel], ['external', false]);
});

test('a run is identified by its instance and run ids together', () => {
  assert.notEqual(runView(row).key, runView({ ...row, run_id: 'r2' }).key);
  assert.notEqual(runView(row).key, runView({ ...row, instance_id: 'i2' }).key);
});

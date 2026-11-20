import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { parse } from 'yaml';

const root = new URL('../workflow/', import.meta.url);
const read = filename => readFileSync(new URL(filename, root), 'utf8');
const manifest = parse(read('pipeline.yaml'));
const flow = parse(read(manifest.entrypoint));
const agents = parse(read(manifest.agents));

test('Desktop workflows gate implementation, acceptance and commit before interactive merge', () => {
  const expected = ['plan', 'implement', 'live-run', 'doc', 'commit', 'merge'];
  assert.deepEqual(flow.stages.map(stage => stage.id), expected);
  for (const [index, stage] of flow.stages.entries()) {
    assert.deepEqual(stage.depends_on ?? [], index > 0 ? [expected[index - 1]] : []);
    assert.equal(stage.interactive, true);
    assert.equal(stage.browser, false);
  }

  const verifier = manifest.roles.verifier.command;
  assert.equal(agents.docker.agents[verifier].model, 'gpt-6-astra');
  for (const stageId of ['implement', 'live-run', 'commit']) {
    const stage = flow.stages.find(candidate => candidate.id === stageId);
    assert.ok(stage.verify.steps.some(step => step.command === verifier), stageId);
  }
  assert.ok(flow.stages.at(-1).description.includes('afm stage wait-answer'));
});

test('Desktop mesh review skills ship with the immutable pipeline snapshot', () => {
  assert.ok(manifest.resources.includes(flow.skills_dir));
  const implement = flow.stages.find(stage => stage.id === 'implement');
  assert.ok(implement.skills.includes('afm-dev-coordinator'));
  assert.ok(implement.skills.includes('afm-dev-escalation'));
  assert.equal(implement.assistants, undefined);
  assert.equal(implement.helpers, undefined);
  const participants = [...flow.stages, ...flow.team];
  for (const skill of new Set(participants.flatMap(participant => participant.skills ?? []))) {
    assert.ok(read(flow.skills_dir + '/' + skill + '/SKILL.md').includes('name: ' + skill));
  }
});

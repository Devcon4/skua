import assert from 'node:assert/strict';
import {existsSync} from 'node:fs';
import {join} from 'node:path';
import {test} from 'vitest';
import {
  batch,
  command,
  createHarness,
  promptSettled,
  waitFor,
} from './harness.ts';

async function admin(
  url: string,
  body: Record<string, unknown>,
): Promise<void> {
  const res = await fetch(`${url}/admin/profiles`, {
    method: 'POST',
    headers: {'content-type': 'application/json'},
    body: JSON.stringify(body),
  });
  assert.equal(res.status, 200);
}

test(
  'profiles sync in the background, a new profile is dispatchable after /skua sync, and a not-Ready one is removed',
  {timeout: 180_000},
  async t => {
    const h = await createHarness({api: {stepMs: 300}});
    try {
      const omp = await h.start();
      const file = (p: string) => join(h.agentsDir, `skua-${p}.md`);
      await waitFor(
        'the startup sync',
        () =>
          existsSync(file('generic')) && existsSync(file('golang'))
            ? true
            : undefined,
        15_000,
      );

      await admin(h.api.url, {name: 'node', description: 'Node remote agent.'});
      const synced = await command(omp, '/skua sync');
      assert.ok(
        synced.some(n =>
          n.startsWith('skua sync: 3 profiles (generic, golang, node)'),
        ),
        JSON.stringify(synced),
      );
      assert.ok(existsSync(file('node')));

      const state = await omp.data<{
        dumpTools?: {name: string; description: string}[];
      }>('get_state');
      const listed =
        state.dumpTools
          ?.find(d => d.name === 'task')
          ?.description.includes('skua-node') ?? false;
      await t.annotate(
        `task tool description lists skua-node right after /skua sync: ${String(listed)}`,
      );

      await promptSettled(
        omp,
        batch([
          {
            agent: 'skua-node',
            name: 'RemoteNode',
            task: 'SCENARIO:report Fix org/test#142.',
          },
        ]),
        60_000,
      );
      assert.deepEqual(
        h.runs().map(r => [r.profile, r.phase]),
        [['node', 'Succeeded']],
      );

      await admin(h.api.url, {name: 'node', ready: false});
      const removed = await command(omp, '/skua sync');
      assert.ok(
        removed.some(n =>
          n.startsWith('skua sync: 2 profiles (generic, golang); removed node'),
        ),
        JSON.stringify(removed),
      );
      assert.ok(!existsSync(file('node')));
      assert.ok(existsSync(file('generic')) && existsSync(file('golang')));
      assert.deepEqual(
        omp.notifications().filter(n => n.includes('See extension/README.md.')),
        [],
      );
    } finally {
      await h.close();
    }
  },
);

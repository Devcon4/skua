import assert from 'node:assert/strict';
import {test} from 'vitest';
import {
  batch,
  childMessages,
  command,
  createHarness,
  prompt,
  promptSettled,
  resultOutput,
  stepIds,
  taskResults,
  waitFor,
} from './harness.ts';
import type {Harness} from './harness.ts';

const REPORT_RESULT =
  'Fixed org/test#142. Findings are in docs/findings.md on the branch.';

async function relayedOnce(
  h: Harness,
  scenario: string,
  name: string,
  steps: number,
  timeoutMs: number,
): Promise<void> {
  const omp = await h.start();
  await command(omp, '/skua sync');
  const mark = await promptSettled(
    omp,
    batch([
      {
        agent: 'skua-generic',
        name,
        task: `SCENARIO:${scenario} Fix org/test#142.`,
      },
    ]),
    timeoutMs,
  );
  assert.equal(h.runs().length, 1);
  const [run] = h.runs();
  assert.equal(run?.phase, 'Succeeded');
  const [result] = taskResults(omp, name, mark);
  assert.ok(result);
  assert.ok(resultOutput(result).includes(REPORT_RESULT), result);
  const ids = stepIds(await childMessages(omp, name));
  assert.equal(ids.length, steps, JSON.stringify(ids));
  assert.equal(new Set(ids).size, steps, JSON.stringify(ids));
}

test(
  'a slow /profiles delays neither startup nor a run for a cached profile',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({
      api: {profilesDelayMs: 20_000, stepMs: 300},
      cachedProfiles: [
        {
          name: 'generic',
          description: 'Generic remote agent.',
          models: ['anthropic/claude-sonnet-5'],
          ready: true,
        },
      ],
    });
    try {
      const omp = await h.start();
      const ready = omp.frames.find(f => f.type === 'ready');
      assert.ok(
        ready && ready._t <= 5000,
        `ready after ${String(ready?._t)} ms`,
      );
      const sent = Date.now();
      const mark = await prompt(
        omp,
        batch([
          {
            agent: 'skua-generic',
            name: 'RemoteCached',
            task: 'SCENARIO:report Fix org/test#142.',
          },
        ]),
      );
      await waitFor('the run', () => h.runs()[0], 5000, 50);
      assert.ok(Date.now() - sent <= 5000);
      await omp.waitSettled(60_000, mark);
      const [result] = taskResults(omp, 'RemoteCached', mark);
      assert.ok(result);
      assert.ok(resultOutput(result).includes(REPORT_RESULT), result);
    } finally {
      await h.close();
    }
  },
);

test(
  'an id_token that expires mid-run is refreshed and the stream resumes without repeating a step',
  {timeout: 180_000},
  async () => {
    const h = await createHarness({
      api: {stepMs: 2000, steps: 5, expireStreams: true},
      issuer: {idTokenTtlS: 5},
    });
    try {
      await relayedOnce(h, 'steps', 'RemoteExpiry', 5, 60_000);
      const [run] = h.runs();
      assert.ok(run);
      const exps = h.api
        .requests()
        .filter(
          r =>
            r.method === 'GET' &&
            r.path === `/v1alpha1/runs/${run.name}/events`,
        )
        .map(r => r.exp ?? 0);
      assert.ok(exps.length >= 2, JSON.stringify(exps));
      exps.reduce((previous, exp) => {
        assert.ok(
          exp > previous,
          `bearer exp increases: ${JSON.stringify(exps)}`,
        );
        return exp;
      });
    } finally {
      await h.close();
    }
  },
);

test(
  "a dropped event stream ends with the run's result and each step once",
  {timeout: 120_000},
  async () => {
    const h = await createHarness({api: {stepMs: 500, steps: 4}});
    try {
      await relayedOnce(h, 'drop', 'RemoteDrop', 4, 60_000);
      const [run] = h.runs();
      assert.ok(run);
      assert.ok(
        h.api
          .requests()
          .filter(r => r.path === `/v1alpha1/runs/${run.name}/events`).length >=
          3,
      );
    } finally {
      await h.close();
    }
  },
);

test(
  '429 with Retry-After on /v1alpha1/runs still creates one run',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({
      api: {
        stepMs: 300,
        rateLimit: {path: '/v1alpha1/runs', times: 2, retryAfterS: 1},
      },
    });
    try {
      await relayedOnce(h, 'report', 'RemoteLimited', 2, 60_000);
      assert.equal(
        h.api
          .requests()
          .filter(r => r.path === '/v1alpha1/runs' && r.status === 429).length,
        2,
      );
    } finally {
      await h.close();
    }
  },
);

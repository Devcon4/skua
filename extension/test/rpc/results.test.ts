import assert from 'node:assert/strict';
import {test} from 'vitest';
import {
  batch,
  command,
  createHarness,
  promptSettled,
  resultOutput,
  taskResults,
} from './harness.ts';

const REPORT_RESULT =
  'Fixed org/test#142. Findings are in docs/findings.md on the branch.';
const SCHEMA_RESULT = {
  issue: 142,
  rootCause: 'nil map write in cache.Put',
  confidence: 0.8,
};
const SCHEMA = {
  type: 'object',
  properties: {
    issue: {type: 'number'},
    rootCause: {type: 'string'},
    confidence: {type: 'number'},
  },
  required: ['issue', 'rootCause', 'confidence'],
};

test(
  "a report run's task result carries the result, the branch and the PR link",
  {timeout: 120_000},
  async () => {
    const h = await createHarness({api: {stepMs: 300}});
    try {
      const omp = await h.start();
      await command(omp, '/skua sync');
      const mark = await promptSettled(
        omp,
        batch([
          {
            agent: 'skua-generic',
            name: 'RemoteReport',
            task: 'SCENARIO:report Fix org/test#142.',
          },
        ]),
        60_000,
      );
      const [run] = h.runs();
      assert.ok(run);
      const results = taskResults(omp, 'RemoteReport', mark);
      assert.equal(results.length, 1);
      const [result] = results;
      assert.ok(result);
      const output = resultOutput(result);
      assert.ok(output.includes(REPORT_RESULT), output);
      assert.ok(output.includes(`Branch: skua/${run.name}`), output);
      assert.ok(output.includes('https://github.com/org/test/pull/87'), output);
    } finally {
      await h.close();
    }
  },
);

test(
  "with task.structured the item's outputSchema reaches the run and the JSON result passes omp's check",
  {timeout: 120_000},
  async () => {
    const h = await createHarness({
      api: {stepMs: 300},
      task: {structured: true},
    });
    try {
      const omp = await h.start();
      await command(omp, '/skua sync');
      const mark = await promptSettled(
        omp,
        batch([
          {
            agent: 'skua-generic',
            name: 'RemoteSchema',
            task: 'SCENARIO:schema Find the root cause of org/test#142.',
            outputSchema: SCHEMA,
          },
        ]),
        60_000,
      );
      const [run] = h.runs();
      assert.deepEqual(run?.schema, SCHEMA);
      const results = taskResults(omp, 'RemoteSchema', mark);
      assert.equal(results.length, 1);
      const [result] = results;
      assert.ok(result);
      assert.ok(result.includes('status="completed"'), result);
      assert.ok(result.includes('Structured output: schema valid'), result);
      assert.deepEqual(JSON.parse(resultOutput(result)), SCHEMA_RESULT);
    } finally {
      await h.close();
    }
  },
);

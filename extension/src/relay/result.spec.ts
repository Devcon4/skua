import assert from 'node:assert/strict';
import {describe, test} from 'vitest';
import type {Run} from '../contract/wire.ts';
import {
  finalText,
  firstPrompt,
  followUpPrompt,
  prUrl,
  schemaFromYieldTool,
  truncateStepResult,
  yieldAcceptsError,
} from './result.ts';
import type {ResolvedWorkspace} from './workspace.ts';

const objectSchema = {
  type: 'object',
  properties: {
    files: {type: 'array', items: {type: 'string'}},
    count: {type: 'number'},
  },
  required: ['files', 'count'],
};

function yieldTool(data: unknown, withError = true) {
  const properties: Record<string, unknown> = {type: {type: 'string'}, data};
  if (withError)
    properties['error'] = {
      type: 'string',
      description: 'Failure reason; mutually exclusive with data',
    };
  return {
    name: 'yield',
    description: 'submit',
    parameters: {type: 'object', properties, required: []},
  };
}

const embedded = yieldTool({
  description: `Structured output matching the schema:\n${JSON.stringify(objectSchema, null, 2)}`,
  anyOf: [objectSchema, objectSchema.properties.files],
});
const noSchema = yieldTool({
  type: 'object',
  additionalProperties: true,
  description: 'Structured JSON output (no schema specified)',
});
const ws: ResolvedWorkspace = {
  kind: 'ok',
  workspace: {repo: 'org/test', baseRef: 'abc'},
  host: 'github.com',
};

function run(over: Partial<Run>): Run {
  return {
    name: 'x',
    profile: 'p',
    clientKey: 's:0',
    phase: 'Succeeded',
    reason: 'Completed',
    result: null,
    outputs: {},
    createdAt: '2026-11-03T00:00:00Z',
    ...over,
  };
}

describe('schemaFromYieldTool', () => {
  test('the description-embedded schema parses to the object schema', () => {
    assert.deepEqual(
      schemaFromYieldTool([{name: 'read', parameters: {}}, embedded]),
      objectSchema,
    );
  });

  test('a data property with a JSON-schema type is returned as is', () => {
    const data = {type: 'array', items: {type: 'string'}};
    assert.deepEqual(schemaFromYieldTool([yieldTool(data)]), data);
  });

  test('a yield without a schema returns undefined', () => {
    assert.equal(schemaFromYieldTool([noSchema]), undefined);
    assert.equal(
      schemaFromYieldTool([yieldTool({description: 'anything'})]),
      undefined,
    );
    assert.equal(schemaFromYieldTool([]), undefined);
    assert.equal(schemaFromYieldTool(undefined), undefined);
  });
});

describe('yieldAcceptsError', () => {
  test('follows the error property', () => {
    assert.equal(yieldAcceptsError([embedded]), true);
    assert.equal(yieldAcceptsError([yieldTool({}, false)]), false);
    assert.equal(yieldAcceptsError(undefined), false);
  });
});

describe('prUrl', () => {
  test('a number with host and repo links the PR', () => {
    assert.equal(
      prUrl({pullRequest: 87}, ws),
      'https://github.com/org/test/pull/87',
    );
  });

  test('a URL is used verbatim and a bare number falls back to PR #n', () => {
    assert.equal(
      prUrl({pullRequest: 'https://ghe.example.com/a/b/pull/3'}, ws),
      'https://ghe.example.com/a/b/pull/3',
    );
    assert.equal(prUrl({pullRequest: 87}, undefined), 'PR #87');
    assert.equal(prUrl({}, ws), undefined);
  });
});

describe('finalText', () => {
  test('a failed run with null result names the reason', () => {
    assert.equal(
      finalText(run({phase: 'Failed', reason: 'BudgetExceeded'}), ws),
      'Remote run x failed: BudgetExceeded.',
    );
  });

  test('a JSON result renders as 2-space JSON', () => {
    assert.equal(
      finalText(run({result: {files: ['a'], count: 1}}), undefined),
      JSON.stringify({files: ['a'], count: 1}, null, 2),
    );
  });

  test('a string result is followed by branch and PR lines', () => {
    const r = run({
      result: 'done',
      outputs: {branch: 'skua/x', pullRequest: 87},
    });
    assert.equal(
      finalText(r, ws),
      'done\n\nBranch: skua/x\nPR: https://github.com/org/test/pull/87',
    );
  });

  test('a failed run keeps its result and outputs', () => {
    const r = run({
      phase: 'Failed',
      reason: 'Error',
      result: 'partial',
      outputs: {branch: 'skua/x'},
    });
    assert.equal(
      finalText(r, ws),
      'Remote run x failed: Error.\n\npartial\n\nBranch: skua/x',
    );
  });
});

describe('prompts', () => {
  test('followUpPrompt carries the previous run and the new texts', () => {
    const prev = run({result: 'first', outputs: {branch: 'skua/x'}});
    assert.equal(
      followUpPrompt(prev, ['one', 'two']),
      'Previous skua run x (Succeeded/Completed) result:\nfirst\nOutputs: {"branch":"skua/x"}\n\nFollow-up:\none\n\ntwo',
    );
  });

  test('firstPrompt puts the batch context before the assignments', () => {
    assert.equal(firstPrompt('ctx', ['a', 'b']), 'ctx\n\na\n\nb');
    assert.equal(firstPrompt(undefined, ['a']), 'a');
  });
});

describe('truncateStepResult', () => {
  test('a 40 KiB result is cut to 32 KiB plus the marker', () => {
    const s = 'a'.repeat(40 * 1024);
    assert.equal(
      truncateStepResult(s),
      `${'a'.repeat(32 * 1024)}\n[truncated ${String(8 * 1024)} bytes]`,
    );
  });

  test('a short result is unchanged and a cut never splits a character', () => {
    assert.equal(truncateStepResult('ok'), 'ok');
    const s = `${'a'.repeat(32 * 1024 - 1)}é${'b'.repeat(10)}`;
    assert.equal(
      truncateStepResult(s),
      `${'a'.repeat(32 * 1024 - 1)}\n[truncated 12 bytes]`,
    );
  });
});

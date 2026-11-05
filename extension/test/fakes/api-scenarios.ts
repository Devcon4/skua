import type {FakeOutputs, FakePhase} from './api.ts';

export interface RunOutcome {
  phase: FakePhase;
  reason: string;
  result: unknown;
  outputs: FakeOutputs;
}

export interface ScriptedTool {
  name: string;
  args: Record<string, unknown>;
  partial: string;
  result: unknown;
}

const DEFAULT_SCENARIO = 'report';
const SILENT_HEARTBEAT_STEP = 2;
const REPORT_RESULT =
  'Fixed org/test#142. Findings are in docs/findings.md on the branch.';
const SCHEMA_RESULT = {
  issue: 142,
  rootCause: 'nil map write in cache.Put',
  confidence: 0.8,
};
const MIXED_CONTENT_SHAPES: unknown[] = [
  {
    content: [
      {type: 'text', text: 'a'},
      {type: 'image'},
      {type: 'text', text: 'b'},
    ],
  },
  42,
];
const CONFIGURED_STEP_SCENARIOS = new Set(['steps', 'nohb', 'drop']);
const FIXED_STEP_COUNTS = new Map([
  ['fail', 1],
  ['long', 0],
]);
const DEFAULT_STEP_COUNT = 2;

export function cancelledOutcome(): RunOutcome {
  return {phase: 'Failed', reason: 'Cancelled', result: null, outputs: {}};
}

export function scenarioOf(prompt: string): string {
  return /SCENARIO:([a-z-]+)/.exec(prompt)?.[1] ?? DEFAULT_SCENARIO;
}

export function stepCount(scenario: string, configuredSteps: number): number {
  if (CONFIGURED_STEP_SCENARIOS.has(scenario)) return configuredSteps;
  return FIXED_STEP_COUNTS.get(scenario) ?? DEFAULT_STEP_COUNT;
}

export function opensWithSilence(scenario: string): boolean {
  return scenario === 'long';
}

export function stepsUseTools(scenario: string): boolean {
  return scenario !== 'notools';
}

export function heartbeatStopsAfterStep(scenario: string, i: number): boolean {
  return scenario === 'nohb' && i === SILENT_HEARTBEAT_STEP;
}

export function createAnswerFails(scenario: string): boolean {
  return scenario === 'flaky-create';
}

export function streamDropsAfterToolEnd(
  scenario: string,
  connection: number,
): boolean {
  return scenario === 'drop' && connection <= 2;
}

export function scenarioOutcome(scenario: string, runName: string): RunOutcome {
  if (scenario === 'fail')
    return {
      phase: 'Failed',
      reason: 'BudgetExceeded',
      result: null,
      outputs: {},
    };
  if (scenario === 'schema')
    return {
      phase: 'Succeeded',
      reason: 'Completed',
      result: SCHEMA_RESULT,
      outputs: {},
    };
  return {
    phase: 'Succeeded',
    reason: 'Completed',
    result: REPORT_RESULT,
    outputs: {branch: `skua/${runName}`, pullRequest: 87},
  };
}

function toolResult(scenario: string, i: number, plain: string): unknown {
  if (scenario !== 'shapes') return plain;
  return MIXED_CONTENT_SHAPES[(i - 1) % MIXED_CONTENT_SHAPES.length];
}

export function stepTool(scenario: string, i: number): ScriptedTool {
  const step = String(i);
  if (i % 2 === 1)
    return {
      name: 'read',
      args: {path: `src/file${step}.go`},
      partial: `package pkg${step}`,
      result: toolResult(
        scenario,
        i,
        `package pkg${step}\n// ${String(i * 10)} lines`,
      ),
    };
  return {
    name: 'bash',
    args: {command: `go test ./pkg${step}/...`},
    partial: `ok  pkg${step}`,
    result: toolResult(scenario, i, `ok  pkg${step} 0.${step}s`),
  };
}

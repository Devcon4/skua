export interface Profile {
  name: string;
  description: string;
  models: string[];
  ready: boolean;
}

export interface Workspace {
  repo: string;
  baseRef: string;
}

export interface CreateRunBody {
  profile: string;
  clientKey: string;
  workspace?: Workspace;
  task: {prompt: string; schema?: unknown};
}

export type RunPhase = 'Pending' | 'Running' | 'Succeeded' | 'Failed';

export interface RunOutputs {
  branch?: string;
  pullRequest?: string | number;
}

export interface Run {
  name: string;
  profile: string;
  clientKey: string;
  phase: RunPhase;
  reason: string;
  result: unknown;
  outputs: RunOutputs;
  createdAt: string;
}

export type RunEvent =
  | {seq: number; type: 'text'; data: {delta: string}}
  | {seq: number; type: 'thinking'; data: {delta: string}}
  | {
      seq: number;
      type: 'tool_start';
      data: {id: string; name: string; args: unknown};
    }
  | {seq: number; type: 'tool_update'; data: {id: string; partial: unknown}}
  | {
      seq: number;
      type: 'tool_end';
      data: {id: string; result: unknown; isError: boolean};
    }
  | {seq: number; type: 'status'; data: {phase: RunPhase; reason?: string}}
  | {
      seq: number;
      type: 'end';
      data: {
        phase: RunPhase;
        reason: string;
        result: unknown;
        outputs: RunOutputs;
      };
    };

export type RunEventType = RunEvent['type'];

const PHASES: readonly string[] = ['Pending', 'Running', 'Succeeded', 'Failed'];

type Fields = Readonly<Record<string, unknown>>;

function isObject(v: unknown): v is Fields {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function isString(v: unknown): v is string {
  return typeof v === 'string';
}

function isAbsentOrString(v: unknown): v is string | undefined {
  return v === undefined || isString(v);
}

function isAbsentOrPullRequest(v: unknown): v is string | number | undefined {
  return isAbsentOrString(v) || typeof v === 'number';
}

function isPhase(v: unknown): v is RunPhase {
  return isString(v) && PHASES.includes(v);
}

function isOutputs(v: unknown): v is RunOutputs {
  if (!isObject(v)) return false;
  const {branch, pullRequest} = v;
  return isAbsentOrString(branch) && isAbsentOrPullRequest(pullRequest);
}

export function isProfile(v: unknown): v is Profile {
  if (!isObject(v)) return false;
  const {name, description, models, ready} = v;
  return (
    isString(name) &&
    isString(description) &&
    Array.isArray(models) &&
    models.every(isString) &&
    typeof ready === 'boolean'
  );
}

export function isRun(v: unknown): v is Run {
  if (!isObject(v)) return false;
  const {name, profile, clientKey, phase, reason, outputs, createdAt} = v;
  return (
    isString(name) &&
    isString(profile) &&
    isString(clientKey) &&
    isPhase(phase) &&
    isString(reason) &&
    isOutputs(outputs) &&
    isString(createdAt)
  );
}

type EventDataCheck = (data: Fields) => boolean;

const hasDelta: EventDataCheck = ({delta}) => isString(delta);

const EVENT_DATA_CHECKS: Readonly<Record<RunEventType, EventDataCheck>> = {
  text: hasDelta,
  thinking: hasDelta,
  tool_start: ({id, name}) => isString(id) && isString(name),
  tool_update: ({id}) => isString(id),
  tool_end: ({id, isError}) => isString(id) && typeof isError === 'boolean',
  status: ({phase, reason}) => isPhase(phase) && isAbsentOrString(reason),
  end: ({phase, reason, outputs}) =>
    isPhase(phase) && isString(reason) && isOutputs(outputs),
};

const EVENT_TYPES: readonly string[] = Object.keys(EVENT_DATA_CHECKS);

function isEventType(v: unknown): v is RunEventType {
  return isString(v) && EVENT_TYPES.includes(v);
}

function isEventData(type: unknown, data: unknown): boolean {
  if (!isObject(data) || !isEventType(type)) return false;
  return EVENT_DATA_CHECKS[type](data);
}

function isSeq(v: unknown): v is number {
  return typeof v === 'number' && Number.isInteger(v);
}

export function isRunEvent(v: unknown): v is RunEvent {
  if (!isObject(v)) return false;
  const {seq, type, data} = v;
  return isSeq(seq) && isEventData(type, data);
}

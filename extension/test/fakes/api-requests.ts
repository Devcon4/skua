import type {FakeProfileInput, FakeWorkspace} from './api.ts';

export type Parsed<T> = {ok: true; value: T} | {ok: false; error: string};

export interface BearerClaims {
  iss: string;
  sub: string;
  exp: number | undefined;
}

export interface CreateRunRequest {
  profile: string;
  clientKey: string;
  workspace: FakeWorkspace | undefined;
  prompt: string;
  schema: unknown;
}

export interface RunPath {
  name: string;
  sub: string;
}

interface RunTask {
  prompt: string;
  schema: unknown;
}

const RUN_PATH = /^\/v1alpha1\/runs\/([^/]+)(\/events|\/steer|\/abort)?$/;
const WORKSPACE_SHAPE = 'workspace must be {repo, baseRef}';

function valid<T>(value: T): Parsed<T> {
  return {ok: true, value};
}

function invalid<T>(error: string): Parsed<T> {
  return {ok: false, error};
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function isNonEmptyString(v: unknown): v is string {
  return typeof v === 'string' && v !== '';
}

function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every(x => typeof x === 'string');
}

export function parseJson(text: string): unknown {
  try {
    const v: unknown = JSON.parse(text);
    return v;
  } catch {
    return undefined;
  }
}

export function parseBearer(
  header: string | undefined,
): BearerClaims | undefined {
  const token = /^Bearer (\S+)$/.exec(header ?? '')?.[1];
  if (token === undefined) return undefined;
  const [, payloadPart] = token.split('.');
  if (payloadPart === undefined) return undefined;
  const payload = parseJson(
    Buffer.from(payloadPart, 'base64url').toString('utf8'),
  );
  if (!isRecord(payload)) return undefined;
  const {iss, sub, exp} = payload;
  if (typeof iss !== 'string' || typeof sub !== 'string') return undefined;
  return {iss, sub, exp: typeof exp === 'number' ? exp : undefined};
}

function parseWorkspace(v: unknown): Parsed<FakeWorkspace | undefined> {
  if (v === undefined) return valid(undefined);
  if (!isRecord(v)) return invalid(WORKSPACE_SHAPE);
  const {repo, baseRef} = v;
  if (typeof repo !== 'string' || typeof baseRef !== 'string')
    return invalid(WORKSPACE_SHAPE);
  return valid({repo, baseRef});
}

function parseTask(v: unknown): RunTask | undefined {
  if (!isRecord(v)) return undefined;
  const {prompt, schema} = v;
  if (typeof prompt !== 'string') return undefined;
  return {prompt, schema};
}

export function parseCreateBody(v: unknown): Parsed<CreateRunRequest> {
  if (!isRecord(v)) return invalid('body must be a JSON object');
  const {profile, clientKey, workspace: rawWorkspace, task: rawTask} = v;
  if (!isNonEmptyString(profile))
    return invalid('profile must be a non-empty string');
  if (!isNonEmptyString(clientKey))
    return invalid('clientKey must be a non-empty string');
  const workspace = parseWorkspace(rawWorkspace);
  if (!workspace.ok) return invalid(workspace.error);
  const task = parseTask(rawTask);
  if (task === undefined) return invalid('task.prompt must be a string');
  return valid({
    profile,
    clientKey,
    workspace: workspace.value,
    prompt: task.prompt,
    schema: task.schema,
  });
}

export function parseProfileInput(v: unknown): FakeProfileInput | undefined {
  if (!isRecord(v)) return undefined;
  const {name, description, models, ready} = v;
  if (!isNonEmptyString(name)) return undefined;
  return {
    name,
    description: typeof description === 'string' ? description : undefined,
    models: isStringArray(models) ? models : undefined,
    ready: typeof ready === 'boolean' ? ready : undefined,
  };
}

export function parseSteerMessage(v: unknown): string | undefined {
  if (!isRecord(v)) return undefined;
  const {message} = v;
  return typeof message === 'string' ? message : undefined;
}

export function parseRunPath(path: string): RunPath | undefined {
  const m = RUN_PATH.exec(path);
  if (!m) return undefined;
  const [, name = '', sub = ''] = m;
  return {name, sub};
}

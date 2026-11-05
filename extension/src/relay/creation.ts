import type {CreateRunBody, Run, Workspace} from '../contract/wire.ts';
import type {Api} from '../integration/skuaapi/client.ts';
import {ApiError} from '../integration/skuaapi/errors.ts';
import {createErrorText} from './errors.ts';
import {schemaFromYieldTool} from './result.ts';
import type {ToolLike} from './result.ts';
import {sleepUnlessAborted} from './timers.ts';
import type {RelayTimers} from './timers.ts';
import type {Turn} from './turn.ts';

export type Created =
  {kind: 'run'; run: Run} | {kind: 'stop'; text: string} | {kind: 'aborted'};

export type NotCreated = Exclude<Created, {kind: 'run'}>;

export interface RunCreator<H> {
  api: Api;
  timers: RelayTimers<H>;
  log: (message: string) => void;
}

export interface RunRequest {
  profile: string;
  clientKey: string;
  prompt: string;
  workspace: Workspace | undefined;
  tools: readonly ToolLike[] | undefined;
}

const CREATE_BACKOFF_MS = [1_000, 2_000, 4_000];

export function createRunBody(request: RunRequest): CreateRunBody {
  const body: CreateRunBody = {
    profile: request.profile,
    clientKey: request.clientKey,
    task: {prompt: request.prompt},
  };
  if (request.workspace !== undefined) body.workspace = request.workspace;
  const schema = schemaFromYieldTool(request.tools);
  if (schema !== undefined) body.task.schema = schema;
  return body;
}

function failureIsTransient(err: unknown): err is ApiError {
  return (
    err instanceof ApiError && (err.code === 'server' || err.code === 'network')
  );
}

export async function createRunWithRetry<H>(
  creator: RunCreator<H>,
  body: CreateRunBody,
  signal: AbortSignal,
): Promise<Created> {
  for (let attempt = 0; ; attempt++) {
    try {
      return {kind: 'run', run: (await creator.api.createRun(body)).run};
    } catch (err) {
      const wait = CREATE_BACKOFF_MS[attempt];
      if (!failureIsTransient(err) || wait === undefined)
        return {kind: 'stop', text: createErrorText(err, body.profile)};
      creator.log(
        `skua: creating run ${body.clientKey} failed (${err.message}); retry in ${String(wait)} ms`,
      );
      if (!(await sleepUnlessAborted(creator.timers, wait, signal)))
        return {kind: 'aborted'};
    }
  }
}

export function endTurnWithoutRun(turn: Turn, outcome: NotCreated): void {
  if (outcome.kind === 'stop') {
    turn.final(outcome.text);
    return;
  }
  turn.aborted();
}

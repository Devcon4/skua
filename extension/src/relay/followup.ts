import type {Run, Workspace} from '../contract/wire.ts';
import type {Api} from '../integration/skuaapi/client.ts';
import {createRunBody, createRunWithRetry} from './creation.ts';
import type {Created, RunCreator} from './creation.ts';
import {unpushedText} from './errors.ts';
import {followUpPrompt} from './result.ts';
import type {ToolLike} from './result.ts';
import {adoptFollowUpRun} from './session.ts';
import type {SessionState} from './session.ts';
import type {ResolvedWorkspace, WorkspaceResolution} from './workspace.ts';

export interface FollowUpRequest {
  sessionId: string;
  state: SessionState;
  previous: Run;
  inputs: string[];
  tools: readonly ToolLike[] | undefined;
  currentWorkspace: () => Promise<WorkspaceResolution>;
}

type FollowUpWorkspace =
  | {kind: 'none'}
  | {kind: 'ok'; workspace: Workspace}
  | {kind: 'unpushed'; sha: string};

type FollowUpBase =
  {kind: 'ref'; ref: string} | {kind: 'unpushed'; sha: string};

/** Steers the live run with each input until it turns terminal; returns the inputs it did not take. */
export async function steerLiveRun(
  api: Api,
  state: SessionState,
  inputs: string[],
): Promise<string[]> {
  if (state.terminal !== undefined) return inputs;
  for (const [index, input] of inputs.entries()) {
    if ((await api.steer(state.run, input)) === 'terminal') {
      state.terminal = await api.getRun(state.run);
      return inputs.slice(index);
    }
  }
  return [];
}

async function followUpBase(
  origin: ResolvedWorkspace,
  previous: Run,
  currentWorkspace: () => Promise<WorkspaceResolution>,
): Promise<FollowUpBase> {
  const branch = previous.outputs.branch;
  if (branch !== undefined) return {kind: 'ref', ref: branch};
  const current = await currentWorkspace();
  if (current.kind === 'unpushed') return current;
  const ref =
    current.kind === 'ok'
      ? current.workspace.baseRef
      : origin.workspace.baseRef;
  return {kind: 'ref', ref};
}

async function followUpWorkspace(
  request: FollowUpRequest,
): Promise<FollowUpWorkspace> {
  const origin = request.state.workspace;
  if (origin === undefined) return {kind: 'none'};
  const base = await followUpBase(
    origin,
    request.previous,
    request.currentWorkspace,
  );
  if (base.kind === 'unpushed') return base;
  return {
    kind: 'ok',
    workspace: {repo: origin.workspace.repo, baseRef: base.ref},
  };
}

export async function createFollowUpRun<H>(
  creator: RunCreator<H>,
  request: FollowUpRequest,
  signal: AbortSignal,
): Promise<Created> {
  const {state} = request;
  const workspace = await followUpWorkspace(request);
  if (workspace.kind === 'unpushed')
    return {kind: 'stop', text: unpushedText(workspace.sha)};
  const followUp = state.followUp + 1;
  const body = createRunBody({
    profile: state.profile,
    clientKey: `${request.sessionId}:${String(followUp)}`,
    prompt: followUpPrompt(request.previous, request.inputs),
    workspace: workspace.kind === 'ok' ? workspace.workspace : undefined,
    tools: request.tools,
  });
  const created = await createRunWithRetry(creator, body, signal);
  if (created.kind === 'run') adoptFollowUpRun(state, created.run, followUp);
  return created;
}

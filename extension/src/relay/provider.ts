import type {Message} from '@oh-my-pi/pi-ai';
import type {Api} from '../integration/skuaapi/client.ts';
import {
  createRunBody,
  createRunWithRetry,
  endTurnWithoutRun,
} from './creation.ts';
import {streamErrorText, unpushedText} from './errors.ts';
import {relayRunEvents} from './events.ts';
import {emitTerminalRun} from './final.ts';
import {createFollowUpRun, steerLiveRun} from './followup.ts';
import {extractBatchContext} from './inputs.ts';
import {pendingInputs, systemPromptText, yieldRejected} from './messages.ts';
import {firstPrompt} from './result.ts';
import type {ToolLike} from './result.ts';
import {openSession, rebuildSession} from './session.ts';
import type {SessionState} from './session.ts';
import type {RelayTimers} from './timers.ts';
import {createTurn} from './turn.ts';
import type {RelayModel, RelayPush, Turn} from './turn.ts';
import type {WorkspaceResolution} from './workspace.ts';

export interface RelayContext {
  systemPrompt?: string | readonly string[];
  messages: readonly Message[];
  tools?: readonly ToolLike[];
}

export interface RelayOptions {
  sessionId?: string | undefined;
  signal?: AbortSignal | undefined;
  cwd?: string | undefined;
}

export interface RelayDeps<H> {
  api: Api;
  resolveWorkspace: (cwd: string) => Promise<WorkspaceResolution>;
  heartbeatTimeoutMs: number;
  giveUpMs?: number;
  timers: RelayTimers<H>;
  log: (message: string) => void;
}

export interface LiveRun {
  sessionId: string;
  run: string;
  followUp: number;
}

export interface Relay {
  stream(
    model: RelayModel,
    context: RelayContext,
    options: RelayOptions | undefined,
    push: RelayPush,
  ): Promise<void>;
  forget(sessionId: string): void;
  live(): LiveRun[];
}

interface RelayCall {
  turn: Turn;
  sessionId: string;
  model: RelayModel;
  context: RelayContext;
  cwd: string | undefined;
  signal: AbortSignal;
}

type SessionOutcome = {kind: 'relaying'; state: SessionState} | {kind: 'ended'};

const NO_OPTIONS: RelayOptions = {};
const TURN_ENDED: SessionOutcome = {kind: 'ended'};

export function createRelay<H>(deps: RelayDeps<H>): Relay {
  const {api} = deps;
  const giveUpMs = deps.giveUpMs ?? 600_000;
  const sessions = new Map<string, SessionState>();

  const resolveIn = async (
    cwd: string | undefined,
  ): Promise<WorkspaceResolution> =>
    cwd === undefined ? {kind: 'none'} : deps.resolveWorkspace(cwd);

  const knownSession = async (
    call: RelayCall,
  ): Promise<SessionState | undefined> => {
    const known = sessions.get(call.sessionId);
    if (known !== undefined) return known;
    const rebuilt = await rebuildSession(
      api,
      call.sessionId,
      call.context.messages,
      () => resolveIn(call.cwd),
    );
    if (rebuilt !== undefined) sessions.set(call.sessionId, rebuilt);
    return rebuilt;
  };

  const startFirstRun = async (
    call: RelayCall,
    inputs: string[],
  ): Promise<SessionOutcome> => {
    const {turn, context} = call;
    const workspace = await resolveIn(call.cwd);
    if (workspace.kind === 'unpushed') {
      turn.final(unpushedText(workspace.sha));
      return TURN_ENDED;
    }
    const body = createRunBody({
      profile: call.model.id,
      clientKey: `${call.sessionId}:0`,
      prompt: firstPrompt(
        extractBatchContext(systemPromptText(context.systemPrompt)),
        inputs,
      ),
      workspace: workspace.kind === 'ok' ? workspace.workspace : undefined,
      tools: context.tools,
    });
    const created = await createRunWithRetry(deps, body, call.signal);
    if (created.kind !== 'run') {
      endTurnWithoutRun(turn, created);
      return TURN_ENDED;
    }
    const state = openSession({
      run: created.run,
      followUp: 0,
      profile: call.model.id,
      committedSeq: 0,
      relayedCount: context.messages.length,
      workspace,
    });
    sessions.set(call.sessionId, state);
    return {kind: 'relaying', state};
  };

  const continueSession = async (
    call: RelayCall,
    state: SessionState,
    inputs: string[],
  ): Promise<SessionOutcome> => {
    const undelivered = await steerLiveRun(api, state, inputs);
    if (state.terminal === undefined || undelivered.length === 0)
      return {kind: 'relaying', state};
    const created = await createFollowUpRun(
      deps,
      {
        sessionId: call.sessionId,
        state,
        previous: state.terminal,
        inputs: undelivered,
        tools: call.context.tools,
        currentWorkspace: () => resolveIn(call.cwd),
      },
      call.signal,
    );
    if (created.kind === 'run') return {kind: 'relaying', state};
    endTurnWithoutRun(call.turn, created);
    return TURN_ENDED;
  };

  const relay = async (call: RelayCall): Promise<void> => {
    const {turn, context, signal} = call;
    const known = await knownSession(call);
    const inputs = pendingInputs(context.messages, known?.relayedCount ?? 0);
    const outcome =
      known === undefined
        ? await startFirstRun(call, inputs)
        : await continueSession(call, known, inputs);
    if (outcome.kind === 'ended') return;

    const {state} = outcome;
    state.relayedCount = context.messages.length;
    if (yieldRejected(context.messages, state.run))
      state.rejectedYieldFor = state.run;
    if (state.terminal !== undefined) {
      emitTerminalRun(turn, state, state.terminal, context.tools);
      return;
    }
    if (signal.aborted) {
      turn.aborted();
      return;
    }
    await relayRunEvents({
      api,
      turn,
      state,
      tools: context.tools,
      signal,
      heartbeatTimeoutMs: deps.heartbeatTimeoutMs,
      giveUpMs,
    });
  };

  return {
    stream: async (model, context, options, push) => {
      const turn = createTurn(model, push);
      const {sessionId, cwd, signal} = options ?? NO_OPTIONS;
      if (sessionId === undefined || sessionId === '') {
        turn.final('skua relay error: omp passed no session id.');
        return;
      }
      const callSignal = signal ?? new AbortController().signal;
      try {
        await relay({turn, sessionId, model, context, cwd, signal: callSignal});
      } catch (err) {
        if (callSignal.aborted) {
          turn.aborted();
          return;
        }
        turn.final(streamErrorText(err, sessions.get(sessionId)?.run));
      }
    },
    forget: sessionId => {
      sessions.delete(sessionId);
    },
    live: () =>
      [...sessions].map(([sessionId, s]) => ({
        sessionId,
        run: s.run,
        followUp: s.followUp,
      })),
  };
}

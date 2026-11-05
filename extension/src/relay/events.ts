import type {Run, RunEvent} from '../contract/wire.ts';
import type {Api} from '../integration/skuaapi/client.ts';
import {emitTerminalRun} from './final.ts';
import {stepResultText} from './messages.ts';
import {truncateStepResult} from './result.ts';
import type {ToolLike} from './result.ts';
import type {SessionState} from './session.ts';
import type {Turn} from './turn.ts';

export interface EventRelay {
  api: Api;
  turn: Turn;
  state: SessionState;
  tools: readonly ToolLike[] | undefined;
  signal: AbortSignal;
  heartbeatTimeoutMs: number;
  giveUpMs: number;
}

type EventEffect = 'streaming' | 'turnDone';

type ToolEndEvent = Extract<RunEvent, {type: 'tool_end'}>;
type EndEvent = Extract<RunEvent, {type: 'end'}>;

function emitStep(turn: Turn, state: SessionState, ev: ToolEndEvent): void {
  const tool = state.openTools.get(ev.data.id);
  state.openTools.delete(ev.data.id);
  turn.toolCall({
    type: 'toolCall',
    id: `skua_${state.run}_${String(ev.seq)}`,
    name: 'skua_step',
    arguments: {
      tool: tool?.name ?? 'unknown',
      args: tool?.args ?? null,
      result: truncateStepResult(stepResultText(ev.data.result)),
      isError: ev.data.isError,
    },
  });
  state.committedSeq = ev.seq;
  turn.done('toolUse');
}

function emitEnd(relay: EventRelay, ev: EndEvent): void {
  const {state} = relay;
  const run: Run = {
    ...state.started,
    phase: ev.data.phase,
    reason: ev.data.reason,
    result: ev.data.result,
    outputs: ev.data.outputs,
  };
  state.terminal = run;
  state.committedSeq = ev.seq;
  emitTerminalRun(relay.turn, state, run, relay.tools);
}

function applyEvent(relay: EventRelay, ev: RunEvent): EventEffect {
  const {turn, state} = relay;
  if (ev.type !== 'thinking') turn.closeThinking();
  switch (ev.type) {
    case 'text':
      turn.text(ev.data.delta);
      return 'streaming';
    case 'thinking':
      turn.thinking(ev.data.delta);
      return 'streaming';
    case 'tool_start':
      state.openTools.set(ev.data.id, {name: ev.data.name, args: ev.data.args});
      return 'streaming';
    case 'tool_update':
    case 'status':
      return 'streaming';
    case 'tool_end':
      emitStep(turn, state, ev);
      return 'turnDone';
    case 'end':
      emitEnd(relay, ev);
      return 'turnDone';
  }
}

/** Relays run events into the turn until a step or the run ends, then closes the turn. */
export async function relayRunEvents(relay: EventRelay): Promise<void> {
  const {api, turn, state, signal} = relay;
  const name = state.run;
  for await (const ev of api.followEvents(name, {
    afterSeq: state.committedSeq,
    signal,
    heartbeatTimeoutMs: relay.heartbeatTimeoutMs,
    giveUpMs: relay.giveUpMs,
  })) {
    if (applyEvent(relay, ev) === 'turnDone') return;
  }
  if (signal.aborted) {
    turn.aborted();
    return;
  }
  turn.final(
    `skua relay error: the event stream for run ${name} closed before its end event.`,
  );
}

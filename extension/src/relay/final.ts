import type {Run} from '../contract/wire.ts';
import {yieldData} from './messages.ts';
import {outputLines} from './outputs.ts';
import {finalText, schemaFromYieldTool, yieldAcceptsError} from './result.ts';
import type {ToolLike} from './result.ts';
import type {SessionState} from './session.ts';
import type {Turn} from './turn.ts';

type FinalForm = 'text' | 'yieldData' | 'yieldError';

function finalForm(
  state: SessionState,
  run: Run,
  tools: readonly ToolLike[] | undefined,
): FinalForm {
  const callerHasYield = tools?.some(t => t.name === 'yield') ?? false;
  if (!callerHasYield || state.rejectedYieldFor === run.name) return 'text';
  if (run.phase === 'Succeeded') return 'yieldData';
  return yieldAcceptsError(tools) ? 'yieldError' : 'text';
}

function yieldResult(
  turn: Turn,
  state: SessionState,
  run: Run,
  tools: readonly ToolLike[] | undefined,
): void {
  const lines = outputLines(run.outputs, state.workspace);
  if (lines.length > 0) turn.block(lines.join('\n'));
  const data = yieldData(run.result, schemaFromYieldTool(tools) !== undefined);
  turn.toolCall({
    type: 'toolCall',
    id: `skua_yield_${run.name}`,
    name: 'yield',
    arguments: {data},
  });
  turn.done('toolUse');
}

function yieldFailure(turn: Turn, run: Run, text: string): void {
  turn.toolCall({
    type: 'toolCall',
    id: `skua_yield_${run.name}`,
    name: 'yield',
    arguments: {error: text},
  });
  turn.done('toolUse');
}

export function emitTerminalRun(
  turn: Turn,
  state: SessionState,
  run: Run,
  tools: readonly ToolLike[] | undefined,
): void {
  const text = finalText(run, state.workspace);
  switch (finalForm(state, run, tools)) {
    case 'text':
      turn.final(text);
      return;
    case 'yieldData':
      yieldResult(turn, state, run, tools);
      return;
    case 'yieldError':
      yieldFailure(turn, run, text);
      return;
  }
}

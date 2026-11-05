import type {
  AssistantMessage,
  ImageContent,
  Message,
  TextContent,
  ToolCall,
} from '@oh-my-pi/pi-ai';
import {STEP_ID, YIELD_ID} from './ids.ts';
import {classifyInput} from './inputs.ts';

export interface RunAnchor {
  run: string;
  seq?: number;
}

interface TextPart {
  type: 'text';
  text: string;
}

type AssistantBlock = AssistantMessage['content'][number];

function isPlainObject(v: unknown): v is object {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function isTextPart(part: unknown): part is TextPart {
  return (
    isPlainObject(part) &&
    'type' in part &&
    part.type === 'text' &&
    'text' in part &&
    typeof part.text === 'string'
  );
}

function hasContentArray(result: unknown): result is {content: unknown[]} {
  return (
    isPlainObject(result) &&
    'content' in result &&
    Array.isArray(result.content)
  );
}

// JSON.stringify returns undefined for undefined, which its lib type hides.
const jsonOrUndefined = (value: unknown): string | undefined =>
  JSON.stringify(value);

export function contentText(
  content: string | readonly (TextContent | ImageContent)[],
): string {
  if (typeof content === 'string') return content;
  return content
    .filter(c => c.type === 'text')
    .map(c => c.text)
    .join('\n');
}

export function stepResultText(result: unknown): string {
  if (typeof result === 'string') return result;
  if (hasContentArray(result)) {
    return result.content
      .filter(isTextPart)
      .map(p => p.text)
      .join('\n');
  }
  return jsonOrUndefined(result) ?? '';
}

export function systemPromptText(
  prompt: string | readonly string[] | undefined,
): string | undefined {
  if (prompt === undefined || typeof prompt === 'string') return prompt;
  return prompt.join('\n');
}

export function yieldData(result: unknown, schemaSent: boolean): unknown {
  if (typeof result !== 'string' || !schemaSent) return result;
  try {
    const parsed: unknown = JSON.parse(result);
    return parsed;
  } catch {
    return result;
  }
}

function isToolCall(block: AssistantBlock): block is ToolCall {
  return typeof block !== 'string' && block.type === 'toolCall';
}

function anchorOfCallId(id: string): RunAnchor | undefined {
  const [, yieldRun] = YIELD_ID.exec(id) ?? [];
  if (yieldRun !== undefined) return {run: yieldRun};
  const [, stepRun, seq] = STEP_ID.exec(id) ?? [];
  if (stepRun !== undefined) return {run: stepRun, seq: Number(seq)};
  return undefined;
}

function latestAnchorIn(
  content: AssistantMessage['content'],
): RunAnchor | undefined {
  for (const block of content.toReversed()) {
    if (!isToolCall(block)) continue;
    const anchor = anchorOfCallId(block.id);
    if (anchor !== undefined) return anchor;
  }
  return undefined;
}

export function findAnchor(
  messages: readonly Message[],
): RunAnchor | undefined {
  for (const message of messages.toReversed()) {
    if (message.role !== 'assistant') continue;
    const anchor = latestAnchorIn(message.content);
    if (anchor !== undefined) return anchor;
  }
  return undefined;
}

export function afterLastAssistant(messages: readonly Message[]): number {
  return messages.findLastIndex(m => m.role === 'assistant') + 1;
}

export function yieldRejected(
  messages: readonly Message[],
  run: string,
): boolean {
  const id = `skua_yield_${run}`;
  return messages.some(
    m => m.role === 'toolResult' && m.toolCallId === id && m.isError,
  );
}

export function pendingInputs(
  messages: readonly Message[],
  relayedCount: number,
): string[] {
  const inputs: string[] = [];
  for (const m of messages.slice(relayedCount)) {
    if (m.role !== 'user' && m.role !== 'developer') continue;
    const input = classifyInput(contentText(m.content));
    if (input.kind === 'text') inputs.push(input.text);
  }
  return inputs;
}

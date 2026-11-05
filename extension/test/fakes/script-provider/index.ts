import {createAssistantMessageEventStream} from '@oh-my-pi/pi-ai';
import type {
  AssistantMessage,
  AssistantMessageEventStream,
  Context,
  Message,
  Model,
  TextContent,
  ToolCall,
} from '@oh-my-pi/pi-ai';
import type {
  ExtensionAPI,
  ProviderModelConfig,
} from '@oh-my-pi/pi-coding-agent';

interface ScriptCall {
  tool: string;
  args: Record<string, unknown>;
}

const MARKER = 'SCRIPT:';
const LOCAL_DELAY_MS = 1000;
let callCounter = 0;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function isScriptCall(v: unknown): v is ScriptCall {
  if (!isRecord(v)) return false;
  const {tool, args} = v;
  return typeof tool === 'string' && isRecord(args);
}

function textOf(m: Message): string {
  if (typeof m.content === 'string') return m.content;
  const parts: string[] = [];
  for (const block of m.content)
    if (block.type === 'text') parts.push(block.text);
  return parts.join('\n');
}

function scriptCalls(context: Context): ScriptCall[] | undefined {
  const last = context.messages.at(-1);
  if (last?.role !== 'user') return undefined;
  const text = textOf(last);
  const at = text.indexOf(MARKER);
  if (at < 0) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text.slice(at + MARKER.length).trim());
  } catch {
    return undefined;
  }
  const calls = Array.isArray(parsed) ? parsed : [parsed];
  return calls.every(isScriptCall) ? calls : undefined;
}

function emit(
  model: Model,
  delayMs: number,
  build: (
    message: AssistantMessage,
    stream: AssistantMessageEventStream,
  ) => 'stop' | 'toolUse',
): AssistantMessageEventStream {
  const stream = createAssistantMessageEventStream();
  const message: AssistantMessage = {
    role: 'assistant',
    content: [],
    api: model.api,
    provider: model.provider,
    model: model.id,
    usage: {
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 0,
      cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0},
    },
    stopReason: 'stop',
    timestamp: Date.now(),
  };
  const run = (): void => {
    stream.push({type: 'start', partial: message});
    const reason = build(message, stream);
    message.stopReason = reason;
    stream.push({type: 'done', reason, message});
    stream.end();
  };
  if (delayMs > 0) {
    setTimeout(run, delayMs);
    return stream;
  }
  run();
  return stream;
}

function pushText(
  message: AssistantMessage,
  stream: AssistantMessageEventStream,
  text: string,
): void {
  const block: TextContent = {type: 'text', text: ''};
  message.content.push(block);
  const contentIndex = message.content.length - 1;
  stream.push({type: 'text_start', contentIndex, partial: message});
  block.text = text;
  stream.push({
    type: 'text_delta',
    contentIndex,
    delta: text,
    partial: message,
  });
  stream.push({
    type: 'text_end',
    contentIndex,
    content: text,
    partial: message,
  });
}

function pushToolCall(
  message: AssistantMessage,
  stream: AssistantMessageEventStream,
  name: string,
  args: Record<string, unknown>,
): void {
  const call: ToolCall = {
    type: 'toolCall',
    id: `script_${String(++callCounter)}`,
    name,
    arguments: args,
  };
  message.content.push(call);
  const contentIndex = message.content.length - 1;
  stream.push({type: 'toolcall_start', contentIndex, partial: message});
  stream.push({
    type: 'toolcall_end',
    contentIndex,
    toolCall: call,
    partial: message,
  });
}

function model(id: string): ProviderModelConfig {
  return {
    id,
    name: `script ${id}`,
    reasoning: false,
    input: ['text'],
    cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0},
    contextWindow: 1000000,
    maxTokens: 64000,
  };
}

export default function scriptProvider(pi: ExtensionAPI): void {
  pi.registerProvider('script', {
    baseUrl: 'http://127.0.0.1:9/',
    apiKey: 'script',
    api: 'script-api',
    models: [model('parent'), model('local')],
    streamSimple: (m, context) =>
      emit(m, m.id === 'local' ? LOCAL_DELAY_MS : 0, (message, stream) => {
        if (m.id === 'local') {
          const yielded = context.messages.some(
            msg =>
              msg.role === 'assistant' &&
              msg.content.some(
                b => b.type === 'toolCall' && b.name === 'yield',
              ),
          );
          if (!yielded && context.tools?.some(t => t.name === 'yield')) {
            pushToolCall(message, stream, 'yield', {data: 'LOCAL-OK'});
            return 'toolUse';
          }
          pushText(message, stream, 'LOCAL-OK');
          return 'stop';
        }
        const calls = scriptCalls(context);
        if (calls === undefined) {
          pushText(message, stream, 'OK');
          return 'stop';
        }
        for (const c of calls) pushToolCall(message, stream, c.tool, c.args);
        return 'toolUse';
      }),
  });
}

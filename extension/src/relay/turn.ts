import type {
  AssistantMessage,
  AssistantMessageEvent,
  Model,
  TextContent,
  ThinkingContent,
  ToolCall,
} from '@oh-my-pi/pi-ai';

export type RelayModel = Pick<Model, 'id' | 'api' | 'provider'>;

export type RelayPush = (event: AssistantMessageEvent) => void;

export interface Turn {
  text(delta: string): void;
  thinking(delta: string): void;
  closeThinking(): void;
  block(text: string): void;
  toolCall(call: ToolCall): void;
  done(reason: 'stop' | 'toolUse'): void;
  final(text: string): void;
  aborted(): void;
}

const ABORTED = 'Request was aborted';

export function createTurn(model: RelayModel, push: RelayPush): Turn {
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
  let openText: {index: number; block: TextContent} | undefined;
  let openThinking: {index: number; block: ThinkingContent} | undefined;

  const closeThinking = (): void => {
    if (!openThinking) return;
    push({
      type: 'thinking_end',
      contentIndex: openThinking.index,
      content: openThinking.block.thinking,
      partial: message,
    });
    openThinking = undefined;
  };
  const closeAll = (): void => {
    closeThinking();
    if (!openText) return;
    push({
      type: 'text_end',
      contentIndex: openText.index,
      content: openText.block.text,
      partial: message,
    });
    openText = undefined;
  };
  const text = (delta: string): void => {
    if (!openText) {
      closeAll();
      const block: TextContent = {type: 'text', text: ''};
      message.content.push(block);
      openText = {index: message.content.length - 1, block};
      push({
        type: 'text_start',
        contentIndex: openText.index,
        partial: message,
      });
    }
    openText.block.text += delta;
    push({
      type: 'text_delta',
      contentIndex: openText.index,
      delta,
      partial: message,
    });
  };
  const done = (reason: 'stop' | 'toolUse'): void => {
    closeAll();
    message.stopReason = reason;
    push({type: 'done', reason, message});
  };
  const block = (s: string): void => {
    closeAll();
    text(s);
    closeAll();
  };

  push({type: 'start', partial: message});
  return {
    text,
    thinking: delta => {
      if (!openThinking) {
        closeAll();
        const block: ThinkingContent = {type: 'thinking', thinking: ''};
        message.content.push(block);
        openThinking = {index: message.content.length - 1, block};
        push({
          type: 'thinking_start',
          contentIndex: openThinking.index,
          partial: message,
        });
      }
      openThinking.block.thinking += delta;
      push({
        type: 'thinking_delta',
        contentIndex: openThinking.index,
        delta,
        partial: message,
      });
    },
    closeThinking,
    block,
    toolCall: call => {
      closeAll();
      message.content.push(call);
      const contentIndex = message.content.length - 1;
      push({type: 'toolcall_start', contentIndex, partial: message});
      push({
        type: 'toolcall_end',
        contentIndex,
        toolCall: call,
        partial: message,
      });
    },
    done,
    final: s => {
      block(s);
      done('stop');
    },
    aborted: () => {
      closeAll();
      message.stopReason = 'aborted';
      message.errorMessage = ABORTED;
      push({type: 'error', reason: 'aborted', error: message});
    },
  };
}

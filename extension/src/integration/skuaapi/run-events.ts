import {isRunEvent} from '../../contract/wire.ts';
import type {RunEvent} from '../../contract/wire.ts';
import {parseJson} from './http.ts';
import type {SseEvent} from './sse.ts';

/** Decodes an SSE event into a RunEvent; undefined when the id is not a seq or the payload is not a valid event. */
export function toRunEvent(item: SseEvent): RunEvent | undefined {
  if (!/^\d+$/.test(item.id)) return undefined;
  const ev: unknown = {
    seq: Number(item.id),
    type: item.event,
    data: parseJson(item.data),
  };
  return isRunEvent(ev) ? ev : undefined;
}

/** True when `ev` decoded and its seq is past `highest`, the last seq already yielded. */
export function isUnseen(
  ev: RunEvent | undefined,
  highest: number,
): ev is RunEvent {
  return ev !== undefined && ev.seq > highest;
}

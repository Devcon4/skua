import { readSseEvents } from "@oh-my-pi/pi-utils";

export interface SseEvent {
	id: string;
	event: string;
	data: string;
}

/**
 * Yields each dispatched event of an SSE body. `id` is the stream's last event ID, which persists
 * across events that carry none. `onChunk` runs as each network chunk arrives, before its events.
 * Events with empty data and an unterminated event at the end of the body are dropped.
 */
export async function* parseSse(body: ReadableStream<Uint8Array>, onChunk: () => void): AsyncGenerator<SseEvent> {
	const reader = body.getReader();
	let ended = false;
	const source = new ReadableStream<Uint8Array>(
		{
			async pull(controller) {
				const { done, value } = await reader.read();
				if (done) {
					ended = true;
					controller.close();
					return;
				}
				onChunk();
				controller.enqueue(value);
			},
			cancel(reason) {
				return reader.cancel(reason);
			},
		},
		{ highWaterMark: 0 },
	);
	let lastId = "";
	for await (const sse of readSseEvents(source)) {
		// readSseEvents dispatches a pending event at end of body; the SSE spec discards it.
		if (ended) return;
		if (sse.id !== undefined) lastId = sse.id;
		if (sse.data === "") continue;
		yield { id: lastId, event: sse.event || "message", data: sse.data };
	}
}

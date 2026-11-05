import assert from "node:assert/strict";
import { test } from "node:test";
import { parseSse } from "../../src/sse.ts";
import type { SseEvent } from "../../src/sse.ts";

type Item = { kind: "chunk" } | ({ kind: "event" } & SseEvent);

function streamOf(chunks: Uint8Array[]): ReadableStream<Uint8Array> {
	return new ReadableStream<Uint8Array>({
		start(controller) {
			for (const c of chunks) controller.enqueue(c);
			controller.close();
		},
	});
}

async function collect(chunks: (string | Uint8Array)[]): Promise<Item[]> {
	const enc = new TextEncoder();
	const items: Item[] = [];
	const body = streamOf(chunks.map(c => (typeof c === "string" ? enc.encode(c) : c)));
	for await (const ev of parseSse(body, () => items.push({ kind: "chunk" }))) items.push({ kind: "event", ...ev });
	return items;
}

async function events(chunks: (string | Uint8Array)[]): Promise<Item[]> {
	return (await collect(chunks)).filter(i => i.kind !== "chunk");
}

const LF_INPUT = 'id: 1\nevent: text\ndata: {"delta":"héllo"}\n\n: hb\nid: 2\nevent: end\ndata: {"a":1}\n\n';

const LF_EXPECTED: Item[] = [
	{ kind: "event", id: "1", event: "text", data: '{"delta":"héllo"}' },
	{ kind: "event", id: "2", event: "end", data: '{"a":1}' },
];

test("LF, CRLF and CR line ends parse to the same events", async () => {
	assert.deepEqual(await events([LF_INPUT]), LF_EXPECTED);
	assert.deepEqual(await events([LF_INPUT.replaceAll("\n", "\r\n")]), LF_EXPECTED);
	assert.deepEqual(await events([LF_INPUT.replaceAll("\n", "\r")]), LF_EXPECTED);
});

test("a frame split across two chunks at every byte offset parses the same", async () => {
	for (const input of [LF_INPUT, LF_INPUT.replaceAll("\n", "\r\n"), LF_INPUT.replaceAll("\n", "\r")]) {
		const bytes = new TextEncoder().encode(input);
		for (let k = 0; k <= bytes.length; k++) {
			const items = await collect([bytes.slice(0, k), bytes.slice(k)]);
			assert.deepEqual(
				items.filter(i => i.kind !== "chunk"),
				LF_EXPECTED,
				`split at ${k} of ${JSON.stringify(input)}`,
			);
			assert.equal(items.filter(i => i.kind === "chunk").length, 2);
		}
	}
});

test("every chunk calls onChunk before its events, including a heartbeat-only chunk", async () => {
	const items = await collect([": hb\n", "id: 3\nevent: text\n", 'data: {"delta":"x"}\n\n']);
	assert.deepEqual(items, [{ kind: "chunk" }, { kind: "chunk" }, { kind: "chunk" }, { kind: "event", id: "3", event: "text", data: '{"delta":"x"}' }]);
});

test("multiple data lines join with a newline", async () => {
	assert.deepEqual(await events(["event: text\ndata: first\ndata:second\ndata\n\n"]), [{ kind: "event", id: "", event: "text", data: "first\nsecond\n" }]);
});

test("comment lines dispatch nothing", async () => {
	assert.deepEqual(await events([": hb\n:\n:another\n\n"]), []);
});

test("an event with no id has an empty id and a later event keeps the last id", async () => {
	assert.deepEqual(await events(["event: text\ndata: a\n\nid: 7\ndata: b\n\nevent: x\ndata: c\n\n"]), [
		{ kind: "event", id: "", event: "text", data: "a" },
		{ kind: "event", id: "7", event: "message", data: "b" },
		{ kind: "event", id: "7", event: "x", data: "c" },
	]);
});

test("an id containing NUL is ignored, retry and unknown fields are ignored", async () => {
	assert.deepEqual(await events(["id: 4\ndata: a\n\nid: 5\u00006\nretry: 10\nfoo: bar\ndata: b\n\n"]), [
		{ kind: "event", id: "4", event: "message", data: "a" },
		{ kind: "event", id: "4", event: "message", data: "b" },
	]);
});

test("a block without data dispatches nothing and an unterminated event at the end is discarded", async () => {
	assert.deepEqual(await events(["id: 1\nevent: status\n\nid: 2\nevent: text\ndata: tail"]), []);
	assert.deepEqual(await events(["id: 1\ndata: done\n\nid: 2\ndata: tail\n"]), [{ kind: "event", id: "1", event: "message", data: "done" }]);
});

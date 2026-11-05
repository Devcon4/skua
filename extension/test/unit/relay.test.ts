import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { test } from "node:test";
import { setImmediate, setTimeout as delay } from "node:timers/promises";
import type { AssistantMessage, AssistantMessageEvent, Message, ToolCall } from "@oh-my-pi/pi-ai";
import { createApi } from "../../src/api.ts";
import type { Api, FetchImpl } from "../../src/api.ts";
import { CONFIG_DEFAULTS } from "../../src/config.ts";
import { createRelay } from "../../src/relay.ts";
import type { Relay, RelayDeps, RelayTimers } from "../../src/relay.ts";
import { finalText } from "../../src/result.ts";
import type { ToolLike } from "../../src/result.ts";
import type { WorkspaceResolution } from "../../src/workspace.ts";
import type { Run } from "../../src/wire.ts";
import { startFakeApi } from "../fake/api.ts";
import type { FakeApi, FakeApiOptions, FakeRunSnapshot } from "../fake/api.ts";

type Ctx = { systemPrompt: string[]; messages: Message[]; tools: ToolLike[] };
type Call = { events: AssistantMessageEvent[]; message: AssistantMessage; reason: string };

const BATCH = "Fix the flaky test in pkg/cache.";
const SYSTEM = [`You are a remote helper.\n\n§ Context\n${BATCH}\n\n§ Rules\nBe terse.`];
const REMINDER = "<system-reminder>\nLast turn had no tool call → session idle. Reminder 1 of 3.\n</system-reminder>";
const NOT_LOGGED_IN = "Not logged in to skua. Run /skua login.";
const SCHEMA = { type: "object", properties: { issue: { type: "number" }, rootCause: { type: "string" }, confidence: { type: "number" } }, required: ["issue"] };
const SCHEMA_RESULT = { issue: 142, rootCause: "nil map write in cache.Put", confidence: 0.8 };
const WORKSPACE: WorkspaceResolution = { kind: "ok", workspace: { repo: "org/test", baseRef: "0123abcd" }, host: "github.com" };

function b64url(v: unknown): string {
	return Buffer.from(JSON.stringify(v)).toString("base64url");
}

function idToken(expInS = 3600): string {
	const exp = Math.floor(Date.now() / 1000) + expInS;
	return `${b64url({ alg: "none", typ: "JWT" })}.${b64url({ iss: "https://issuer.test", sub: "alice", exp })}.`;
}

function client(fake: FakeApi): Api {
	return createApi({ endpoint: fake.url, getToken: async () => idToken(), forceRefresh: async () => {}, setTimer: setTimeout, clearTimer: clearTimeout });
}

function relayFor(api: Api, over: Partial<RelayDeps<NodeJS.Timeout>> = {}): Relay {
	return createRelay({
		api,
		resolveWorkspace: async () => ({ kind: "none" }),
		heartbeatTimeoutMs: 5000,
		giveUpMs: 60_000,
		timers: { setTimer: setTimeout, clearTimer: clearTimeout },
		log: () => {},
		...over,
	});
}

function assignmentBody(scenario: string): string {
	return `Complete the assignment. SCENARIO:${scenario} Investigate the cache.`;
}

function user(text: string): Message {
	return { role: "user", content: text, timestamp: 0 };
}

function developer(text: string): Message {
	return { role: "developer", content: text, timestamp: 0 };
}

function irc(body: string): Message {
	return developer(`<irc>\nIncoming IRC message from agent \`Main\`:\n\n${body}\nIf response expected, reply via \`write\` (agent://Main), when available.</irc>`);
}

function context(scenario: string, tools: ToolLike[] = []): Ctx {
	const assignment = `<system-reminder>\nToday: 2026-11-03; current working directory: '/tmp/skua-scratch'. Do not repeat this information in your reply.\n</system-reminder>\n${assignmentBody(scenario)}`;
	return { systemPrompt: SYSTEM, messages: [user(assignment)], tools };
}

function yieldTool(withError = false): ToolLike {
	const properties: Record<string, unknown> = { data: { description: `Structured output matching the schema:\n${JSON.stringify(SCHEMA)}` } };
	if (withError) properties.error = { type: "string" };
	return { name: "yield", parameters: { type: "object", properties } };
}

async function call(relay: Relay, ctx: Ctx, sessionId: string, opts: { signal?: AbortSignal; onEvent?: (e: AssistantMessageEvent) => void } = {}): Promise<Call> {
	const events: AssistantMessageEvent[] = [];
	await relay.stream({ id: "generic", api: "skua-relay", provider: "skua" }, ctx, { sessionId, signal: opts.signal, cwd: "/tmp/skua-scratch" }, e => {
		events.push(e);
		opts.onEvent?.(e);
	});
	const last = events.at(-1);
	assert.ok(last && (last.type === "done" || last.type === "error"), "the call ends with done or error");
	return last.type === "done" ? { events, message: last.message, reason: last.reason } : { events, message: last.error, reason: last.reason };
}

function toolCalls(m: AssistantMessage): ToolCall[] {
	return m.content.filter((c): c is ToolCall => c.type === "toolCall");
}

function texts(m: AssistantMessage): string[] {
	return m.content.flatMap(c => (c.type === "text" ? [c.text] : []));
}

function append(ctx: Ctx, c: Call): void {
	ctx.messages.push(c.message);
	for (const tc of toolCalls(c.message)) {
		const result = tc.name === "skua_step" ? String(tc.arguments.result) : "ok";
		ctx.messages.push({ role: "toolResult", toolCallId: tc.id, toolName: tc.name, content: [{ type: "text", text: result }], isError: false, timestamp: 0 });
	}
}

async function untilStop(relay: Relay, ctx: Ctx, sessionId: string): Promise<Call[]> {
	const calls: Call[] = [];
	for (let i = 0; i < 20; i++) {
		const c = await call(relay, ctx, sessionId);
		calls.push(c);
		append(ctx, c);
		if (c.reason === "stop") return calls;
	}
	throw new Error("the relay never stopped");
}

async function waitForRun(fake: FakeApi, done: (r: FakeRunSnapshot) => boolean): Promise<FakeRunSnapshot> {
	for (let i = 0; i < 1000; i++) {
		const run = fake.runs().find(done);
		if (run) return run;
		await delay(10);
	}
	throw new Error("run did not reach the expected state");
}

function posts(fake: FakeApi): number {
	return fake.requests().filter(r => r.method === "POST" && r.path === "/v1alpha1/runs").length;
}

async function withFake(opts: FakeApiOptions, fn: (fake: FakeApi) => Promise<void>): Promise<void> {
	const fake = await startFakeApi(opts);
	try {
		await fn(fake);
	} finally {
		await fake.close();
	}
}

test("steps: three calls end at one skua_step each, the fourth stops with the PR link", () =>
	withFake({ steps: 3, stepMs: 30 }, async fake => {
		const sid = randomUUID();
		const relay = relayFor(client(fake), { resolveWorkspace: async () => WORKSPACE });
		const ctx = context("steps");
		let lastSeq = 0;
		let runName: string | undefined;
		for (let i = 0; i < 3; i++) {
			const c = await call(relay, ctx, sid);
			assert.equal(c.reason, "toolUse");
			const calls = toolCalls(c.message);
			assert.equal(calls.length, 1);
			assert.equal(calls[0].name, "skua_step");
			const m = /^skua_([a-z0-9-]+)_(\d+)$/.exec(calls[0].id);
			assert.ok(m);
			runName ??= m[1];
			assert.equal(m[1], runName);
			assert.ok(Number(m[2]) > lastSeq);
			lastSeq = Number(m[2]);
			append(ctx, c);
		}
		const last = await call(relay, ctx, sid);
		assert.equal(last.reason, "stop");
		assert.match(texts(last.message).join("\n"), /https:\/\/github\.com\/org\/test\/pull\/87/);
		const runs = fake.runs();
		assert.equal(runs.length, 1);
		assert.equal(runs[0].clientKey, `${sid}:0`);
		assert.equal(runs[0].prompt, `${BATCH}\n\n${assignmentBody("steps")}`);
		assert.deepEqual(runs[0].workspace, { repo: "org/test", baseRef: "0123abcd" });
		assert.deepEqual(relay.live(), [{ sessionId: sid, run: runName, followUp: 0 }]);
	}));

test("an Agent Hub steer reaches the run as bare text within one step", () =>
	withFake({ steps: 3, stepMs: 100 }, async fake => {
		const sid = randomUUID();
		const relay = relayFor(client(fake));
		const ctx = context("steps");
		append(ctx, await call(relay, ctx, sid));
		ctx.messages.push(user("<system-notice>\nUser interjection during work: priority; supersedes conflicting prior instructions. Re-read; ensure current work reflects user intent.\n</system-notice>\nSTEER-A: also check pkg9"));
		const second = await call(relay, ctx, sid);
		assert.equal(second.reason, "toolUse");
		const [steer] = fake.runs()[0].steers;
		assert.equal(steer.message, "STEER-A: also check pkg9");
		assert.notEqual(steer.appliedAtStep, null);
		assert.ok((steer.appliedAtStep ?? Infinity) - steer.receivedAtStep <= 1);
		assert.match(texts(second.message).join(""), /received steer at step \d+: STEER-A/);
	}));

test("a fresh relay resumes from the last step call id without a new run or repeated text", () =>
	withFake({ steps: 3, stepMs: 30 }, async fake => {
		const sid = randomUUID();
		const api = client(fake);
		const ctx = context("steps");
		const first = await call(relayFor(api), ctx, sid);
		assert.equal(toolCalls(first.message)[0].id.split("_").at(-1), "5");
		append(ctx, first);
		const resumed = await call(relayFor(api), ctx, sid);
		assert.equal(resumed.reason, "toolUse");
		assert.ok(fake.requests().some(r => r.path.endsWith("/events") && r.lastEventId === "5"));
		assert.equal(posts(fake), 1);
		assert.doesNotMatch(texts(resumed.message).join(""), /Step 1:/);
		assert.match(texts(resumed.message).join(""), /Step 2:/);
	}));

test("notools: a fresh relay finds the finished run through listRuns and creates follow-up 1", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const api = client(fake);
		const ctx = context("notools");
		const done = await call(relayFor(api), ctx, sid);
		assert.equal(done.reason, "stop");
		assert.equal(toolCalls(done.message).length, 0);
		ctx.messages.push(done.message, irc("FOLLOW-1: now also fix pkg10"));
		await call(relayFor(api), ctx, sid);
		assert.deepEqual(
			fake.runs().map(r => r.clientKey),
			[`${sid}:0`, `${sid}:1`],
		);
	}));

test("a rejected yield payload ends with stop and finalText, and a reminder never re-emits yield", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const api = client(fake);
		const { run } = await api.createRun({ profile: "generic", clientKey: `${sid}:0`, task: { prompt: "SCENARIO:schema", schema: SCHEMA } });
		await waitForRun(fake, r => r.name === run.name && r.phase === "Succeeded");
		const expected = finalText(await api.getRun(run.name), undefined);
		const id = `skua_yield_${run.name}`;
		const ctx = context("schema", [yieldTool()]);
		ctx.messages.push(
			{ role: "assistant", content: [{ type: "toolCall", id, name: "yield", arguments: { data: "nope" } }], api: "skua-relay", provider: "skua", model: "generic", usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } }, stopReason: "toolUse", timestamp: 0 },
			{ role: "toolResult", toolCallId: id, toolName: "yield", content: [{ type: "text", text: "data does not match the schema" }], isError: true, timestamp: 0 },
		);
		const relay = relayFor(api);
		const first = await call(relay, ctx, sid);
		assert.equal(first.reason, "stop");
		assert.equal(toolCalls(first.message).length, 0);
		assert.deepEqual(texts(first.message), [expected]);
		append(ctx, first);
		ctx.messages.push(developer(REMINDER));
		const again = await call(relay, ctx, sid);
		assert.equal(again.reason, "stop");
		assert.equal(toolCalls(again.message).length, 0);
		assert.deepEqual(texts(again.message), [expected]);
		assert.equal(posts(fake), 1);
	}));

test("shapes: a content array result becomes its joined text parts and a number its JSON", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const relay = relayFor(client(fake));
		const ctx = context("shapes");
		const first = await call(relay, ctx, sid);
		assert.equal(toolCalls(first.message)[0].arguments.result, "a\nb");
		assert.equal(toolCalls(first.message)[0].arguments.tool, "read");
		append(ctx, first);
		const second = await call(relay, ctx, sid);
		assert.equal(toolCalls(second.message)[0].arguments.result, "42");
	}));

test("a token source that is no longer logged in mid-stream ends the call with the login hint", () =>
	withFake({ steps: 3, stepMs: 5000 }, async fake => {
		const sid = randomUUID();
		let loggedIn = true;
		const api = createApi({
			endpoint: fake.url,
			getToken: async () => {
				if (!loggedIn) throw new Error("not logged in");
				return idToken(2);
			},
			forceRefresh: async () => {},
			fetchImpl: async (input, init) => {
				const res = await fetch(input, init);
				if (input.endsWith("/events")) loggedIn = false;
				return res;
			},
			setTimer: setTimeout,
			clearTimer: clearTimeout,
		});
		const relay = relayFor(api);
		const c = await call(relay, context("steps"), sid);
		assert.ok(fake.requests().some(r => r.path.endsWith("/events") && r.status === 200));
		assert.equal(c.reason, "stop");
		assert.equal(texts(c.message).at(-1), NOT_LOGGED_IN);
		assert.equal(fake.runs()[0].phase, "Running");
	}));

test("after a finished run a reminder re-emits the final text and an IRC message creates one follow-up", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const relay = relayFor(client(fake));
		const ctx = context("report");
		const calls = await untilStop(relay, ctx, sid);
		const final = texts(calls.at(-1)?.message ?? assert.fail("no calls"));
		ctx.messages.push(developer(REMINDER));
		const reminded = await call(relay, ctx, sid);
		assert.equal(reminded.reason, "stop");
		assert.deepEqual(texts(reminded.message), final);
		assert.equal(posts(fake), 1);
		append(ctx, reminded);
		ctx.messages.push(irc("FOLLOW-2: also update the changelog"));
		await call(relay, ctx, sid);
		const runs = fake.runs();
		assert.equal(runs.length, 2);
		assert.equal(runs[1].clientKey, `${sid}:1`);
		assert.match(runs[1].prompt, /Fixed org\/test#142/);
		assert.match(runs[1].prompt, /FOLLOW-2: also update the changelog/);
		await call(relay, ctx, sid);
		assert.equal(fake.runs().length, 2);
		assert.deepEqual(relay.live(), [{ sessionId: sid, run: runs[1].name, followUp: 1 }]);
	}));

test("fail: the call stops with the failure line after one POST /runs", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const calls = await untilStop(relayFor(client(fake)), context("fail"), sid);
		const name = fake.runs()[0].name;
		assert.deepEqual(texts(calls.at(-1)?.message ?? assert.fail("no calls")), [`Remote run ${name} failed: BudgetExceeded.`]);
		assert.equal(posts(fake), 1);
	}));

test("schema: the run carries the yield schema and the final call yields the result object", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const relay = relayFor(client(fake));
		const ctx = context("schema", [yieldTool()]);
		let yielded: ToolCall | undefined;
		for (let i = 0; i < 10 && yielded === undefined; i++) {
			const c = await call(relay, ctx, sid);
			assert.equal(c.reason, "toolUse");
			yielded = toolCalls(c.message).find(t => t.name === "yield");
			append(ctx, c);
		}
		assert.deepEqual(fake.runs()[0].schema, SCHEMA);
		assert.ok(yielded);
		assert.equal(yielded.id, `skua_yield_${fake.runs()[0].name}`);
		assert.deepEqual(yielded.arguments.data, SCHEMA_RESULT);
	}));

test("flaky-create: a 503 on create retries to the same run and relays normally", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const c = await call(relayFor(client(fake)), context("flaky-create"), sid);
		assert.equal(c.reason, "toolUse");
		assert.equal(toolCalls(c.message)[0].name, "skua_step");
		assert.equal(fake.runs().length, 1);
		assert.equal(posts(fake), 2);
	}));

test("rateLimit: a 429 on create waits for Retry-After and relays normally", () =>
	withFake({ stepMs: 20, rateLimit: { path: "/v1alpha1/runs", times: 1, retryAfterS: 1 } }, async fake => {
		const sid = randomUUID();
		const c = await call(relayFor(client(fake)), context("report"), sid);
		assert.equal(c.reason, "toolUse");
		assert.equal(fake.runs().length, 1);
		assert.equal(fake.requests().filter(r => r.path === "/v1alpha1/runs" && r.status === 429).length, 1);
	}));

test("409 race: a steer after the run ended between calls becomes one follow-up run", () =>
	withFake({ stepMs: 50 }, async fake => {
		const sid = randomUUID();
		const relay = relayFor(client(fake));
		const ctx = context("report");
		const first = await call(relay, ctx, sid);
		assert.equal(first.reason, "toolUse");
		append(ctx, first);
		await waitForRun(fake, r => r.clientKey === `${sid}:0` && r.phase === "Succeeded");
		ctx.messages.push(irc("FOLLOW-3: and add a regression test"));
		await call(relay, ctx, sid);
		const runs = fake.runs();
		assert.deepEqual(
			runs.map(r => r.clientKey),
			[`${sid}:0`, `${sid}:1`],
		);
		assert.ok(fake.requests().some(r => r.path === `/v1alpha1/runs/${runs[0].name}/steer` && r.status === 409));
		assert.deepEqual(runs[0].steers, []);
		assert.match(runs[1].prompt, /Fixed org\/test#142/);
		assert.match(runs[1].prompt, /FOLLOW-3: and add a regression test/);
	}));

test("an abort mid-step emits one aborted error, leaves the run running and sends no abort", () =>
	withFake({ steps: 3, stepMs: 200 }, async fake => {
		const sid = randomUUID();
		const ctl = new AbortController();
		const c = await call(relayFor(client(fake)), context("steps"), sid, {
			signal: ctl.signal,
			onEvent: e => {
				if (e.type === "text_delta") ctl.abort();
			},
		});
		const errors = c.events.filter(e => e.type === "error");
		assert.equal(errors.length, 1);
		assert.equal(c.reason, "aborted");
		assert.equal(c.message.errorMessage, "Request was aborted");
		assert.equal(fake.runs()[0].phase, "Running");
		assert.equal(fake.requests().filter(r => r.path.endsWith("/abort")).length, 0);
	}));

test("an unpushed HEAD ends the call with the push hint and creates no run", () =>
	withFake({ stepMs: 20 }, async fake => {
		const sid = randomUUID();
		const relay = relayFor(client(fake), { resolveWorkspace: async () => ({ kind: "unpushed", sha: "deadbeef" }) });
		const c = await call(relay, context("report"), sid);
		assert.equal(c.reason, "stop");
		assert.deepEqual(texts(c.message), ["HEAD deadbeef is not on origin. Push it so the remote agent sees the same code, then ask again."]);
		assert.equal(fake.runs().length, 0);
	}));

function fakeClock(): { now: () => number; timers: RelayTimers<number>; advance: (ms: number) => void } {
	let now = 0;
	let next = 0;
	const pending = new Map<number, { at: number; fn: () => void }>();
	return {
		now: () => now,
		timers: {
			setTimer: (fn, ms) => {
				pending.set(++next, { at: now + ms, fn });
				return next;
			},
			clearTimer: h => {
				pending.delete(h);
			},
		},
		advance: ms => {
			const target = now + ms;
			for (;;) {
				const due = [...pending].filter(([, t]) => t.at <= target).sort((a, b) => a[1].at - b[1].at)[0];
				if (due === undefined) break;
				pending.delete(due[0]);
				now = due[1].at;
				due[1].fn();
			}
			now = target;
		},
	};
}

test("a run silent for 10 minutes except heartbeats ends in one call over one events stream with no error", async () => {
	const SILENCE_MS = 600_000;
	const HEARTBEAT_MS = 15_000;
	const sid = randomUUID();
	const clock = fakeClock();
	const run: Run = { name: "generic-silent", profile: "generic", clientKey: `${sid}:0`, phase: "Running", reason: "", result: null, outputs: {}, createdAt: "2026-11-04T00:00:00Z" };
	const end = { phase: "Succeeded" as const, reason: "Completed", result: "Fixed org/test#142.", outputs: { branch: `skua/${run.name}` } };
	const requests: string[] = [];
	const sse = Promise.withResolvers<ReadableStreamDefaultController<Uint8Array>>();
	let streamSignal: AbortSignal | undefined;
	const fetchImpl: FetchImpl = async (input, init) => {
		const path = new URL(input).pathname;
		requests.push(`${init.method} ${path}`);
		if (init.method === "GET" && path === "/v1alpha1/runs") return Response.json([]);
		if (init.method === "POST" && path === "/v1alpha1/runs") return Response.json(run, { status: 201 });
		if (init.method === "GET" && path === `/v1alpha1/runs/${run.name}/events`) {
			streamSignal = init.signal ?? undefined;
			return new Response(new ReadableStream<Uint8Array>({ start: c => sse.resolve(c) }), { headers: { "content-type": "text/event-stream" } });
		}
		return Response.json({ error: "unexpected request" }, { status: 500 });
	};
	const api = createApi({ endpoint: "http://skua.test", getToken: async () => idToken(), forceRefresh: async () => {}, fetchImpl, now: clock.now, ...clock.timers });
	const relay = createRelay({ api, resolveWorkspace: async () => ({ kind: "none" }), heartbeatTimeoutMs: CONFIG_DEFAULTS.heartbeatTimeoutMs, timers: clock.timers, log: () => {} });
	const enc = new TextEncoder();

	const done = call(relay, context("long"), sid);
	const stream = await sse.promise;
	stream.enqueue(enc.encode(`id: 1\nevent: text\ndata: ${JSON.stringify({ delta: "Thinking." })}\n\n`));
	await setImmediate();
	for (let elapsed = 0; elapsed < SILENCE_MS; elapsed += HEARTBEAT_MS) {
		clock.advance(HEARTBEAT_MS);
		stream.enqueue(enc.encode(": heartbeat\n\n"));
		await setImmediate();
		assert.equal(streamSignal?.aborted, false, `the events stream was cut after ${elapsed + HEARTBEAT_MS} ms of silence`);
	}
	stream.enqueue(enc.encode(`id: 2\nevent: end\ndata: ${JSON.stringify(end)}\n\n`));
	stream.close();
	const c = await done;

	assert.equal(clock.now(), SILENCE_MS);
	assert.equal(c.reason, "stop");
	assert.deepEqual(c.events.filter(e => e.type === "error"), []);
	assert.equal(texts(c.message).at(-1), finalText({ ...run, ...end }, undefined));
	assert.deepEqual(requests, ["GET /v1alpha1/runs", "POST /v1alpha1/runs", `GET /v1alpha1/runs/${run.name}/events`]);
});

import type { AssistantMessage, AssistantMessageEvent, ImageContent, Message, Model, SimpleStreamOptions, TextContent, ThinkingContent, ToolCall } from "@oh-my-pi/pi-ai";
import { ApiError } from "./api.ts";
import type { Api } from "./api.ts";
import { classifyInput, extractBatchContext } from "./inputs.ts";
import { finalText, firstPrompt, followUpPrompt, prUrl, schemaFromYieldTool, truncateStepResult, yieldAcceptsError } from "./result.ts";
import type { ToolLike } from "./result.ts";
import type { CreateRunBody, Run, RunOutputs, Workspace } from "./wire.ts";
import type { ResolvedWorkspace, WorkspaceResolution } from "./workspace.ts";

export type RelayModel = Pick<Model, "id" | "api" | "provider">;

export interface RelayContext {
	systemPrompt?: string | readonly string[];
	messages: readonly Message[];
	tools?: readonly ToolLike[];
}

export type RelayOptions = Pick<SimpleStreamOptions, "sessionId" | "signal" | "cwd">;

export type RelayPush = (event: AssistantMessageEvent) => void;

export interface RelayTimers<H> {
	setTimer: (fn: () => void, ms: number) => H;
	clearTimer: (handle: H) => void;
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
	stream(model: RelayModel, context: RelayContext, options: RelayOptions | undefined, push: RelayPush): Promise<void>;
	forget(sessionId: string): void;
	live(): LiveRun[];
}

interface SessionState {
	run: string;
	started: Run;
	followUp: number;
	profile: string;
	committedSeq: number;
	relayedCount: number;
	terminal?: Run;
	workspace?: ResolvedWorkspace;
	openTools: Map<string, { name: string; args: unknown }>;
	rejectedYieldFor?: string;
}

type Created = { kind: "run"; run: Run } | { kind: "stop"; text: string } | { kind: "aborted" };

const STEP_ID = /^skua_([a-z0-9-]+)_(\d+)$/;
const YIELD_ID = /^skua_yield_([a-z0-9-]+)$/;
const CREATE_BACKOFF_MS = [1_000, 2_000, 4_000];
const NOT_LOGGED_IN = "Not logged in to skua. Run /skua login.";
const ABORTED = "Request was aborted";

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function relayErrorText(err: unknown): string {
	return `skua relay error: ${err instanceof Error ? err.message : String(err)}`;
}

function contentText(content: string | readonly (TextContent | ImageContent)[]): string {
	if (typeof content === "string") return content;
	return content
		.filter(c => c.type === "text")
		.map(c => c.text)
		.join("\n");
}

function stepResultText(result: unknown): string {
	if (typeof result === "string") return result;
	if (isObject(result) && Array.isArray(result.content)) {
		return result.content
			.filter((p): p is { type: "text"; text: string } => isObject(p) && p.type === "text" && typeof p.text === "string")
			.map(p => p.text)
			.join("\n");
	}
	return JSON.stringify(result) ?? "";
}

function systemPromptText(prompt: string | readonly string[] | undefined): string | undefined {
	if (prompt === undefined || typeof prompt === "string") return prompt;
	return prompt.join("\n");
}

function followUpOf(clientKey: string): number | undefined {
	const suffix = clientKey.slice(clientKey.lastIndexOf(":") + 1);
	return /^\d+$/.test(suffix) ? Number(suffix) : undefined;
}

function outputLines(outputs: RunOutputs, ws: ResolvedWorkspace | undefined): string[] {
	const lines: string[] = [];
	if (outputs.branch !== undefined) lines.push(`Branch: ${outputs.branch}`);
	const url = prUrl(outputs, ws);
	if (url !== undefined) lines.push(`PR: ${url}`);
	return lines;
}

function yieldData(result: unknown, schemaSent: boolean): unknown {
	if (typeof result !== "string" || !schemaSent) return result;
	try {
		const parsed: unknown = JSON.parse(result);
		return parsed;
	} catch {
		return result;
	}
}

function findAnchor(messages: readonly Message[]): { run: string; seq?: number } | undefined {
	for (let i = messages.length - 1; i >= 0; i--) {
		const m = messages[i];
		if (m.role !== "assistant") continue;
		for (let j = m.content.length - 1; j >= 0; j--) {
			const block = m.content[j];
			if (block.type !== "toolCall") continue;
			const y = YIELD_ID.exec(block.id);
			if (y) return { run: y[1] };
			const s = STEP_ID.exec(block.id);
			if (s) return { run: s[1], seq: Number(s[2]) };
		}
	}
	return undefined;
}

function afterLastAssistant(messages: readonly Message[]): number {
	for (let i = messages.length - 1; i >= 0; i--) {
		if (messages[i].role === "assistant") return i + 1;
	}
	return 0;
}

function yieldRejected(messages: readonly Message[], run: string): boolean {
	const id = `skua_yield_${run}`;
	return messages.some(m => m.role === "toolResult" && m.toolCallId === id && m.isError);
}

function createErrorText(err: unknown, profile: string): string {
	if (!(err instanceof ApiError)) return relayErrorText(err);
	switch (err.code) {
		case "not_found":
			return `skua profile ${profile} is unknown to skua-api. Run /skua sync.`;
		case "conflict":
			return `skua profile ${profile} is not Ready.`;
		case "unauthorized":
			return NOT_LOGGED_IN;
		case "bad_request":
			return `skua-api rejected the run: ${err.message}`;
		case "rate_limited":
			return "skua-api is rate limiting new runs. Ask again later.";
		default:
			return relayErrorText(err);
	}
}

function streamErrorText(err: unknown, run: string | undefined): string {
	if (err instanceof ApiError) {
		if (err.code === "unauthorized") return NOT_LOGGED_IN;
		if (run !== undefined && err.code === "network") {
			return `Lost contact with skua-api for run ${run}; it may still be running. Send this agent a message to reattach, or run /skua abort ${run}.`;
		}
		if (run !== undefined && err.code === "not_found") return `Remote run ${run} no longer exists.`;
	}
	return relayErrorText(err);
}

interface Turn {
	text(delta: string): void;
	thinking(delta: string): void;
	closeThinking(): void;
	block(text: string): void;
	toolCall(call: ToolCall): void;
	done(reason: "stop" | "toolUse"): void;
	final(text: string): void;
	aborted(): void;
}

function createTurn(model: RelayModel, push: RelayPush): Turn {
	const message: AssistantMessage = {
		role: "assistant",
		content: [],
		api: model.api,
		provider: model.provider,
		model: model.id,
		usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
		stopReason: "stop",
		timestamp: Date.now(),
	};
	let openText: { index: number; block: TextContent } | undefined;
	let openThinking: { index: number; block: ThinkingContent } | undefined;

	const closeThinking = (): void => {
		if (!openThinking) return;
		push({ type: "thinking_end", contentIndex: openThinking.index, content: openThinking.block.thinking, partial: message });
		openThinking = undefined;
	};
	const closeAll = (): void => {
		closeThinking();
		if (!openText) return;
		push({ type: "text_end", contentIndex: openText.index, content: openText.block.text, partial: message });
		openText = undefined;
	};
	const text = (delta: string): void => {
		if (!openText) {
			closeAll();
			const block: TextContent = { type: "text", text: "" };
			message.content.push(block);
			openText = { index: message.content.length - 1, block };
			push({ type: "text_start", contentIndex: openText.index, partial: message });
		}
		openText.block.text += delta;
		push({ type: "text_delta", contentIndex: openText.index, delta, partial: message });
	};
	const done = (reason: "stop" | "toolUse"): void => {
		closeAll();
		message.stopReason = reason;
		push({ type: "done", reason, message });
	};
	const block = (s: string): void => {
		closeAll();
		text(s);
		closeAll();
	};

	push({ type: "start", partial: message });
	return {
		text,
		thinking: delta => {
			if (!openThinking) {
				closeAll();
				const block: ThinkingContent = { type: "thinking", thinking: "" };
				message.content.push(block);
				openThinking = { index: message.content.length - 1, block };
				push({ type: "thinking_start", contentIndex: openThinking.index, partial: message });
			}
			openThinking.block.thinking += delta;
			push({ type: "thinking_delta", contentIndex: openThinking.index, delta, partial: message });
		},
		closeThinking,
		block,
		toolCall: call => {
			closeAll();
			message.content.push(call);
			const contentIndex = message.content.length - 1;
			push({ type: "toolcall_start", contentIndex, partial: message });
			push({ type: "toolcall_end", contentIndex, toolCall: call, partial: message });
		},
		done,
		final: s => {
			block(s);
			done("stop");
		},
		aborted: () => {
			closeAll();
			message.stopReason = "aborted";
			message.errorMessage = ABORTED;
			push({ type: "error", reason: "aborted", error: message });
		},
	};
}

export function createRelay<H>(deps: RelayDeps<H>): Relay {
	const { api, resolveWorkspace, heartbeatTimeoutMs, timers, log } = deps;
	const giveUpMs = deps.giveUpMs ?? 600_000;
	const sessions = new Map<string, SessionState>();

	const sleep = (ms: number, signal: AbortSignal): Promise<boolean> => {
		if (signal.aborted) return Promise.resolve(false);
		const { promise, resolve } = Promise.withResolvers<boolean>();
		const finish = (ok: boolean): void => {
			timers.clearTimer(timer);
			signal.removeEventListener("abort", onAbort);
			resolve(ok);
		};
		const onAbort = (): void => finish(false);
		const timer = timers.setTimer(() => finish(true), ms);
		signal.addEventListener("abort", onAbort, { once: true });
		return promise;
	};

	const resolveIn = async (cwd: string | undefined): Promise<WorkspaceResolution> => (cwd === undefined ? { kind: "none" } : resolveWorkspace(cwd));

	const createRun = async (body: CreateRunBody, signal: AbortSignal): Promise<Created> => {
		for (let attempt = 0; ; attempt++) {
			try {
				return { kind: "run", run: (await api.createRun(body)).run };
			} catch (err) {
				const wait = CREATE_BACKOFF_MS[attempt];
				if (!(err instanceof ApiError) || (err.code !== "server" && err.code !== "network") || wait === undefined) {
					return { kind: "stop", text: createErrorText(err, body.profile) };
				}
				log(`skua: creating run ${body.clientKey} failed (${err.message}); retry in ${wait} ms`);
				if (!(await sleep(wait, signal))) return { kind: "aborted" };
			}
		}
	};

	const rebuild = async (sessionId: string, context: RelayContext, cwd: string | undefined): Promise<SessionState | undefined> => {
		const anchor = findAnchor(context.messages);
		let name = anchor?.run;
		if (name === undefined) {
			const prefix = `${sessionId}:`;
			let best: { name: string; n: number } | undefined;
			for (const r of await api.listRuns()) {
				if (!r.clientKey.startsWith(prefix)) continue;
				const n = followUpOf(r.clientKey);
				if (n !== undefined && (best === undefined || n > best.n)) best = { name: r.name, n };
			}
			name = best?.name;
		}
		if (name === undefined) return undefined;
		const run = await api.getRun(name);
		const ws = await resolveIn(cwd);
		const state: SessionState = {
			run: run.name,
			started: run,
			followUp: followUpOf(run.clientKey) ?? 0,
			profile: run.profile,
			committedSeq: anchor?.seq ?? 0,
			relayedCount: afterLastAssistant(context.messages),
			openTools: new Map(),
		};
		if (run.phase === "Succeeded" || run.phase === "Failed") state.terminal = run;
		if (ws.kind === "ok") state.workspace = ws;
		return state;
	};

	const unpushedText = (sha: string): string => `HEAD ${sha} is not on origin. Push it so the remote agent sees the same code, then ask again.`;

	const followUp = async (sessionId: string, state: SessionState, prev: Run, context: RelayContext, cwd: string | undefined, inputs: string[], signal: AbortSignal): Promise<Created> => {
		let workspace: Workspace | undefined;
		if (state.workspace) {
			let baseRef = prev.outputs.branch;
			if (baseRef === undefined) {
				const current = await resolveIn(cwd);
				if (current.kind === "unpushed") return { kind: "stop", text: unpushedText(current.sha) };
				baseRef = current.kind === "ok" ? current.workspace.baseRef : state.workspace.workspace.baseRef;
			}
			workspace = { repo: state.workspace.workspace.repo, baseRef };
		}
		const n = state.followUp + 1;
		const body: CreateRunBody = { profile: state.profile, clientKey: `${sessionId}:${n}`, task: { prompt: followUpPrompt(prev, state.workspace, inputs) } };
		if (workspace) body.workspace = workspace;
		const schema = schemaFromYieldTool(context.tools);
		if (schema !== undefined) body.task.schema = schema;
		const created = await createRun(body, signal);
		if (created.kind === "run") {
			state.run = created.run.name;
			state.started = created.run;
			state.followUp = n;
			state.committedSeq = 0;
			state.terminal = undefined;
			state.openTools = new Map();
		}
		return created;
	};

	const emitFinal = (turn: Turn, state: SessionState, run: Run, context: RelayContext): void => {
		const text = finalText(run, state.workspace);
		const hasYield = context.tools?.some(t => t.name === "yield") ?? false;
		if (!hasYield || state.rejectedYieldFor === run.name) return turn.final(text);
		const id = `skua_yield_${run.name}`;
		if (run.phase === "Succeeded") {
			const lines = outputLines(run.outputs, state.workspace);
			if (lines.length > 0) turn.block(lines.join("\n"));
			const data = yieldData(run.result, schemaFromYieldTool(context.tools) !== undefined);
			turn.toolCall({ type: "toolCall", id, name: "yield", arguments: { data } });
			return turn.done("toolUse");
		}
		if (!yieldAcceptsError(context.tools)) return turn.final(text);
		turn.toolCall({ type: "toolCall", id, name: "yield", arguments: { error: text } });
		turn.done("toolUse");
	};

	const relay = async (turn: Turn, sessionId: string, model: RelayModel, context: RelayContext, cwd: string | undefined, signal: AbortSignal): Promise<void> => {
		let state = sessions.get(sessionId);
		if (!state) {
			state = await rebuild(sessionId, context, cwd);
			if (state) sessions.set(sessionId, state);
		}

		const inputs: string[] = [];
		for (const m of context.messages.slice(state?.relayedCount ?? 0)) {
			if (m.role !== "user" && m.role !== "developer") continue;
			const c = classifyInput(m.role, contentText(m.content));
			if (c.kind === "text") inputs.push(c.text);
		}

		if (!state) {
			const ws = await resolveIn(cwd);
			if (ws.kind === "unpushed") return turn.final(unpushedText(ws.sha));
			const body: CreateRunBody = {
				profile: model.id,
				clientKey: `${sessionId}:0`,
				task: { prompt: firstPrompt(extractBatchContext(systemPromptText(context.systemPrompt)), inputs) },
			};
			if (ws.kind === "ok") body.workspace = ws.workspace;
			const schema = schemaFromYieldTool(context.tools);
			if (schema !== undefined) body.task.schema = schema;
			const created = await createRun(body, signal);
			if (created.kind === "stop") return turn.final(created.text);
			if (created.kind === "aborted") return turn.aborted();
			state = {
				run: created.run.name,
				started: created.run,
				followUp: 0,
				profile: model.id,
				committedSeq: 0,
				relayedCount: context.messages.length,
				openTools: new Map(),
			};
			if (ws.kind === "ok") state.workspace = ws;
			sessions.set(sessionId, state);
		} else if (inputs.length > 0) {
			let rest = inputs;
			while (!state.terminal && rest.length > 0) {
				if ((await api.steer(state.run, rest[0])) === "terminal") state.terminal = await api.getRun(state.run);
				else rest = rest.slice(1);
			}
			if (state.terminal && rest.length > 0) {
				const created = await followUp(sessionId, state, state.terminal, context, cwd, rest, signal);
				if (created.kind === "stop") return turn.final(created.text);
				if (created.kind === "aborted") return turn.aborted();
			}
		}
		state.relayedCount = context.messages.length;

		if (yieldRejected(context.messages, state.run)) state.rejectedYieldFor = state.run;
		if (state.terminal) return emitFinal(turn, state, state.terminal, context);
		if (signal.aborted) return turn.aborted();

		const name = state.run;
		for await (const ev of api.followEvents(name, { afterSeq: state.committedSeq, signal, heartbeatTimeoutMs, giveUpMs })) {
			if (ev.type !== "thinking") turn.closeThinking();
			switch (ev.type) {
				case "text":
					turn.text(ev.data.delta);
					break;
				case "thinking":
					turn.thinking(ev.data.delta);
					break;
				case "tool_start":
					state.openTools.set(ev.data.id, { name: ev.data.name, args: ev.data.args });
					break;
				case "tool_end": {
					const tool = state.openTools.get(ev.data.id);
					state.openTools.delete(ev.data.id);
					turn.toolCall({
						type: "toolCall",
						id: `skua_${name}_${ev.seq}`,
						name: "skua_step",
						arguments: { tool: tool?.name ?? "unknown", args: tool?.args ?? null, result: truncateStepResult(stepResultText(ev.data.result)), isError: ev.data.isError },
					});
					state.committedSeq = ev.seq;
					return turn.done("toolUse");
				}
				case "end": {
					const run: Run = { ...state.started, phase: ev.data.phase, reason: ev.data.reason, result: ev.data.result, outputs: ev.data.outputs };
					state.terminal = run;
					state.committedSeq = ev.seq;
					return emitFinal(turn, state, run, context);
				}
			}
		}
		if (signal.aborted) return turn.aborted();
		return turn.final(`skua relay error: the event stream for run ${name} closed before its end event.`);
	};

	return {
		stream: async (model, context, options, push) => {
			const turn = createTurn(model, push);
			const sessionId = options?.sessionId;
			if (!sessionId) return turn.final("skua relay error: omp passed no session id.");
			const signal = options?.signal ?? new AbortController().signal;
			try {
				await relay(turn, sessionId, model, context, options?.cwd, signal);
			} catch (err) {
				if (signal.aborted) return turn.aborted();
				turn.final(streamErrorText(err, sessions.get(sessionId)?.run));
			}
		},
		forget: sessionId => {
			sessions.delete(sessionId);
		},
		live: () => [...sessions].map(([sessionId, s]) => ({ sessionId, run: s.run, followUp: s.followUp })),
	};
}

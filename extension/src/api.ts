import { parseSse } from "./sse.ts";
import type { SseEvent } from "./sse.ts";
import { isProfile, isRun, isRunEvent } from "./wire.ts";
import type { CreateRunBody, Profile, Run, RunEvent } from "./wire.ts";

export type ApiErrorCode = "unauthorized" | "bad_request" | "not_found" | "conflict" | "rate_limited" | "server" | "network";

export class ApiError extends Error {
	readonly status: number;
	readonly code: ApiErrorCode;
	readonly retryAfterMs?: number;

	constructor(status: number, code: ApiErrorCode, message: string, retryAfterMs?: number) {
		super(message);
		this.name = "ApiError";
		this.status = status;
		this.code = code;
		if (retryAfterMs !== undefined) this.retryAfterMs = retryAfterMs;
	}
}

export type FetchImpl = (input: string, init: RequestInit) => Promise<Response>;

export interface ApiOptions<H> {
	endpoint: string;
	getToken: () => Promise<string>;
	forceRefresh: () => Promise<unknown>;
	fetchImpl?: FetchImpl;
	setTimer: (fn: () => void, ms: number) => H;
	clearTimer: (handle: H) => void;
	now?: () => number;
}

export interface FollowOptions {
	afterSeq: number;
	signal: AbortSignal;
	heartbeatTimeoutMs: number;
	giveUpMs: number;
}

export interface Api {
	profiles(): Promise<Profile[]>;
	createRun(body: CreateRunBody): Promise<{ created: boolean; run: Run }>;
	getRun(name: string): Promise<Run>;
	listRuns(): Promise<Run[]>;
	steer(name: string, message: string): Promise<"accepted" | "terminal">;
	abort(name: string): Promise<void>;
	followEvents(name: string, opts: FollowOptions): AsyncGenerator<RunEvent>;
}

const REQUEST_TIMEOUT_MS = 15_000;
const RATE_LIMIT_RETRIES = 3;
const RETRY_AFTER_CAP_MS = 30_000;
const BACKOFF_MIN_MS = 1_000;
const BACKOFF_MAX_MS = 30_000;

type Reply = { status: number; text: string; headers: Headers };

function messageOf(err: unknown): string {
	if (!(err instanceof Error)) return String(err);
	const cause = err.cause instanceof Error ? `: ${err.cause.message}` : "";
	return `${err.message}${cause}`;
}

function codeFor(status: number): ApiErrorCode {
	if (status === 400) return "bad_request";
	if (status === 401 || status === 403) return "unauthorized";
	if (status === 404) return "not_found";
	if (status === 409) return "conflict";
	if (status === 429) return "rate_limited";
	if (status >= 500) return "server";
	return "bad_request";
}

function parseJson(text: string): unknown {
	try {
		const v: unknown = JSON.parse(text);
		return v;
	} catch {
		return undefined;
	}
}

function toRunEvent(item: SseEvent): RunEvent | undefined {
	if (!/^\d+$/.test(item.id)) return undefined;
	const ev: unknown = { seq: Number(item.id), type: item.event, data: parseJson(item.data) };
	return isRunEvent(ev) ? ev : undefined;
}

async function discard(res: Response): Promise<void> {
	try {
		await res.body?.cancel();
	} catch {}
}

export function createApi<H>(opts: ApiOptions<H>): Api {
	const { getToken, forceRefresh, setTimer, clearTimer } = opts;
	const now = opts.now ?? Date.now;
	const base = opts.endpoint.endsWith("/") ? opts.endpoint : `${opts.endpoint}/`;
	const runPath = (name: string, sub = ""): string => `v1alpha1/runs/${encodeURIComponent(name)}${sub}`;
	const fetchImpl: FetchImpl = opts.fetchImpl ?? fetch;

	const sleep = (ms: number, signal?: AbortSignal): Promise<boolean> => {
		if (signal?.aborted) return Promise.resolve(false);
		const { promise, resolve } = Promise.withResolvers<boolean>();
		const done = (ok: boolean): void => {
			clearTimer(timer);
			signal?.removeEventListener("abort", onAbort);
			resolve(ok);
		};
		const onAbort = (): void => done(false);
		const timer = setTimer(() => done(true), ms);
		signal?.addEventListener("abort", onAbort, { once: true });
		return promise;
	};

	const tokenStep = async <T>(step: () => Promise<T>): Promise<T> => {
		try {
			return await step();
		} catch (err) {
			if (err instanceof ApiError) throw err;
			throw new ApiError(401, "unauthorized", messageOf(err));
		}
	};

	const retryAfterMs = (headers: Headers): number | undefined => {
		const v = headers.get("retry-after")?.trim();
		if (!v) return undefined;
		if (/^\d+$/.test(v)) return Number(v) * 1000;
		const at = Date.parse(v);
		return Number.isNaN(at) ? undefined : Math.max(0, at - now());
	};

	const errorFor = (method: string, path: string, reply: Reply): ApiError => {
		const body = parseJson(reply.text);
		const detail = typeof body === "object" && body !== null && "error" in body && typeof body.error === "string" ? body.error : `HTTP ${reply.status}`;
		const after = reply.status === 429 ? retryAfterMs(reply.headers) : undefined;
		return new ApiError(reply.status, codeFor(reply.status), `${method} /${path}: ${detail}`, after);
	};

	const send = async (method: string, path: string, token: string, body: unknown, signal: AbortSignal | undefined): Promise<Reply> => {
		const ctl = new AbortController();
		const onAbort = (): void => ctl.abort();
		if (signal?.aborted) ctl.abort();
		signal?.addEventListener("abort", onAbort, { once: true });
		let timedOut = false;
		const timer = setTimer(() => {
			timedOut = true;
			try {
				ctl.abort();
			} catch {}
		}, REQUEST_TIMEOUT_MS);
		const headers: Record<string, string> = { authorization: `Bearer ${token}`, accept: "application/json" };
		if (body !== undefined) headers["content-type"] = "application/json";
		try {
			const res = await fetchImpl(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: ctl.signal });
			const text = await res.text();
			return { status: res.status, text, headers: res.headers };
		} catch (err) {
			const why = timedOut ? `timed out after ${REQUEST_TIMEOUT_MS / 1000} s` : signal?.aborted ? "aborted" : messageOf(err);
			throw new ApiError(0, "network", `${method} /${path}: ${why}`);
		} finally {
			clearTimer(timer);
			signal?.removeEventListener("abort", onAbort);
		}
	};

	const call = async (method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<Reply> => {
		let refreshed = false;
		let limited = 0;
		for (;;) {
			const reply = await send(method, path, await tokenStep(getToken), body, signal);
			if (reply.status === 401 && !refreshed) {
				refreshed = true;
				await tokenStep(forceRefresh);
				continue;
			}
			if (reply.status === 429 && limited < RATE_LIMIT_RETRIES) {
				limited++;
				const wait = Math.min(retryAfterMs(reply.headers) ?? BACKOFF_MIN_MS, RETRY_AFTER_CAP_MS);
				if (!(await sleep(wait, signal))) throw new ApiError(0, "network", `${method} /${path}: aborted`);
				continue;
			}
			if (reply.status >= 200 && reply.status < 300) return reply;
			throw errorFor(method, path, reply);
		}
	};

	const invalid = (method: string, path: string, what: string): ApiError => new ApiError(0, "server", `${method} /${path}: response is not ${what}`);

	const readRun = (method: string, path: string, reply: Reply): Run => {
		const v = parseJson(reply.text);
		if (!isRun(v)) throw invalid(method, path, "a Run");
		return v;
	};

	const getRun = async (name: string, signal?: AbortSignal): Promise<Run> => {
		const path = runPath(name);
		return readRun("GET", path, await call("GET", path, undefined, signal));
	};

	const runAfterDrop = async (name: string, signal: AbortSignal): Promise<Run | undefined> => {
		try {
			return await getRun(name, signal);
		} catch (err) {
			if (signal.aborted) return undefined;
			if (err instanceof ApiError && (err.code === "not_found" || err.code === "unauthorized")) throw err;
			return undefined;
		}
	};

	async function* followEvents(name: string, follow: FollowOptions): AsyncGenerator<RunEvent> {
		const { signal, heartbeatTimeoutMs, giveUpMs } = follow;
		const path = runPath(name, "/events");
		let highest = follow.afterSeq;
		let backoff = BACKOFF_MIN_MS;
		let failingSince: number | undefined;
		let refreshed = false;
		while (!signal.aborted) {
			let wait = 0;
			const conn = new AbortController();
			const onAbort = (): void => conn.abort();
			signal.addEventListener("abort", onAbort, { once: true });
			let timer: { handle: H } | undefined;
			const disarm = (): void => {
				if (timer) clearTimer(timer.handle);
				timer = undefined;
			};
			const arm = (): void => {
				disarm();
				timer = {
					handle: setTimer(() => {
						try {
							conn.abort();
						} catch {}
					}, heartbeatTimeoutMs),
				};
			};
			try {
				const token = await tokenStep(getToken);
				if (signal.aborted) return;
				const headers: Record<string, string> = { authorization: `Bearer ${token}`, accept: "text/event-stream", "cache-control": "no-cache" };
				if (highest > 0) headers["last-event-id"] = String(highest);
				arm();
				let res: Response | undefined;
				try {
					res = await fetchImpl(base + path, { method: "GET", headers, signal: conn.signal });
				} catch {
					res = undefined;
				}
				if (signal.aborted) return;
				if (res?.status === 401) {
					await discard(res);
					if (refreshed) throw new ApiError(401, "unauthorized", `GET /${path}: unauthorized after a token refresh`);
					refreshed = true;
					disarm();
					await tokenStep(forceRefresh);
					continue;
				}
				if (res?.status === 404) {
					await discard(res);
					throw new ApiError(404, "not_found", `GET /${path}: no run ${name}`);
				}
				if (res && res.status === 200 && res.body) {
					refreshed = false;
					try {
						const onChunk = (): void => {
							failingSince = undefined;
							arm();
						};
						for await (const item of parseSse(res.body, onChunk)) {
							if (signal.aborted) return;
							const ev = toRunEvent(item);
							if (!ev || ev.seq <= highest) continue;
							highest = ev.seq;
							backoff = BACKOFF_MIN_MS;
							disarm();
							yield ev;
							if (ev.type === "end") return;
							arm();
						}
					} catch {}
					if (signal.aborted) return;
				} else if (res) {
					if (res.status >= 300 && res.status < 500 && res.status !== 429) {
						throw errorFor("GET", path, { status: res.status, text: await res.text().catch(() => ""), headers: res.headers });
					}
					await discard(res);
					if (res.status === 429) wait = Math.min(retryAfterMs(res.headers) ?? 0, RETRY_AFTER_CAP_MS);
				}
			} finally {
				disarm();
				signal.removeEventListener("abort", onAbort);
				conn.abort();
			}
			failingSince ??= now();
			const run = await runAfterDrop(name, signal);
			if (signal.aborted) return;
			if (run && (run.phase === "Succeeded" || run.phase === "Failed")) {
				yield { seq: highest + 1, type: "end", data: { phase: run.phase, reason: run.reason, result: run.result, outputs: run.outputs } };
				return;
			}
			if (now() - failingSince >= giveUpMs) throw new ApiError(0, "network", `GET /${path}: no contact with skua-api for ${giveUpMs} ms`);
			if (!(await sleep(Math.max(backoff, wait), signal))) return;
			backoff = Math.min(backoff * 2, BACKOFF_MAX_MS);
		}
	}

	return {
		profiles: async () => {
			const path = "v1alpha1/profiles";
			const v = parseJson((await call("GET", path)).text);
			if (!Array.isArray(v) || !v.every(isProfile)) throw invalid("GET", path, "a profile list");
			return v;
		},
		createRun: async body => {
			const path = "v1alpha1/runs";
			const reply = await call("POST", path, body);
			return { created: reply.status === 201, run: readRun("POST", path, reply) };
		},
		getRun,
		listRuns: async () => {
			const path = "v1alpha1/runs";
			const v = parseJson((await call("GET", path)).text);
			if (!Array.isArray(v) || !v.every(isRun)) throw invalid("GET", path, "a run list");
			return v;
		},
		steer: async (name, message) => {
			try {
				await call("POST", runPath(name, "/steer"), { message });
				return "accepted";
			} catch (err) {
				if (err instanceof ApiError && err.code === "conflict") return "terminal";
				throw err;
			}
		},
		abort: async name => {
			await call("POST", runPath(name, "/abort"));
		},
		followEvents,
	};
}

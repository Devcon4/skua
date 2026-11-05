import { createHash, randomBytes } from "node:crypto";
import { createServer } from "node:http";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { setTimeout as delay } from "node:timers/promises";

export type FakeProfile = { name: string; description: string; models: string[]; ready: boolean };
export type FakeProfileInput = { name: string; description?: string; models?: string[]; ready?: boolean };
export type FakePhase = "Pending" | "Running" | "Succeeded" | "Failed";
export type FakeWorkspace = { repo: string; baseRef: string };
export type FakeOutputs = { branch?: string; pullRequest?: number | string };
export type FakeSteer = { message: string; receivedAtStep: number; appliedAtStep: number | null };
export type RateLimit = { path: string; times: number; retryAfterS: number };

export type FakeRunSnapshot = {
	name: string;
	profile: string;
	clientKey: string;
	owner: string;
	phase: FakePhase;
	reason: string;
	prompt: string;
	schema: unknown;
	workspace: FakeWorkspace | undefined;
	steers: FakeSteer[];
	aborted: boolean;
	lastSeq: number;
};

export type FakeRequest = {
	method: string;
	path: string;
	status: number;
	lastEventId: string | undefined;
	exp: number | undefined;
	bytes: number;
};

export type FakeApiOptions = {
	port?: number;
	stepMs?: number;
	steps?: number;
	gapMs?: number;
	hbMs?: number;
	profilesDelayMs?: number;
	expireStreams?: boolean;
	rateLimit?: RateLimit;
};

export type FakeApi = {
	url: string;
	close(): Promise<void>;
	runs(): FakeRunSnapshot[];
	requests(): FakeRequest[];
	setProfile(p: FakeProfileInput): void;
	restart(): void;
};

type RunEventRecord = { seq: number; type: string; data: Record<string, unknown> };

type EventStream = {
	res: ServerResponse;
	connection: number;
	closed: boolean;
	timers: NodeJS.Timeout[];
};

type RunState = {
	name: string;
	profile: string;
	clientKey: string;
	owner: string;
	prompt: string;
	schema: unknown;
	workspace: FakeWorkspace | undefined;
	scenario: string;
	phase: FakePhase;
	reason: string;
	result: unknown;
	outputs: FakeOutputs;
	createdAt: string;
	events: RunEventRecord[];
	lastSeq: number;
	step: number;
	stepStartSeq: number;
	steers: FakeSteer[];
	pendingSteers: FakeSteer[];
	aborted: boolean;
	streams: Set<EventStream>;
	muted: Set<EventStream>;
	connections: number;
	stop: AbortController;
};

type Caller = { owner: string; exp: number | undefined };

const DEFAULT_MODELS = ["anthropic/claude-sonnet-5", "anthropic/claude-haiku-4-5"];
const MAX_TIMER_MS = 2_147_483_647;
const DROP_AFTER_WRITE_MS = 100;
const REPORT_RESULT = "Fixed org/test#142. Findings are in docs/findings.md on the branch.";
const SCHEMA_RESULT = { issue: 142, rootCause: "nil map write in cache.Put", confidence: 0.8 };

function isRecord(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function parseJson(text: string): unknown {
	try {
		const v: unknown = JSON.parse(text);
		return v;
	} catch {
		return undefined;
	}
}

async function readBody(req: IncomingMessage): Promise<Buffer> {
	const chunks: Buffer[] = [];
	for await (const c of req) chunks.push(c);
	return Buffer.concat(chunks);
}

function parseBearer(header: string | undefined): { iss: string; sub: string; exp: number | undefined } | undefined {
	const m = /^Bearer (\S+)$/.exec(header ?? "");
	if (!m) return undefined;
	const parts = m[1].split(".");
	if (parts.length < 2) return undefined;
	const payload = parseJson(Buffer.from(parts[1], "base64url").toString("utf8"));
	if (!isRecord(payload) || typeof payload.iss !== "string" || typeof payload.sub !== "string") return undefined;
	return { iss: payload.iss, sub: payload.sub, exp: typeof payload.exp === "number" ? payload.exp : undefined };
}

function isTerminal(run: RunState): boolean {
	return run.phase === "Succeeded" || run.phase === "Failed";
}

function runJson(run: RunState): Record<string, unknown> {
	return {
		name: run.name,
		profile: run.profile,
		clientKey: run.clientKey,
		phase: run.phase,
		reason: run.reason,
		result: run.result,
		outputs: run.outputs,
		createdAt: run.createdAt,
	};
}

function parseCreateBody(v: unknown): { profile: string; clientKey: string; workspace: FakeWorkspace | undefined; prompt: string; schema: unknown } | string {
	if (!isRecord(v)) return "body must be a JSON object";
	if (typeof v.profile !== "string" || v.profile === "") return "profile must be a non-empty string";
	if (typeof v.clientKey !== "string" || v.clientKey === "") return "clientKey must be a non-empty string";
	let workspace: FakeWorkspace | undefined;
	if (v.workspace !== undefined) {
		const w = v.workspace;
		if (!isRecord(w) || typeof w.repo !== "string" || typeof w.baseRef !== "string") return "workspace must be {repo, baseRef}";
		workspace = { repo: w.repo, baseRef: w.baseRef };
	}
	const task = v.task;
	if (!isRecord(task) || typeof task.prompt !== "string") return "task.prompt must be a string";
	return { profile: v.profile, clientKey: v.clientKey, workspace, prompt: task.prompt, schema: task.schema };
}

function stepTool(run: RunState, i: number): { name: string; args: Record<string, unknown>; partial: string; result: unknown } {
	const shapes: unknown[] = [{ content: [{ type: "text", text: "a" }, { type: "image" }, { type: "text", text: "b" }] }, 42];
	const read = i % 2 === 1;
	const plain = read ? `package pkg${i}\n// ${i * 10} lines` : `ok  pkg${i} 0.${i}s`;
	return {
		name: read ? "read" : "bash",
		args: read ? { path: `src/file${i}.go` } : { command: `go test ./pkg${i}/...` },
		partial: read ? `package pkg${i}` : `ok  pkg${i}`,
		result: run.scenario === "shapes" ? shapes[(i - 1) % shapes.length] : plain,
	};
}

export async function startFakeApi(opts: FakeApiOptions = {}): Promise<FakeApi> {
	const stepMs = opts.stepMs ?? 4000;
	const stepsOpt = opts.steps ?? 6;
	const gapMs = opts.gapMs ?? 240000;
	const hbMs = opts.hbMs ?? 15000;
	const profilesDelayMs = opts.profilesDelayMs ?? 0;
	const expireStreams = opts.expireStreams ?? true;
	const rateLimit = opts.rateLimit;

	const profiles = new Map<string, FakeProfile>();
	const runs = new Map<string, RunState>();
	const byOwnerKey = new Map<string, RunState>();
	const requestLog: FakeRequest[] = [];
	const closing = new AbortController();
	let rateLimited = 0;
	let runCounter = 0;

	const setProfile = (p: FakeProfileInput): void => {
		const prev = profiles.get(p.name);
		profiles.set(p.name, {
			name: p.name,
			description: p.description ?? prev?.description ?? `${p.name} remote agent.`,
			models: p.models ?? prev?.models ?? [...DEFAULT_MODELS],
			ready: p.ready ?? prev?.ready ?? true,
		});
	};
	setProfile({ name: "generic", description: "Generic remote agent: git, gh, shells, omp.", models: [...DEFAULT_MODELS], ready: true });
	setProfile({ name: "golang", description: "Go toolchain remote agent.", models: [...DEFAULT_MODELS], ready: true });

	const clearStreamTimers = (s: EventStream): void => {
		for (const t of s.timers) clearTimeout(t);
		s.timers = [];
	};

	const detach = (run: RunState, s: EventStream): void => {
		s.closed = true;
		clearStreamTimers(s);
		run.streams.delete(s);
		run.muted.delete(s);
	};

	const endStream = (run: RunState, s: EventStream): void => {
		detach(run, s);
		if (!s.res.writableEnded) s.res.end();
	};

	const destroyStream = (run: RunState, s: EventStream): void => {
		detach(run, s);
		s.res.destroy();
	};

	const send = (run: RunState, s: EventStream, e: RunEventRecord): void => {
		if (s.closed || s.res.destroyed || s.res.writableEnded) return;
		const frame = `id: ${e.seq}\nevent: ${e.type}\ndata: ${JSON.stringify(e.data)}\n\n`;
		if (e.type === "end") {
			s.res.write(frame);
			endStream(run, s);
			return;
		}
		if (run.scenario === "drop" && e.type === "tool_end" && s.connection <= 2) {
			detach(run, s);
			s.res.write(frame, () => setTimeout(() => s.res.destroy(), DROP_AFTER_WRITE_MS));
			return;
		}
		s.res.write(frame);
	};

	const emit = (run: RunState, type: string, data: Record<string, unknown>): void => {
		const e: RunEventRecord = { seq: ++run.lastSeq, type, data };
		run.events.push(e);
		for (const s of [...run.streams]) send(run, s, e);
	};

	const finish = (run: RunState, phase: FakePhase, reason: string, result: unknown, outputs: FakeOutputs): void => {
		run.phase = phase;
		run.reason = reason;
		run.result = result;
		run.outputs = outputs;
		emit(run, "end", { phase, reason, result, outputs });
	};

	const beginStep = (run: RunState, k: number): void => {
		run.step = k;
		run.stepStartSeq = run.lastSeq + 1;
		for (const s of run.pendingSteers.splice(0)) {
			s.appliedAtStep = k;
			emit(run, "text", { delta: `[remote agent received steer at step ${k}: ${s.message}]` });
		}
	};

	const pause = async (run: RunState, ms: number): Promise<boolean> => {
		try {
			await delay(ms, undefined, { signal: run.stop.signal });
		} catch {
			return false;
		}
		return !isTerminal(run);
	};

	const stepCount = (scenario: string): number => {
		if (scenario === "steps" || scenario === "nohb" || scenario === "drop") return stepsOpt;
		if (scenario === "fail") return 1;
		if (scenario === "long") return 0;
		return 2;
	};

	const script = async (run: RunState): Promise<void> => {
		run.phase = "Running";
		emit(run, "status", { phase: "Running" });
		if (run.scenario === "long") {
			emit(run, "text", { delta: "Thinking quietly for a long time." });
			if (!(await pause(run, gapMs))) return;
		}
		const n = stepCount(run.scenario);
		for (let i = 1; i <= n; i++) {
			if (!(await pause(run, stepMs))) return;
			beginStep(run, i);
			if (run.scenario === "notools") {
				emit(run, "text", { delta: `Step ${i}: thinking without tools. ` });
				continue;
			}
			const tool = stepTool(run, i);
			const id = `call-${i}`;
			emit(run, "text", { delta: `Step ${i}: inspecting the code. ` });
			emit(run, "tool_start", { id, name: tool.name, args: tool.args });
			emit(run, "tool_update", { id, partial: tool.partial });
			emit(run, "tool_end", { id, result: tool.result, isError: false });
			if (run.scenario === "nohb" && i === 2) {
				for (const s of run.streams) {
					clearStreamTimers(s);
					run.muted.add(s);
				}
				run.streams.clear();
				if (!(await pause(run, gapMs))) return;
			}
		}
		beginStep(run, n + 1);
		if (run.scenario === "fail") finish(run, "Failed", "BudgetExceeded", null, {});
		else if (run.scenario === "schema") finish(run, "Succeeded", "Completed", SCHEMA_RESULT, {});
		else finish(run, "Succeeded", "Completed", REPORT_RESULT, { branch: `skua/${run.name}`, pullRequest: 87 });
	};

	const restart = (): void => {
		for (const run of runs.values()) {
			for (const s of [...run.streams, ...run.muted]) destroyStream(run, s);
			run.events = isTerminal(run) ? [] : run.events.filter(e => e.seq >= run.stepStartSeq);
		}
	};

	const json = (res: ServerResponse, entry: FakeRequest, status: number, body: unknown, headers: Record<string, string> = {}): void => {
		entry.status = status;
		res.writeHead(status, { "content-type": "application/json", ...headers });
		res.end(JSON.stringify(body));
	};

	const openEvents = (req: IncomingMessage, res: ServerResponse, entry: FakeRequest, run: RunState, caller: Caller): void => {
		const from = Number(req.headers["last-event-id"] ?? 0);
		const after = Number.isFinite(from) ? from : 0;
		entry.status = 200;
		res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache", connection: "keep-alive" });
		res.flushHeaders();
		const s: EventStream = { res, connection: ++run.connections, closed: false, timers: [] };
		res.on("close", () => detach(run, s));
		if (isTerminal(run) && !run.events.some(e => e.type === "end")) {
			send(run, s, { seq: run.lastSeq, type: "end", data: { phase: run.phase, reason: run.reason, result: run.result, outputs: run.outputs } });
			return;
		}
		for (const e of run.events) {
			if (e.seq > after) send(run, s, e);
			if (s.closed) return;
		}
		if (isTerminal(run)) {
			endStream(run, s);
			return;
		}
		run.streams.add(s);
		s.timers.push(setInterval(() => {
			if (!s.closed && !res.destroyed && !res.writableEnded) res.write(": hb\n");
		}, hbMs));
		if (expireStreams && caller.exp !== undefined) {
			const ms = caller.exp * 1000 - Date.now();
			if (ms <= MAX_TIMER_MS) s.timers.push(setTimeout(() => endStream(run, s), Math.max(0, ms)));
		}
	};

	const handleApi = async (req: IncomingMessage, res: ServerResponse, entry: FakeRequest, body: Buffer, caller: Caller): Promise<void> => {
		const method = req.method ?? "GET";
		const path = entry.path;
		if (method === "GET" && path === "/v1alpha1/profiles") {
			if (profilesDelayMs > 0) {
				try {
					await delay(profilesDelayMs, undefined, { signal: closing.signal });
				} catch {
					return;
				}
			}
			return json(res, entry, 200, [...profiles.values()]);
		}
		if (path === "/v1alpha1/runs" && method === "GET") {
			return json(res, entry, 200, [...runs.values()].filter(r => r.owner === caller.owner).map(runJson));
		}
		if (path === "/v1alpha1/runs" && method === "POST") {
			const parsed = parseCreateBody(parseJson(body.toString("utf8")));
			if (typeof parsed === "string") return json(res, entry, 400, { error: parsed });
			const key = `${caller.owner}\n${parsed.clientKey}`;
			const existing = byOwnerKey.get(key);
			if (existing) return json(res, entry, 200, runJson(existing));
			const profile = profiles.get(parsed.profile);
			if (!profile) return json(res, entry, 404, { error: `unknown profile ${parsed.profile}` });
			if (!profile.ready) return json(res, entry, 409, { error: `profile ${parsed.profile} is not Ready` });
			const run: RunState = {
				name: `${parsed.profile}-${(++runCounter).toString(36)}${randomBytes(2).toString("hex")}`,
				profile: parsed.profile,
				clientKey: parsed.clientKey,
				owner: caller.owner,
				prompt: parsed.prompt,
				schema: parsed.schema,
				workspace: parsed.workspace,
				scenario: /SCENARIO:([a-z-]+)/.exec(parsed.prompt)?.[1] ?? "report",
				phase: "Pending",
				reason: "",
				result: null,
				outputs: {},
				createdAt: new Date().toISOString(),
				events: [],
				lastSeq: 0,
				step: 0,
				stepStartSeq: 1,
				steers: [],
				pendingSteers: [],
				aborted: false,
				streams: new Set(),
				muted: new Set(),
				connections: 0,
				stop: new AbortController(),
			};
			runs.set(run.name, run);
			byOwnerKey.set(key, run);
			if (run.scenario === "flaky-create") json(res, entry, 503, { error: "upstream unavailable" });
			else json(res, entry, 201, runJson(run));
			void script(run);
			return;
		}
		const m = /^\/v1alpha1\/runs\/([^/]+)(\/events|\/steer|\/abort)?$/.exec(path);
		const run = m ? runs.get(m[1]) : undefined;
		if (!m) return json(res, entry, 404, { error: "not found" });
		if (!run || run.owner !== caller.owner) return json(res, entry, 404, { error: `no run ${m[1]}` });
		const sub = m[2] ?? "";
		if (method === "GET" && sub === "") return json(res, entry, 200, runJson(run));
		if (method === "GET" && sub === "/events") return openEvents(req, res, entry, run, caller);
		if (method === "POST" && sub === "/steer") {
			const v = parseJson(body.toString("utf8"));
			if (!isRecord(v) || typeof v.message !== "string") return json(res, entry, 400, { error: "body must be {message}" });
			if (isTerminal(run)) return json(res, entry, 409, { error: `run ${run.name} is ${run.phase}` });
			const steer: FakeSteer = { message: v.message, receivedAtStep: run.step, appliedAtStep: null };
			run.steers.push(steer);
			run.pendingSteers.push(steer);
			return json(res, entry, 202, {});
		}
		if (method === "POST" && sub === "/abort") {
			run.aborted = true;
			if (!isTerminal(run)) {
				finish(run, "Failed", "Cancelled", null, {});
				run.stop.abort();
			}
			return json(res, entry, 202, {});
		}
		return json(res, entry, 405, { error: "method not allowed" });
	};

	const snapshot = (): FakeRunSnapshot[] =>
		[...runs.values()].map(r => ({
			name: r.name,
			profile: r.profile,
			clientKey: r.clientKey,
			owner: r.owner,
			phase: r.phase,
			reason: r.reason,
			prompt: r.prompt,
			schema: r.schema,
			workspace: r.workspace,
			steers: r.steers.map(s => ({ ...s })),
			aborted: r.aborted,
			lastSeq: r.lastSeq,
		}));

	const handle = async (req: IncomingMessage, res: ServerResponse): Promise<void> => {
		res.on("error", () => {});
		const body = await readBody(req);
		const method = req.method ?? "GET";
		const path = new URL(req.url ?? "/", "http://fake").pathname;
		const lastEventId = req.headers["last-event-id"];
		const bearer = parseBearer(req.headers.authorization);
		const entry: FakeRequest = {
			method,
			path,
			status: 0,
			lastEventId: typeof lastEventId === "string" ? lastEventId : undefined,
			exp: bearer?.exp,
			bytes: body.length,
		};
		requestLog.push(entry);

		if (path === "/admin/profiles" && method === "POST") {
			const v = parseJson(body.toString("utf8"));
			if (!isRecord(v) || typeof v.name !== "string" || v.name === "") return json(res, entry, 400, { error: "name is required" });
			const models = Array.isArray(v.models) && v.models.every(x => typeof x === "string") ? v.models : undefined;
			setProfile({
				name: v.name,
				description: typeof v.description === "string" ? v.description : undefined,
				models,
				ready: typeof v.ready === "boolean" ? v.ready : undefined,
			});
			return json(res, entry, 200, profiles.get(v.name));
		}
		if (path === "/admin/runs" && method === "GET") return json(res, entry, 200, snapshot());
		if (path === "/admin/restart" && method === "POST") {
			restart();
			return json(res, entry, 200, {});
		}
		if (!path.startsWith("/v1alpha1/")) return json(res, entry, 404, { error: "not found" });
		if (!bearer) return json(res, entry, 401, { error: "missing or invalid bearer token" });
		if (bearer.exp !== undefined && bearer.exp * 1000 <= Date.now()) return json(res, entry, 401, { error: "token expired" });
		if (rateLimit && path === rateLimit.path && rateLimited < rateLimit.times) {
			rateLimited++;
			return json(res, entry, 429, { error: "rate limited" }, { "retry-after": String(rateLimit.retryAfterS) });
		}
		const owner = createHash("sha256").update(`${bearer.iss}\n${bearer.sub}`).digest("hex").slice(0, 40);
		await handleApi(req, res, entry, body, { owner, exp: bearer.exp });
	};

	const server = createServer((req, res) => {
		handle(req, res).catch((err: unknown) => {
			if (!res.headersSent) {
				res.writeHead(500, { "content-type": "application/json" });
				res.end(JSON.stringify({ error: err instanceof Error ? err.message : String(err) }));
			}
		});
	});
	const listening = Promise.withResolvers<void>();
	server.once("error", listening.reject);
	server.listen(opts.port ?? 0, "127.0.0.1", () => listening.resolve());
	await listening.promise;
	const { port } = server.address() as AddressInfo;

	return {
		url: `http://127.0.0.1:${port}`,
		runs: snapshot,
		requests: () => requestLog.map(r => ({ ...r })),
		setProfile,
		restart,
		close: async () => {
			closing.abort();
			for (const run of runs.values()) {
				run.stop.abort();
				for (const s of [...run.streams, ...run.muted]) destroyStream(run, s);
			}
			server.closeAllConnections();
			const closed = Promise.withResolvers<void>();
			server.close(() => closed.resolve());
			await closed.promise;
		},
	};
}

import { randomBytes } from "node:crypto";
import { mkdirSync, readdirSync, readFileSync, renameSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { ProviderModelConfig } from "@oh-my-pi/pi-coding-agent";
import { isProfile } from "./wire.ts";
import type { Profile } from "./wire.ts";

export interface ProfileSource {
	profiles(): Promise<Profile[]>;
}

export interface ProfileCache {
	endpoint: string;
	fetchedAt: string;
	profiles: Profile[];
}

export interface AgentFilesResult {
	written: string[];
	removed: string[];
	unchanged: string[];
	skipped: string[];
}

export type SyncProfilesResult =
	| { ok: true; profiles: Profile[]; files: AgentFilesResult }
	| { ok: false; error: Error };

export interface SyncProfilesOptions {
	api: ProfileSource;
	agentDir: string;
	agentsDir: string;
	endpoint: string;
	timeoutMs?: number;
}

const DNS1123_LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;
const AGENT_FILE = /^skua-.+\.md$/;
const YAML_UNSAFE_BREAKS = /[\u0085\u2028\u2029]/g;

export function cachePath(agentDir: string): string {
	return join(agentDir, "skua", "profiles.json");
}

function isCache(v: unknown): v is ProfileCache {
	if (typeof v !== "object" || v === null || Array.isArray(v)) return false;
	const c = v as Record<string, unknown>;
	return typeof c.endpoint === "string" && typeof c.fetchedAt === "string" && Array.isArray(c.profiles) && c.profiles.every(isProfile);
}

function writeAtomic(dir: string, name: string, content: string, mode: number): void {
	const tmp = join(dir, `.${name}.${randomBytes(6).toString("hex")}.tmp`);
	try {
		writeFileSync(tmp, content, { mode });
		renameSync(tmp, join(dir, name));
	} catch (err) {
		rmSync(tmp, { force: true });
		throw err;
	}
}

export function readCachedProfiles(agentDir: string, endpoint: string): Profile[] {
	let parsed: unknown;
	try {
		parsed = JSON.parse(readFileSync(cachePath(agentDir), "utf8"));
	} catch {
		return [];
	}
	if (!isCache(parsed) || parsed.endpoint !== endpoint) return [];
	return parsed.profiles;
}

export function writeCachedProfiles(agentDir: string, endpoint: string, profiles: Profile[]): void {
	const dir = join(agentDir, "skua");
	mkdirSync(dir, { recursive: true, mode: 0o700 });
	const cache: ProfileCache = { endpoint, fetchedAt: new Date().toISOString(), profiles };
	writeAtomic(dir, "profiles.json", `${JSON.stringify(cache, null, 2)}\n`, 0o600);
}

export function renderAgentFile(p: Profile): string {
	const description =
		`Remote skua agent, profile ${p.name}: ${p.description} Models: ${p.models.join(", ")}. ` +
		"Runs in the skua cluster on the pushed HEAD of origin and returns a report or schema-checked JSON; it may push skua/<run> and open a PR.";
	const quoted = JSON.stringify(description).replace(YAML_UNSAFE_BREAKS, c => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`);
	return [
		"---",
		`name: skua-${p.name}`,
		`description: ${quoted}`,
		`model: skua/${p.name}`,
		"tools: []",
		"---",
		"",
		"Relay for a remote skua run. The remote agent does the work; this session mirrors it.",
		"",
	].join("\n");
}

function readOptional(path: string): string | undefined {
	try {
		return readFileSync(path, "utf8");
	} catch {
		return undefined;
	}
}

export function syncAgentFiles(agentsDir: string, profiles: Profile[]): AgentFilesResult {
	const result: AgentFilesResult = { written: [], removed: [], unchanged: [], skipped: [] };
	mkdirSync(agentsDir, { recursive: true });
	const keep = new Set<string>();
	for (const p of profiles) {
		if (!p.ready) continue;
		if (!DNS1123_LABEL.test(p.name)) {
			result.skipped.push(p.name);
			continue;
		}
		const name = `skua-${p.name}.md`;
		if (keep.has(name)) continue;
		keep.add(name);
		const content = renderAgentFile(p);
		if (readOptional(join(agentsDir, name)) === content) {
			result.unchanged.push(name);
			continue;
		}
		writeAtomic(agentsDir, name, content, 0o644);
		result.written.push(name);
	}
	for (const entry of readdirSync(agentsDir, { withFileTypes: true })) {
		if (!AGENT_FILE.test(entry.name) || keep.has(entry.name) || entry.isDirectory()) continue;
		unlinkSync(join(agentsDir, entry.name));
		result.removed.push(entry.name);
	}
	return result;
}

function fetchWithTimeout(api: ProfileSource, timeoutMs: number): Promise<Profile[]> {
	const { promise: timedOut, reject } = Promise.withResolvers<never>();
	const signal = AbortSignal.timeout(timeoutMs);
	signal.addEventListener(
		"abort",
		() => {
			const err = new Error(`fetching profiles timed out after ${timeoutMs} ms`);
			err.name = "TimeoutError";
			reject(err);
		},
		{ once: true },
	);
	return Promise.race([api.profiles(), timedOut]);
}

export async function syncProfiles(opts: SyncProfilesOptions): Promise<SyncProfilesResult> {
	try {
		const profiles = await fetchWithTimeout(opts.api, opts.timeoutMs ?? 10000);
		if (!Array.isArray(profiles) || !profiles.every(isProfile)) throw new Error("skua-api returned a malformed profile list");
		writeCachedProfiles(opts.agentDir, opts.endpoint, profiles);
		const files = syncAgentFiles(opts.agentsDir, profiles);
		return { ok: true, profiles, files };
	} catch (err) {
		return { ok: false, error: err instanceof Error ? err : new Error(String(err)) };
	}
}

export function readyModels(profiles: Profile[]): ProviderModelConfig[] {
	return profiles
		.filter(p => p.ready)
		.map(p => ({
			id: p.name,
			name: `skua ${p.name}`,
			reasoning: false,
			input: ["text"],
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
			contextWindow: 1000000,
			maxTokens: 64000,
		}));
}

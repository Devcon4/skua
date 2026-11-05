import { accessSync, constants, readFileSync, rmSync, statSync } from "node:fs";
import { delimiter, join } from "node:path";
import type { Api } from "./api.ts";
import { loadConfig, saveConfig } from "./config.ts";
import type { SkuaConfig, SkuaConfigInput } from "./config.ts";
import { decodeIdToken, login as oidcLogin } from "./oidc.ts";
import type { OpenUrl } from "./oidc.ts";
import { cachePath, readCachedProfiles, syncAgentFiles } from "./profiles.ts";
import type { SyncProfilesResult } from "./profiles.ts";
import type { LiveRun } from "./relay.ts";
import { createTokenSource, saveToken } from "./token.ts";
import type { TokenSource } from "./token.ts";
import type { VersionCheck } from "./version.ts";
import type { Run } from "./wire.ts";

export type NotifyLevel = "info" | "warning" | "error";

export interface SkuaServices {
	tokens: TokenSource;
	api: Api;
	relay: { live(): LiveRun[] };
}

export interface CommandDeps {
	agentDir: string;
	agentsDir: string;
	omp: { version: string; check: VersionCheck };
	notify: (text: string, level: NotifyLevel) => void;
	services: (cfg: SkuaConfig) => SkuaServices;
	sync: (cfg: SkuaConfig) => Promise<SyncProfilesResult>;
	/** Starts the background profile refresh for this session unless it already runs. */
	startRefresh: (cfg: SkuaConfig) => void;
	openUrl: OpenUrl;
	login?: typeof oidcLogin;
	now?: () => number;
}

export type SkuaCommand =
	| { kind: "login"; values: { endpoint: string; issuer: string; clientId: string } | undefined; device: boolean }
	| { kind: "sync" }
	| { kind: "list" }
	| { kind: "abort"; run: string }
	| { kind: "status" }
	| { kind: "usage"; error: string | undefined };

export type OpenUrlExec = (command: string, args: string[]) => Promise<{ code: number }>;

export const USAGE = [
	"Usage: /skua <subcommand>",
	"  login [<endpoint> <issuer> <clientId>] [--device]  sign in; the three values are required the first time and replace the stored ones",
	"  sync                                               refresh the profile list and the skua-<profile> agent files",
	"  list                                               list your skua runs",
	"  abort <run>                                        cancel a run",
	"  status                                             show login, omp version, profile cache and relayed runs",
].join("\n");

const NOT_CONFIGURED = "skua: not configured. Run /skua login <endpoint> <issuer> <clientId>.";
const BROWSER_COMMANDS = ["xdg-open", "open", "wslview"];
const AGENT_FILE = /^skua-(.+)\.md$/;

function errText(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}

export function parseSkuaCommand(args: string): SkuaCommand {
	const [sub, ...rest] = args.trim().split(/\s+/).filter(s => s.length > 0);
	switch (sub) {
		case undefined:
		case "help":
			return { kind: "usage", error: undefined };
		case "login": {
			const device = rest.includes("--device");
			const values = rest.filter(a => a !== "--device");
			if (values.some(v => v.startsWith("--"))) return { kind: "usage", error: `unknown login flag ${values.find(v => v.startsWith("--"))}` };
			if (values.length === 0) return { kind: "login", values: undefined, device };
			if (values.length !== 3) return { kind: "usage", error: "login takes <endpoint> <issuer> <clientId> together, or none of them" };
			const [endpoint, issuer, clientId] = values;
			if (!URL.canParse(endpoint)) return { kind: "usage", error: `endpoint ${endpoint} is not a URL` };
			if (!URL.canParse(issuer)) return { kind: "usage", error: `issuer ${issuer} is not a URL` };
			return { kind: "login", values: { endpoint, issuer, clientId }, device };
		}
		case "abort":
			return rest.length === 1 ? { kind: "abort", run: rest[0] } : { kind: "usage", error: "abort takes exactly one run name" };
		case "sync":
		case "list":
		case "status":
			return rest.length === 0 ? { kind: sub } : { kind: "usage", error: `${sub} takes no arguments` };
		default:
			return { kind: "usage", error: `unknown subcommand ${sub}` };
	}
}

export function formatRuns(runs: readonly Run[], live: readonly LiveRun[]): string {
	if (runs.length === 0) return "skua list: no runs";
	const relayed = new Set(live.map(l => l.run));
	const lines = runs.map(r => {
		const cols = [r.name, r.profile, r.phase, r.reason || "-", r.createdAt];
		if (relayed.has(r.name)) cols.push("relayed here");
		return cols.join("  ");
	});
	return ["skua runs:", ...lines].join("\n");
}

function isExecutableFile(path: string): boolean {
	try {
		accessSync(path, constants.X_OK);
		return statSync(path).isFile();
	} catch {
		return false;
	}
}

function findOnPath(name: string, path: string | undefined): string | undefined {
	for (const dir of (path ?? "").split(delimiter)) {
		if (dir.length === 0) continue;
		const candidate = join(dir, name);
		if (isExecutableFile(candidate)) return candidate;
	}
	return undefined;
}

/** Opens `url` with `$BROWSER` (run through `sh -c`, so it may carry arguments), else the first of xdg-open, open, wslview on `PATH`; `false` when none exists or the command exits non-zero. */
export function createOpenUrl(opts: { exec: OpenUrlExec; env: Record<string, string | undefined> }): OpenUrl {
	return async url => {
		try {
			const browser = opts.env.BROWSER;
			if (browser !== undefined && browser.trim().length > 0) {
				return (await opts.exec("sh", ["-c", `${browser} "$1"`, "sh", url])).code === 0;
			}
			for (const name of BROWSER_COMMANDS) {
				const bin = findOnPath(name, opts.env.PATH);
				if (bin !== undefined) return (await opts.exec(bin, [url])).code === 0;
			}
			return false;
		} catch {
			return false;
		}
	};
}

function profileNames(files: readonly string[]): string[] {
	return files
		.map(f => AGENT_FILE.exec(f)?.[1])
		.filter((n): n is string => n !== undefined)
		.sort();
}

function listed(names: readonly string[]): string {
	return names.length > 0 ? names.join(", ") : "none";
}

function readConfig(deps: CommandDeps): SkuaConfig | undefined {
	let cfg: SkuaConfig | undefined;
	try {
		cfg = loadConfig(deps.agentDir);
	} catch (err) {
		deps.notify(`skua: ${errText(err)}. Fix it or run /skua login <endpoint> <issuer> <clientId>.`, "error");
		return undefined;
	}
	if (cfg === undefined) deps.notify(NOT_CONFIGURED, "error");
	return cfg;
}

function usage(deps: CommandDeps, error: string | undefined): void {
	if (error === undefined) return deps.notify(USAGE, "info");
	deps.notify(`skua: ${error}\n${USAGE}`, "error");
}

function resetEndpointState(deps: CommandDeps, cfg: SkuaConfig): void {
	createTokenSource({ agentDir: deps.agentDir, cfg }).clear();
	rmSync(cachePath(deps.agentDir), { force: true });
	syncAgentFiles(deps.agentsDir, []);
}

async function runLogin(cmd: Extract<SkuaCommand, { kind: "login" }>, deps: CommandDeps): Promise<void> {
	let stored: SkuaConfig | undefined;
	let loadError: string | undefined;
	try {
		stored = loadConfig(deps.agentDir);
	} catch (err) {
		loadError = errText(err);
	}
	let cfg = stored;
	if (cmd.values !== undefined) {
		const input: SkuaConfigInput = { ...cmd.values };
		if (stored !== undefined) {
			input.scopes = stored.scopes;
			input.loopbackPort = stored.loopbackPort;
			input.heartbeatTimeoutMs = stored.heartbeatTimeoutMs;
			input.profileRefreshMs = stored.profileRefreshMs;
		}
		const { endpointChanged } = saveConfig(deps.agentDir, input);
		cfg = loadConfig(deps.agentDir);
		if (cfg !== undefined && endpointChanged) resetEndpointState(deps, cfg);
	}
	if (cfg === undefined) {
		return usage(deps, loadError ?? "no stored skua config; login needs <endpoint> <issuer> <clientId> the first time");
	}

	const login = deps.login ?? oidcLogin;
	let tokens;
	try {
		tokens = await login({ cfg, mode: cmd.device ? "device" : "auto", openUrl: deps.openUrl, notify: text => deps.notify(text, "info") });
	} catch (err) {
		return deps.notify(`skua login failed: ${errText(err)}`, "error");
	}
	saveToken(deps.agentDir, tokens);

	let who: string;
	try {
		const claims = decodeIdToken(tokens.idToken);
		who = claims.email ?? claims.sub;
	} catch {
		who = "unknown subject";
	}
	const synced = await deps.sync(cfg);
	deps.startRefresh(cfg);
	if (!synced.ok) {
		return deps.notify(`skua: logged in as ${who} at ${cfg.endpoint}; profiles: not synced (${synced.error.message})`, "warning");
	}
	deps.notify(`skua: logged in as ${who} at ${cfg.endpoint}; profiles: ${listed(profileNames([...synced.files.written, ...synced.files.unchanged]))}`, "info");
}

async function runSync(deps: CommandDeps): Promise<void> {
	const cfg = readConfig(deps);
	if (cfg === undefined) return;
	const res = await deps.sync(cfg);
	if (!res.ok) return deps.notify(`skua sync failed: ${res.error.message}`, "error");
	const names = profileNames([...res.files.written, ...res.files.unchanged]);
	let text = `skua sync: ${names.length} profiles (${listed(names)}); removed ${listed(profileNames(res.files.removed))}`;
	if (res.files.skipped.length > 0) text += `; skipped ${res.files.skipped.join(", ")} (not a DNS-1123 label)`;
	if (res.files.written.length > 0) text += ". New agents are dispatchable now; the task tool description lists them after the next base-prompt rebuild.";
	deps.notify(text, "info");
}

async function runList(deps: CommandDeps): Promise<void> {
	const cfg = readConfig(deps);
	if (cfg === undefined) return;
	const { api, relay } = deps.services(cfg);
	let runs: Run[];
	try {
		runs = await api.listRuns();
	} catch (err) {
		return deps.notify(`skua list failed: ${errText(err)}`, "error");
	}
	deps.notify(formatRuns(runs, relay.live()), "info");
}

async function runAbort(run: string, deps: CommandDeps): Promise<void> {
	const cfg = readConfig(deps);
	if (cfg === undefined) return;
	try {
		await deps.services(cfg).api.abort(run);
	} catch (err) {
		return deps.notify(`skua abort ${run}: ${errText(err)}`, "error");
	}
	deps.notify(`skua abort ${run}: requested`, "info");
}

export function formatAge(ms: number): string {
	const s = Math.max(0, Math.floor(ms / 1000));
	if (s < 60) return `${s}s`;
	if (s < 3600) return `${Math.floor(s / 60)}m`;
	if (s < 86400) return `${Math.floor(s / 3600)}h`;
	return `${Math.floor(s / 86400)}d`;
}

function cacheFetchedAt(agentDir: string): number | undefined {
	try {
		const parsed: unknown = JSON.parse(readFileSync(cachePath(agentDir), "utf8"));
		if (typeof parsed !== "object" || parsed === null || !("fetchedAt" in parsed) || typeof parsed.fetchedAt !== "string") return undefined;
		const at = Date.parse(parsed.fetchedAt);
		return Number.isNaN(at) ? undefined : at;
	} catch {
		return undefined;
	}
}

function liveLine(live: readonly LiveRun[]): string {
	if (live.length === 0) return "relayed here: none";
	return `relayed here: ${live.map(l => `${l.run} (follow-up ${l.followUp})`).join(", ")}`;
}

function runStatus(deps: CommandDeps): void {
	const now = (deps.now ?? Date.now)();
	const omp = deps.omp.check.ok ? `omp: ${deps.omp.version}, in the tested range` : `omp: ${deps.omp.version}; ${deps.omp.check.message}`;
	let cfg: SkuaConfig | undefined;
	try {
		cfg = loadConfig(deps.agentDir);
	} catch (err) {
		return deps.notify(["skua status", `config: ${errText(err)}`, omp].join("\n"), "warning");
	}
	if (cfg === undefined) return deps.notify(["skua status", "config: none. Run /skua login <endpoint> <issuer> <clientId>.", omp].join("\n"), "info");

	const { tokens, relay } = deps.services(cfg);
	const st = tokens.status();
	let login: string;
	if (!st.loggedIn) login = "login: not logged in. Run /skua login.";
	else {
		const expiry = st.expiresAt === undefined ? "unknown" : new Date(st.expiresAt * 1000).toISOString();
		const who = st.email !== undefined ? `${st.email} (subject ${st.subject ?? "unknown"})` : `subject ${st.subject ?? "unknown"}`;
		login = `login: ${who}; token expires ${expiry}`;
	}
	const count = readCachedProfiles(deps.agentDir, cfg.endpoint).length;
	const fetchedAt = count > 0 ? cacheFetchedAt(deps.agentDir) : undefined;
	const cache = count === 0 ? "profiles: none cached" : `profiles: ${count} cached${fetchedAt === undefined ? "" : `, fetched ${formatAge(now - fetchedAt)} ago`}`;
	deps.notify(["skua status", `endpoint: ${cfg.endpoint}`, `issuer: ${cfg.issuer}`, `client id: ${cfg.clientId}`, login, omp, cache, liveLine(relay.live())].join("\n"), "info");
}

/** Runs one `/skua` command line; every outcome, including failures, is reported through `deps.notify`. Never rejects. */
export async function runSkuaCommand(args: string, deps: CommandDeps): Promise<void> {
	const cmd = parseSkuaCommand(args);
	try {
		switch (cmd.kind) {
			case "usage":
				return usage(deps, cmd.error);
			case "login":
				return await runLogin(cmd, deps);
			case "sync":
				return await runSync(deps);
			case "list":
				return await runList(deps);
			case "abort":
				return await runAbort(cmd.run, deps);
			case "status":
				return runStatus(deps);
		}
	} catch (err) {
		deps.notify(`skua ${cmd.kind}: ${errText(err)}`, "error");
	}
}

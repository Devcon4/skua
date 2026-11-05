import { randomBytes } from "node:crypto";
import { chmodSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export interface SkuaConfig {
	endpoint: string;
	issuer: string;
	clientId: string;
	scopes: string[];
	loopbackPort: number;
	heartbeatTimeoutMs: number;
	profileRefreshMs: number;
}

export type SkuaConfigInput = Pick<SkuaConfig, "endpoint" | "issuer" | "clientId"> &
	Partial<Pick<SkuaConfig, "scopes" | "loopbackPort" | "heartbeatTimeoutMs" | "profileRefreshMs">>;

export const CONFIG_DEFAULTS = {
	scopes: ["openid", "profile", "email", "offline_access"],
	loopbackPort: 0,
	heartbeatTimeoutMs: 45_000,
	profileRefreshMs: 600_000,
} as const;

export function skuaDir(agentDir: string): string {
	return join(agentDir, "skua");
}

export function configPath(agentDir: string): string {
	return join(skuaDir(agentDir), "config.json");
}

/** Writes `data` to `<agentDir>/skua/<name>` through a same-directory rename; the directory is created or reset to mode 0700. */
export function writePrivateFile(agentDir: string, name: string, data: string, mode: number): void {
	const dir = skuaDir(agentDir);
	mkdirSync(dir, { recursive: true, mode: 0o700 });
	chmodSync(dir, 0o700);
	const target = join(dir, name);
	const tmp = `${target}.${process.pid}.${randomBytes(6).toString("hex")}.tmp`;
	try {
		writeFileSync(tmp, data, { mode, flag: "wx" });
		renameSync(tmp, target);
	} catch (err) {
		rmSync(tmp, { force: true });
		throw err;
	}
}

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isNonEmptyString(v: unknown): v is string {
	return typeof v === "string" && v.length > 0;
}

function isPositiveNumber(v: unknown): v is number {
	return typeof v === "number" && Number.isFinite(v) && v > 0;
}

function isPort(v: unknown): v is number {
	return typeof v === "number" && Number.isInteger(v) && v >= 0 && v <= 65535;
}

function isConfigInput(v: unknown): v is SkuaConfigInput {
	if (!isObject(v)) return false;
	if (!isNonEmptyString(v.endpoint) || !isNonEmptyString(v.issuer) || !isNonEmptyString(v.clientId)) return false;
	if (v.scopes !== undefined && !(Array.isArray(v.scopes) && v.scopes.every(isNonEmptyString))) return false;
	if (v.loopbackPort !== undefined && !isPort(v.loopbackPort)) return false;
	if (v.heartbeatTimeoutMs !== undefined && !isPositiveNumber(v.heartbeatTimeoutMs)) return false;
	if (v.profileRefreshMs !== undefined && !isPositiveNumber(v.profileRefreshMs)) return false;
	return true;
}

function withDefaults(cfg: SkuaConfigInput): SkuaConfig {
	return {
		endpoint: cfg.endpoint,
		issuer: cfg.issuer,
		clientId: cfg.clientId,
		scopes: [...(cfg.scopes ?? CONFIG_DEFAULTS.scopes)],
		loopbackPort: cfg.loopbackPort ?? CONFIG_DEFAULTS.loopbackPort,
		heartbeatTimeoutMs: cfg.heartbeatTimeoutMs ?? CONFIG_DEFAULTS.heartbeatTimeoutMs,
		profileRefreshMs: cfg.profileRefreshMs ?? CONFIG_DEFAULTS.profileRefreshMs,
	};
}

/** Reads `<agentDir>/skua/config.json` with defaults filled in; `undefined` when the file does not exist. Throws when the file is not valid config JSON. */
export function loadConfig(agentDir: string): SkuaConfig | undefined {
	const path = configPath(agentDir);
	let text: string;
	try {
		text = readFileSync(path, "utf8");
	} catch (err) {
		if (isObject(err) && err.code === "ENOENT") return undefined;
		throw err;
	}
	let parsed: unknown;
	try {
		parsed = JSON.parse(text);
	} catch {
		throw new Error(`skua config ${path} is not valid JSON`);
	}
	if (!isConfigInput(parsed)) throw new Error(`skua config ${path} needs endpoint, issuer and clientId strings and valid optional fields`);
	return withDefaults(parsed);
}

/** Stores `cfg`; `endpointChanged` is true when a stored config named another endpoint or could not be read, so the caller clears the token, the profile cache and the agent files. */
export function saveConfig(agentDir: string, cfg: SkuaConfigInput): { endpointChanged: boolean } {
	if (!isConfigInput(cfg)) throw new Error("skua config needs endpoint, issuer and clientId strings and valid optional fields");
	let endpointChanged: boolean;
	try {
		const previous = loadConfig(agentDir);
		endpointChanged = previous !== undefined && previous.endpoint !== cfg.endpoint;
	} catch {
		endpointChanged = true;
	}
	const stored: SkuaConfigInput = { endpoint: cfg.endpoint, issuer: cfg.issuer, clientId: cfg.clientId };
	if (cfg.scopes !== undefined) stored.scopes = [...cfg.scopes];
	if (cfg.loopbackPort !== undefined) stored.loopbackPort = cfg.loopbackPort;
	if (cfg.heartbeatTimeoutMs !== undefined) stored.heartbeatTimeoutMs = cfg.heartbeatTimeoutMs;
	if (cfg.profileRefreshMs !== undefined) stored.profileRefreshMs = cfg.profileRefreshMs;
	writePrivateFile(agentDir, "config.json", `${JSON.stringify(stored, null, 2)}\n`, 0o600);
	return { endpointChanged };
}

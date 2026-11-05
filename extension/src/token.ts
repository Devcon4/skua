import { readFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { skuaDir, writePrivateFile } from "./config.ts";
import type { SkuaConfig } from "./config.ts";
import { OidcError, decodeIdToken, refresh } from "./oidc.ts";
import type { TokenSet } from "./oidc.ts";

export type RefreshImpl = (cfg: SkuaConfig, refreshToken: string) => Promise<TokenSet>;

export interface TokenStatus {
	loggedIn: boolean;
	subject: string | undefined;
	email: string | undefined;
	/** The id_token `exp` claim, in epoch seconds. */
	expiresAt: number | undefined;
}

export interface TokenSource {
	/** Returns the cached id_token, refreshing it first when under 60 s remain. Rejects with `TokenError`. */
	getToken(): Promise<string>;
	/** Refreshes now unless another writer already stored a different token; for a bearer skua-api rejected. Rejects with `TokenError`. */
	forceRefresh(): Promise<string>;
	status(): TokenStatus;
	clear(): void;
}

/** `not_logged_in`: no usable token or refresh token, run `/skua login`. `unauthorized`: the token expired and the issuer could not refresh it. */
export class TokenError extends Error {
	readonly code: "not_logged_in" | "unauthorized";
	constructor(code: "not_logged_in" | "unauthorized", message: string) {
		super(message);
		this.name = "TokenError";
		this.code = code;
	}
}

const REFRESH_MARGIN_MS = 60_000;
const TOKEN_FILE = "token.json";
const LOGGED_OUT: TokenStatus = { loggedIn: false, subject: undefined, email: undefined, expiresAt: undefined };

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isTokenSet(v: unknown): v is TokenSet {
	return (
		isObject(v) &&
		typeof v.issuer === "string" &&
		typeof v.clientId === "string" &&
		typeof v.idToken === "string" &&
		v.idToken.length > 0 &&
		(v.refreshToken === undefined || (typeof v.refreshToken === "string" && v.refreshToken.length > 0)) &&
		typeof v.expiresAt === "number" &&
		Number.isFinite(v.expiresAt)
	);
}

function notLoggedIn(): TokenError {
	return new TokenError("not_logged_in", "not logged in: run /skua login");
}

/** Stores `tokens` as `<agentDir>/skua/token.json`, mode 0600, through a rename. */
export function saveToken(agentDir: string, tokens: TokenSet): void {
	const stored: TokenSet = { issuer: tokens.issuer, clientId: tokens.clientId, idToken: tokens.idToken, expiresAt: tokens.expiresAt };
	if (tokens.refreshToken !== undefined) stored.refreshToken = tokens.refreshToken;
	writePrivateFile(agentDir, TOKEN_FILE, `${JSON.stringify(stored)}\n`, 0o600);
}

export function createTokenSource(opts: { agentDir: string; cfg: SkuaConfig; refreshImpl?: RefreshImpl; now?: () => number }): TokenSource {
	const { agentDir, cfg } = opts;
	const refreshImpl = opts.refreshImpl ?? refresh;
	const now = opts.now ?? Date.now;
	const path = join(skuaDir(agentDir), TOKEN_FILE);
	let inflight: Promise<string> | undefined;
	let lastIssued: string | undefined;

	const read = (): TokenSet | undefined => {
		let parsed: unknown;
		try {
			parsed = JSON.parse(readFileSync(path, "utf8"));
		} catch {
			return undefined;
		}
		if (!isTokenSet(parsed) || parsed.issuer !== cfg.issuer || parsed.clientId !== cfg.clientId) return undefined;
		return parsed;
	};

	const remainingMs = (t: TokenSet): number => t.expiresAt * 1000 - now();

	const clear = (): void => {
		rmSync(path, { force: true });
		lastIssued = undefined;
	};

	const issue = (t: TokenSet): string => {
		lastIssued = t.idToken;
		return t.idToken;
	};

	const doRefresh = async (force: boolean): Promise<string> => {
		const current = read();
		if (!current) throw notLoggedIn();
		const remaining = remainingMs(current);
		const valid = remaining > 0;
		const fresh = remaining >= REFRESH_MARGIN_MS;
		if (fresh && (!force || (lastIssued !== undefined && current.idToken !== lastIssued))) return issue(current);
		if (current.refreshToken === undefined) {
			if (!force && valid) return issue(current);
			throw notLoggedIn();
		}
		let next: TokenSet;
		try {
			next = await refreshImpl(cfg, current.refreshToken);
		} catch (err) {
			if (err instanceof OidcError && err.code === "invalid_grant") {
				const latest = read();
				if (latest && latest.refreshToken !== current.refreshToken && remainingMs(latest) > 0) return issue(latest);
				clear();
				throw notLoggedIn();
			}
			if (!force && valid) return issue(current);
			const reason = err instanceof Error ? err.message : String(err);
			throw new TokenError("unauthorized", `unauthorized: the skua token expired and the issuer could not refresh it (${reason})`);
		}
		const stored: TokenSet = { ...next, refreshToken: next.refreshToken ?? current.refreshToken };
		saveToken(agentDir, stored);
		return issue(stored);
	};

	const refreshOnce = (force: boolean): Promise<string> => {
		inflight ??= doRefresh(force).finally(() => {
			inflight = undefined;
		});
		return inflight;
	};

	return {
		getToken: async () => {
			const current = read();
			if (!current) throw notLoggedIn();
			if (remainingMs(current) >= REFRESH_MARGIN_MS) return issue(current);
			return refreshOnce(false);
		},
		forceRefresh: () => refreshOnce(true),
		status: () => {
			const current = read();
			if (!current) return { ...LOGGED_OUT };
			try {
				const claims = decodeIdToken(current.idToken);
				const loggedIn = current.refreshToken !== undefined || remainingMs(current) > 0;
				return { loggedIn, subject: claims.sub, email: claims.email, expiresAt: current.expiresAt };
			} catch {
				return { ...LOGGED_OUT };
			}
		},
		clear,
	};
}

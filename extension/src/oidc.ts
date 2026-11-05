import { randomBytes } from "node:crypto";
import { ConfigurationError, LoginCancelledError, OAuthError } from "@oh-my-pi/pi-ai/error";
import { OAuthCallbackFlow } from "@oh-my-pi/pi-ai/oauth/callback-server";
import { pollOAuthDeviceCodeFlow } from "@oh-my-pi/pi-ai/oauth/device-code";
import { generatePKCE } from "@oh-my-pi/pi-ai/oauth/pkce";
import type { OAuthController, OAuthCredentials } from "@oh-my-pi/pi-ai/oauth/types";
import type { SkuaConfig } from "./config.ts";

export interface IssuerMetadata {
	issuer: string;
	authorizationEndpoint: string;
	tokenEndpoint: string;
	deviceAuthorizationEndpoint: string | undefined;
}

export interface TokenSet {
	issuer: string;
	clientId: string;
	idToken: string;
	refreshToken?: string;
	/** The id_token `exp` claim, in epoch seconds. */
	expiresAt: number;
}

export interface IdTokenClaims {
	iss: string;
	sub: string;
	aud: string[];
	exp: number;
	email: string | undefined;
	nonce: string | undefined;
}

export type OpenUrl = (url: string) => boolean | Promise<boolean>;
export type Notify = (text: string) => void;

/** Login, discovery and token endpoint failure; `code` is the issuer's OAuth `error` value or one of this module's names. */
export class OidcError extends Error {
	readonly code: string;
	readonly status: number | undefined;
	constructor(code: string, message: string, status?: number) {
		super(message);
		this.name = "OidcError";
		this.code = code;
		this.status = status;
	}
}

const REQUEST_TIMEOUT_MS = 15_000;
const DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code";

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

function errText(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}

function randomToken(bytes: number): string {
	return randomBytes(bytes).toString("base64url");
}

async function getJson(url: string): Promise<unknown> {
	let res: Response;
	try {
		res = await fetch(url, { headers: { accept: "application/json" }, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
	} catch (err) {
		throw new OidcError("network", `cannot reach ${url}: ${errText(err)}`);
	}
	if (!res.ok) throw new OidcError("discovery", `${url} answered ${res.status}`, res.status);
	try {
		return await res.json();
	} catch {
		throw new OidcError("discovery", `${url} did not return JSON`, res.status);
	}
}

async function postForm(url: string, form: Record<string, string>): Promise<Record<string, unknown>> {
	let res: Response;
	try {
		res = await fetch(url, {
			method: "POST",
			headers: { "content-type": "application/x-www-form-urlencoded", accept: "application/json" },
			body: new URLSearchParams(form),
			signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
		});
	} catch (err) {
		throw new OidcError("network", `cannot reach ${url}: ${errText(err)}`);
	}
	let body: unknown;
	try {
		body = await res.json();
	} catch {
		body = undefined;
	}
	if (res.ok && isObject(body)) return body;
	if (res.status >= 500) throw new OidcError("server", `${url} answered ${res.status}`, res.status);
	if (isObject(body) && typeof body.error === "string") {
		const description = typeof body.error_description === "string" ? `: ${body.error_description}` : "";
		throw new OidcError(body.error, `issuer returned ${body.error}${description}`, res.status);
	}
	throw new OidcError("token_endpoint", `${url} answered ${res.status} without an OAuth error`, res.status);
}

function optionalString(v: unknown): string | undefined {
	return typeof v === "string" && v.length > 0 ? v : undefined;
}

export async function discover(issuer: string): Promise<IssuerMetadata> {
	const url = `${issuer.replace(/\/+$/, "")}/.well-known/openid-configuration`;
	const doc = await getJson(url);
	if (!isObject(doc)) throw new OidcError("discovery", `${url} is not a JSON object`);
	if (doc.issuer !== issuer) {
		throw new OidcError("issuer_mismatch", `issuer metadata names ${String(doc.issuer)}, expected the configured issuer ${issuer}`);
	}
	const methods = doc.code_challenge_methods_supported;
	if (!Array.isArray(methods) || !methods.includes("S256")) {
		throw new OidcError("no_s256", `issuer ${issuer} does not list S256 in code_challenge_methods_supported; skua login needs PKCE with S256`);
	}
	const authorizationEndpoint = optionalString(doc.authorization_endpoint);
	const tokenEndpoint = optionalString(doc.token_endpoint);
	if (!authorizationEndpoint || !tokenEndpoint) {
		throw new OidcError("discovery", `issuer ${issuer} metadata lacks authorization_endpoint or token_endpoint`);
	}
	return { issuer, authorizationEndpoint, tokenEndpoint, deviceAuthorizationEndpoint: optionalString(doc.device_authorization_endpoint) };
}

/** Decodes the id_token payload without verifying the signature; skua-api verifies it. */
export function decodeIdToken(idToken: string): IdTokenClaims {
	const payload = idToken.split(".")[1];
	let claims: unknown;
	try {
		claims = JSON.parse(Buffer.from(payload ?? "", "base64url").toString("utf8"));
	} catch {
		throw new OidcError("id_token", "id_token payload is not JSON");
	}
	if (!isObject(claims)) throw new OidcError("id_token", "id_token payload is not a JSON object");
	const aud = typeof claims.aud === "string" ? [claims.aud] : Array.isArray(claims.aud) ? claims.aud.filter(a => typeof a === "string") : [];
	if (typeof claims.iss !== "string" || typeof claims.sub !== "string" || typeof claims.exp !== "number") {
		throw new OidcError("id_token", "id_token lacks iss, sub or exp");
	}
	return {
		iss: claims.iss,
		sub: claims.sub,
		aud,
		exp: claims.exp,
		email: optionalString(claims.email),
		nonce: optionalString(claims.nonce),
	};
}

function tokenSet(cfg: SkuaConfig, meta: IssuerMetadata, body: Record<string, unknown>, nonce: string | undefined): TokenSet {
	const idToken = optionalString(body.id_token);
	if (!idToken) throw new OidcError("id_token", "token response has no id_token");
	const claims = decodeIdToken(idToken);
	if (claims.iss !== meta.issuer) throw new OidcError("id_token", `id_token iss ${claims.iss} is not the configured issuer ${meta.issuer}`);
	if (!claims.aud.includes(cfg.clientId)) throw new OidcError("id_token", `id_token aud does not contain client id ${cfg.clientId}`);
	if (nonce !== undefined && claims.nonce !== nonce) throw new OidcError("id_token", "id_token nonce does not match the login request");
	const set: TokenSet = { issuer: cfg.issuer, clientId: cfg.clientId, idToken, expiresAt: claims.exp };
	const refreshToken = optionalString(body.refresh_token);
	if (refreshToken) set.refreshToken = refreshToken;
	return set;
}

class LoopbackFlow extends OAuthCallbackFlow {
	tokens: TokenSet | undefined;
	exchanging = false;
	readonly #cfg: SkuaConfig;
	readonly #meta: IssuerMetadata;
	readonly #pkce: { verifier: string; challenge: string };
	readonly #nonce: string;

	constructor(ctrl: OAuthController, cfg: SkuaConfig, meta: IssuerMetadata, pkce: { verifier: string; challenge: string }) {
		super(ctrl, { preferredPort: cfg.loopbackPort, callbackHostname: "127.0.0.1", allowPortFallback: false });
		this.#cfg = cfg;
		this.#meta = meta;
		this.#pkce = pkce;
		this.#nonce = randomToken(16);
	}

	async generateAuthUrl(state: string, redirectUri: string): Promise<{ url: string }> {
		const authorize = new URL(this.#meta.authorizationEndpoint);
		authorize.searchParams.set("response_type", "code");
		authorize.searchParams.set("client_id", this.#cfg.clientId);
		authorize.searchParams.set("redirect_uri", redirectUri);
		authorize.searchParams.set("scope", this.#cfg.scopes.join(" "));
		authorize.searchParams.set("state", state);
		authorize.searchParams.set("nonce", this.#nonce);
		authorize.searchParams.set("code_challenge", this.#pkce.challenge);
		authorize.searchParams.set("code_challenge_method", "S256");
		return { url: authorize.toString() };
	}

	async exchangeToken(code: string, _state: string, redirectUri: string): Promise<OAuthCredentials> {
		this.exchanging = true;
		const body = await postForm(this.#meta.tokenEndpoint, {
			grant_type: "authorization_code",
			code,
			redirect_uri: redirectUri,
			client_id: this.#cfg.clientId,
			code_verifier: this.#pkce.verifier,
		});
		const tokens = tokenSet(this.#cfg, this.#meta, body, this.#nonce);
		this.tokens = tokens;
		return { access: tokens.idToken, refresh: tokens.refreshToken ?? "", expires: tokens.expiresAt * 1000 };
	}
}

async function browserFlow(cfg: SkuaConfig, meta: IssuerMetadata, openUrl: OpenUrl, notify: Notify, timeoutMs: number): Promise<TokenSet> {
	const noBrowser = new AbortController();
	const timeout = AbortSignal.timeout(timeoutMs);
	const onAuth = ({ url }: { url: string }): void => {
		const opening = (async () => {
			try {
				return await openUrl(url);
			} catch {
				return false;
			}
		})();
		void opening.then(opened => {
			if (!opened && !flow.exchanging) {
				noBrowser.abort();
				return;
			}
			notify(`skua login: finish signing in in the browser. If it did not open, visit ${url}`);
		});
	};
	const flow = new LoopbackFlow({ onAuth, signal: AbortSignal.any([noBrowser.signal, timeout]) }, cfg, meta, await generatePKCE());
	try {
		await flow.login();
	} catch (err) {
		if (err instanceof OidcError) throw err;
		if (err instanceof ConfigurationError) {
			throw new OidcError("port_in_use", `cannot listen on 127.0.0.1:${cfg.loopbackPort} for the login callback (${err.message})`);
		}
		if (err instanceof LoginCancelledError && noBrowser.signal.aborted) {
			throw new OidcError("browser_unavailable", "no browser could be started for the login page");
		}
		if (err instanceof LoginCancelledError && timeout.aborted) {
			throw new OidcError("timeout", `no login callback within ${Math.round(timeoutMs / 1000)} s`);
		}
		if (err instanceof OAuthError) throw new OidcError("authorization_failed", `issuer refused the login: ${err.message}`);
		throw err;
	}
	if (!flow.tokens) throw new OidcError("invalid_callback", "login finished without a token response");
	return flow.tokens;
}

const DEVICE_EXPIRED = "the device code expired before the login was approved";

async function deviceFlow(cfg: SkuaConfig, meta: IssuerMetadata, notify: Notify): Promise<TokenSet> {
	if (!meta.deviceAuthorizationEndpoint) {
		throw new OidcError("no_device", `issuer ${meta.issuer} does not advertise device_authorization_endpoint`);
	}
	const start = await postForm(meta.deviceAuthorizationEndpoint, { client_id: cfg.clientId, scope: cfg.scopes.join(" ") });
	const deviceCode = optionalString(start.device_code);
	const userCode = optionalString(start.user_code);
	const verificationUri = optionalString(start.verification_uri);
	if (!deviceCode || !userCode || !verificationUri) {
		throw new OidcError("device_authorization", "device authorization response lacks device_code, user_code or verification_uri");
	}
	const complete = optionalString(start.verification_uri_complete);
	notify(
		complete
			? `skua login: visit ${complete} and confirm the code ${userCode}`
			: `skua login: visit ${verificationUri} and enter the code ${userCode}`,
	);
	try {
		return await pollOAuthDeviceCodeFlow<TokenSet>({
			intervalSeconds: typeof start.interval === "number" && start.interval > 0 ? start.interval : undefined,
			expiresInSeconds: typeof start.expires_in === "number" && start.expires_in > 0 ? start.expires_in : 600,
			poll: async () => {
				try {
					const body = await postForm(meta.tokenEndpoint, { grant_type: DEVICE_GRANT, device_code: deviceCode, client_id: cfg.clientId });
					return { status: "complete", value: tokenSet(cfg, meta, body, undefined) };
				} catch (err) {
					if (!(err instanceof OidcError)) throw err;
					if (err.code === "authorization_pending") return { status: "pending" };
					if (err.code === "slow_down") return { status: "slow_down" };
					if (err.code === "expired_token") throw new OidcError("expired_token", DEVICE_EXPIRED, err.status);
					if (err.code === "access_denied") throw new OidcError("access_denied", "the device login was denied", err.status);
					throw err;
				}
			},
		});
	} catch (err) {
		if (err instanceof OAuthError && err.kind === "timeout") throw new OidcError("expired_token", DEVICE_EXPIRED);
		throw err;
	}
}

export async function loginWithBrowser(opts: { cfg: SkuaConfig; openUrl: OpenUrl; notify: Notify; timeoutMs?: number }): Promise<TokenSet> {
	const meta = await discover(opts.cfg.issuer);
	return browserFlow(opts.cfg, meta, opts.openUrl, opts.notify, opts.timeoutMs ?? 300_000);
}

export async function loginWithDevice(opts: { cfg: SkuaConfig; notify: Notify }): Promise<TokenSet> {
	const meta = await discover(opts.cfg.issuer);
	return deviceFlow(opts.cfg, meta, opts.notify);
}

/** `auto` tries the browser flow and falls back to the device flow when no browser starts or the loopback port cannot be bound. */
export async function login(opts: { cfg: SkuaConfig; mode: "auto" | "device"; openUrl: OpenUrl; notify: Notify; timeoutMs?: number }): Promise<TokenSet> {
	const meta = await discover(opts.cfg.issuer);
	if (opts.mode === "device") return deviceFlow(opts.cfg, meta, opts.notify);
	try {
		return await browserFlow(opts.cfg, meta, opts.openUrl, opts.notify, opts.timeoutMs ?? 300_000);
	} catch (err) {
		if (!(err instanceof OidcError) || (err.code !== "browser_unavailable" && err.code !== "port_in_use")) throw err;
		if (!meta.deviceAuthorizationEndpoint) {
			throw new OidcError(
				"no_login_flow",
				`skua login cannot proceed: the browser flow failed (${err.message}) and the issuer offers no device flow (no device_authorization_endpoint). Set BROWSER to a command that opens URLs, free the loopback port, or enable the device flow at the issuer.`,
			);
		}
		return deviceFlow(opts.cfg, meta, opts.notify);
	}
}

export async function refresh(cfg: SkuaConfig, refreshToken: string): Promise<TokenSet> {
	const meta = await discover(cfg.issuer);
	const body = await postForm(meta.tokenEndpoint, { grant_type: "refresh_token", refresh_token: refreshToken, client_id: cfg.clientId });
	return tokenSet(cfg, meta, body, undefined);
}

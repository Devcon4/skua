import { createHash, randomBytes } from "node:crypto";
import { createServer } from "node:http";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export type FakeIssuerOptions = {
	port?: number;
	clientId?: string;
	subject?: string;
	email?: string;
	idTokenTtlS?: number;
	failRefreshTimes?: number;
	noDevice?: boolean;
	noS256?: boolean;
	interval?: number;
	deviceExpiresInS?: number;
	now?: () => number;
};

export type FakeTokenRequest = { grantType: string; status: number; error: string | undefined };

export type FakeIssuer = {
	url: string;
	close(): Promise<void>;
	approveDevice(userCode: string): void;
	expireRefreshTokens(): void;
	tokenRequests(): FakeTokenRequest[];
};

type PendingCode = { redirectUri: string; challenge: string; nonce: string | undefined; expiresAt: number };
type PendingDevice = { userCode: string; approved: boolean; expiresAt: number; intervalS: number; lastPollAt: number | undefined };

const DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code";
const USER_CODE_ALPHABET = "BCDFGHJKLMNPQRSTVWXZ";

function b64url(v: unknown): string {
	return Buffer.from(JSON.stringify(v)).toString("base64url");
}

function userCode(): string {
	const bytes = randomBytes(8);
	const chars = [...bytes].map(b => USER_CODE_ALPHABET[b % USER_CODE_ALPHABET.length]);
	return `${chars.slice(0, 4).join("")}-${chars.slice(4).join("")}`;
}

function isLoopbackRedirect(uri: string): boolean {
	try {
		const u = new URL(uri);
		return u.protocol === "http:" && (u.hostname === "127.0.0.1" || u.hostname === "localhost" || u.hostname === "[::1]");
	} catch {
		return false;
	}
}

async function readBody(req: IncomingMessage): Promise<string> {
	const chunks: Buffer[] = [];
	for await (const c of req) chunks.push(c);
	return Buffer.concat(chunks).toString("utf8");
}

export async function startFakeIssuer(opts: FakeIssuerOptions = {}): Promise<FakeIssuer> {
	const clientId = opts.clientId ?? "skua-omp";
	const subject = opts.subject ?? "fake-user";
	const email = opts.email ?? "dev@example.test";
	const idTokenTtlS = opts.idTokenTtlS ?? 3600;
	const intervalS = opts.interval ?? 5;
	const deviceExpiresInS = opts.deviceExpiresInS ?? 600;
	const now = opts.now ?? Date.now;
	let refreshFailuresLeft = opts.failRefreshTimes ?? 0;

	const codes = new Map<string, PendingCode>();
	const devices = new Map<string, PendingDevice>();
	const refreshTokens = new Set<string>();
	const tokenLog: FakeTokenRequest[] = [];
	let url = "";

	const json = (res: ServerResponse, status: number, body: unknown): void => {
		res.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" });
		res.end(JSON.stringify(body));
	};

	const issueTokens = (nonce: string | undefined): Record<string, unknown> => {
		const iat = Math.floor(now() / 1000);
		const claims: Record<string, unknown> = { iss: url, sub: subject, aud: clientId, email, email_verified: true, iat, exp: iat + idTokenTtlS };
		if (nonce !== undefined) claims.nonce = nonce;
		const refreshToken = randomBytes(24).toString("base64url");
		refreshTokens.add(refreshToken);
		return {
			access_token: randomBytes(24).toString("base64url"),
			token_type: "Bearer",
			expires_in: idTokenTtlS,
			id_token: `${b64url({ alg: "none", typ: "JWT" })}.${b64url(claims)}.`,
			refresh_token: refreshToken,
		};
	};

	const tokenGrant = (form: URLSearchParams): { status: number; body: Record<string, unknown> } => {
		const fail = (status: number, error: string, description: string) => ({ status, body: { error, error_description: description } });
		if (form.get("client_id") !== clientId) return fail(401, "invalid_client", "unknown client_id");
		const grant = form.get("grant_type");
		if (grant === "authorization_code") {
			const code = form.get("code") ?? "";
			const pending = codes.get(code);
			codes.delete(code);
			if (!pending || pending.expiresAt <= now()) return fail(400, "invalid_grant", "unknown or expired code");
			if (form.get("redirect_uri") !== pending.redirectUri) return fail(400, "invalid_grant", "redirect_uri mismatch");
			const challenge = createHash("sha256").update(form.get("code_verifier") ?? "").digest("base64url");
			if (challenge !== pending.challenge) return fail(400, "invalid_grant", "PKCE verification failed");
			return { status: 200, body: issueTokens(pending.nonce) };
		}
		if (grant === "refresh_token") {
			if (refreshFailuresLeft > 0) {
				refreshFailuresLeft--;
				return fail(503, "temporarily_unavailable", "issuer outage");
			}
			const token = form.get("refresh_token") ?? "";
			if (!refreshTokens.delete(token)) return fail(400, "invalid_grant", "unknown or revoked refresh token");
			return { status: 200, body: issueTokens(undefined) };
		}
		if (grant === DEVICE_GRANT) {
			const deviceCode = form.get("device_code") ?? "";
			const device = devices.get(deviceCode);
			if (!device) return fail(400, "invalid_grant", "unknown device_code");
			const t = now();
			if (device.expiresAt <= t) {
				devices.delete(deviceCode);
				return fail(400, "expired_token", "device code expired");
			}
			const tooFast = device.lastPollAt !== undefined && t - device.lastPollAt < device.intervalS * 1000;
			device.lastPollAt = t;
			if (tooFast) {
				device.intervalS += 5;
				return fail(400, "slow_down", `poll at most every ${device.intervalS} s`);
			}
			if (!device.approved) return fail(400, "authorization_pending", "waiting for the user");
			devices.delete(deviceCode);
			return { status: 200, body: issueTokens(undefined) };
		}
		return fail(400, "unsupported_grant_type", `grant_type ${grant ?? "(none)"} is not supported`);
	};

	const authorize = (q: URLSearchParams, res: ServerResponse): void => {
		const redirectUri = q.get("redirect_uri") ?? "";
		if (q.get("client_id") !== clientId) {
			res.writeHead(400, { "content-type": "text/plain" });
			res.end("unknown client_id");
			return;
		}
		if (!isLoopbackRedirect(redirectUri)) {
			res.writeHead(400, { "content-type": "text/plain" });
			res.end("redirect_uri must be an http loopback URL");
			return;
		}
		const target = new URL(redirectUri);
		const state = q.get("state");
		if (state !== null) target.searchParams.set("state", state);
		const challenge = q.get("code_challenge") ?? "";
		if (q.get("response_type") !== "code") {
			target.searchParams.set("error", "unsupported_response_type");
		} else if (opts.noS256 || q.get("code_challenge_method") !== "S256" || challenge === "") {
			target.searchParams.set("error", "invalid_request");
			target.searchParams.set("error_description", "code_challenge with code_challenge_method=S256 is required");
		} else {
			const code = randomBytes(16).toString("base64url");
			codes.set(code, { redirectUri, challenge, nonce: q.get("nonce") ?? undefined, expiresAt: now() + 60_000 });
			target.searchParams.set("code", code);
		}
		res.writeHead(302, { location: target.toString() });
		res.end();
	};

	const handle = async (req: IncomingMessage, res: ServerResponse): Promise<void> => {
		const body = await readBody(req);
		const u = new URL(req.url ?? "/", "http://fake");
		const method = req.method ?? "GET";
		if (method === "GET" && u.pathname === "/.well-known/openid-configuration") {
			const doc: Record<string, unknown> = {
				issuer: url,
				authorization_endpoint: `${url}/authorize`,
				token_endpoint: `${url}/token`,
				jwks_uri: `${url}/keys`,
				response_types_supported: ["code"],
				subject_types_supported: ["public"],
				id_token_signing_alg_values_supported: ["none"],
				scopes_supported: ["openid", "profile", "email", "offline_access"],
				grant_types_supported: ["authorization_code", "refresh_token", ...(opts.noDevice ? [] : [DEVICE_GRANT])],
				code_challenge_methods_supported: opts.noS256 ? ["plain"] : ["S256"],
			};
			if (!opts.noDevice) doc.device_authorization_endpoint = `${url}/device_authorization`;
			return json(res, 200, doc);
		}
		if (method === "GET" && u.pathname === "/keys") return json(res, 200, { keys: [] });
		if (method === "GET" && u.pathname === "/authorize") return authorize(u.searchParams, res);
		if (method === "POST" && u.pathname === "/token") {
			const form = new URLSearchParams(body);
			const { status, body: out } = tokenGrant(form);
			tokenLog.push({ grantType: form.get("grant_type") ?? "", status, error: typeof out.error === "string" ? out.error : undefined });
			return json(res, status, out);
		}
		if (method === "POST" && u.pathname === "/device_authorization" && !opts.noDevice) {
			const form = new URLSearchParams(body);
			if (form.get("client_id") !== clientId) return json(res, 401, { error: "invalid_client", error_description: "unknown client_id" });
			const deviceCode = randomBytes(24).toString("base64url");
			const code = userCode();
			devices.set(deviceCode, { userCode: code, approved: false, expiresAt: now() + deviceExpiresInS * 1000, intervalS, lastPollAt: undefined });
			return json(res, 200, {
				device_code: deviceCode,
				user_code: code,
				verification_uri: `${url}/device`,
				verification_uri_complete: `${url}/device?user_code=${code}`,
				expires_in: deviceExpiresInS,
				interval: intervalS,
			});
		}
		json(res, 404, { error: "not_found" });
	};

	const server = createServer((req, res) => {
		handle(req, res).catch((err: unknown) => {
			if (!res.headersSent) json(res, 500, { error: "server_error", error_description: err instanceof Error ? err.message : String(err) });
		});
	});
	server.keepAliveTimeout = 60_000;
	const listening = Promise.withResolvers<void>();
	server.once("error", listening.reject);
	server.listen(opts.port ?? 0, "127.0.0.1", () => listening.resolve());
	await listening.promise;
	url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;

	return {
		url,
		approveDevice: (code: string) => {
			const device = [...devices.values()].find(d => d.userCode === code);
			if (!device) throw new Error(`no pending device authorization for user code ${code}`);
			device.approved = true;
		},
		expireRefreshTokens: () => refreshTokens.clear(),
		tokenRequests: () => tokenLog.map(t => ({ ...t })),
		close: async () => {
			server.closeAllConnections();
			const closed = Promise.withResolvers<void>();
			server.close(() => closed.resolve());
			await closed.promise;
		},
	};
}

import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { test } from "node:test";
import type { SkuaConfig } from "../../src/config.ts";
import { OidcError, decodeIdToken, discover, login, loginWithBrowser, loginWithDevice } from "../../src/oidc.ts";
import { startFakeIssuer } from "../fake/oidc.ts";
import type { FakeIssuer, FakeIssuerOptions } from "../fake/oidc.ts";

function config(issuer: FakeIssuer, loopbackPort = 0): SkuaConfig {
	return {
		endpoint: "http://127.0.0.1:32080",
		issuer: issuer.url,
		clientId: "skua-omp",
		scopes: ["openid", "profile", "email", "offline_access"],
		loopbackPort,
		heartbeatTimeoutMs: 45_000,
		profileRefreshMs: 600_000,
	};
}

async function withIssuer(opts: FakeIssuerOptions, fn: (issuer: FakeIssuer) => Promise<void>): Promise<void> {
	const issuer = await startFakeIssuer(opts);
	try {
		await fn(issuer);
	} finally {
		await issuer.close();
	}
}

function userCodeOf(text: string): string {
	const m = /code ([A-Z]{4}-[A-Z]{4})/.exec(text);
	assert.ok(m, `no user code in ${text}`);
	return m[1];
}

test("browser flow completes with PKCE through the loopback callback", async () => {
	await withIssuer({}, async issuer => {
		const notes: string[] = [];
		const tokens = await loginWithBrowser({
			cfg: config(issuer),
			notify: text => notes.push(text),
			openUrl: async url => {
				const res = await fetch(url);
				await res.text();
				assert.equal(res.status, 200);
				return true;
			},
		});
		assert.equal(tokens.issuer, issuer.url);
		assert.equal(tokens.clientId, "skua-omp");
		assert.ok(tokens.refreshToken);
		const claims = decodeIdToken(tokens.idToken);
		assert.equal(claims.sub, "fake-user");
		assert.equal(tokens.expiresAt, claims.exp);
		assert.equal(notes.length, 1);
		const authorize = new URL(/visit (\S+)$/.exec(notes[0])?.[1] ?? "");
		assert.equal(authorize.searchParams.get("code_challenge_method"), "S256");
		assert.match(authorize.searchParams.get("redirect_uri") ?? "", /^http:\/\/127\.0\.0\.1:\d+\/callback$/);
		assert.equal(authorize.searchParams.get("scope"), "openid profile email offline_access");
		for (const note of notes) assert.ok(!note.includes(tokens.idToken));
		assert.deepEqual(issuer.tokenRequests(), [{ grantType: "authorization_code", status: 200, error: undefined }]);
	});
});

test("a callback with the wrong state is refused and the genuine callback still completes the login", async () => {
	await withIssuer({}, async issuer => {
		let forgedStatus = 0;
		const tokens = await loginWithBrowser({
			cfg: config(issuer),
			notify: () => {},
			openUrl: async url => {
				const res = await fetch(url, { redirect: "manual" });
				const callback = new URL(res.headers.get("location") ?? "");
				const forged = new URL(callback);
				forged.searchParams.set("state", "forged");
				const cb = await fetch(forged);
				forgedStatus = cb.status;
				await cb.text();
				assert.deepEqual(issuer.tokenRequests(), []);
				return (await fetch(callback)).ok;
			},
		});
		assert.equal(forgedStatus, 500);
		assert.ok(tokens.idToken);
		assert.deepEqual(issuer.tokenRequests(), [{ grantType: "authorization_code", status: 200, error: undefined }]);
	});
});

test("an issuer error on the callback fails the login", async () => {
	await withIssuer({}, async issuer => {
		const flow = loginWithBrowser({
			cfg: config(issuer),
			notify: () => {},
			openUrl: async url => {
				const u = new URL(url);
				u.searchParams.set("response_type", "token");
				await (await fetch(u)).text();
				return true;
			},
		});
		await assert.rejects(flow, (err: unknown) => err instanceof OidcError && err.code === "authorization_failed" && /unsupported_response_type/.test(err.message));
	});
});

test("the browser flow times out without a callback", async () => {
	await withIssuer({}, async issuer => {
		await assert.rejects(
			loginWithBrowser({ cfg: config(issuer), notify: () => {}, openUrl: () => true, timeoutMs: 200 }),
			(err: unknown) => err instanceof OidcError && err.code === "timeout",
		);
	});
});

test("discovery fails without S256", async () => {
	await withIssuer({ noS256: true }, async issuer => {
		await assert.rejects(discover(issuer.url), (err: unknown) => err instanceof OidcError && err.code === "no_s256" && /S256/.test(err.message));
	});
});

test("discovery fails when the issuer differs from the configured value", async () => {
	await withIssuer({}, async issuer => {
		await assert.rejects(discover(`${issuer.url}/`), (err: unknown) => err instanceof OidcError && err.code === "issuer_mismatch");
	});
});

test("auto falls back to the device flow when no browser starts", async () => {
	await withIssuer({ interval: 1 }, async issuer => {
		const notes: string[] = [];
		const tokens = await login({
			cfg: config(issuer),
			mode: "auto",
			openUrl: () => false,
			notify: text => {
				notes.push(text);
				issuer.approveDevice(userCodeOf(text));
			},
		});
		assert.equal(notes.length, 1);
		assert.match(notes[0], /\/device\?user_code=/);
		assert.equal(decodeIdToken(tokens.idToken).aud[0], "skua-omp");
		assert.deepEqual(
			issuer.tokenRequests().map(r => r.grantType),
			["urn:ietf:params:oauth:grant-type:device_code"],
		);
	});
});

test("auto falls back to the device flow when the loopback port is taken", async () => {
	const blocker = createServer();
	const listening = Promise.withResolvers<void>();
	blocker.listen(0, "127.0.0.1", () => listening.resolve());
	await listening.promise;
	const port = (blocker.address() as AddressInfo).port;
	try {
		await withIssuer({ interval: 1 }, async issuer => {
			let opened = false;
			const tokens = await login({
				cfg: config(issuer, port),
				mode: "auto",
				openUrl: () => {
					opened = true;
					return true;
				},
				notify: text => issuer.approveDevice(userCodeOf(text)),
			});
			assert.equal(opened, false);
			assert.ok(tokens.idToken);
		});
	} finally {
		blocker.close();
	}
});

test("slow_down raises the polling interval by 5 s", { timeout: 20_000 }, async () => {
	let issuer: FakeIssuer | undefined;
	let userCode = "";
	const secondPollTooSoon = (): number => {
		if (issuer?.tokenRequests().length !== 1) return Date.now();
		issuer.approveDevice(userCode);
		return Date.now() - 600;
	};
	await withIssuer({ interval: 1, now: secondPollTooSoon }, async started => {
		issuer = started;
		const begin = Date.now();
		const tokens = await loginWithDevice({
			cfg: config(started),
			notify: text => {
				userCode = userCodeOf(text);
			},
		});
		assert.ok(tokens.idToken);
		assert.ok(Date.now() - begin >= 7000, `login took ${Date.now() - begin} ms, want 1 s + 6 s of polling waits`);
		assert.deepEqual(
			started.tokenRequests().map(r => r.error),
			["authorization_pending", "slow_down", undefined],
		);
	});
});

test("an expired device code stops the device flow", async () => {
	await withIssuer({ interval: 1, deviceExpiresInS: 2 }, async issuer => {
		await assert.rejects(
			loginWithDevice({ cfg: config(issuer), notify: () => {} }),
			(err: unknown) => err instanceof OidcError && err.code === "expired_token",
		);
	});
});

test("no browser and no device flow fails naming both flows", async () => {
	await withIssuer({ noDevice: true }, async issuer => {
		await assert.rejects(
			login({ cfg: config(issuer), mode: "auto", openUrl: () => false, notify: () => {} }),
			(err: unknown) => err instanceof OidcError && err.code === "no_login_flow" && /browser/.test(err.message) && /device/.test(err.message),
		);
	});
});

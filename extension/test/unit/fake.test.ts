import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { startFakeApi } from "../fake/api.ts";
import type { FakeApi, FakeRunSnapshot } from "../fake/api.ts";
import { startFakeIssuer } from "../fake/oidc.ts";

type Frame = { id: number; event: string; data: unknown };

function b64url(v: unknown): string {
	return Buffer.from(JSON.stringify(v)).toString("base64url");
}

function bearer(sub: string): string {
	return `Bearer ${b64url({ alg: "none" })}.${b64url({ iss: "https://issuer.test", sub, exp: Math.floor(Date.now() / 1000) + 3600 })}.`;
}

async function createRun(api: FakeApi, auth: string, clientKey: string, prompt: string): Promise<{ status: number; name: string }> {
	const res = await fetch(`${api.url}/v1alpha1/runs`, {
		method: "POST",
		headers: { authorization: auth, "content-type": "application/json" },
		body: JSON.stringify({ profile: "generic", clientKey, task: { prompt } }),
	});
	const run = (await res.json()) as { name: string };
	return { status: res.status, name: run.name };
}

async function waitForRun(api: FakeApi, done: (r: FakeRunSnapshot) => boolean): Promise<FakeRunSnapshot> {
	for (let i = 0; i < 1000; i++) {
		const run = api.runs()[0];
		if (run && done(run)) return run;
		await delay(10);
	}
	throw new Error("run did not reach the expected state");
}

async function readEvents(url: string, auth: string, opts: { lastEventId?: number; stopWhen?: (seen: Frame[]) => boolean } = {}): Promise<Frame[]> {
	const headers: Record<string, string> = { authorization: auth };
	if (opts.lastEventId !== undefined) headers["last-event-id"] = String(opts.lastEventId);
	const res = await fetch(url, { headers });
	assert.equal(res.status, 200);
	assert.ok(res.body);
	const frames: Frame[] = [];
	const decoder = new TextDecoder();
	let buf = "";
	for await (const chunk of res.body) {
		buf += decoder.decode(chunk, { stream: true });
		for (let i = buf.indexOf("\n\n"); i >= 0; i = buf.indexOf("\n\n")) {
			const fields = new Map(
				buf
					.slice(0, i)
					.split("\n")
					.filter(l => !l.startsWith(":"))
					.map(l => [l.slice(0, l.indexOf(":")), l.slice(l.indexOf(":") + 2)]),
			);
			buf = buf.slice(i + 2);
			if (!fields.has("event")) continue;
			frames.push({ id: Number(fields.get("id")), event: fields.get("event") ?? "", data: JSON.parse(fields.get("data") ?? "null") });
			if (opts.stopWhen?.(frames)) return frames;
		}
	}
	return frames;
}

test("clientKey is idempotent per owner", async () => {
	const api = await startFakeApi({ stepMs: 10 });
	try {
		const alice = bearer("alice");
		const first = await createRun(api, alice, "sess-1:0", "hi");
		const again = await createRun(api, alice, "sess-1:0", "hi");
		const other = await createRun(api, bearer("bob"), "sess-1:0", "hi");
		assert.equal(first.status, 201);
		assert.equal(again.status, 200);
		assert.equal(again.name, first.name);
		assert.equal(other.status, 201);
		assert.notEqual(other.name, first.name);
		assert.equal(api.runs().length, 2);
	} finally {
		await api.close();
	}
});

test("events replay after Last-Event-ID and the stream closes after end", async () => {
	const api = await startFakeApi({ stepMs: 10, steps: 2 });
	try {
		const auth = bearer("alice");
		const { name } = await createRun(api, auth, "sess-2:0", "SCENARIO:steps");
		await waitForRun(api, r => r.phase === "Succeeded");
		const url = `${api.url}/v1alpha1/runs/${name}/events`;
		const all = await readEvents(url, auth);
		assert.deepEqual(all.map(f => f.id), all.map((_, i) => i + 1));
		assert.equal(all.at(-1)?.event, "end");
		const resumed = await readEvents(url, auth, { lastEventId: 3 });
		assert.deepEqual(resumed, all.filter(f => f.id > 3));
		const lastEventIds = api.requests().filter(r => r.path.endsWith("/events")).map(r => r.lastEventId);
		assert.deepEqual(lastEventIds, [undefined, "3"]);
	} finally {
		await api.close();
	}
});

test("steer is accepted while running and gets 409 once the run is terminal", async () => {
	const api = await startFakeApi({ stepMs: 50 });
	try {
		const auth = bearer("alice");
		const { name } = await createRun(api, auth, "sess-3:0", "SCENARIO:report");
		const steer = (message: string) =>
			fetch(`${api.url}/v1alpha1/runs/${name}/steer`, {
				method: "POST",
				headers: { authorization: auth, "content-type": "application/json" },
				body: JSON.stringify({ message }),
			});
		assert.equal((await steer("also check pkg9")).status, 202);
		const done = await waitForRun(api, r => r.phase === "Succeeded");
		assert.deepEqual(done.steers, [{ message: "also check pkg9", receivedAtStep: 0, appliedAtStep: 1 }]);
		assert.equal((await steer("too late")).status, 409);
		assert.equal(api.runs()[0]?.steers.length, 1);
	} finally {
		await api.close();
	}
});

test("restart replays the current step with its original seqs, then a terminal run yields one status-built end", async () => {
	const api = await startFakeApi({ stepMs: 300, steps: 4 });
	try {
		const auth = bearer("alice");
		const { name } = await createRun(api, auth, "sess-4:0", "SCENARIO:steps");
		const url = `${api.url}/v1alpha1/runs/${name}/events`;
		const before = await readEvents(url, auth, { stopWhen: seen => seen.filter(f => f.event === "tool_end").length === 2 });
		api.restart();
		const after = await readEvents(url, auth);
		assert.deepEqual(after.slice(0, 4), before.slice(-4));
		assert.deepEqual(after.map(f => f.id), after.map((_, i) => before.at(-4)!.id + i));
		const end = after.at(-1);
		assert.equal(end?.event, "end");

		api.restart();
		const replayed = await readEvents(url, auth);
		const run = (await (await fetch(`${api.url}/v1alpha1/runs/${name}`, { headers: { authorization: auth } })).json()) as Record<string, unknown>;
		assert.deepEqual(replayed, [
			{ id: end.id, event: "end", data: { phase: run.phase, reason: run.reason, result: run.result, outputs: run.outputs } },
		]);
		assert.equal(api.runs()[0]?.lastSeq, end.id);
	} finally {
		await api.close();
	}
});

test("issuer rejects a code exchange with the wrong PKCE verifier", async () => {
	const issuer = await startFakeIssuer();
	try {
		const redirectUri = "http://127.0.0.1:9/callback";
		const authorize = async (verifier: string): Promise<string> => {
			const q = new URLSearchParams({
				response_type: "code",
				client_id: "skua-omp",
				redirect_uri: redirectUri,
				scope: "openid email offline_access",
				state: "st-1",
				nonce: "n-1",
				code_challenge: createHash("sha256").update(verifier).digest("base64url"),
				code_challenge_method: "S256",
			});
			const res = await fetch(`${issuer.url}/authorize?${q}`, { redirect: "manual" });
			assert.equal(res.status, 302);
			const location = new URL(res.headers.get("location") ?? "");
			assert.equal(location.searchParams.get("state"), "st-1");
			return location.searchParams.get("code") ?? "";
		};
		const exchange = (code: string, verifier: string) =>
			fetch(`${issuer.url}/token`, {
				method: "POST",
				headers: { "content-type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ grant_type: "authorization_code", code, redirect_uri: redirectUri, client_id: "skua-omp", code_verifier: verifier }),
			});
		const verifier = randomBytes(32).toString("base64url");

		const rejected = await exchange(await authorize(verifier), randomBytes(32).toString("base64url"));
		assert.equal(rejected.status, 400);
		assert.match(await rejected.text(), /"error":"invalid_grant"/);

		const accepted = await exchange(await authorize(verifier), verifier);
		assert.equal(accepted.status, 200);
		const tokens = (await accepted.json()) as { id_token: string };
		const claims = JSON.parse(Buffer.from(tokens.id_token.split(".")[1], "base64url").toString("utf8")) as Record<string, unknown>;
		assert.equal(claims.iss, issuer.url);
		assert.equal(claims.aud, "skua-omp");
		assert.equal(claims.nonce, "n-1");
		assert.deepEqual(issuer.tokenRequests().map(t => t.status), [400, 200]);
	} finally {
		await issuer.close();
	}
});

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import { cachePath, readCachedProfiles, readyModels, renderAgentFile, syncAgentFiles, syncProfiles } from "../../src/profiles.ts";
import type { ProfileSource } from "../../src/profiles.ts";
import { isProfile } from "../../src/wire.ts";
import type { Profile } from "../../src/wire.ts";
import { startFakeApi } from "../fake/api.ts";
import type { FakeApi } from "../fake/api.ts";

const ENDPOINT = "https://skua.test";
const roots: string[] = [];

after(() => {
	for (const r of roots) rmSync(r, { recursive: true, force: true });
});

function dirs(): { agentDir: string; agentsDir: string } {
	const root = mkdtempSync(join(tmpdir(), "skua-profiles-"));
	roots.push(root);
	return { agentDir: join(root, "agent"), agentsDir: join(root, "extension", "agents") };
}

function b64url(v: unknown): string {
	return Buffer.from(JSON.stringify(v)).toString("base64url");
}

function source(api: FakeApi): ProfileSource {
	return {
		profiles: async () => {
			const auth = `Bearer ${b64url({ alg: "none" })}.${b64url({ iss: "https://issuer.test", sub: "dev", exp: Math.floor(Date.now() / 1000) + 3600 })}.`;
			const res = await fetch(`${api.url}/v1alpha1/profiles`, { headers: { authorization: auth } });
			if (!res.ok) throw new Error(`profiles: HTTP ${res.status}`);
			const body: unknown = await res.json();
			if (!Array.isArray(body) || !body.every(isProfile)) throw new Error("profiles: bad body");
			return body;
		},
	};
}

function snapshot(agentDir: string, agentsDir: string): { cache: string; files: Map<string, string> } {
	const files = new Map<string, string>();
	for (const name of readdirSync(agentsDir).sort()) files.set(name, readFileSync(join(agentsDir, name), "utf8"));
	return { cache: readFileSync(cachePath(agentDir), "utf8"), files };
}

function decodeYamlDoubleQuoted(line: string): string {
	const m = /^description: "((?:[^"\\\n\r\u0085\u2028\u2029]|\\.)*)"$/.exec(line);
	assert.ok(m, `description line is not a single-line YAML double-quoted scalar: ${line}`);
	const simple: Record<string, string> = { '"': '"', "\\": "\\", "/": "/", n: "\n", r: "\r", t: "\t", b: "\b", f: "\f", "0": "\0" };
	return m[1].replace(/\\(u[0-9a-fA-F]{4}|.)/g, (_, e: string) => {
		if (e.startsWith("u") && e.length === 5) return String.fromCharCode(Number.parseInt(e.slice(1), 16));
		const v = simple[e];
		assert.ok(v !== undefined, `unknown escape \\${e}`);
		return v;
	});
}

test("two ready profiles write two agent files, the cache and two models", async () => {
	const api = await startFakeApi();
	try {
		const { agentDir, agentsDir } = dirs();
		const res = await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT });
		assert.ok(res.ok);
		assert.deepEqual(res.files, { written: ["skua-generic.md", "skua-golang.md"], removed: [], unchanged: [], skipped: [] });
		assert.deepEqual(readdirSync(agentsDir).sort(), ["skua-generic.md", "skua-golang.md"]);
		const generic = readFileSync(join(agentsDir, "skua-generic.md"), "utf8");
		assert.equal(generic, renderAgentFile(res.profiles[0]));
		assert.match(generic, /^---\nname: skua-generic\ndescription: ".*"\nmodel: skua\/generic\ntools: \[\]\n---\n\nRelay for a remote skua run\./);
		assert.deepEqual(
			readCachedProfiles(agentDir, ENDPOINT).map(p => p.name),
			["generic", "golang"],
		);
		assert.deepEqual(readyModels(readCachedProfiles(agentDir, ENDPOINT)), [
			{ id: "generic", name: "skua generic", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000000, maxTokens: 64000 },
			{ id: "golang", name: "skua golang", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000000, maxTokens: 64000 },
		]);
	} finally {
		await api.close();
	}
});

test("a third profile is written, then removed once not ready; notes.md survives", async () => {
	const api = await startFakeApi();
	try {
		const { agentDir, agentsDir } = dirs();
		mkdirSync(agentsDir, { recursive: true });
		writeFileSync(join(agentsDir, "notes.md"), "keep me\n");
		writeFileSync(join(agentsDir, "skua-stale.md"), "old\n");

		api.setProfile({ name: "rust", description: "Rust toolchain remote agent.", ready: true });
		const first = await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT });
		assert.ok(first.ok);
		assert.deepEqual(first.files.written.sort(), ["skua-generic.md", "skua-golang.md", "skua-rust.md"]);
		assert.deepEqual(first.files.removed, ["skua-stale.md"]);

		api.setProfile({ name: "rust", ready: false });
		const second = await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT });
		assert.ok(second.ok);
		assert.deepEqual(second.files, { written: [], removed: ["skua-rust.md"], unchanged: ["skua-generic.md", "skua-golang.md"], skipped: [] });
		assert.deepEqual(readdirSync(agentsDir).sort(), ["notes.md", "skua-generic.md", "skua-golang.md"]);
		assert.equal(readFileSync(join(agentsDir, "notes.md"), "utf8"), "keep me\n");
		assert.deepEqual(readyModels(readCachedProfiles(agentDir, ENDPOINT)).map(m => m.id), ["generic", "golang"]);
	} finally {
		await api.close();
	}
});

test("a second sync with no change reports every file unchanged and keeps mtimes", async () => {
	const api = await startFakeApi();
	try {
		const { agentDir, agentsDir } = dirs();
		assert.ok((await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT })).ok);
		const past = new Date(Date.now() - 60_000);
		for (const name of readdirSync(agentsDir)) utimesSync(join(agentsDir, name), past, past);
		const before = readdirSync(agentsDir).map(n => statSync(join(agentsDir, n)).mtimeMs);

		const res = await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT });
		assert.ok(res.ok);
		assert.deepEqual(res.files, { written: [], removed: [], unchanged: ["skua-generic.md", "skua-golang.md"], skipped: [] });
		assert.deepEqual(
			readdirSync(agentsDir).map(n => statSync(join(agentsDir, n)).mtimeMs),
			before,
		);
	} finally {
		await api.close();
	}
});

test("a description with quotes, a newline and unicode line breaks renders valid frontmatter", () => {
	const p: Profile = { name: "quirky", description: 'Says "hi" and \\o/\nsecond line\u2028third: yes # no', models: ["m/one", "m/two"], ready: true };
	const text = renderAgentFile(p);
	const lines = text.split("\n");
	assert.equal(lines[0], "---");
	assert.equal(lines[5], "---");
	assert.equal(lines[1], "name: skua-quirky");
	assert.equal(lines[3], "model: skua/quirky");
	assert.equal(lines[4], "tools: []");
	assert.doesNotMatch(text, /[\u2028\u2029\u0085]/);
	assert.equal(
		decodeYamlDoubleQuoted(lines[2]),
		`Remote skua agent, profile quirky: ${p.description} Models: m/one, m/two. Runs in the skua cluster on the pushed HEAD of origin and returns a report or schema-checked JSON; it may push skua/<run> and open a PR.`,
	);
});

test("a profile name that is not DNS-1123 is skipped and reported", () => {
	const { agentsDir } = dirs();
	const profiles: Profile[] = [
		{ name: "ok-1", description: "d", models: [], ready: true },
		{ name: "Bad_Name", description: "d", models: [], ready: true },
		{ name: "../escape", description: "d", models: [], ready: true },
		{ name: "off", description: "d", models: [], ready: false },
	];
	assert.deepEqual(syncAgentFiles(agentsDir, profiles), { written: ["skua-ok-1.md"], removed: [], unchanged: [], skipped: ["Bad_Name", "../escape"] });
	assert.deepEqual(readdirSync(agentsDir), ["skua-ok-1.md"]);
});

test("a fetch failure keeps the previous cache bytes and agent files", async () => {
	const api = await startFakeApi();
	const { agentDir, agentsDir } = dirs();
	try {
		assert.ok((await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT })).ok);
	} finally {
		await api.close();
	}
	const before = snapshot(agentDir, agentsDir);
	const res = await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT });
	assert.equal(res.ok, false);
	assert.ok(!res.ok && res.error instanceof Error);
	assert.deepEqual(snapshot(agentDir, agentsDir), before);
});

test("a slow /profiles past timeoutMs returns a timeout error and keeps cache and files", async () => {
	const fast = await startFakeApi();
	const { agentDir, agentsDir } = dirs();
	try {
		assert.ok((await syncProfiles({ api: source(fast), agentDir, agentsDir, endpoint: ENDPOINT })).ok);
	} finally {
		await fast.close();
	}
	const before = snapshot(agentDir, agentsDir);
	const slow = await startFakeApi({ profilesDelayMs: 2000 });
	try {
		slow.setProfile({ name: "rust", ready: true });
		const started = Date.now();
		const res = await syncProfiles({ api: source(slow), agentDir, agentsDir, endpoint: ENDPOINT, timeoutMs: 200 });
		const elapsed = Date.now() - started;
		assert.ok(!res.ok);
		assert.equal(res.error.name, "TimeoutError");
		assert.ok(elapsed >= 150 && elapsed < 1500, `returned after ${elapsed} ms`);
		assert.deepEqual(snapshot(agentDir, agentsDir), before);
	} finally {
		await slow.close();
	}
});

test("the cache reads as empty when missing, corrupt or for another endpoint", async () => {
	const api = await startFakeApi();
	try {
		const { agentDir, agentsDir } = dirs();
		assert.deepEqual(readCachedProfiles(agentDir, ENDPOINT), []);
		assert.ok((await syncProfiles({ api: source(api), agentDir, agentsDir, endpoint: ENDPOINT })).ok);
		assert.equal(readCachedProfiles(agentDir, ENDPOINT).length, 2);
		assert.deepEqual(readCachedProfiles(agentDir, "https://other.test"), []);
		writeFileSync(cachePath(agentDir), "{not json");
		assert.deepEqual(readCachedProfiles(agentDir, ENDPOINT), []);
		writeFileSync(cachePath(agentDir), JSON.stringify({ endpoint: ENDPOINT, fetchedAt: "x", profiles: [{ name: 1 }] }));
		assert.deepEqual(readCachedProfiles(agentDir, ENDPOINT), []);
	} finally {
		await api.close();
	}
});

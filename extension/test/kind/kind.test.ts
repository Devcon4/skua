import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { existsSync, statSync } from "node:fs";
import { mkdir, readFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { saveConfig } from "../../src/config.ts";
import { startOmp } from "../rpc/driver.ts";
import type { Omp } from "../rpc/driver.ts";
import {
	CLIENT_ID,
	batch,
	childMessages,
	childSessionId,
	command,
	createHarness,
	messageText,
	prompt,
	resultOutput,
	sessionFileOf,
	stepIds,
	taskResults,
	waitFor,
} from "../rpc/harness.ts";

const KUBECONFIG = resolve(process.env.KUBECONFIG || "/tmp/skua-dev.kubeconfig");
const ENDPOINT = "http://127.0.0.1:32080";
const ISSUER = "http://dex.127.0.0.1.nip.io:32000";
const ISSUER_HOST = new URL(ISSUER).hostname;
const LOOPBACK_PORT = 38080;
const SCOPES = ["openid", "profile", "email", "offline_access", "groups"];
const RUN_NAMESPACE = "skua";
const FIXTURES = fileURLToPath(new URL("../../../test/e2e/fixtures/", import.meta.url));
const DEX_BROWSER = fileURLToPath(new URL("./dex-browser.ts", import.meta.url));
const STEP_ID = /^skua_([a-z0-9-]+)_\d+$/;

function kubectl(...args: string[]): Promise<string> {
	const { promise, resolve: done, reject } = Promise.withResolvers<string>();
	execFile("kubectl", ["--kubeconfig", KUBECONFIG, ...args], { timeout: 330_000 }, (err, stdout, stderr) =>
		err ? reject(new Error(`kubectl ${args.join(" ")}: ${err.message}\n${stderr}`)) : done(stdout),
	);
	return promise;
}

async function ensureFakeProfile(): Promise<void> {
	try {
		await kubectl("get", "agentprofile", "fake");
		return;
	} catch {
		await kubectl("apply", "-f", FIXTURES);
	}
	await kubectl("wait", "--for=condition=Ready", "agentprofile/fake", "--timeout=300s");
}

function jwtClaims(jwt: string): Record<string, unknown> {
	const claims: unknown = JSON.parse(Buffer.from(jwt.split(".")[1] ?? "", "base64url").toString("utf8"));
	assert.ok(typeof claims === "object" && claims !== null && !Array.isArray(claims), "the id_token payload is a JSON object");
	return claims as Record<string, unknown>;
}

async function storedIdToken(agentDir: string): Promise<string> {
	const stored: unknown = JSON.parse(await readFile(join(agentDir, "skua", "token.json"), "utf8"));
	assert.ok(typeof stored === "object" && stored !== null && "idToken" in stored && typeof stored.idToken === "string", "token.json holds an idToken");
	return stored.idToken;
}

async function runOf(omp: Omp, id: string): Promise<string> {
	return waitFor(
		`the first skua_step of ${id}`,
		async () => {
			const messages = await childMessages(omp, id).catch((err: unknown) => {
				if (err instanceof Error && err.message.includes("Unknown subagent")) return undefined;
				throw err;
			});
			if (messages === undefined) return undefined;
			const [first] = stepIds(messages);
			return first === undefined ? undefined : STEP_ID.exec(first)?.[1];
		},
		60_000,
		500,
	);
}

test("kind: Dex login, a relayed fake run, a steer and /skua abort against the real stack", { timeout: 240_000 }, async () => {
	assert.notEqual(KUBECONFIG, join(homedir(), ".kube", "config"), "set KUBECONFIG to the kind cluster's own file, not ~/.kube/config");
	assert.ok(existsSync(KUBECONFIG), `KUBECONFIG ${KUBECONFIG} does not exist; bring up the kind environment first`);
	const profileReady = ensureFakeProfile();
	const h = await createHarness({ loggedIn: false, task: { agentIdleTtlMs: 600_000 } });
	let omp: Omp | undefined;
	try {
		await profileReady;
		saveConfig(h.agentDir, { endpoint: ENDPOINT, issuer: ISSUER, clientId: CLIENT_ID, loopbackPort: LOOPBACK_PORT, scopes: SCOPES });
		const cwd = join(h.root, "no-git");
		await mkdir(cwd);
		const noProxy = `127.0.0.1,localhost,${ISSUER_HOST}`;
		omp = startOmp({
			packageDir: h.packageDir,
			agentDir: h.agentDir,
			cwd,
			env: { BROWSER: `node ${DEX_BROWSER}`, KUBECONFIG, NO_PROXY: noProxy, no_proxy: noProxy },
		});
		await omp.waitFrame(f => f.type === "ready", 30_000);
		await omp.data("set_subagent_subscription", { level: "progress" });

		const notes = await command(omp, "/skua login", 60_000);
		assert.ok(
			notes.some(n => n.startsWith(`skua: logged in as dev@skua.test at ${ENDPOINT}; profiles: `) && /\bfake\b/.test(n)),
			JSON.stringify(notes),
		);
		assert.equal(statSync(join(h.agentDir, "skua", "token.json")).mode & 0o777, 0o600);
		const idToken = await storedIdToken(h.agentDir);
		const claims = jwtClaims(idToken);
		assert.equal(claims.iss, ISSUER);
		const aud = typeof claims.aud === "string" ? [claims.aud] : claims.aud;
		assert.ok(Array.isArray(aud) && aud.includes(CLIENT_ID), `aud ${JSON.stringify(claims.aud)} contains ${CLIENT_ID}`);
		assert.ok(Array.isArray(claims.groups), `groups claim present: ${JSON.stringify(claims.groups)}`);
		assert.ok(!claims.groups.includes("skua-admins"), `groups ${JSON.stringify(claims.groups)} exclude skua-admins`);
		const profiles = await fetch(`${ENDPOINT}/v1alpha1/profiles`, { headers: { authorization: `Bearer ${idToken}` } });
		await profiles.arrayBuffer();
		assert.equal(profiles.status, 200);
		assert.ok(existsSync(join(h.agentsDir, "skua-fake.md")), "profile sync wrote skua-fake.md");

		const mark = await prompt(
			omp,
			batch(
				[
					{ agent: "skua-fake", name: "KindSteps", task: "SCRIPT:steps 3 2s" },
					{ agent: "skua-fake", name: "KindSteer", task: "SCRIPT:steps 10 2s" },
				],
				"skua kind end-to-end test.",
			),
		);
		const steered = await runOf(omp, "KindSteer");
		const steer = await omp.cmd("steer_subagent", { subagentId: "KindSteer", message: "kind-steer-1" });
		assert.equal(steer.success, true, JSON.stringify(steer));
		await waitFor(
			"steer-received: kind-steer-1 in KindSteer's history",
			async () => ((await childMessages(omp as Omp, "KindSteer")).some(m => messageText(m).includes("steer-received: kind-steer-1")) ? true : undefined),
			30_000,
			500,
		);
		const aborted = await command(omp, `/skua abort ${steered}`);
		assert.ok(aborted.includes(`skua abort ${steered}: requested`), JSON.stringify(aborted));
		await waitFor(
			`AgentRun ${steered} to end Failed/Cancelled`,
			async () => ((await kubectl("-n", RUN_NAMESPACE, "get", "agentrun", steered, "-o", "jsonpath={.status.phase}/{.status.reason}")) === "Failed/Cancelled" ? true : undefined),
			30_000,
			500,
		);

		await omp.waitSettled(90_000, mark);
		const [stepsResult] = taskResults(omp, "KindSteps", mark);
		assert.ok(stepsResult, "the parent received KindSteps' task result");
		assert.notEqual(resultOutput(stepsResult).trim(), "");
		assert.ok(stepIds(await childMessages(omp, "KindSteps")).length >= 1, "KindSteps' history holds a skua_step call");

		const sessionIds = await Promise.all(["KindSteps", "KindSteer"].map(id => childSessionId(sessionFileOf(omp as Omp, id))));
		const keys = (await kubectl("-n", RUN_NAMESPACE, "get", "agentruns", "-o", 'jsonpath={range .items[*]}{.spec.clientKey}{"\\n"}{end}'))
			.split("\n")
			.filter(k => k !== "");
		const ours = sessionIds.map(sid => keys.filter(k => k.startsWith(`${sid}:`)));
		assert.deepEqual(
			ours.map(k => k.length),
			[1, 1],
			`one AgentRun per child: ${JSON.stringify(ours)}`,
		);
	} finally {
		await omp?.kill();
		await h.close();
	}
});

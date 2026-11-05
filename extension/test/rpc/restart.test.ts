import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import type { Omp } from "./driver.ts";
import type { Harness } from "./harness.ts";
import { batch, childMessages, command, createHarness, promptSettled, resultOutput, stepIds, taskResults, waitFor, writeAgent } from "./harness.ts";

const REPORT_RESULT = "Fixed org/test#142. Findings are in docs/findings.md on the branch.";

function eventRequests(h: Harness, run: string): (string | undefined)[] {
	return h.api.requests().filter(r => r.method === "GET" && r.path === `/v1alpha1/runs/${run}/events`).map(r => r.lastEventId);
}

async function committedStepSeqs(sessionFile: string, run: string): Promise<number[]> {
	const pattern = new RegExp(`"toolCallId":"skua_${run}_(\\d+)"`, "g");
	return [...(await readFile(sessionFile, "utf8")).matchAll(pattern)].map(m => Number(m[1]));
}

async function assertStepsOnce(omp: Omp, id: string, run: string, steps: number): Promise<void> {
	const ids = stepIds(await childMessages(omp, id));
	assert.equal(ids.length, steps, JSON.stringify(ids));
	assert.equal(new Set(ids).size, steps, JSON.stringify(ids));
	for (const s of ids) assert.match(s, new RegExp(`^skua_${run}_\\d+$`));
}

async function reportOnce(h: Harness, scenario: string, name: string, apiTimeoutMs = 60_000): Promise<{ omp: Omp; mark: number }> {
	const omp = await h.start();
	await command(omp, "/skua sync");
	const mark = await promptSettled(omp, batch([{ agent: "skua-generic", name, task: `SCENARIO:${scenario} Fix org/test#142.` }]), apiTimeoutMs);
	return { omp, mark };
}

test("after SIGKILL and --resume a parent message reattaches the child to its run from the last step", { timeout: 240_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 2000, steps: 6 } });
	try {
		const first = await h.start();
		await command(first, "/skua sync");
		const parent = (await first.data<{ sessionId: string }>("get_state")).sessionId;
		await first.data("prompt", { message: batch([{ agent: "skua-generic", name: "RemoteM3", task: "SCENARIO:steps Fix org/test#142." }]) });
		const row = await first.waitSubagent("RemoteM3", "running", 20_000);
		assert.ok(row.sessionFile);
		const [run] = await waitFor("two relayed steps", async () => {
			const r = h.runs()[0];
			return r !== undefined && (await committedStepSeqs(row.sessionFile ?? "", r.name)).length >= 2 ? [r] : undefined;
		}, 30_000);
		await first.kill("SIGKILL");
		const committed = Math.max(...(await committedStepSeqs(row.sessionFile, run.name)));
		const before = eventRequests(h, run.name).length;

		const second = await h.start({ args: ["--resume", parent] });
		assert.equal((await second.data<{ sessionId: string }>("get_state")).sessionId, parent);
		await promptSettled(second, writeAgent("RemoteM3", "STEER-M3: also check pkg9"), 120_000);
		await waitFor("the run to finish", () => h.runs().find(r => r.phase === "Succeeded"), 60_000);

		assert.equal(h.runs().length, 1);
		const [after] = h.runs();
		assert.deepEqual(after.steers.map(s => s.message), ["STEER-M3: also check pkg9"]);
		const resumed = eventRequests(h, run.name).slice(before);
		assert.ok(resumed.length >= 1, "an events request after the restart");
		assert.equal(resumed[0], String(committed));
		await assertStepsOnce(second, "RemoteM3", run.name, 6);
	} finally {
		await h.close();
	}
});

test("a create answered with 503 is retried into the same run", { timeout: 120_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 300 } });
	try {
		const { omp, mark } = await reportOnce(h, "flaky-create", "RemoteFlaky");
		assert.equal(h.runs().length, 1);
		assert.ok(h.api.requests().filter(r => r.method === "POST" && r.path === "/v1alpha1/runs").length >= 2);
		const [result] = taskResults(omp, "RemoteFlaky", mark);
		assert.ok(resultOutput(result).includes(REPORT_RESULT), result);
	} finally {
		await h.close();
	}
});

test("a stream without heartbeats is reopened from the last event and repeats no step", { timeout: 120_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 500, steps: 4, gapMs: 4000, hbMs: 1000 }, heartbeatTimeoutMs: 1500 });
	try {
		const { omp, mark } = await reportOnce(h, "nohb", "RemoteNohb");
		const [run] = h.runs();
		const requests = eventRequests(h, run.name);
		assert.ok(requests.length >= 2, JSON.stringify(requests));
		assert.ok(requests.slice(1).every(id => id !== undefined && Number(id) > 0), JSON.stringify(requests));
		const [result] = taskResults(omp, "RemoteNohb", mark);
		assert.ok(resultOutput(result).includes(REPORT_RESULT), result);
		await assertStepsOnce(omp, "RemoteNohb", run.name, 4);
	} finally {
		await h.close();
	}
});

test("a skua-api restart mid-run ends the child with the run's real result and no repeated step", { timeout: 120_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 1500, steps: 4 } });
	try {
		const omp = await h.start();
		await command(omp, "/skua sync");
		const mark = omp.mark();
		await omp.data("prompt", { message: batch([{ agent: "skua-generic", name: "RemoteRestart", task: "SCENARIO:steps Fix org/test#142." }]) });
		const run = await waitFor("the first step", () => h.runs().find(r => r.lastSeq >= 4), 30_000);
		const restarted = await fetch(`${h.api.url}/admin/restart`, { method: "POST" });
		assert.equal(restarted.status, 200);
		await omp.waitSettled(60_000, mark);
		assert.equal(h.runs().length, 1);
		const [result] = taskResults(omp, "RemoteRestart", mark);
		assert.ok(resultOutput(result).includes(REPORT_RESULT), result);
		assert.ok(resultOutput(result).includes(`Branch: skua/${run.name}`), result);
		assert.ok(eventRequests(h, run.name).length >= 2);
		await assertStepsOnce(omp, "RemoteRestart", run.name, 4);
	} finally {
		await h.close();
	}
});

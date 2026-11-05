import assert from "node:assert/strict";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import type { Omp } from "./driver.ts";
import type { Harness } from "./harness.ts";
import { batch, childMessages, childSessionId, command, createHarness, messageText, promptSettled, sessionFileOf, writeAgent } from "./harness.ts";

const REPORT_RESULT = "Fixed org/test#142. Findings are in docs/findings.md on the branch.";
const PARK_TTL_MS = 3000;

async function followUp(h: Harness, omp: Omp, sid: string, n: number, text: string): Promise<{ receipt: string; delivered: string }> {
	const before = h.runs().length;
	const mark = await promptSettled(omp, writeAgent("RemoteFollow", text), 60_000);
	const runs = h.runs();
	assert.equal(runs.length, before + 1);
	const run = runs.find(r => r.clientKey === `${sid}:${n}`);
	assert.ok(run, `run ${sid}:${n} in ${JSON.stringify(runs.map(r => r.clientKey))}`);
	const previous = runs.find(r => r.clientKey === `${sid}:${n - 1}`);
	assert.ok(previous);
	assert.ok(run.prompt.includes(REPORT_RESULT), run.prompt);
	assert.ok(run.prompt.includes(`skua/${previous.name}`), run.prompt);
	assert.ok(run.prompt.includes(text), run.prompt);
	assert.equal(run.phase, "Succeeded");

	const frames = omp.frames.slice(mark).filter(f => f.type === "message_end").map(f => JSON.stringify(f.message));
	const receipt = /"outcome":"([a-z]+)"/.exec(frames.find(f => f.includes('"toolName":"write"')) ?? "")?.[1] ?? "none";
	const delivered = frames.filter(f => f.includes("agent://RemoteFollow") && f.includes(`Branch: skua/${run.name}`));
	assert.equal(delivered.length, 1, frames.join("\n"));
	return { receipt, delivered: delivered[0] };
}

test("a message after a finished run starts one follow-up run, also after the child parked", { timeout: 180_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 300 }, task: { agentIdleTtlMs: PARK_TTL_MS } });
	try {
		const omp = await h.start();
		await command(omp, "/skua sync");
		await promptSettled(omp, batch([{ agent: "skua-generic", name: "RemoteFollow", task: "SCENARIO:report Fix org/test#142." }]), 60_000);
		const sid = await childSessionId(sessionFileOf(omp, "RemoteFollow"));
		assert.deepEqual(h.runs().map(r => r.clientKey), [`${sid}:0`]);

		const first = await followUp(h, omp, sid, 1, "FOLLOWUP-1: now also update the changelog");
		assert.equal(first.receipt, "woken");
		assert.ok(first.delivered.includes(REPORT_RESULT));

		const branches = (n: number) => h.runs().slice(0, n).map(r => `Branch: skua/${r.name}`);
		const finals = async () => (await childMessages(omp, "RemoteFollow")).filter(m => m.role === "assistant" && messageText(m).includes("Branch: skua/")).map(m => messageText(m));
		assert.deepEqual((await finals()).map(t => t.split("\n").find(l => l.startsWith("Branch: "))), branches(2));

		// omp emits no RPC frame when a child parks, so wait out agentIdleTtlMs.
		await delay(PARK_TTL_MS + 1500);
		assert.deepEqual(await omp.subagents(), []);
		const second = await followUp(h, omp, sid, 2, "FOLLOWUP-2: and bump the version");
		assert.equal(second.receipt, "revived");
		assert.ok(second.delivered.includes(REPORT_RESULT));
		assert.deepEqual((await finals()).map(t => t.split("\n").find(l => l.startsWith("Branch: "))), branches(3));
	} finally {
		await h.close();
	}
});

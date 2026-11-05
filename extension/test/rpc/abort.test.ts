import assert from "node:assert/strict";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import type { FakeRunSnapshot } from "../fake/api.ts";
import type { Omp } from "./driver.ts";
import type { Harness } from "./harness.ts";
import { abortRequests, batch, childMessages, command, createHarness, lifecycle, messageText, prompt, promptSettled, waitFor } from "./harness.ts";

async function startRemote(h: Harness, omp: Omp, name: string): Promise<FakeRunSnapshot> {
	const known = new Set(h.runs().map(r => r.name));
	await prompt(omp, batch([{ agent: "skua-generic", name, task: "SCENARIO:steps Fix org/test#142." }]));
	await omp.waitSubagent(name, "running", 20_000);
	return waitFor(`the first step of ${name}'s run`, () => h.runs().find(r => !known.has(r.name) && r.lastSeq >= 3), 20_000);
}

async function assertStillRunning(h: Harness, run: FakeRunSnapshot): Promise<void> {
	const before = h.runs().find(r => r.name === run.name)?.lastSeq ?? 0;
	await delay(1500);
	const after = h.runs().find(r => r.name === run.name);
	assert.equal(after?.phase, "Running");
	assert.ok((after?.lastSeq ?? 0) > before, `run ${run.name} kept producing events`);
	assert.equal(abortRequests(h, run.name), 0);
}

test("cancel_subagent and a parent abort detach from the run; /skua abort cancels it", { timeout: 180_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 500, steps: 60 } });
	try {
		const omp = await h.start();
		await command(omp, "/skua sync");

		const killed = await startRemote(h, omp, "RemoteKill");
		const cancel = await omp.cmd("cancel_subagent", { subagentId: "RemoteKill" });
		assert.equal(cancel.success, true, JSON.stringify(cancel));
		await waitFor("RemoteKill to stop", () => (lifecycle(omp, "RemoteKill").some(s => s !== "started") ? true : undefined), 15_000);
		await assertStillRunning(h, killed);

		const escaped = await startRemote(h, omp, "RemoteEsc");
		const abort = await omp.cmd("abort");
		assert.equal(abort.success, true, JSON.stringify(abort));
		await assertStillRunning(h, escaped);

		const notes = await command(omp, `/skua abort ${escaped.name}`);
		assert.ok(notes.includes(`skua abort ${escaped.name}: requested`), JSON.stringify(notes));
		const cancelled = await waitFor("the run to end", () => h.runs().find(r => r.name === escaped.name && r.phase !== "Running"), 15_000);
		assert.equal(cancelled.aborted, true);
		assert.equal(`${cancelled.phase}/${cancelled.reason}`, "Failed/Cancelled");
		assert.equal(abortRequests(h, escaped.name), 1);
	} finally {
		await h.close();
	}
});

test("closing omp's stdin leaves the run going", { timeout: 120_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 500, steps: 60 } });
	try {
		const omp = await h.start();
		await command(omp, "/skua sync");
		const run = await startRemote(h, omp, "RemoteEof");
		omp.closeStdin();
		await Promise.race([omp.exited, delay(20_000).then(() => omp.kill())]);
		await assertStillRunning(h, run);
	} finally {
		await h.close();
	}
});

test("a failed run ends the child with one final message and a completed task", { timeout: 120_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 300 } });
	try {
		const omp = await h.start();
		await command(omp, "/skua sync");
		await promptSettled(omp, batch([{ agent: "skua-generic", name: "RemoteFail", task: "SCENARIO:fail Fix org/test#142." }]), 60_000);

		const [run] = h.runs();
		assert.equal(h.runs().length, 1);
		assert.equal(h.api.requests().filter(r => r.method === "POST" && r.path === "/v1alpha1/runs").length, 1);
		const expected = `Remote run ${run.name} failed: BudgetExceeded.`;
		const finals = (await childMessages(omp, "RemoteFail")).filter(m => m.role === "assistant" && messageText(m).includes(expected));
		assert.equal(finals.length, 1);
		assert.equal(finals[0].stopReason, "stop");
		assert.deepEqual(lifecycle(omp, "RemoteFail"), ["started", "completed"]);
	} finally {
		await h.close();
	}
});

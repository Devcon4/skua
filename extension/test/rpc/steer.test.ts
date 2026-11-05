import assert from "node:assert/strict";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { batch, childMessages, command, createHarness, prompt, stepIds, toolCalls, waitFor, writeAgent } from "./harness.ts";

test("Agent Hub steer and a parent agent:// write reach the remote run as bare text within one step", { timeout: 180_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 2000, steps: 6 } });
	try {
		const omp = await h.start();
		await command(omp, "/skua sync");
		const mark = await prompt(omp, batch([{ agent: "skua-golang", name: "RemoteSteer", task: "SCENARIO:steps Fix org/test#142." }]));
		await omp.waitSubagent("RemoteSteer", "running", 20_000);
		const run = await waitFor("the run's first step", () => h.runs().find(r => r.lastSeq >= 3), 20_000);

		await delay(1000);
		const steer = await omp.cmd("steer_subagent", { subagentId: "RemoteSteer", message: "STEER-B: also check pkg9" });
		assert.equal(steer.success, true, JSON.stringify(steer));
		await waitFor("the steer on the run", () => h.runs().find(r => r.name === run.name && r.steers.length === 1), 15_000);

		await prompt(omp, writeAgent("RemoteSteer", "STEER-M: also check pkg7"));
		await waitFor("the IRC message on the run", () => h.runs().find(r => r.name === run.name && r.steers.length === 2), 15_000);

		await omp.waitSettled(120_000, mark);
		const [final] = h.runs().filter(r => r.name === run.name);
		assert.equal(final.phase, "Succeeded");
		assert.deepEqual(final.steers.map(s => s.message), ["STEER-B: also check pkg9", "STEER-M: also check pkg7"]);
		for (const s of final.steers) {
			assert.ok(s.appliedAtStep !== null, `steer ${s.message} was applied`);
			assert.ok(s.appliedAtStep - s.receivedAtStep <= 1, `steer ${s.message} received at ${s.receivedAtStep}, applied at ${s.appliedAtStep}`);
		}

		const messages = await childMessages(omp, "RemoteSteer");
		const steps = messages.filter(m => toolCalls(m).some(c => c.name === "skua_step"));
		assert.equal(steps.length, 6);
		for (const m of steps) {
			const calls = toolCalls(m);
			assert.equal(calls.length, 1);
			assert.match(calls[0].id, new RegExp(`^skua_${run.name}_\\d+$`));
			assert.equal(messages.filter(r => r.role === "toolResult" && r.toolCallId === calls[0].id).length, 1);
		}
		assert.equal(new Set(stepIds(messages)).size, 6);
	} finally {
		await h.close();
	}
});

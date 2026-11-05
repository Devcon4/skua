import assert from "node:assert/strict";
import { test } from "node:test";
import type { Subagent } from "./driver.ts";
import { batch, childSessionId, command, createHarness, prompt, waitFor } from "./harness.ts";

test("one task call with 5 remote and 2 local items creates 5 runs with the batch context and the pushed HEAD", { timeout: 180_000 }, async () => {
	const h = await createHarness({ api: { stepMs: 1500 } });
	try {
		const omp = await h.start();
		await command(omp, "/skua sync");
		const remote = [0, 1, 2, 3, 4].map(i => ({
			agent: i % 2 === 0 ? "skua-generic" : "skua-golang",
			name: `Remote${i}`,
			task: `SCENARIO:report Unit ${i}: fix org/test#142.`,
		}));
		const local = [
			{ agent: "scout", name: "LocalScout", task: "Report the first line of README.md." },
			{ agent: "task", name: "LocalTask", task: "Reply with LOCAL-OK." },
		];
		const mark = await prompt(omp, batch([...remote, ...local]));
		const rows = new Map<string, Subagent>();
		await waitFor("7 subagent rows", async () => {
			for (const s of await omp.subagents()) rows.set(s.id, s);
			return rows.size >= 7 ? true : undefined;
		}, 20_000, 100);
		assert.deepEqual(
			[...rows.values()].map(r => [r.id, r.agent]).sort(),
			[...remote, ...local].map(t => [t.name, t.agent]).sort(),
		);
		await omp.waitSettled(120_000, mark);
		const runs = h.runs();
		assert.equal(runs.length, 5);
		for (const item of remote) {
			const row = rows.get(item.name);
			assert.ok(row);
			const sid = await childSessionId(row.sessionFile);
			const run = runs.find(r => r.clientKey === `${sid}:0`);
			assert.ok(run, `run for ${item.name} with clientKey ${sid}:0 in ${JSON.stringify(runs.map(r => r.clientKey))}`);
			assert.equal(run.profile, item.agent.slice("skua-".length));
			assert.ok(run.prompt.includes("Shared batch context: repo org/test, issue 142."), run.prompt);
			assert.ok(run.prompt.includes(item.task), run.prompt);
			assert.deepEqual(run.workspace, { repo: "org/test", baseRef: h.headSha });
		}
	} finally {
		await h.close();
	}
});

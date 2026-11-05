import assert from "node:assert/strict";
import { existsSync, statSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test } from "node:test";
import { CLIENT_ID, command, createHarness, prompt } from "./harness.ts";

const BROWSER_SCRIPT = `const res = await fetch(process.argv[2], { redirect: "manual" });
const location = res.headers.get("location");
if (location === null) process.exit(1);
process.exit((await fetch(location)).ok ? 0 : 1);
`;

test("/skua login with a browser stores a private token and syncs profiles", { timeout: 120_000 }, async () => {
	const h = await createHarness({ loggedIn: false });
	try {
		const browser = join(h.root, "browser.mjs");
		await writeFile(browser, BROWSER_SCRIPT);
		const omp = await h.start({ env: { BROWSER: `node ${browser}` } });
		const notes = await command(omp, `/skua login ${h.api.url} ${h.issuer.url} ${CLIENT_ID}`, 60_000);
		assert.ok(notes.includes(`skua: logged in as dev@example.test at ${h.api.url}; profiles: generic, golang`), JSON.stringify(notes));
		const token = join(h.agentDir, "skua", "token.json");
		assert.equal(statSync(token).mode & 0o777, 0o600);
		assert.ok(existsSync(join(h.agentsDir, "skua-generic.md")) && existsSync(join(h.agentsDir, "skua-golang.md")));
		const status = await command(omp, "/skua status");
		assert.ok(status.some(n => n.includes("fake-user")), JSON.stringify(status));
	} finally {
		await h.close();
	}
});

test("/skua login falls back to the device flow when no browser starts", { timeout: 120_000 }, async () => {
	const h = await createHarness({ loggedIn: false, issuer: { interval: 1 } });
	try {
		const omp = await h.start({ env: { BROWSER: "false" } });
		const mark = await prompt(omp, `/skua login ${h.api.url} ${h.issuer.url} ${CLIENT_ID}`);
		const notice = await omp.waitFrame(f => f.type === "extension_ui_request" && f.method === "notify" && /confirm the code [A-Z]+-[A-Z]+/.test(String(f.message)), 30_000, mark);
		const code = /confirm the code ([A-Z]+-[A-Z]+)/.exec(String(notice.message))?.[1];
		assert.ok(code);
		h.issuer.approveDevice(code);
		await omp.waitFrame(f => f.type === "prompt_result", 60_000, mark);
		const notes = omp.notifications();
		assert.ok(notes.includes(`skua: logged in as dev@example.test at ${h.api.url}; profiles: generic, golang`), JSON.stringify(notes));
		assert.ok(h.issuer.tokenRequests().some(r => r.grantType === "urn:ietf:params:oauth:grant-type:device_code" && r.status === 200));
		const status = await command(omp, "/skua status");
		assert.ok(status.some(n => n.includes("fake-user")), JSON.stringify(status));
	} finally {
		await h.close();
	}
});

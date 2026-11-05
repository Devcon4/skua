import type { HookAPI } from "@oh-my-pi/pi-coding-agent/extensibility/hooks";
import { readFileSync } from "node:fs";

// Read-gate, not a linter: the skill owns the rules and a human judges style at review.
const SKILL = "skill://gh-writing";
const SKILL_READ = /skill:\/\/gh-writing\b|gh-writing\/SKILL\.md/;
const GH_WRITE = /\bgh\s+(pr|issue)\s+(create|edit|comment)\b|\bgh\s+api\b[^|]*\/(pulls|issues)\b/;
const RUNS_FILE = /(?:^|[;&|]\s*)(?:bash|sh|zsh|node|python3?|bun|deno)\s+(\S+\.(?:sh|bash|mjs|cjs|js|ts|py))|(?:^|[;&|]\s*)(\.\/\S+)/;

export default function hook(pi: HookAPI): void {
	let hasRead = false;

	pi.on("tool_result", async (event) => {
		if (hasRead || event.isError) return;
		if (SKILL_READ.test(JSON.stringify(event.input ?? {}))) hasRead = true;
	});

	pi.on("tool_call", async (event) => {
		if (hasRead) return;

		let text =
			event.toolName === "bash"
				? String(event.input.command ?? "")
				: event.toolName === "eval"
					? String(event.input.code ?? "")
					: "";
		if (!text) return;

		// `bash open-pr.sh` hides the gh call from the command string, so check the script body.
		if (!GH_WRITE.test(text)) {
			const script = text.match(RUNS_FILE)?.[1] ?? text.match(RUNS_FILE)?.[2];
			if (!script) return;
			try {
				text = readFileSync(script.replace(/^['"]|['"]$/g, ""), "utf8");
			} catch {
				return;
			}
			if (!GH_WRITE.test(text)) return;
		}

		return {
			block: true,
			reason:
				`gh-writing: read ${SKILL} before writing to GitHub.\n\n` +
				"It defines the blocks a PR or issue body uses, which blocks each change type " +
				"takes, the voice rules, and when a thing should be an issue at all.",
		};
	});
}

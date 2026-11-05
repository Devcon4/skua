import { execFileSync } from "node:child_process";

const LOGIN = "dev@skua.test";
const MAX_HOPS = 10;
const REQUEST_TIMEOUT_MS = 15_000;

const cookies = new Map<string, string>();

function devPassword(): string {
	const kubeconfig = process.env.KUBECONFIG;
	if (kubeconfig === undefined || kubeconfig === "") throw new Error("KUBECONFIG is not set");
	const encoded = execFileSync("kubectl", ["--kubeconfig", kubeconfig, "-n", "dex", "get", "secret", "dex-test-users", "-o", "jsonpath={.data.dev-password}"], {
		encoding: "utf8",
		stdio: ["ignore", "pipe", "inherit"],
	});
	const password = Buffer.from(encoded.trim(), "base64").toString("utf8");
	if (password === "") throw new Error("Secret dex/dex-test-users has no dev-password");
	return password;
}

async function send(url: string, form?: URLSearchParams): Promise<Response> {
	const headers: Record<string, string> = {};
	if (cookies.size > 0) headers.cookie = [...cookies].map(([k, v]) => `${k}=${v}`).join("; ");
	const res = await fetch(url, {
		method: form === undefined ? "GET" : "POST",
		headers,
		body: form,
		redirect: "manual",
		signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
	});
	for (const c of res.headers.getSetCookie()) {
		const [pair] = c.split(";");
		const eq = pair.indexOf("=");
		if (eq > 0) cookies.set(pair.slice(0, eq).trim(), pair.slice(eq + 1).trim());
	}
	await res.arrayBuffer();
	return res;
}

async function follow(issuerOrigin: string, url: string, form?: URLSearchParams): Promise<{ url: string; res: Response }> {
	let current = url;
	let res = await send(current, form);
	for (let hop = 0; res.status >= 300 && res.status < 400; hop++) {
		const location = res.headers.get("location");
		if (location === null || hop >= MAX_HOPS) throw new Error(`redirect without a usable location at ${current} (status ${res.status})`);
		current = new URL(location, current).href;
		if (new URL(current).origin !== issuerOrigin) return { url: current, res };
		res = await send(current);
	}
	return { url: current, res };
}

async function main(authorizeUrl: string | undefined): Promise<void> {
	if (authorizeUrl === undefined) throw new Error("usage: dex-browser.ts <authorize url>");
	const issuerOrigin = new URL(authorizeUrl).origin;
	const formPage = await follow(issuerOrigin, authorizeUrl);
	if (new URL(formPage.url).origin !== issuerOrigin || formPage.res.status !== 200) {
		throw new Error(`Dex did not show its login form: status ${formPage.res.status} at ${formPage.url}`);
	}
	const callback = await follow(issuerOrigin, formPage.url, new URLSearchParams({ login: LOGIN, password: devPassword() }));
	if (new URL(callback.url).origin === issuerOrigin) {
		throw new Error(`Dex refused the password login for ${LOGIN}: status ${callback.res.status} at ${callback.url}`);
	}
	const landed = await fetch(callback.url, { signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
	await landed.arrayBuffer();
	if (!landed.ok) throw new Error(`the login callback answered ${landed.status}`);
}

main(process.argv[2]).then(
	() => process.exit(0),
	(err: unknown) => {
		process.stderr.write(`dex-browser: ${err instanceof Error ? err.message : String(err)}\n`);
		process.exit(1);
	},
);

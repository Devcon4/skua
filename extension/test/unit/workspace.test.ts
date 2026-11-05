import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";
import { type GitExec, parseOrigin, resolveWorkspace } from "../../src/workspace.ts";

const exec: GitExec = (cmd, args, { cwd }) => {
  const { promise, resolve } = Promise.withResolvers<{ stdout: string; code: number }>();
  execFile(cmd, args, { cwd }, (err, stdout) => {
    resolve({ stdout, code: err === null ? 0 : typeof err.code === "number" ? err.code : -1 });
  });
  return promise;
};

async function git(cwd: string, ...args: string[]): Promise<string> {
  const r = await exec("git", args, { cwd });
  assert.equal(r.code, 0, `git ${args.join(" ")}`);
  return r.stdout.trim();
}

let root: string;
let n = 0;

async function repo(origin?: string): Promise<string> {
  const dir = join(root, `r${n++}`);
  await git(root, "init", "-q", "-b", "main", dir);
  await git(dir, "config", "user.email", "t@example.com");
  await git(dir, "config", "user.name", "t");
  await git(dir, "config", "commit.gpgsign", "false");
  await writeFile(join(dir, "a.txt"), "a\n");
  await git(dir, "add", "a.txt");
  await git(dir, "commit", "-q", "-m", "a");
  if (origin !== undefined) {
    await git(dir, "remote", "add", "origin", origin);
    await git(dir, "update-ref", "refs/remotes/origin/main", "HEAD");
  }
  return dir;
}

before(async () => {
  root = await mkdtemp(join(tmpdir(), "skua-ws-"));
});

after(async () => {
  await rm(root, { recursive: true, force: true });
});

describe("resolveWorkspace", () => {
  for (const [label, url, host] of [
    ["https", "https://github.com/org/test.git", "github.com"],
    ["scp-style", "git@ghe.example.com:org/test.git", "ghe.example.com"],
    ["ssh", "ssh://git@ghe.example.com:2222/org/test", "ghe.example.com"],
  ] as const) {
    test(`${label} origin with HEAD on origin/main is ok`, async () => {
      const dir = await repo(url);
      const sha = await git(dir, "rev-parse", "HEAD");
      assert.deepEqual(await resolveWorkspace(dir, exec), {
        kind: "ok",
        workspace: { repo: "org/test", baseRef: sha },
        host,
      });
    });
  }

  test("a local commit on top of origin/main is unpushed with that sha", async () => {
    const dir = await repo("https://github.com/org/test.git");
    await writeFile(join(dir, "b.txt"), "b\n");
    await git(dir, "add", "b.txt");
    await git(dir, "commit", "-q", "-m", "b");
    const sha = await git(dir, "rev-parse", "HEAD");
    assert.deepEqual(await resolveWorkspace(dir, exec), { kind: "unpushed", sha });
  });

  test("a repo without origin is none", async () => {
    assert.deepEqual(await resolveWorkspace(await repo(), exec), { kind: "none" });
  });

  test("a directory outside any repo is none", async () => {
    const dir = await mkdtemp(join(tmpdir(), "skua-norepo-"));
    try {
      assert.deepEqual(await resolveWorkspace(dir, exec), { kind: "none" });
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });

  test("an origin owner with an underscore fails the CRD pattern and is none", async () => {
    const dir = await repo("https://github.com/my_org/test.git");
    assert.deepEqual(await resolveWorkspace(dir, exec), { kind: "none" });
  });
});

describe("parseOrigin", () => {
  test("accepts the three origin shapes", () => {
    assert.deepEqual(parseOrigin("https://github.com/org/test"), { host: "github.com", repo: "org/test" });
    assert.deepEqual(parseOrigin("git@github.com:org/test.git"), { host: "github.com", repo: "org/test" });
    assert.deepEqual(parseOrigin("ssh://github.com/org/te.st.git"), { host: "github.com", repo: "org/te.st" });
  });

  test("rejects other shapes", () => {
    assert.equal(parseOrigin("/srv/git/test.git"), undefined);
    assert.equal(parseOrigin("https://github.com/org/sub/test.git"), undefined);
    assert.equal(parseOrigin("file:///srv/git/org/test.git"), undefined);
  });
});

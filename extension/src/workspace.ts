import type { Workspace } from "./wire.ts";

export type GitExec = (cmd: string, args: string[], options: { cwd: string }) => Promise<{ stdout: string; code: number }>;

export type ResolvedWorkspace = { kind: "ok"; workspace: Workspace; host: string };

export type WorkspaceResolution = { kind: "none" } | ResolvedWorkspace | { kind: "unpushed"; sha: string };

const REPO_PATTERN = /^[A-Za-z0-9-]+\/[A-Za-z0-9._-]+$/;
const ORIGIN_PATTERNS = [
  /^https:\/\/(?:[^@/]+@)?([^/:@]+)(?::\d+)?\/([^/]+)\/([^/]+?)(?:\.git)?\/?$/,
  /^ssh:\/\/(?:[^@/]+@)?([^/:@]+)(?::\d+)?\/([^/]+)\/([^/]+?)(?:\.git)?\/?$/,
  /^(?:[^@/:]+@)?([^/:@]+):([^/]+)\/([^/]+?)(?:\.git)?\/?$/,
];

export function parseOrigin(url: string): { host: string; repo: string } | undefined {
  for (const pattern of ORIGIN_PATTERNS) {
    const m = pattern.exec(url);
    if (m === null) continue;
    const repo = `${m[2]}/${m[3]}`;
    return REPO_PATTERN.test(repo) ? { host: m[1], repo } : undefined;
  }
  return undefined;
}

export async function resolveWorkspace(cwd: string, exec: GitExec): Promise<WorkspaceResolution> {
  const remote = await exec("git", ["remote", "get-url", "origin"], { cwd });
  if (remote.code !== 0) return { kind: "none" };
  const origin = parseOrigin(remote.stdout.trim());
  if (origin === undefined) return { kind: "none" };

  const head = await exec("git", ["rev-parse", "HEAD"], { cwd });
  const sha = head.stdout.trim();
  if (head.code !== 0 || sha === "") return { kind: "none" };

  const refs = await exec("git", ["for-each-ref", "--contains", "HEAD", "refs/remotes/origin"], { cwd });
  if (refs.code !== 0 || refs.stdout.trim() === "") return { kind: "unpushed", sha };
  return { kind: "ok", workspace: { repo: origin.repo, baseRef: sha }, host: origin.host };
}

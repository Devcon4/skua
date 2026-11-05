import {mkdirSync, readFileSync} from 'node:fs';
import {join} from 'node:path';
import type {ProviderModelConfig} from '@oh-my-pi/pi-coding-agent';
import {isProfile} from '../../contract/wire.ts';
import type {Profile} from '../../contract/wire.ts';
import {syncAgentFiles} from './agents.ts';
import type {AgentFilesResult} from './agents.ts';
import {writeAtomic} from './files.ts';

export interface ProfileSource {
  profiles(): Promise<Profile[]>;
}

export interface ProfileCache {
  endpoint: string;
  fetchedAt: string;
  profiles: Profile[];
}

export type SyncProfilesResult =
  | {ok: true; profiles: Profile[]; files: AgentFilesResult}
  | {ok: false; error: Error};

export interface SyncProfilesOptions {
  api: ProfileSource;
  agentDir: string;
  agentsDir: string;
  endpoint: string;
  timeoutMs?: number;
}

export function cachePath(agentDir: string): string {
  return join(agentDir, 'skua', 'profiles.json');
}

function isCache(v: unknown): v is ProfileCache {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return false;
  const c = v as Partial<Record<keyof ProfileCache, unknown>>;
  return (
    typeof c.endpoint === 'string' &&
    typeof c.fetchedAt === 'string' &&
    Array.isArray(c.profiles) &&
    c.profiles.every(isProfile)
  );
}

export function readCachedProfiles(
  agentDir: string,
  endpoint: string,
): Profile[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(cachePath(agentDir), 'utf8'));
  } catch {
    return [];
  }
  if (!isCache(parsed) || parsed.endpoint !== endpoint) return [];
  return parsed.profiles;
}

export function writeCachedProfiles(
  agentDir: string,
  endpoint: string,
  profiles: Profile[],
): void {
  const dir = join(agentDir, 'skua');
  mkdirSync(dir, {recursive: true, mode: 0o700});
  const cache: ProfileCache = {
    endpoint,
    fetchedAt: new Date().toISOString(),
    profiles,
  };
  writeAtomic(
    dir,
    'profiles.json',
    `${JSON.stringify(cache, null, 2)}\n`,
    0o600,
  );
}

function fetchWithTimeout(
  api: ProfileSource,
  timeoutMs: number,
): Promise<Profile[]> {
  const {promise: timedOut, reject} = Promise.withResolvers<never>();
  const signal = AbortSignal.timeout(timeoutMs);
  signal.addEventListener(
    'abort',
    () => {
      const err = new Error(
        `fetching profiles timed out after ${String(timeoutMs)} ms`,
      );
      err.name = 'TimeoutError';
      reject(err);
    },
    {once: true},
  );
  return Promise.race([api.profiles(), timedOut]);
}

export async function syncProfiles(
  opts: SyncProfilesOptions,
): Promise<SyncProfilesResult> {
  try {
    const profiles = await fetchWithTimeout(opts.api, opts.timeoutMs ?? 10000);
    if (!Array.isArray(profiles) || !profiles.every(isProfile))
      throw new Error('skua-api returned a malformed profile list');
    writeCachedProfiles(opts.agentDir, opts.endpoint, profiles);
    const files = syncAgentFiles(opts.agentsDir, profiles);
    return {ok: true, profiles, files};
  } catch (err) {
    return {
      ok: false,
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
}

export function readyModels(profiles: Profile[]): ProviderModelConfig[] {
  return profiles
    .filter(p => p.ready)
    .map(p => ({
      id: p.name,
      name: `skua ${p.name}`,
      reasoning: false,
      input: ['text'],
      cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0},
      contextWindow: 1000000,
      maxTokens: 64000,
    }));
}

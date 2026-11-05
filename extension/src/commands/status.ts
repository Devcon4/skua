import {readFileSync} from 'node:fs';
import {loadConfig} from '../support/auth/config.ts';
import type {SkuaConfig} from '../support/auth/config.ts';
import type {TokenStatus} from '../support/auth/token.ts';
import {cachePath, readCachedProfiles} from '../support/profiles/cache.ts';
import {errText} from './deps.ts';
import type {CommandDeps} from './deps.ts';
import {formatAge, liveLine} from './format.ts';

type ConfigState =
  | {kind: 'loaded'; cfg: SkuaConfig}
  | {kind: 'missing'}
  | {kind: 'unreadable'; reason: string};

function cacheFetchedAt(agentDir: string): number | undefined {
  try {
    const parsed: unknown = JSON.parse(
      readFileSync(cachePath(agentDir), 'utf8'),
    );
    if (
      typeof parsed !== 'object' ||
      parsed === null ||
      !('fetchedAt' in parsed) ||
      typeof parsed.fetchedAt !== 'string'
    )
      return undefined;
    const at = Date.parse(parsed.fetchedAt);
    return Number.isNaN(at) ? undefined : at;
  } catch {
    return undefined;
  }
}

function readConfigState(agentDir: string): ConfigState {
  try {
    const cfg = loadConfig(agentDir);
    if (cfg === undefined) return {kind: 'missing'};
    return {kind: 'loaded', cfg};
  } catch (err) {
    return {kind: 'unreadable', reason: errText(err)};
  }
}

function loginLine(st: TokenStatus): string {
  if (!st.loggedIn) return 'login: not logged in. Run /skua login.';
  const expiry =
    st.expiresAt === undefined
      ? 'unknown'
      : new Date(st.expiresAt * 1000).toISOString();
  const subject = `subject ${st.subject ?? 'unknown'}`;
  const who = st.email === undefined ? subject : `${st.email} (${subject})`;
  return `login: ${who}; token expires ${expiry}`;
}

function profileCacheLine(
  agentDir: string,
  endpoint: string,
  now: number,
): string {
  const count = readCachedProfiles(agentDir, endpoint).length;
  if (count === 0) return 'profiles: none cached';
  const fetchedAt = cacheFetchedAt(agentDir);
  const age =
    fetchedAt === undefined
      ? ''
      : `, fetched ${formatAge(now - fetchedAt)} ago`;
  return `profiles: ${String(count)} cached${age}`;
}

export function runStatus(deps: CommandDeps): void {
  const now = (deps.now ?? Date.now)();
  const omp = deps.omp.check.ok
    ? `omp: ${deps.omp.version}, in the tested range`
    : `omp: ${deps.omp.version}; ${deps.omp.check.message}`;
  const config = readConfigState(deps.agentDir);
  if (config.kind === 'unreadable') {
    deps.notify(
      ['skua status', `config: ${config.reason}`, omp].join('\n'),
      'warning',
    );
    return;
  }
  if (config.kind === 'missing') {
    deps.notify(
      [
        'skua status',
        'config: none. Run /skua login <endpoint> <issuer> <clientId>.',
        omp,
      ].join('\n'),
      'info',
    );
    return;
  }

  const {cfg} = config;
  const {tokens, relay} = deps.services(cfg);
  const login = loginLine(tokens.status());
  deps.notify(
    [
      'skua status',
      `endpoint: ${cfg.endpoint}`,
      `issuer: ${cfg.issuer}`,
      `client id: ${cfg.clientId}`,
      login,
      omp,
      profileCacheLine(deps.agentDir, cfg.endpoint, now),
      liveLine(relay.live()),
    ].join('\n'),
    'info',
  );
}

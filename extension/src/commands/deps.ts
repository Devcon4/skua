import {rmSync} from 'node:fs';
import type {Api} from '../integration/skuaapi/client.ts';
import {loadConfig} from '../support/auth/config.ts';
import type {SkuaConfig} from '../support/auth/config.ts';
import type {OpenUrl, login as oidcLogin} from '../support/auth/login.ts';
import {createTokenSource} from '../support/auth/token.ts';
import type {TokenSource} from '../support/auth/token.ts';
import {syncAgentFiles} from '../support/profiles/agents.ts';
import {cachePath} from '../support/profiles/cache.ts';
import type {SyncProfilesResult} from '../support/profiles/cache.ts';
import type {VersionCheck} from '../support/version.ts';

export type NotifyLevel = 'info' | 'warning' | 'error';

export interface LiveRun {
  sessionId: string;
  run: string;
  followUp: number;
}

export interface SkuaServices {
  tokens: TokenSource;
  api: Api;
  relay: {live(): LiveRun[]};
}

export interface CommandDeps {
  agentDir: string;
  agentsDir: string;
  omp: {version: string; check: VersionCheck};
  notify: (text: string, level: NotifyLevel) => void;
  services: (cfg: SkuaConfig) => SkuaServices;
  sync: (cfg: SkuaConfig) => Promise<SyncProfilesResult>;
  /** Starts the background profile refresh for this session unless it already runs. */
  startRefresh: (cfg: SkuaConfig) => void;
  openUrl: OpenUrl;
  login: typeof oidcLogin;
  now?: () => number;
}

const NOT_CONFIGURED =
  'skua: not configured. Run /skua login <endpoint> <issuer> <clientId>.';

export function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function readConfig(deps: CommandDeps): SkuaConfig | undefined {
  let cfg: SkuaConfig | undefined;
  try {
    cfg = loadConfig(deps.agentDir);
  } catch (err) {
    deps.notify(
      `skua: ${errText(err)}. Fix it or run /skua login <endpoint> <issuer> <clientId>.`,
      'error',
    );
    return undefined;
  }
  if (cfg === undefined) deps.notify(NOT_CONFIGURED, 'error');
  return cfg;
}

export function resetEndpointState(deps: CommandDeps, cfg: SkuaConfig): void {
  createTokenSource({agentDir: deps.agentDir, cfg}).clear();
  rmSync(cachePath(deps.agentDir), {force: true});
  syncAgentFiles(deps.agentsDir, []);
}

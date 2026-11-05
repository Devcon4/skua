import {loadConfig, saveConfig} from '../support/auth/config.ts';
import type {SkuaConfig, SkuaConfigInput} from '../support/auth/config.ts';
import {decodeIdToken} from '../support/auth/idtoken.ts';
import {saveToken} from '../support/auth/token.ts';
import {errText, resetEndpointState} from './deps.ts';
import type {CommandDeps} from './deps.ts';
import {listed, profileNames} from './format.ts';
import {usage} from './usage.ts';

export interface LoginCommand {
  kind: 'login';
  values: {endpoint: string; issuer: string; clientId: string} | undefined;
  device: boolean;
}

type LoginTarget = NonNullable<LoginCommand['values']>;

interface StoredConfigState {
  cfg: SkuaConfig | undefined;
  loadError: string | undefined;
}

function readStoredConfig(agentDir: string): StoredConfigState {
  try {
    return {cfg: loadConfig(agentDir), loadError: undefined};
  } catch (err) {
    return {cfg: undefined, loadError: errText(err)};
  }
}

function loginConfigInput(
  target: LoginTarget,
  stored: SkuaConfig | undefined,
): SkuaConfigInput {
  if (stored === undefined) return {...target};
  return {
    ...target,
    scopes: stored.scopes,
    loopbackPort: stored.loopbackPort,
    heartbeatTimeoutMs: stored.heartbeatTimeoutMs,
    profileRefreshMs: stored.profileRefreshMs,
    loginTimeoutSeconds: stored.loginTimeoutSeconds,
  };
}

function storeLoginTarget(
  deps: CommandDeps,
  target: LoginTarget,
  stored: SkuaConfig | undefined,
): SkuaConfig | undefined {
  const {endpointChanged} = saveConfig(
    deps.agentDir,
    loginConfigInput(target, stored),
  );
  const cfg = loadConfig(deps.agentDir);
  if (cfg !== undefined && endpointChanged) resetEndpointState(deps, cfg);
  return cfg;
}

function loggedInSubject(idToken: string): string {
  try {
    const claims = decodeIdToken(idToken);
    return claims.email ?? claims.sub;
  } catch {
    return 'unknown subject';
  }
}

export async function runLogin(
  cmd: LoginCommand,
  deps: CommandDeps,
): Promise<void> {
  const stored = readStoredConfig(deps.agentDir);
  const cfg =
    cmd.values === undefined
      ? stored.cfg
      : storeLoginTarget(deps, cmd.values, stored.cfg);
  if (cfg === undefined) {
    usage(
      deps,
      stored.loadError ??
        'no stored skua config; login needs <endpoint> <issuer> <clientId> the first time',
    );
    return;
  }

  let tokens;
  try {
    tokens = await deps.login({
      cfg,
      mode: cmd.device ? 'device' : 'auto',
      openUrl: deps.openUrl,
      notify: text => {
        deps.notify(text, 'info');
      },
    });
  } catch (err) {
    deps.notify(`skua login failed: ${errText(err)}`, 'error');
    return;
  }
  saveToken(deps.agentDir, tokens);

  const who = loggedInSubject(tokens.idToken);
  const synced = await deps.sync(cfg);
  deps.startRefresh(cfg);
  if (!synced.ok) {
    deps.notify(
      `skua: logged in as ${who} at ${cfg.endpoint}; profiles: not synced (${synced.error.message})`,
      'warning',
    );
    return;
  }
  deps.notify(
    `skua: logged in as ${who} at ${cfg.endpoint}; profiles: ${listed(profileNames([...synced.files.written, ...synced.files.unchanged]))}`,
    'info',
  );
}

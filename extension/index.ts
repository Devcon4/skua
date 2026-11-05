import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createAssistantMessageEventStream} from '@oh-my-pi/pi-ai';
import type {
  AssistantMessageEventStream,
  Context,
  Model,
  SimpleStreamOptions,
} from '@oh-my-pi/pi-ai';
import {VERSION, getAgentDir} from '@oh-my-pi/pi-coding-agent';
import type {
  ExtensionAPI,
  ExtensionContext,
  ProviderModelConfig,
} from '@oh-my-pi/pi-coding-agent';
import {readSseEvents} from '@oh-my-pi/pi-utils';
import type {SkuaServices} from './src/commands/deps.ts';
import {runSkuaCommand} from './src/commands/dispatch.ts';
import {createOpenUrl} from './src/commands/open-url.ts';
import {createApi} from './src/integration/skuaapi/client.ts';
import {createRelay} from './src/relay/provider.ts';
import type {Relay} from './src/relay/provider.ts';
import {resolveWorkspace} from './src/relay/workspace.ts';
import {CONFIG_DEFAULTS, loadConfig} from './src/support/auth/config.ts';
import type {SkuaConfig} from './src/support/auth/config.ts';
import {login} from './src/support/auth/login.ts';
import {TokenError, createTokenSource} from './src/support/auth/token.ts';
import type {TokenSource} from './src/support/auth/token.ts';
import {
  readCachedProfiles,
  readyModels,
  syncProfiles,
} from './src/support/profiles/cache.ts';
import type {SyncProfilesResult} from './src/support/profiles/cache.ts';
import {
  checkOmpVersion,
  isOmpRange,
  type VersionCheck,
} from './src/support/version.ts';

interface Services extends SkuaServices {
  key: string;
  relay: Relay;
}

const PLACEHOLDER_ENDPOINT = 'https://skua.invalid/';
const AGENTS_DIR = fileURLToPath(new URL('./agents/', import.meta.url));

const services = new Map<string, Services>();
let versionWarned = false;

const loggedOut: TokenSource = {
  getToken: () =>
    Promise.reject(
      new TokenError('not_logged_in', 'not logged in: run /skua login'),
    ),
  forceRefresh: () =>
    Promise.reject(
      new TokenError('not_logged_in', 'not logged in: run /skua login'),
    ),
  status: () => ({
    loggedIn: false,
    subject: undefined,
    email: undefined,
    expiresAt: undefined,
  }),
  clear: () => undefined,
};

function versionCheck(): VersionCheck {
  try {
    const range: unknown = JSON.parse(
      readFileSync(new URL('./omp-range.json', import.meta.url), 'utf8'),
    );
    if (!isOmpRange(range)) {
      return {
        ok: false,
        message:
          'skua: extension/omp-range.json is not a valid omp range. See extension/README.md.',
      };
    }
    return checkOmpVersion(VERSION, range);
  } catch (err) {
    return {
      ok: false,
      message: `skua: cannot read extension/omp-range.json: ${String(err)}. See extension/README.md.`,
    };
  }
}

function servicesFor(
  pi: ExtensionAPI,
  agentDir: string,
  cfg: SkuaConfig | undefined,
): Services {
  const endpoint = cfg?.endpoint ?? PLACEHOLDER_ENDPOINT;
  const key =
    cfg === undefined
      ? ''
      : JSON.stringify([cfg.issuer, cfg.clientId, cfg.heartbeatTimeoutMs]);
  const existing = services.get(endpoint);
  if (existing?.key === key) return existing;
  const tokens =
    cfg === undefined ? loggedOut : createTokenSource({agentDir, cfg});
  const timers = {
    setTimer: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimer: (h: NodeJS.Timeout) => {
      clearTimeout(h);
    },
  };
  const api = createApi({
    endpoint,
    getToken: () => tokens.getToken(),
    forceRefresh: () => tokens.forceRefresh(),
    readSse: readSseEvents,
    ...timers,
  });
  const relay = createRelay({
    api,
    resolveWorkspace: cwd =>
      resolveWorkspace(cwd, (cmd, args, options) =>
        pi.exec(cmd, args, options),
      ),
    heartbeatTimeoutMs:
      cfg?.heartbeatTimeoutMs ?? CONFIG_DEFAULTS.heartbeatTimeoutMs,
    timers,
    log: message => {
      pi.logger.warn(message);
    },
  });
  const created: Services = {key, tokens, api, relay};
  services.set(endpoint, created);
  return created;
}

export default function skua(pi: ExtensionAPI): void {
  const agentDir = getAgentDir();

  const readConfig = (): SkuaConfig | undefined => {
    try {
      return loadConfig(agentDir);
    } catch (err) {
      pi.logger.warn(
        `skua: ${err instanceof Error ? err.message : String(err)}`,
      );
      return undefined;
    }
  };

  const streamSimple = (
    model: Model,
    context: Context,
    options?: SimpleStreamOptions,
  ): AssistantMessageEventStream => {
    const stream = createAssistantMessageEventStream();
    const {relay} = servicesFor(pi, agentDir, readConfig());
    relay
      .stream(model, context, options, event => {
        stream.push(event);
      })
      .then(
        () => {
          stream.end();
        },
        (err: unknown) => {
          pi.logger.warn(
            `skua: relay failed: ${err instanceof Error ? err.message : String(err)}`,
          );
          stream.end();
        },
      );
    return stream;
  };

  const registerProvider = (
    cfg: SkuaConfig | undefined,
    models: ProviderModelConfig[],
  ): void => {
    pi.registerProvider('skua', {
      baseUrl: cfg?.endpoint ?? PLACEHOLDER_ENDPOINT,
      apiKey: 'skua-relay',
      api: 'skua-relay',
      streamSimple,
      models,
    });
  };

  const sync = async (cfg: SkuaConfig): Promise<SyncProfilesResult> => {
    const res = await syncProfiles({
      api: servicesFor(pi, agentDir, cfg).api,
      agentDir,
      agentsDir: AGENTS_DIR,
      endpoint: cfg.endpoint,
    });
    if (!res.ok) return res;
    try {
      registerProvider(cfg, readyModels(res.profiles));
    } catch (err) {
      pi.logger.warn(
        `skua: re-registering the skua provider failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    return res;
  };

  const refreshTick = async (): Promise<void> => {
    const current = readConfig();
    if (current === undefined) return;
    const res = await sync(current);
    if (!res.ok)
      pi.logger.warn(`skua: profile sync failed: ${res.error.message}`);
  };

  // eslint-disable-next-line @typescript-eslint/no-misused-promises -- ctx timers report a rejected tick on omp's extension error channel
  const refreshTimerTick: () => void = refreshTick;

  let refreshing = false;
  const startRefresh = (ctx: ExtensionContext, cfg: SkuaConfig): void => {
    if (refreshing) return;
    refreshing = true;
    ctx.setInterval(refreshTimerTick, cfg.profileRefreshMs);
  };

  const startup = readConfig();
  registerProvider(
    startup,
    startup === undefined
      ? []
      : readyModels(readCachedProfiles(agentDir, startup.endpoint)),
  );

  const stepParams = pi.zod.object({
    tool: pi.zod.string(),
    args: pi.zod.unknown(),
    result: pi.zod.string(),
    isError: pi.zod.boolean().optional(),
  });
  pi.registerTool<typeof stepParams, {tool: string; args: unknown}>({
    name: 'skua_step',
    label: 'skua step',
    description:
      'Internal: shows one tool call a remote skua agent already ran. Never call it yourself.',
    parameters: stepParams,
    execute: (_toolCallId, params) => {
      if (params.isError) return Promise.reject(new Error(params.result));
      return Promise.resolve({
        content: [{type: 'text', text: params.result}],
        details: {tool: params.tool, args: params.args},
      });
    },
  });

  pi.registerCommand('skua', {
    description: 'skua remote agents: login, sync, list, abort, status',
    handler: (args, ctx) =>
      runSkuaCommand(args, {
        agentDir,
        agentsDir: AGENTS_DIR,
        omp: {version: VERSION, check: versionCheck()},
        notify: (text, level) => {
          ctx.ui.notify(text, level);
        },
        services: cfg => servicesFor(pi, agentDir, cfg),
        sync,
        startRefresh: cfg => {
          startRefresh(ctx, cfg);
        },
        openUrl: createOpenUrl({
          exec: (cmd, cmdArgs) => pi.exec(cmd, cmdArgs),
          env: process.env,
        }),
        login,
      }),
  });

  pi.on('session_start', (_event, ctx) => {
    if (ctx.agent.kind !== 'main') return;
    if (!versionWarned) {
      const check = versionCheck();
      if (!check.ok) {
        versionWarned = true;
        ctx.ui.notify(check.message, 'warning');
      }
    }
    const cfg = readConfig();
    if (cfg === undefined) {
      ctx.ui.notify(
        'skua: not logged in. Run /skua login <endpoint> <issuer> <clientId> to use remote skua agents.',
        'info',
      );
      return;
    }
    if (!servicesFor(pi, agentDir, cfg).tokens.status().loggedIn) {
      ctx.ui.notify(
        `skua: not logged in at ${cfg.endpoint}. Run /skua login to use remote skua agents.`,
        'info',
      );
      return;
    }
    ctx.setTimeout(refreshTimerTick, 0);
    startRefresh(ctx, cfg);
  });

  pi.on('session_shutdown', (_event, ctx) => {
    if (ctx.agent.kind === 'main') refreshing = false;
    if (ctx.agent.kind !== 'sub') return;
    const sessionId = ctx.sessionManager.getSessionId();
    for (const s of services.values()) s.relay.forget(sessionId);
  });
}

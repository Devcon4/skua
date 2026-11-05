import {randomBytes} from 'node:crypto';
import {
  ConfigurationError,
  LoginCancelledError,
  OAuthError,
} from '@oh-my-pi/pi-ai/error';
import {OAuthCallbackFlow} from '@oh-my-pi/pi-ai/oauth/callback-server';
import {generatePKCE} from '@oh-my-pi/pi-ai/oauth/pkce';
import type {
  OAuthController,
  OAuthCredentials,
} from '@oh-my-pi/pi-ai/oauth/types';
import type {SkuaConfig} from './config.ts';
import {deviceFlow} from './device-flow.ts';
import {OidcError, discover, postForm} from './discovery.ts';
import type {IssuerMetadata} from './discovery.ts';
import {tokenSet} from './idtoken.ts';
import type {TokenSet} from './idtoken.ts';

export type OpenUrl = (url: string) => boolean | Promise<boolean>;
export type Notify = (text: string) => void;

function randomToken(bytes: number): string {
  return randomBytes(bytes).toString('base64url');
}

class LoopbackFlow extends OAuthCallbackFlow {
  tokens: TokenSet | undefined;
  exchanging = false;
  readonly #cfg: SkuaConfig;
  readonly #meta: IssuerMetadata;
  readonly #pkce: {verifier: string; challenge: string};
  readonly #nonce: string;

  constructor(
    ctrl: OAuthController,
    cfg: SkuaConfig,
    meta: IssuerMetadata,
    pkce: {verifier: string; challenge: string},
  ) {
    super(ctrl, {
      preferredPort: cfg.loopbackPort,
      callbackHostname: '127.0.0.1',
      allowPortFallback: false,
    });
    this.#cfg = cfg;
    this.#meta = meta;
    this.#pkce = pkce;
    this.#nonce = randomToken(16);
  }

  generateAuthUrl(state: string, redirectUri: string): Promise<{url: string}> {
    const authorize = new URL(this.#meta.authorizationEndpoint);
    authorize.searchParams.set('response_type', 'code');
    authorize.searchParams.set('client_id', this.#cfg.clientId);
    authorize.searchParams.set('redirect_uri', redirectUri);
    authorize.searchParams.set('scope', this.#cfg.scopes.join(' '));
    authorize.searchParams.set('state', state);
    authorize.searchParams.set('nonce', this.#nonce);
    authorize.searchParams.set('code_challenge', this.#pkce.challenge);
    authorize.searchParams.set('code_challenge_method', 'S256');
    return Promise.resolve({url: authorize.toString()});
  }

  async exchangeToken(
    code: string,
    _state: string,
    redirectUri: string,
  ): Promise<OAuthCredentials> {
    this.exchanging = true;
    const body = await postForm(this.#meta.tokenEndpoint, {
      grant_type: 'authorization_code',
      code,
      redirect_uri: redirectUri,
      client_id: this.#cfg.clientId,
      code_verifier: this.#pkce.verifier,
    });
    const tokens = tokenSet(this.#cfg, this.#meta, body, this.#nonce);
    this.tokens = tokens;
    return {
      access: tokens.idToken,
      refresh: tokens.refreshToken ?? '',
      expires: tokens.expiresAt * 1000,
    };
  }
}

async function browserFlow(
  cfg: SkuaConfig,
  meta: IssuerMetadata,
  openUrl: OpenUrl,
  notify: Notify,
): Promise<TokenSet> {
  const noBrowser = new AbortController();
  const timeout = AbortSignal.timeout(cfg.loginTimeoutSeconds * 1000);
  const onAuth = ({url}: {url: string}): void => {
    const opening = (async () => {
      try {
        return await openUrl(url);
      } catch {
        return false;
      }
    })();
    void opening.then(opened => {
      if (!opened && !flow.exchanging) {
        noBrowser.abort();
        return;
      }
      notify(
        `skua login: finish signing in in the browser. If it did not open, visit ${url}`,
      );
    });
  };
  const flow = new LoopbackFlow(
    {onAuth, signal: AbortSignal.any([noBrowser.signal, timeout])},
    cfg,
    meta,
    await generatePKCE(),
  );
  try {
    await flow.login();
  } catch (err) {
    if (err instanceof OidcError) throw err;
    if (err instanceof ConfigurationError) {
      throw new OidcError(
        'port_in_use',
        `cannot listen on 127.0.0.1:${String(cfg.loopbackPort)} for the login callback (${err.message})`,
      );
    }
    if (err instanceof LoginCancelledError && noBrowser.signal.aborted) {
      throw new OidcError(
        'browser_unavailable',
        'no browser could be started for the login page',
      );
    }
    if (err instanceof LoginCancelledError && timeout.aborted) {
      throw new OidcError(
        'timeout',
        `no login callback within ${String(cfg.loginTimeoutSeconds)} s`,
      );
    }
    if (err instanceof OAuthError)
      throw new OidcError(
        'authorization_failed',
        `issuer refused the login: ${err.message}`,
      );
    throw err;
  }
  if (!flow.tokens)
    throw new OidcError(
      'invalid_callback',
      'login finished without a token response',
    );
  return flow.tokens;
}

/** `auto` tries the browser flow and falls back to the device flow when no browser starts or the loopback port cannot be bound. */
export async function login(opts: {
  cfg: SkuaConfig;
  mode: 'auto' | 'device';
  openUrl: OpenUrl;
  notify: Notify;
}): Promise<TokenSet> {
  const meta = await discover(opts.cfg.issuer);
  if (opts.mode === 'device') return deviceFlow(opts.cfg, meta, opts.notify);
  try {
    return await browserFlow(opts.cfg, meta, opts.openUrl, opts.notify);
  } catch (err) {
    if (
      !(err instanceof OidcError) ||
      (err.code !== 'browser_unavailable' && err.code !== 'port_in_use')
    )
      throw err;
    if (!meta.deviceAuthorizationEndpoint) {
      throw new OidcError(
        'no_login_flow',
        `skua login cannot proceed: the browser flow failed (${err.message}) and the issuer offers no device flow (no device_authorization_endpoint). Set BROWSER to a command that opens URLs, free the loopback port, or enable the device flow at the issuer.`,
      );
    }
    return deviceFlow(opts.cfg, meta, opts.notify);
  }
}

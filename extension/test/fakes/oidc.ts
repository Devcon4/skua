import {createHash, randomBytes} from 'node:crypto';
import {createServer} from 'node:http';
import type {IncomingMessage, ServerResponse} from 'node:http';
import {buffer} from 'node:stream/consumers';
import {closeServer, listenOnLoopback} from './loopback-server.ts';

export interface FakeIssuerOptions {
  port?: number | undefined;
  clientId?: string | undefined;
  subject?: string | undefined;
  email?: string | undefined;
  idTokenTtlS?: number | undefined;
  failRefreshTimes?: number | undefined;
  noDevice?: boolean | undefined;
  noS256?: boolean | undefined;
  interval?: number | undefined;
  deviceExpiresInS?: number | undefined;
  now?: (() => number) | undefined;
}

export interface FakeTokenRequest {
  grantType: string;
  status: number;
  error: string | undefined;
}

export interface FakeIssuer {
  url: string;
  close(): Promise<void>;
  approveDevice(userCode: string): void;
  expireRefreshTokens(): void;
  tokenRequests(): FakeTokenRequest[];
  mint(nonce?: string): Record<string, unknown>;
}

interface IssuerSettings {
  clientId: string;
  subject: string;
  email: string;
  idTokenTtlS: number;
  intervalS: number;
  deviceExpiresInS: number;
  now: () => number;
  refreshFailures: number;
}

interface PendingCode {
  redirectUri: string;
  challenge: string;
  nonce: string | undefined;
  expiresAt: number;
}
interface PendingDevice {
  userCode: string;
  approved: boolean;
  expiresAt: number;
  intervalS: number;
  lastPollAt: number | undefined;
}

interface GrantOutcome {
  status: number;
  body: Record<string, unknown>;
  error: string | undefined;
}

type Grant = (form: URLSearchParams) => GrantOutcome;
type Route = (u: URL, body: string, res: ServerResponse) => void;

const DEVICE_GRANT = 'urn:ietf:params:oauth:grant-type:device_code';
const USER_CODE_ALPHABET = 'BCDFGHJKLMNPQRSTVWXZ';
const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '[::1]']);
const CODE_TTL_MS = 60_000;
const SLOW_DOWN_STEP_S = 5;

function issuerSettings(opts: FakeIssuerOptions): IssuerSettings {
  return {
    clientId: opts.clientId ?? 'skua-omp',
    subject: opts.subject ?? 'fake-user',
    email: opts.email ?? 'dev@example.test',
    idTokenTtlS: opts.idTokenTtlS ?? 3600,
    intervalS: opts.interval ?? 5,
    deviceExpiresInS: opts.deviceExpiresInS ?? 600,
    now: opts.now ?? Date.now,
    refreshFailures: opts.failRefreshTimes ?? 0,
  };
}

function b64url(v: unknown): string {
  return Buffer.from(JSON.stringify(v)).toString('base64url');
}

function userCode(): string {
  const bytes = randomBytes(8);
  const chars = [...bytes].map(
    b => USER_CODE_ALPHABET[b % USER_CODE_ALPHABET.length],
  );
  return `${chars.slice(0, 4).join('')}-${chars.slice(4).join('')}`;
}

function isLoopbackRedirect(uri: string): boolean {
  try {
    const u = new URL(uri);
    return u.protocol === 'http:' && LOOPBACK_HOSTS.has(u.hostname);
  } catch {
    return false;
  }
}

function grantFailure(
  status: number,
  error: string,
  description: string,
): GrantOutcome {
  return {status, error, body: {error, error_description: description}};
}

function grantSuccess(tokens: Record<string, unknown>): GrantOutcome {
  return {status: 200, error: undefined, body: tokens};
}

function pkceChallenge(verifier: string): string {
  return createHash('sha256').update(verifier).digest('base64url');
}

function json(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, {
    'content-type': 'application/json',
    'cache-control': 'no-store',
  });
  res.end(JSON.stringify(body));
}

function rejectAuthorize(res: ServerResponse, reason: string): void {
  res.writeHead(400, {'content-type': 'text/plain'});
  res.end(reason);
}

export async function startFakeIssuer(
  opts: FakeIssuerOptions = {},
): Promise<FakeIssuer> {
  const settings = issuerSettings(opts);
  const {clientId, now} = settings;
  let refreshFailuresLeft = settings.refreshFailures;

  const codes = new Map<string, PendingCode>();
  const devices = new Map<string, PendingDevice>();
  const refreshTokens = new Set<string>();
  const tokenLog: FakeTokenRequest[] = [];
  let url = '';

  const issueTokens = (nonce: string | undefined): Record<string, unknown> => {
    const iat = Math.floor(now() / 1000);
    const claims = {
      iss: url,
      sub: settings.subject,
      aud: clientId,
      email: settings.email,
      email_verified: true,
      iat,
      exp: iat + settings.idTokenTtlS,
      ...(nonce === undefined ? {} : {nonce}),
    };
    const refreshToken = randomBytes(24).toString('base64url');
    refreshTokens.add(refreshToken);
    return {
      access_token: randomBytes(24).toString('base64url'),
      token_type: 'Bearer',
      expires_in: settings.idTokenTtlS,
      id_token: `${b64url({alg: 'none', typ: 'JWT'})}.${b64url(claims)}.`,
      refresh_token: refreshToken,
    };
  };

  const exchangeCode: Grant = form => {
    const code = form.get('code') ?? '';
    const pending = codes.get(code);
    codes.delete(code);
    if (!pending || pending.expiresAt <= now())
      return grantFailure(400, 'invalid_grant', 'unknown or expired code');
    if (form.get('redirect_uri') !== pending.redirectUri)
      return grantFailure(400, 'invalid_grant', 'redirect_uri mismatch');
    if (pkceChallenge(form.get('code_verifier') ?? '') !== pending.challenge)
      return grantFailure(400, 'invalid_grant', 'PKCE verification failed');
    return grantSuccess(issueTokens(pending.nonce));
  };

  const redeemRefreshToken: Grant = form => {
    if (refreshFailuresLeft > 0) {
      refreshFailuresLeft--;
      return grantFailure(503, 'temporarily_unavailable', 'issuer outage');
    }
    if (!refreshTokens.delete(form.get('refresh_token') ?? ''))
      return grantFailure(
        400,
        'invalid_grant',
        'unknown or revoked refresh token',
      );
    return grantSuccess(issueTokens(undefined));
  };

  const pollingTooFast = (device: PendingDevice, t: number): boolean =>
    device.lastPollAt !== undefined &&
    t - device.lastPollAt < device.intervalS * 1000;

  const pollDeviceCode: Grant = form => {
    const deviceCode = form.get('device_code') ?? '';
    const device = devices.get(deviceCode);
    if (!device)
      return grantFailure(400, 'invalid_grant', 'unknown device_code');
    const t = now();
    if (device.expiresAt <= t) {
      devices.delete(deviceCode);
      return grantFailure(400, 'expired_token', 'device code expired');
    }
    const tooFast = pollingTooFast(device, t);
    device.lastPollAt = t;
    if (tooFast) {
      device.intervalS += SLOW_DOWN_STEP_S;
      return grantFailure(
        400,
        'slow_down',
        `poll at most every ${String(device.intervalS)} s`,
      );
    }
    if (!device.approved)
      return grantFailure(400, 'authorization_pending', 'waiting for the user');
    devices.delete(deviceCode);
    return grantSuccess(issueTokens(undefined));
  };

  const grants = new Map<string, Grant>([
    ['authorization_code', exchangeCode],
    ['refresh_token', redeemRefreshToken],
    [DEVICE_GRANT, pollDeviceCode],
  ]);

  const tokenGrant: Grant = form => {
    if (form.get('client_id') !== clientId)
      return grantFailure(401, 'invalid_client', 'unknown client_id');
    const grantType = form.get('grant_type');
    const grant = grants.get(grantType ?? '');
    if (!grant)
      return grantFailure(
        400,
        'unsupported_grant_type',
        `grant_type ${grantType ?? '(none)'} is not supported`,
      );
    return grant(form);
  };

  const authorizeRejection = (q: URLSearchParams): string | undefined => {
    if (q.get('client_id') !== clientId) return 'unknown client_id';
    if (!isLoopbackRedirect(q.get('redirect_uri') ?? ''))
      return 'redirect_uri must be an http loopback URL';
    return undefined;
  };

  const s256ChallengeGiven = (q: URLSearchParams, challenge: string): boolean =>
    !opts.noS256 &&
    q.get('code_challenge_method') === 'S256' &&
    challenge !== '';

  const authorizationResult = (
    q: URLSearchParams,
    redirectUri: string,
  ): [string, string][] => {
    if (q.get('response_type') !== 'code')
      return [['error', 'unsupported_response_type']];
    const challenge = q.get('code_challenge') ?? '';
    if (!s256ChallengeGiven(q, challenge))
      return [
        ['error', 'invalid_request'],
        [
          'error_description',
          'code_challenge with code_challenge_method=S256 is required',
        ],
      ];
    const code = randomBytes(16).toString('base64url');
    codes.set(code, {
      redirectUri,
      challenge,
      nonce: q.get('nonce') ?? undefined,
      expiresAt: now() + CODE_TTL_MS,
    });
    return [['code', code]];
  };

  const authorize: Route = (u, _body, res) => {
    const q = u.searchParams;
    const rejection = authorizeRejection(q);
    if (rejection !== undefined) {
      rejectAuthorize(res, rejection);
      return;
    }
    const redirectUri = q.get('redirect_uri') ?? '';
    const target = new URL(redirectUri);
    const state = q.get('state');
    if (state !== null) target.searchParams.set('state', state);
    for (const [key, value] of authorizationResult(q, redirectUri))
      target.searchParams.set(key, value);
    res.writeHead(302, {location: target.toString()});
    res.end();
  };

  const discoveryDocument = (): Record<string, unknown> => ({
    issuer: url,
    authorization_endpoint: `${url}/authorize`,
    token_endpoint: `${url}/token`,
    jwks_uri: `${url}/keys`,
    response_types_supported: ['code'],
    subject_types_supported: ['public'],
    id_token_signing_alg_values_supported: ['none'],
    scopes_supported: ['openid', 'profile', 'email', 'offline_access'],
    grant_types_supported: [
      'authorization_code',
      'refresh_token',
      ...(opts.noDevice ? [] : [DEVICE_GRANT]),
    ],
    code_challenge_methods_supported: opts.noS256 ? ['plain'] : ['S256'],
    ...(opts.noDevice
      ? {}
      : {device_authorization_endpoint: `${url}/device_authorization`}),
  });

  const token: Route = (_u, body, res) => {
    const form = new URLSearchParams(body);
    const outcome = tokenGrant(form);
    tokenLog.push({
      grantType: form.get('grant_type') ?? '',
      status: outcome.status,
      error: outcome.error,
    });
    json(res, outcome.status, outcome.body);
  };

  const deviceAuthorization: Route = (_u, body, res) => {
    const form = new URLSearchParams(body);
    if (form.get('client_id') !== clientId) {
      json(res, 401, {
        error: 'invalid_client',
        error_description: 'unknown client_id',
      });
      return;
    }
    const deviceCode = randomBytes(24).toString('base64url');
    const code = userCode();
    devices.set(deviceCode, {
      userCode: code,
      approved: false,
      expiresAt: now() + settings.deviceExpiresInS * 1000,
      intervalS: settings.intervalS,
      lastPollAt: undefined,
    });
    json(res, 200, {
      device_code: deviceCode,
      user_code: code,
      verification_uri: `${url}/device`,
      verification_uri_complete: `${url}/device?user_code=${code}`,
      expires_in: settings.deviceExpiresInS,
      interval: settings.intervalS,
    });
  };

  const routes = new Map<string, Route>([
    [
      'GET /.well-known/openid-configuration',
      (_u, _body, res) => {
        json(res, 200, discoveryDocument());
      },
    ],
    [
      'GET /keys',
      (_u, _body, res) => {
        json(res, 200, {keys: []});
      },
    ],
    ['GET /authorize', authorize],
    ['POST /token', token],
  ]);
  if (!opts.noDevice)
    routes.set('POST /device_authorization', deviceAuthorization);

  const handle = async (
    req: IncomingMessage,
    res: ServerResponse,
  ): Promise<void> => {
    const body = (await buffer(req)).toString('utf8');
    const u = new URL(req.url ?? '/', 'http://fake');
    const route = routes.get(`${req.method ?? 'GET'} ${u.pathname}`);
    if (!route) {
      json(res, 404, {error: 'not_found'});
      return;
    }
    route(u, body, res);
  };

  const server = createServer((req, res) => {
    handle(req, res).catch((err: unknown) => {
      if (!res.headersSent)
        json(res, 500, {
          error: 'server_error',
          error_description: err instanceof Error ? err.message : String(err),
        });
    });
  });
  server.keepAliveTimeout = 60_000;
  url = await listenOnLoopback(server, opts.port);

  return {
    url,
    approveDevice: (code: string) => {
      const device = [...devices.values()].find(d => d.userCode === code);
      if (!device)
        throw new Error(
          `no pending device authorization for user code ${code}`,
        );
      device.approved = true;
    },
    expireRefreshTokens: () => {
      refreshTokens.clear();
    },
    tokenRequests: () => tokenLog.map(t => ({...t})),
    mint: (nonce?: string) => issueTokens(nonce),
    close: () => closeServer(server),
  };
}

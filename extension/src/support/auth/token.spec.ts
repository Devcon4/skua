import assert from 'node:assert/strict';
import {existsSync, mkdtempSync, rmSync, statSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {test} from 'vitest';
import {startFakeIssuer} from '../../../test/fakes/oidc.ts';
import type {FakeIssuer, FakeIssuerOptions} from '../../../test/fakes/oidc.ts';
import type {SkuaConfig} from './config.ts';
import {discover} from './discovery.ts';
import {tokenSet} from './idtoken.ts';
import type {TokenSet} from './idtoken.ts';
import {TokenError, createTokenSource, saveToken} from './token.ts';
import type {TokenSource} from './token.ts';

interface Env {
  issuer: FakeIssuer;
  cfg: SkuaConfig;
  agentDir: string;
  tokenPath: string;
  clock: {ms: number};
  login(): Promise<TokenSet>;
  refreshes(): {status: number; error: string | undefined}[];
}

async function withEnv(
  opts: FakeIssuerOptions,
  fn: (env: Env) => Promise<void>,
): Promise<void> {
  const clock = {ms: Date.now()};
  const issuer = await startFakeIssuer({...opts, now: () => clock.ms});
  const agentDir = mkdtempSync(join(tmpdir(), 'skua-token-'));
  const cfg: SkuaConfig = {
    endpoint: 'http://127.0.0.1:32080',
    issuer: issuer.url,
    clientId: 'skua-omp',
    scopes: ['openid', 'profile', 'email', 'offline_access'],
    loopbackPort: 0,
    heartbeatTimeoutMs: 45_000,
    profileRefreshMs: 600_000,
    loginTimeoutSeconds: 300,
  };
  const login = async () =>
    tokenSet(cfg, await discover(cfg.issuer), issuer.mint('login'), 'login');
  const refreshes = () =>
    issuer
      .tokenRequests()
      .filter(r => r.grantType === 'refresh_token')
      .map(r => ({status: r.status, error: r.error}));
  try {
    await fn({
      issuer,
      cfg,
      agentDir,
      tokenPath: join(agentDir, 'skua', 'token.json'),
      clock,
      login,
      refreshes,
    });
  } finally {
    await issuer.close();
    rmSync(agentDir, {recursive: true, force: true});
  }
}

async function loggedIn(
  env: Env,
): Promise<{tokens: TokenSet; source: TokenSource}> {
  const tokens = await env.login();
  saveToken(env.agentDir, tokens);
  return {
    tokens,
    source: createTokenSource({
      agentDir: env.agentDir,
      cfg: env.cfg,
      now: () => env.clock.ms,
    }),
  };
}

function isTokenError(code: TokenError['code']): (err: unknown) => boolean {
  return err => err instanceof TokenError && err.code === code;
}

test('refreshes at 59 s remaining and not at 61 s', async () => {
  await withEnv({}, async env => {
    const {tokens, source} = await loggedIn(env);
    env.clock.ms = tokens.expiresAt * 1000 - 61_000;
    assert.equal(await source.getToken(), tokens.idToken);
    assert.equal(env.refreshes().length, 0);
    env.clock.ms = tokens.expiresAt * 1000 - 59_000;
    const refreshed = await source.getToken();
    assert.notEqual(refreshed, tokens.idToken);
    assert.deepEqual(env.refreshes(), [{status: 200, error: undefined}]);
    assert.equal(await source.getToken(), refreshed);
    assert.equal(env.refreshes().length, 1);
  });
});

test('two concurrent getToken calls make one refresh request', async () => {
  await withEnv({}, async env => {
    const {tokens, source} = await loggedIn(env);
    env.clock.ms = tokens.expiresAt * 1000 - 10_000;
    const [a, b] = await Promise.all([source.getToken(), source.getToken()]);
    assert.equal(a, b);
    assert.notEqual(a, tokens.idToken);
    assert.equal(env.refreshes().length, 1);
  });
});

test('a token another writer refreshed is used without a refresh', async () => {
  await withEnv({}, async env => {
    const {tokens, source} = await loggedIn(env);
    env.clock.ms = tokens.expiresAt * 1000 - 61_000;
    assert.equal(await source.getToken(), tokens.idToken);
    const other = await env.login();
    saveToken(env.agentDir, other);
    env.clock.ms = tokens.expiresAt * 1000 - 59_000;
    assert.equal(await source.getToken(), other.idToken);
    assert.equal(env.refreshes().length, 0);
  });
});

test('a revoked refresh token clears the cache and reports not logged in', async () => {
  await withEnv({}, async env => {
    const {tokens, source} = await loggedIn(env);
    env.issuer.expireRefreshTokens();
    env.clock.ms = tokens.expiresAt * 1000 - 30_000;
    await assert.rejects(
      source.getToken(),
      (err: unknown) =>
        isTokenError('not_logged_in')(err) &&
        String(err).includes('not logged in: run /skua login'),
    );
    assert.deepEqual(env.refreshes(), [{status: 400, error: 'invalid_grant'}]);
    assert.equal(existsSync(env.tokenPath), false);
    assert.equal(source.status().loggedIn, false);
    await assert.rejects(source.getToken(), isTokenError('not_logged_in'));
  });
});

test('a failed refresh keeps the valid cached token and retries on the next call', async () => {
  await withEnv({failRefreshTimes: 1}, async env => {
    const {tokens, source} = await loggedIn(env);
    env.clock.ms = tokens.expiresAt * 1000 - 30_000;
    assert.equal(await source.getToken(), tokens.idToken);
    assert.deepEqual(env.refreshes(), [
      {status: 503, error: 'temporarily_unavailable'},
    ]);
    const refreshed = await source.getToken();
    assert.notEqual(refreshed, tokens.idToken);
    assert.equal(env.refreshes().length, 2);
  });
});

test('a failing refresh after expiry surfaces unauthorized', async () => {
  await withEnv({failRefreshTimes: 5}, async env => {
    const {tokens, source} = await loggedIn(env);
    env.clock.ms = tokens.expiresAt * 1000 + 1_000;
    await assert.rejects(
      source.getToken(),
      (err: unknown) =>
        isTokenError('unauthorized')(err) &&
        String(err).includes('unauthorized'),
    );
    assert.equal(env.refreshes().length, 1);
    assert.equal(existsSync(env.tokenPath), true);
  });
});

test('forceRefresh refreshes a token with time left', async () => {
  await withEnv({}, async env => {
    const {tokens, source} = await loggedIn(env);
    assert.equal(await source.getToken(), tokens.idToken);
    const forced = await source.forceRefresh();
    assert.notEqual(forced, tokens.idToken);
    assert.equal(env.refreshes().length, 1);
    assert.equal(await source.getToken(), forced);
  });
});

test('the token file is mode 0600 before and after a refresh', async () => {
  await withEnv({}, async env => {
    const {tokens, source} = await loggedIn(env);
    assert.equal(statSync(env.tokenPath).mode & 0o777, 0o600);
    env.clock.ms = tokens.expiresAt * 1000 - 1_000;
    await source.getToken();
    assert.equal(statSync(env.tokenPath).mode & 0o777, 0o600);
    assert.equal(statSync(join(env.agentDir, 'skua')).mode & 0o777, 0o700);
  });
});

test('status reports the login and clear logs out', async () => {
  await withEnv({}, async env => {
    const source = createTokenSource({
      agentDir: env.agentDir,
      cfg: env.cfg,
      now: () => env.clock.ms,
    });
    assert.deepEqual(source.status(), {
      loggedIn: false,
      subject: undefined,
      email: undefined,
      expiresAt: undefined,
    });
    await assert.rejects(source.getToken(), isTokenError('not_logged_in'));
    const tokens = await env.login();
    saveToken(env.agentDir, tokens);
    assert.deepEqual(source.status(), {
      loggedIn: true,
      subject: 'fake-user',
      email: 'dev@example.test',
      expiresAt: tokens.expiresAt,
    });
    source.clear();
    assert.equal(source.status().loggedIn, false);
  });
});

import assert from 'node:assert/strict';
import {setTimeout as delay} from 'node:timers/promises';
import {test} from 'vitest';
import {readSseEvents} from '@oh-my-pi/pi-utils/stream';
import {startFakeApi} from '../../../test/fakes/api.ts';
import type {FakeApi, FakeRunSnapshot} from '../../../test/fakes/api.ts';
import {startFakeIssuer} from '../../../test/fakes/oidc.ts';
import type {CreateRunBody, RunEvent} from '../../contract/wire.ts';
import {createApi} from './client.ts';
import type {Api, ApiOptions} from './client.ts';
import {ApiError} from './errors.ts';
import type {FollowOptions} from './follow.ts';
import type {FetchImpl} from './http.ts';

interface TokenClaims {
  n?: unknown;
  exp?: unknown;
}

interface Connect {
  lastEventId: string | null;
  highest: number;
  claims: TokenClaims;
}

function b64url(v: unknown): string {
  return Buffer.from(JSON.stringify(v)).toString('base64url');
}

function idToken(expInS: number, extra: Record<string, unknown> = {}): string {
  const exp = Math.floor(Date.now() / 1000) + expInS;
  return `${b64url({alg: 'none', typ: 'JWT'})}.${b64url({iss: 'https://issuer.test', sub: 'alice', exp, ...extra})}.`;
}

function claimsOf(jwt: string): TokenClaims {
  return JSON.parse(
    Buffer.from(jwt.split('.')[1] ?? '', 'base64url').toString('utf8'),
  ) as TokenClaims;
}

function client(
  fake: FakeApi,
  over: Partial<ApiOptions<NodeJS.Timeout>> = {},
): Api {
  return createApi({
    endpoint: fake.url,
    getToken: () => Promise.resolve(idToken(3600)),
    forceRefresh: () => Promise.resolve(),
    setTimer: setTimeout,
    clearTimer: clearTimeout,
    readSse: readSseEvents,
    ...over,
  });
}

function body(clientKey: string, prompt: string): CreateRunBody {
  return {profile: 'generic', clientKey, task: {prompt}};
}

async function follow(
  api: Api,
  name: string,
  opts: Partial<FollowOptions> & {onEvent?: (e: RunEvent) => void} = {},
): Promise<RunEvent[]> {
  const seen: RunEvent[] = [];
  const follow: FollowOptions = {
    afterSeq: opts.afterSeq ?? 0,
    signal: opts.signal ?? new AbortController().signal,
    heartbeatTimeoutMs: opts.heartbeatTimeoutMs ?? 5000,
    giveUpMs: opts.giveUpMs ?? 60_000,
  };
  for await (const e of api.followEvents(name, follow)) {
    seen.push(e);
    opts.onEvent?.(e);
  }
  return seen;
}

function recordEvents(state: {highest: number}): {
  fetchImpl: FetchImpl;
  connects: Connect[];
} {
  const connects: Connect[] = [];
  const fetchImpl: FetchImpl = (input, init) => {
    if (input.endsWith('/events')) {
      const headers = new Headers(init.headers);
      const auth = headers.get('authorization') ?? '';
      connects.push({
        lastEventId: headers.get('last-event-id'),
        highest: state.highest,
        claims: claimsOf(auth.replace(/^Bearer /, '')),
      });
    }
    return fetch(input, init);
  };
  return {fetchImpl, connects};
}

function assertContiguousFromOne(seen: RunEvent[]): void {
  assert.deepEqual(
    seen.map(e => e.seq),
    seen.map((_, i) => i + 1),
  );
}

function highestAt(connects: Connect[], index: number): number {
  return connects[index]?.highest ?? 0;
}

function isApiError(code: ApiError['code']): (err: unknown) => boolean {
  return err => err instanceof ApiError && err.code === code;
}

async function waitForRun(
  fake: FakeApi,
  done: (r: FakeRunSnapshot) => boolean,
): Promise<void> {
  for (let i = 0; i < 1000; i++) {
    if (fake.runs().some(done)) return;
    await delay(10);
  }
  throw new Error('run did not reach the expected state');
}

test(
  'nohb: the watchdog reconnects once with Last-Event-ID equal to the last seq yielded',
  {timeout: 20_000},
  async () => {
    const fake = await startFakeApi({
      stepMs: 50,
      steps: 4,
      gapMs: 5000,
      hbMs: 300,
    });
    try {
      const state = {highest: 0};
      const {fetchImpl, connects} = recordEvents(state);
      const api = client(fake, {fetchImpl});
      const {run} = await api.createRun(body('s-nohb:0', 'SCENARIO:nohb'));
      const seen = await follow(api, run.name, {
        heartbeatTimeoutMs: 1500,
        onEvent: e => (state.highest = e.seq),
      });
      assert.equal(connects.length, 2);
      assert.equal(connects[0]?.lastEventId, null);
      assert.ok(highestAt(connects, 1) > 0);
      assert.equal(connects[1]?.lastEventId, String(connects[1]?.highest));
      assertContiguousFromOne(seen);
      assert.equal(seen.at(-1)?.type, 'end');
    } finally {
      await fake.close();
    }
  },
);

test('restart mid-run: yields continue with no repeated seq and end with the real end event', async () => {
  const fake = await startFakeApi({stepMs: 300, steps: 4});
  try {
    const api = client(fake);
    const {run} = await api.createRun(body('s-restart:0', 'SCENARIO:steps'));
    let restarted = false;
    const seen = await follow(api, run.name, {
      onEvent: e => {
        if (!restarted && e.type === 'tool_end') {
          restarted = true;
          fake.restart();
        }
      },
    });
    assert.ok(restarted);
    assertContiguousFromOne(seen);
    const final = await api.getRun(run.name);
    assert.deepEqual(seen.at(-1), {
      seq: fake.runs()[0]?.lastSeq,
      type: 'end',
      data: {
        phase: 'Succeeded',
        reason: final.reason,
        result: final.result,
        outputs: final.outputs,
      },
    });
    assert.equal(
      fake.requests().filter(r => r.path.endsWith('/events')).length,
      2,
    );
  } finally {
    await fake.close();
  }
});

test('restart after the run turned terminal while disconnected: one status-built end, then return', async () => {
  const fake = await startFakeApi({stepMs: 100, steps: 2});
  try {
    const api = client(fake);
    const {run} = await api.createRun(body('s-gone:0', 'SCENARIO:steps'));
    const ctl = new AbortController();
    const first = await follow(api, run.name, {
      signal: ctl.signal,
      onEvent: e => {
        if (e.type === 'tool_end') ctl.abort();
      },
    });
    const last = first.at(-1);
    assert.ok(last);
    assert.equal(last.type, 'tool_end');
    await waitForRun(fake, r => r.phase === 'Succeeded');
    fake.restart();
    const second = await follow(api, run.name, {afterSeq: last.seq});
    const final = await api.getRun(run.name);
    assert.deepEqual(second, [
      {
        seq: fake.runs()[0]?.lastSeq,
        type: 'end',
        data: {
          phase: final.phase,
          reason: final.reason,
          result: final.result,
          outputs: final.outputs,
        },
      },
    ]);
    assert.deepEqual(
      fake
        .requests()
        .filter(r => r.path.endsWith('/events'))
        .map(r => r.lastEventId),
      [undefined, String(last.seq)],
    );
  } finally {
    await fake.close();
  }
});

test('a drop while the run finishes yields a synthetic end built from GET with seq highest + 1', async () => {
  const fake = await startFakeApi({stepMs: 100, steps: 3});
  try {
    let offline = false;
    const api = client(fake, {
      fetchImpl: (input, init) =>
        offline && input.endsWith('/events')
          ? Promise.reject(new TypeError('fetch failed'))
          : fetch(input, init),
    });
    const {run} = await api.createRun(body('s-synth:0', 'SCENARIO:steps'));
    const seen = await follow(api, run.name, {
      onEvent: e => {
        if (!offline && e.type === 'tool_end') {
          offline = true;
          fake.restart();
        }
      },
    });
    const cut = seen.findIndex(e => e.type === 'tool_end');
    const final = await api.getRun(run.name);
    assert.equal(final.phase, 'Succeeded');
    assert.equal(seen.length, cut + 2);
    assert.deepEqual(seen.at(-1), {
      seq: (seen[cut]?.seq ?? 0) + 1,
      type: 'end',
      data: {
        phase: final.phase,
        reason: final.reason,
        result: final.result,
        outputs: final.outputs,
      },
    });
  } finally {
    await fake.close();
  }
});

test('an expired id_token gets one forceRefresh and succeeds; a second 401 surfaces unauthorized', async () => {
  const fake = await startFakeApi();
  try {
    let fresh = false;
    let refreshes = 0;
    const api = client(fake, {
      getToken: () => Promise.resolve(idToken(fresh ? 3600 : -10)),
      forceRefresh: () => {
        refreshes++;
        fresh = true;
        return Promise.resolve();
      },
    });
    assert.deepEqual(
      (await api.profiles()).map(p => p.name),
      ['generic', 'golang'],
    );
    assert.equal(refreshes, 1);

    let staleRefreshes = 0;
    const stale = client(fake, {
      getToken: () => Promise.resolve(idToken(-10)),
      forceRefresh: () => {
        staleRefreshes++;
        return Promise.resolve();
      },
    });
    await assert.rejects(
      stale.profiles(),
      err =>
        err instanceof ApiError &&
        err.code === 'unauthorized' &&
        err.status === 401,
    );
    assert.equal(staleRefreshes, 1);
  } finally {
    await fake.close();
  }
});

test('drop: reconnects after each destroyed socket, reads getToken on each connect, yields every seq once', async () => {
  const fake = await startFakeApi({stepMs: 600});
  try {
    const state = {highest: 0};
    const {fetchImpl, connects} = recordEvents(state);
    let gets = 0;
    const api = client(fake, {
      fetchImpl,
      getToken: () => Promise.resolve(idToken(3600, {n: ++gets})),
    });
    const {run} = await api.createRun(body('s-drop:0', 'SCENARIO:drop'));
    const seen = await follow(api, run.name, {
      onEvent: e => (state.highest = e.seq),
    });
    assert.equal(connects.length, 3);
    assert.deepEqual(
      connects.map(c => c.lastEventId),
      [null, String(connects[1]?.highest), String(connects[2]?.highest)],
    );
    assert.ok(highestAt(connects, 1) > 0);
    assert.ok(highestAt(connects, 2) > highestAt(connects, 1));
    const ns = connects.map(c => Number(c.claims.n));
    assert.ok(
      ns.every((n, i) => i === 0 || n > (ns[i - 1] ?? 0)),
      `getToken counters per connect: ${ns.join(',')}`,
    );
    assertContiguousFromOne(seen);
    assert.equal(seen.at(-1)?.type, 'end');
    assert.equal(seen.at(-1)?.seq, fake.runs()[0]?.lastSeq);
  } finally {
    await fake.close();
  }
});

test('followEvents: a connect that gets 401 twice throws unauthorized after exactly one forceRefresh', async () => {
  const fake = await startFakeApi();
  try {
    let gets = 0;
    let refreshes = 0;
    const api = client(fake, {
      getToken: () => {
        gets++;
        return Promise.resolve(idToken(-10));
      },
      forceRefresh: () => {
        refreshes++;
        return Promise.resolve();
      },
    });
    await assert.rejects(
      follow(api, 'generic-x'),
      err =>
        err instanceof ApiError &&
        err.code === 'unauthorized' &&
        err.status === 401,
    );
    assert.equal(refreshes, 1);
    assert.equal(gets, 2);
  } finally {
    await fake.close();
  }
});

test('followEvents on an unknown run throws not_found at once', async () => {
  const fake = await startFakeApi();
  try {
    await assert.rejects(
      follow(client(fake), 'generic-missing'),
      isApiError('not_found'),
    );
    assert.equal(fake.requests().length, 1);
  } finally {
    await fake.close();
  }
});

test('a 400 from createRun surfaces bad_request', async () => {
  const fake = await startFakeApi();
  try {
    await assert.rejects(
      client(fake).createRun(body('', 'hi')),
      isApiError('bad_request'),
    );
  } finally {
    await fake.close();
  }
});

test('createRun reports created then existing for the same clientKey', async () => {
  const fake = await startFakeApi();
  try {
    const api = client(fake);
    const first = await api.createRun(body('s-idem:0', 'hi'));
    const again = await api.createRun(body('s-idem:0', 'hi'));
    assert.equal(first.created, true);
    assert.equal(again.created, false);
    assert.equal(again.run.name, first.run.name);
    assert.deepEqual(
      (await api.listRuns()).map(r => r.name),
      [first.run.name],
    );
  } finally {
    await fake.close();
  }
});

test('steer is accepted while running and returns terminal on a finished run', async () => {
  const fake = await startFakeApi({stepMs: 50});
  try {
    const api = client(fake);
    const {run} = await api.createRun(body('s-steer:0', 'SCENARIO:report'));
    assert.equal(await api.steer(run.name, 'also check pkg9'), 'accepted');
    await waitForRun(fake, r => r.phase === 'Succeeded');
    assert.equal(await api.steer(run.name, 'too late'), 'terminal');
  } finally {
    await fake.close();
  }
});

test(
  'expireStreams: the stream closes at the token exp and the next connect sends a newer bearer',
  {timeout: 20_000},
  async () => {
    const issuer = await startFakeIssuer({idTokenTtlS: 3});
    const fake = await startFakeApi({stepMs: 1000, steps: 6, hbMs: 500});
    try {
      interface Tokens {
        id_token: string;
        refresh_token: string;
      }
      const grant = async (params: Record<string, string>): Promise<Tokens> => {
        const res = await fetch(`${issuer.url}/token`, {
          method: 'POST',
          body: new URLSearchParams({client_id: 'skua-omp', ...params}),
        });
        assert.equal(res.status, 200);
        return (await res.json()) as Tokens;
      };
      const device = (await (
        await fetch(`${issuer.url}/device_authorization`, {
          method: 'POST',
          body: new URLSearchParams({client_id: 'skua-omp'}),
        })
      ).json()) as {device_code: string; user_code: string};
      issuer.approveDevice(device.user_code);
      let current = await grant({
        grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
        device_code: device.device_code,
      });
      const refresh = async (): Promise<void> => {
        current = await grant({
          grant_type: 'refresh_token',
          refresh_token: current.refresh_token,
        });
      };
      const api = client(fake, {
        getToken: async () => {
          if (Number(claimsOf(current.id_token).exp) * 1000 - Date.now() < 1000)
            await refresh();
          return current.id_token;
        },
        forceRefresh: refresh,
      });
      const {run} = await api.createRun(body('s-exp:0', 'SCENARIO:steps'));
      const seen = await follow(api, run.name);
      const connects = fake.requests().filter(r => r.path.endsWith('/events'));
      assert.ok(
        connects.length >= 2,
        `events connects: ${String(connects.length)}`,
      );
      assert.ok(connects.every(c => c.status === 200));
      const exps = connects.map(c => c.exp ?? 0);
      assert.ok(
        exps.every((exp, i) => i === 0 || exp > (exps[i - 1] ?? 0)),
        `exp per connect: ${exps.join(',')}`,
      );
      assertContiguousFromOne(seen);
      assert.equal(seen.at(-1)?.type, 'end');
      assert.equal(seen.at(-1)?.seq, fake.runs()[0]?.lastSeq);
    } finally {
      await fake.close();
      await issuer.close();
    }
  },
);

test(
  'rateLimit: createRun succeeds on the third attempt after Retry-After, and surfaces rate_limited after 3 retries',
  {timeout: 20_000},
  async () => {
    const limited = await startFakeApi({
      rateLimit: {path: '/v1alpha1/runs', times: 2, retryAfterS: 1},
    });
    try {
      const start = performance.now();
      const {created} = await client(limited).createRun(body('s-rl:0', 'hi'));
      assert.equal(created, true);
      assert.ok(performance.now() - start >= 2000);
      assert.deepEqual(
        limited
          .requests()
          .filter(r => r.path === '/v1alpha1/runs')
          .map(r => r.status),
        [429, 429, 201],
      );
    } finally {
      await limited.close();
    }

    const blocked = await startFakeApi({
      rateLimit: {path: '/v1alpha1/runs', times: 5, retryAfterS: 1},
    });
    try {
      await assert.rejects(
        client(blocked).createRun(body('s-rl:1', 'hi')),
        err =>
          err instanceof ApiError &&
          err.code === 'rate_limited' &&
          err.retryAfterMs === 1000,
      );
      assert.equal(
        blocked.requests().filter(r => r.path === '/v1alpha1/runs').length,
        4,
      );
    } finally {
      await blocked.close();
    }
  },
);

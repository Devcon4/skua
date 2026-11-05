import type {Run, RunEvent} from '../../contract/wire.ts';
import {ApiError} from './errors.ts';
import {heartbeatWatchdog} from './heartbeat.ts';
import type {Watchdog} from './heartbeat.ts';
import {runPath, tryAbort} from './http.ts';
import type {FetchImpl, Reply} from './http.ts';
import {
  BACKOFF_MAX_MS,
  BACKOFF_MIN_MS,
  RETRY_AFTER_CAP_MS,
  retryAfterMs,
  sleep,
  tokenStep,
} from './retry.ts';
import type {Timers} from './retry.ts';
import {isUnseen, toRunEvent} from './run-events.ts';
import {parseSse} from './sse.ts';
import type {ReadSseEvents} from './sse.ts';

export interface FollowOptions {
  afterSeq: number;
  signal: AbortSignal;
  heartbeatTimeoutMs: number;
  giveUpMs: number;
}

export interface FollowDeps<H> extends Timers<H> {
  base: string;
  fetchImpl: FetchImpl;
  getToken: () => Promise<string>;
  forceRefresh: () => Promise<unknown>;
  now: () => number;
  errorFor: (method: string, path: string, reply: Reply) => ApiError;
  getRun: (name: string, signal?: AbortSignal) => Promise<Run>;
  readSse: ReadSseEvents;
}

interface FollowState {
  highest: number;
  backoff: number;
  failingSince: number | undefined;
  refreshed: boolean;
}

interface FollowContext<H> {
  deps: FollowDeps<H>;
  name: string;
  path: string;
  follow: FollowOptions;
  state: FollowState;
}

type ConnectOutcome =
  {kind: 'stop'} | {kind: 'reconnect'} | {kind: 'drop'; waitMs: number};

type StreamEnd = 'stop' | 'drop';

type DropVerdict = 'stop' | 'retry';

const STOP: ConnectOutcome = {kind: 'stop'};
const RECONNECT: ConnectOutcome = {kind: 'reconnect'};

function dropped(waitMs: number): ConnectOutcome {
  return {kind: 'drop', waitMs};
}

function followCancelled(follow: FollowOptions): boolean {
  return follow.signal.aborted;
}

function runIsTerminal(run: Run): boolean {
  return run.phase === 'Succeeded' || run.phase === 'Failed';
}

function statusIsPermanentFailure(status: number): boolean {
  return status >= 300 && status < 500 && status !== 429;
}

async function discard(res: Response): Promise<void> {
  await res.body?.cancel().catch(() => undefined);
}

async function fetchOrUndefined(
  fetchImpl: FetchImpl,
  url: string,
  init: RequestInit,
): Promise<Response | undefined> {
  try {
    return await fetchImpl(url, init);
  } catch {
    return undefined;
  }
}

function streamHeaders(token: string, highest: number): Record<string, string> {
  const headers: Record<string, string> = {
    authorization: `Bearer ${token}`,
    accept: 'text/event-stream',
    'cache-control': 'no-cache',
  };
  if (highest > 0) headers['last-event-id'] = String(highest);
  return headers;
}

async function runAfterDrop(
  getRun: FollowDeps<unknown>['getRun'],
  name: string,
  signal: AbortSignal,
): Promise<Run | undefined> {
  try {
    return await getRun(name, signal);
  } catch (err) {
    if (signal.aborted) return undefined;
    if (
      err instanceof ApiError &&
      (err.code === 'not_found' || err.code === 'unauthorized')
    )
      throw err;
    return undefined;
  }
}

async function reauthorize<H>(
  {deps, path, state}: FollowContext<H>,
  res: Response,
  watchdog: Watchdog,
): Promise<ConnectOutcome> {
  await discard(res);
  if (state.refreshed)
    throw new ApiError(
      401,
      'unauthorized',
      `GET /${path}: unauthorized after a token refresh`,
    );
  state.refreshed = true;
  watchdog.disarm();
  await tokenStep(deps.forceRefresh);
  return RECONNECT;
}

async function rejectedReply<H>(
  {deps, path}: FollowContext<H>,
  res: Response,
): Promise<ConnectOutcome> {
  if (statusIsPermanentFailure(res.status)) {
    throw deps.errorFor('GET', path, {
      status: res.status,
      text: await res.text().catch(() => ''),
      headers: res.headers,
    });
  }
  await discard(res);
  if (res.status !== 429) return dropped(0);
  return dropped(
    Math.min(retryAfterMs(res.headers, deps.now) ?? 0, RETRY_AFTER_CAP_MS),
  );
}

async function* yieldUnseenEvents<H>(
  {deps, follow, state}: FollowContext<H>,
  body: ReadableStream<Uint8Array>,
  watchdog: Watchdog,
): AsyncGenerator<RunEvent, StreamEnd> {
  const onChunk = (): void => {
    state.failingSince = undefined;
    watchdog.arm();
  };
  try {
    for await (const item of parseSse(deps.readSse, body, onChunk)) {
      if (followCancelled(follow)) return 'stop';
      const ev = toRunEvent(item);
      if (!isUnseen(ev, state.highest)) continue;
      state.highest = ev.seq;
      state.backoff = BACKOFF_MIN_MS;
      watchdog.disarm();
      yield ev;
      if (ev.type === 'end') return 'stop';
      watchdog.arm();
    }
    return 'drop';
  } catch {
    return 'drop';
  }
}

async function* relayStream<H>(
  ctx: FollowContext<H>,
  body: ReadableStream<Uint8Array>,
  watchdog: Watchdog,
): AsyncGenerator<RunEvent, ConnectOutcome> {
  ctx.state.refreshed = false;
  const end = yield* yieldUnseenEvents(ctx, body, watchdog);
  if (end === 'stop' || followCancelled(ctx.follow)) return STOP;
  return dropped(0);
}

async function* handleReply<H>(
  ctx: FollowContext<H>,
  res: Response | undefined,
  watchdog: Watchdog,
): AsyncGenerator<RunEvent, ConnectOutcome> {
  if (res === undefined) return dropped(0);
  if (res.status === 401) return await reauthorize(ctx, res, watchdog);
  if (res.status === 404) {
    await discard(res);
    throw new ApiError(
      404,
      'not_found',
      `GET /${ctx.path}: no run ${ctx.name}`,
    );
  }
  if (res.status === 200 && res.body)
    return yield* relayStream(ctx, res.body, watchdog);
  return await rejectedReply(ctx, res);
}

async function* connectOnce<H>(
  ctx: FollowContext<H>,
): AsyncGenerator<RunEvent, ConnectOutcome> {
  const {deps, follow, path, state} = ctx;
  const conn = new AbortController();
  const onAbort = (): void => {
    conn.abort();
  };
  follow.signal.addEventListener('abort', onAbort, {once: true});
  const watchdog = heartbeatWatchdog(deps, follow.heartbeatTimeoutMs, () => {
    tryAbort(conn);
  });
  try {
    const token = await tokenStep(deps.getToken);
    if (followCancelled(follow)) return STOP;
    const headers = streamHeaders(token, state.highest);
    watchdog.arm();
    const res = await fetchOrUndefined(deps.fetchImpl, deps.base + path, {
      method: 'GET',
      headers,
      signal: conn.signal,
    });
    if (followCancelled(follow)) return STOP;
    return yield* handleReply(ctx, res, watchdog);
  } finally {
    watchdog.disarm();
    follow.signal.removeEventListener('abort', onAbort);
    conn.abort();
  }
}

function endFromRun(run: Run, highest: number): RunEvent {
  return {
    seq: highest + 1,
    type: 'end',
    data: {
      phase: run.phase,
      reason: run.reason,
      result: run.result,
      outputs: run.outputs,
    },
  };
}

async function* settleDrop<H>(
  {deps, name, path, follow, state}: FollowContext<H>,
  waitMs: number,
): AsyncGenerator<RunEvent, DropVerdict> {
  const failingSince = (state.failingSince ??= deps.now());
  const run = await runAfterDrop(deps.getRun, name, follow.signal);
  if (followCancelled(follow)) return 'stop';
  if (run && runIsTerminal(run)) {
    yield endFromRun(run, state.highest);
    return 'stop';
  }
  if (deps.now() - failingSince >= follow.giveUpMs)
    throw new ApiError(
      0,
      'network',
      `GET /${path}: no contact with skua-api for ${String(follow.giveUpMs)} ms`,
    );
  const slept = await sleep(
    deps,
    Math.max(state.backoff, waitMs),
    follow.signal,
  );
  if (!slept) return 'stop';
  state.backoff = Math.min(state.backoff * 2, BACKOFF_MAX_MS);
  return 'retry';
}

export async function* followEvents<H>(
  deps: FollowDeps<H>,
  name: string,
  follow: FollowOptions,
): AsyncGenerator<RunEvent> {
  const ctx: FollowContext<H> = {
    deps,
    name,
    follow,
    path: runPath(name, '/events'),
    state: {
      highest: follow.afterSeq,
      backoff: BACKOFF_MIN_MS,
      failingSince: undefined,
      refreshed: false,
    },
  };
  while (!follow.signal.aborted) {
    const outcome = yield* connectOnce(ctx);
    if (outcome.kind === 'stop') return;
    if (outcome.kind === 'reconnect') continue;
    const verdict = yield* settleDrop(ctx, outcome.waitMs);
    if (verdict === 'stop') return;
  }
}

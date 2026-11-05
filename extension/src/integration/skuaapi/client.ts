import {isProfile, isRun} from '../../contract/wire.ts';
import type {
  CreateRunBody,
  Profile,
  Run,
  RunEvent,
} from '../../contract/wire.ts';
import {ApiError} from './errors.ts';
import type {ApiErrorCode} from './errors.ts';
import {followEvents} from './follow.ts';
import type {FollowDeps, FollowOptions} from './follow.ts';
import {parseJson, runPath, tryAbort} from './http.ts';
import type {FetchImpl, Reply} from './http.ts';
import {
  BACKOFF_MIN_MS,
  RETRY_AFTER_CAP_MS,
  messageOf,
  retryAfterMs,
  sleep,
  tokenStep,
} from './retry.ts';
import type {ReadSseEvents} from './sse.ts';

export interface ApiOptions<H> {
  endpoint: string;
  getToken: () => Promise<string>;
  forceRefresh: () => Promise<unknown>;
  fetchImpl?: FetchImpl;
  setTimer: (fn: () => void, ms: number) => H;
  clearTimer: (handle: H) => void;
  now?: () => number;
  readSse: ReadSseEvents;
}

export interface Api {
  profiles(): Promise<Profile[]>;
  createRun(body: CreateRunBody): Promise<{created: boolean; run: Run}>;
  getRun(name: string): Promise<Run>;
  listRuns(): Promise<Run[]>;
  steer(name: string, message: string): Promise<'accepted' | 'terminal'>;
  abort(name: string): Promise<void>;
  followEvents(name: string, opts: FollowOptions): AsyncGenerator<RunEvent>;
}

const REQUEST_TIMEOUT_MS = 15_000;
const RATE_LIMIT_RETRIES = 3;

function codeFor(status: number): ApiErrorCode {
  if (status === 400) return 'bad_request';
  if (status === 401 || status === 403) return 'unauthorized';
  if (status === 404) return 'not_found';
  if (status === 409) return 'conflict';
  if (status === 429) return 'rate_limited';
  if (status >= 500) return 'server';
  return 'bad_request';
}

function jsonRequest(
  method: string,
  token: string,
  body: unknown,
  signal: AbortSignal,
): RequestInit {
  const headers: Record<string, string> = {
    authorization: `Bearer ${token}`,
    accept: 'application/json',
  };
  if (body === undefined) return {method, headers, signal};
  headers['content-type'] = 'application/json';
  return {method, headers, body: JSON.stringify(body), signal};
}

function networkFailure(
  deadlinePassed: boolean,
  signal: AbortSignal | undefined,
  err: unknown,
): string {
  if (deadlinePassed)
    return `timed out after ${String(REQUEST_TIMEOUT_MS / 1000)} s`;
  if (signal?.aborted) return 'aborted';
  return messageOf(err);
}

export function createApi<H>(opts: ApiOptions<H>): Api {
  const {getToken, forceRefresh, setTimer, clearTimer} = opts;
  const now = opts.now ?? Date.now;
  const base = opts.endpoint.endsWith('/')
    ? opts.endpoint
    : `${opts.endpoint}/`;
  const fetchImpl: FetchImpl = opts.fetchImpl ?? fetch;

  const errorFor = (method: string, path: string, reply: Reply): ApiError => {
    const body = parseJson(reply.text);
    const detail =
      typeof body === 'object' &&
      body !== null &&
      'error' in body &&
      typeof body.error === 'string'
        ? body.error
        : `HTTP ${String(reply.status)}`;
    const after =
      reply.status === 429 ? retryAfterMs(reply.headers, now) : undefined;
    return new ApiError(
      reply.status,
      codeFor(reply.status),
      `${method} /${path}: ${detail}`,
      after,
    );
  };

  const send = async (
    method: string,
    path: string,
    token: string,
    body: unknown,
    signal: AbortSignal | undefined,
  ): Promise<Reply> => {
    const ctl = new AbortController();
    const onAbort = (): void => {
      ctl.abort();
    };
    if (signal?.aborted) ctl.abort();
    signal?.addEventListener('abort', onAbort, {once: true});
    const deadline = {passed: false};
    const timer = setTimer(() => {
      deadline.passed = true;
      tryAbort(ctl);
    }, REQUEST_TIMEOUT_MS);
    try {
      const res = await fetchImpl(
        base + path,
        jsonRequest(method, token, body, ctl.signal),
      );
      const text = await res.text();
      return {status: res.status, text, headers: res.headers};
    } catch (err) {
      const why = networkFailure(deadline.passed, signal, err);
      throw new ApiError(0, 'network', `${method} /${path}: ${why}`);
    } finally {
      clearTimer(timer);
      signal?.removeEventListener('abort', onAbort);
    }
  };

  const call = async (
    method: string,
    path: string,
    body?: unknown,
    signal?: AbortSignal,
  ): Promise<Reply> => {
    let refreshed = false;
    let limited = 0;
    for (;;) {
      const reply = await send(
        method,
        path,
        await tokenStep(getToken),
        body,
        signal,
      );
      if (reply.status === 401 && !refreshed) {
        refreshed = true;
        await tokenStep(forceRefresh);
        continue;
      }
      if (reply.status === 429 && limited < RATE_LIMIT_RETRIES) {
        limited++;
        const wait = Math.min(
          retryAfterMs(reply.headers, now) ?? BACKOFF_MIN_MS,
          RETRY_AFTER_CAP_MS,
        );
        if (!(await sleep(opts, wait, signal)))
          throw new ApiError(0, 'network', `${method} /${path}: aborted`);
        continue;
      }
      if (reply.status >= 200 && reply.status < 300) return reply;
      throw errorFor(method, path, reply);
    }
  };

  const invalid = (method: string, path: string, what: string): ApiError =>
    new ApiError(0, 'server', `${method} /${path}: response is not ${what}`);

  const readRun = (method: string, path: string, reply: Reply): Run => {
    const v = parseJson(reply.text);
    if (!isRun(v)) throw invalid(method, path, 'a Run');
    return v;
  };

  const getRun = async (name: string, signal?: AbortSignal): Promise<Run> => {
    const path = runPath(name);
    return readRun('GET', path, await call('GET', path, undefined, signal));
  };

  const follow: FollowDeps<H> = {
    base,
    fetchImpl,
    getToken,
    forceRefresh,
    setTimer,
    clearTimer,
    now,
    errorFor,
    getRun,
    readSse: opts.readSse,
  };

  return {
    profiles: async () => {
      const path = 'v1alpha1/profiles';
      const v = parseJson((await call('GET', path)).text);
      if (!Array.isArray(v) || !v.every(isProfile))
        throw invalid('GET', path, 'a profile list');
      return v;
    },
    createRun: async body => {
      const path = 'v1alpha1/runs';
      const reply = await call('POST', path, body);
      return {created: reply.status === 201, run: readRun('POST', path, reply)};
    },
    getRun,
    listRuns: async () => {
      const path = 'v1alpha1/runs';
      const v = parseJson((await call('GET', path)).text);
      if (!Array.isArray(v) || !v.every(isRun))
        throw invalid('GET', path, 'a run list');
      return v;
    },
    steer: async (name, message) => {
      try {
        await call('POST', runPath(name, '/steer'), {message});
        return 'accepted';
      } catch (err) {
        if (err instanceof ApiError && err.code === 'conflict')
          return 'terminal';
        throw err;
      }
    },
    abort: async name => {
      await call('POST', runPath(name, '/abort'));
    },
    followEvents: (name, options) => followEvents(follow, name, options),
  };
}

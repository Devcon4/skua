import {ApiError} from './errors.ts';

export const RETRY_AFTER_CAP_MS = 30_000;
export const BACKOFF_MIN_MS = 1_000;
export const BACKOFF_MAX_MS = 30_000;

export interface Timers<H> {
  setTimer: (fn: () => void, ms: number) => H;
  clearTimer: (handle: H) => void;
}

export function messageOf(err: unknown): string {
  if (!(err instanceof Error)) return String(err);
  const cause = err.cause instanceof Error ? `: ${err.cause.message}` : '';
  return `${err.message}${cause}`;
}

export function sleep<H>(
  {setTimer, clearTimer}: Timers<H>,
  ms: number,
  signal?: AbortSignal,
): Promise<boolean> {
  if (signal?.aborted) return Promise.resolve(false);
  const {promise, resolve} = Promise.withResolvers<boolean>();
  const done = (ok: boolean): void => {
    clearTimer(timer);
    signal?.removeEventListener('abort', onAbort);
    resolve(ok);
  };
  const onAbort = (): void => {
    done(false);
  };
  const timer = setTimer(() => {
    done(true);
  }, ms);
  signal?.addEventListener('abort', onAbort, {once: true});
  return promise;
}

export async function tokenStep<T>(step: () => Promise<T>): Promise<T> {
  try {
    return await step();
  } catch (err) {
    if (err instanceof ApiError) throw err;
    throw new ApiError(401, 'unauthorized', messageOf(err));
  }
}

export function retryAfterMs(
  headers: Headers,
  now: () => number,
): number | undefined {
  const v = headers.get('retry-after')?.trim();
  if (!v) return undefined;
  if (/^\d+$/.test(v)) return Number(v) * 1000;
  const at = Date.parse(v);
  return Number.isNaN(at) ? undefined : Math.max(0, at - now());
}

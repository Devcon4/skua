export interface RelayTimers<H> {
  setTimer: (fn: () => void, ms: number) => H;
  clearTimer: (handle: H) => void;
}

/** Resolves true after `ms`, or false as soon as `signal` aborts. */
export function sleepUnlessAborted<H>(
  timers: RelayTimers<H>,
  ms: number,
  signal: AbortSignal,
): Promise<boolean> {
  if (signal.aborted) return Promise.resolve(false);
  const {promise, resolve} = Promise.withResolvers<boolean>();
  const finish = (slept: boolean): void => {
    timers.clearTimer(timer);
    signal.removeEventListener('abort', onAbort);
    resolve(slept);
  };
  const onAbort = (): void => {
    finish(false);
  };
  const timer = timers.setTimer(() => {
    finish(true);
  }, ms);
  signal.addEventListener('abort', onAbort, {once: true});
  return promise;
}

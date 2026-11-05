import type {Timers} from './retry.ts';

export interface Watchdog {
  arm: () => void;
  disarm: () => void;
}

/** Calls `onSilence` once `timeoutMs` passes after the latest `arm`, unless `disarm` runs first. */
export function heartbeatWatchdog<H>(
  {setTimer, clearTimer}: Timers<H>,
  timeoutMs: number,
  onSilence: () => void,
): Watchdog {
  let timer: {handle: H} | undefined;
  const disarm = (): void => {
    if (timer) clearTimer(timer.handle);
    timer = undefined;
  };
  const arm = (): void => {
    disarm();
    timer = {handle: setTimer(onSilence, timeoutMs)};
  };
  return {arm, disarm};
}

import {ApiError} from '../integration/skuaapi/errors.ts';

const NOT_LOGGED_IN = 'Not logged in to skua. Run /skua login.';

export function relayErrorText(err: unknown): string {
  return `skua relay error: ${err instanceof Error ? err.message : String(err)}`;
}

export function createErrorText(err: unknown, profile: string): string {
  if (!(err instanceof ApiError)) return relayErrorText(err);
  switch (err.code) {
    case 'not_found':
      return `skua profile ${profile} is unknown to skua-api. Run /skua sync.`;
    case 'conflict':
      return `skua profile ${profile} is not Ready.`;
    case 'unauthorized':
      return NOT_LOGGED_IN;
    case 'bad_request':
      return `skua-api rejected the run: ${err.message}`;
    case 'rate_limited':
      return 'skua-api is rate limiting new runs. Ask again later.';
    default:
      return relayErrorText(err);
  }
}

export function streamErrorText(err: unknown, run: string | undefined): string {
  if (err instanceof ApiError) {
    if (err.code === 'unauthorized') return NOT_LOGGED_IN;
    if (run !== undefined && err.code === 'network') {
      return `Lost contact with skua-api for run ${run}; it may still be running. Send this agent a message to reattach, or run /skua abort ${run}.`;
    }
    if (run !== undefined && err.code === 'not_found')
      return `Remote run ${run} no longer exists.`;
  }
  return relayErrorText(err);
}

export function unpushedText(sha: string): string {
  return `HEAD ${sha} is not on origin. Push it so the remote agent sees the same code, then ask again.`;
}

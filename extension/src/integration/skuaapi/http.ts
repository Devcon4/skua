export type FetchImpl = (input: string, init: RequestInit) => Promise<Response>;

export interface Reply {
  status: number;
  text: string;
  headers: Headers;
}

export function parseJson(text: string): unknown {
  try {
    const v: unknown = JSON.parse(text);
    return v;
  } catch {
    return undefined;
  }
}

export function runPath(name: string, sub = ''): string {
  return `v1alpha1/runs/${encodeURIComponent(name)}${sub}`;
}

/** Aborts `ctl`; returns false instead of throwing if the abort throws. */
export function tryAbort(ctl: AbortController): boolean {
  try {
    ctl.abort();
    return true;
  } catch {
    return false;
  }
}

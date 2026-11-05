export const STEP_ID = /^skua_([a-z0-9-]+)_(\d+)$/;
export const YIELD_ID = /^skua_yield_([a-z0-9-]+)$/;

export function followUpOf(clientKey: string): number | undefined {
  const suffix = clientKey.slice(clientKey.lastIndexOf(':') + 1);
  return /^\d+$/.test(suffix) ? Number(suffix) : undefined;
}

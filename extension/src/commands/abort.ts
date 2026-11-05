import {errText, readConfig} from './deps.ts';
import type {CommandDeps} from './deps.ts';

export async function runAbort(run: string, deps: CommandDeps): Promise<void> {
  const cfg = readConfig(deps);
  if (cfg === undefined) return;
  try {
    await deps.services(cfg).api.abort(run);
  } catch (err) {
    deps.notify(`skua abort ${run}: ${errText(err)}`, 'error');
    return;
  }
  deps.notify(`skua abort ${run}: requested`, 'info');
}

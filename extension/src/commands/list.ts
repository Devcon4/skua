import type {Run} from '../contract/wire.ts';
import {errText, readConfig} from './deps.ts';
import type {CommandDeps} from './deps.ts';
import {formatRuns} from './format.ts';

export async function runList(deps: CommandDeps): Promise<void> {
  const cfg = readConfig(deps);
  if (cfg === undefined) return;
  const {api, relay} = deps.services(cfg);
  let runs: Run[];
  try {
    runs = await api.listRuns();
  } catch (err) {
    deps.notify(`skua list failed: ${errText(err)}`, 'error');
    return;
  }
  deps.notify(formatRuns(runs, relay.live()), 'info');
}

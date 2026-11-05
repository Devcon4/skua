import {readConfig} from './deps.ts';
import type {CommandDeps} from './deps.ts';
import {listed, profileNames} from './format.ts';

export async function runSync(deps: CommandDeps): Promise<void> {
  const cfg = readConfig(deps);
  if (cfg === undefined) return;
  const res = await deps.sync(cfg);
  if (!res.ok) {
    deps.notify(`skua sync failed: ${res.error.message}`, 'error');
    return;
  }
  const names = profileNames([...res.files.written, ...res.files.unchanged]);
  let text = `skua sync: ${String(names.length)} profiles (${listed(names)}); removed ${listed(profileNames(res.files.removed))}`;
  if (res.files.skipped.length > 0)
    text += `; skipped ${res.files.skipped.join(', ')} (not a DNS-1123 label)`;
  if (res.files.written.length > 0)
    text +=
      '. New agents are dispatchable now; the task tool description lists them after the next base-prompt rebuild.';
  deps.notify(text, 'info');
}

import type {CommandDeps} from './deps.ts';

export const USAGE = [
  'Usage: /skua <subcommand>',
  '  login [<endpoint> <issuer> <clientId>] [--device]  sign in; the three values are required the first time and replace the stored ones',
  '  sync                                               refresh the profile list and the skua-<profile> agent files',
  '  list                                               list your skua runs',
  '  abort <run>                                        cancel a run',
  '  status                                             show login, omp version, profile cache and relayed runs',
].join('\n');

export function usage(deps: CommandDeps, error: string | undefined): void {
  if (error === undefined) {
    deps.notify(USAGE, 'info');
    return;
  }
  deps.notify(`skua: ${error}\n${USAGE}`, 'error');
}

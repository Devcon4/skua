import {runAbort} from './abort.ts';
import {errText} from './deps.ts';
import type {CommandDeps} from './deps.ts';
import {runList} from './list.ts';
import {runLogin} from './login.ts';
import type {LoginCommand} from './login.ts';
import {runStatus} from './status.ts';
import {runSync} from './sync.ts';
import {usage} from './usage.ts';

export type SkuaCommand =
  | LoginCommand
  | {kind: 'sync'}
  | {kind: 'list'}
  | {kind: 'abort'; run: string}
  | {kind: 'status'}
  | {kind: 'usage'; error: string | undefined};

type UsageCommand = Extract<SkuaCommand, {kind: 'usage'}>;
type ArgumentlessKind = 'sync' | 'list' | 'status';

const DEVICE_FLAG = '--device';

function usageError(error: string): UsageCommand {
  return {kind: 'usage', error};
}

function parseLogin(args: string[]): SkuaCommand {
  const device = args.includes(DEVICE_FLAG);
  const values = args.filter(a => a !== DEVICE_FLAG);
  const unknownFlag = values.find(v => v.startsWith('--'));
  if (unknownFlag !== undefined)
    return usageError(`unknown login flag ${unknownFlag}`);
  if (values.length === 0) return {kind: 'login', values: undefined, device};
  const [endpoint, issuer, clientId, ...extra] = values;
  if (
    endpoint === undefined ||
    issuer === undefined ||
    clientId === undefined ||
    extra.length > 0
  )
    return usageError(
      'login takes <endpoint> <issuer> <clientId> together, or none of them',
    );
  if (!URL.canParse(endpoint))
    return usageError(`endpoint ${endpoint} is not a URL`);
  if (!URL.canParse(issuer)) return usageError(`issuer ${issuer} is not a URL`);
  return {kind: 'login', values: {endpoint, issuer, clientId}, device};
}

function parseAbort(args: string[]): SkuaCommand {
  const [run, ...extra] = args;
  if (run === undefined || extra.length > 0)
    return usageError('abort takes exactly one run name');
  return {kind: 'abort', run};
}

function parseArgumentless(
  kind: ArgumentlessKind,
  args: string[],
): SkuaCommand {
  if (args.length > 0) return usageError(`${kind} takes no arguments`);
  return {kind};
}

export function parseSkuaCommand(args: string): SkuaCommand {
  const [sub, ...rest] = args
    .trim()
    .split(/\s+/)
    .filter(s => s.length > 0);
  switch (sub) {
    case undefined:
    case 'help':
      return {kind: 'usage', error: undefined};
    case 'login':
      return parseLogin(rest);
    case 'abort':
      return parseAbort(rest);
    case 'sync':
    case 'list':
    case 'status':
      return parseArgumentless(sub, rest);
    default:
      return usageError(`unknown subcommand ${sub}`);
  }
}

/** Runs one `/skua` command line; every outcome, including failures, is reported through `deps.notify`. Never rejects. */
export async function runSkuaCommand(
  args: string,
  deps: CommandDeps,
): Promise<void> {
  const cmd = parseSkuaCommand(args);
  try {
    switch (cmd.kind) {
      case 'usage': {
        usage(deps, cmd.error);
        return;
      }
      case 'login': {
        await runLogin(cmd, deps);
        return;
      }
      case 'sync': {
        await runSync(deps);
        return;
      }
      case 'list': {
        await runList(deps);
        return;
      }
      case 'abort': {
        await runAbort(cmd.run, deps);
        return;
      }
      case 'status': {
        runStatus(deps);
        return;
      }
    }
  } catch (err) {
    deps.notify(`skua ${cmd.kind}: ${errText(err)}`, 'error');
  }
}

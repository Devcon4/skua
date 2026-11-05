import type {Message} from '@oh-my-pi/pi-ai';
import type {Run} from '../contract/wire.ts';
import type {Api} from '../integration/skuaapi/client.ts';
import {followUpOf} from './ids.ts';
import {afterLastAssistant, findAnchor} from './messages.ts';
import type {ResolvedWorkspace, WorkspaceResolution} from './workspace.ts';

export interface OpenTool {
  name: string;
  args: unknown;
}

export interface SessionState {
  run: string;
  started: Run;
  followUp: number;
  profile: string;
  committedSeq: number;
  relayedCount: number;
  terminal?: Run | undefined;
  workspace?: ResolvedWorkspace;
  openTools: Map<string, OpenTool>;
  rejectedYieldFor?: string;
}

export interface SessionStart {
  run: Run;
  followUp: number;
  profile: string;
  committedSeq: number;
  relayedCount: number;
  workspace: WorkspaceResolution;
}

export function runIsTerminal(run: Run): boolean {
  return run.phase === 'Succeeded' || run.phase === 'Failed';
}

export function openSession(start: SessionStart): SessionState {
  const state: SessionState = {
    run: start.run.name,
    started: start.run,
    followUp: start.followUp,
    profile: start.profile,
    committedSeq: start.committedSeq,
    relayedCount: start.relayedCount,
    openTools: new Map(),
  };
  if (start.workspace.kind === 'ok') state.workspace = start.workspace;
  return state;
}

export function adoptFollowUpRun(
  state: SessionState,
  run: Run,
  followUp: number,
): void {
  state.run = run.name;
  state.started = run;
  state.followUp = followUp;
  state.committedSeq = 0;
  state.terminal = undefined;
  state.openTools = new Map();
}

async function latestRunOfSession(
  api: Api,
  sessionId: string,
): Promise<string | undefined> {
  const prefix = `${sessionId}:`;
  let latest: {name: string; followUp: number} | undefined;
  for (const run of await api.listRuns()) {
    if (!run.clientKey.startsWith(prefix)) continue;
    const followUp = followUpOf(run.clientKey);
    if (followUp === undefined) continue;
    if (latest === undefined || followUp > latest.followUp)
      latest = {name: run.name, followUp};
  }
  return latest?.name;
}

export async function rebuildSession(
  api: Api,
  sessionId: string,
  messages: readonly Message[],
  currentWorkspace: () => Promise<WorkspaceResolution>,
): Promise<SessionState | undefined> {
  const anchor = findAnchor(messages);
  const name = anchor?.run ?? (await latestRunOfSession(api, sessionId));
  if (name === undefined) return undefined;
  const run = await api.getRun(name);
  const workspace = await currentWorkspace();
  const state = openSession({
    run,
    followUp: followUpOf(run.clientKey) ?? 0,
    profile: run.profile,
    committedSeq: anchor?.seq ?? 0,
    relayedCount: afterLastAssistant(messages),
    workspace,
  });
  if (runIsTerminal(run)) state.terminal = run;
  return state;
}

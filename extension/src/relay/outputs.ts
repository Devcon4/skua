import type {RunOutputs} from '../contract/wire.ts';
import {prUrl} from './result.ts';
import type {ResolvedWorkspace} from './workspace.ts';

export function outputLines(
  outputs: RunOutputs,
  ws: ResolvedWorkspace | undefined,
): string[] {
  const lines: string[] = [];
  if (outputs.branch !== undefined) lines.push(`Branch: ${outputs.branch}`);
  const url = prUrl(outputs, ws);
  if (url !== undefined) lines.push(`PR: ${url}`);
  return lines;
}

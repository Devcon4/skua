import type {Run} from '../contract/wire.ts';
import type {LiveRun} from './deps.ts';

const AGENT_FILE = /^skua-(.+)\.md$/;

export function formatRuns(
  runs: readonly Run[],
  live: readonly LiveRun[],
): string {
  if (runs.length === 0) return 'skua list: no runs';
  const relayed = new Set(live.map(l => l.run));
  const lines = runs.map(r => {
    const cols = [r.name, r.profile, r.phase, r.reason || '-', r.createdAt];
    if (relayed.has(r.name)) cols.push('relayed here');
    return cols.join('  ');
  });
  return ['skua runs:', ...lines].join('\n');
}

export function formatAge(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${String(s)}s`;
  if (s < 3600) return `${String(Math.floor(s / 60))}m`;
  if (s < 86400) return `${String(Math.floor(s / 3600))}h`;
  return `${String(Math.floor(s / 86400))}d`;
}

export function profileNames(files: readonly string[]): string[] {
  return files
    .map(f => AGENT_FILE.exec(f)?.[1])
    .filter((n): n is string => n !== undefined)
    .sort();
}

export function listed(names: readonly string[]): string {
  return names.length > 0 ? names.join(', ') : 'none';
}

export function liveLine(live: readonly LiveRun[]): string {
  if (live.length === 0) return 'relayed here: none';
  return `relayed here: ${live.map(l => `${l.run} (follow-up ${String(l.followUp)})`).join(', ')}`;
}

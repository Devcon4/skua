import {mkdirSync, readdirSync, readFileSync, unlinkSync} from 'node:fs';
import {join} from 'node:path';
import type {Profile} from '../../contract/wire.ts';
import {writeAtomic} from './files.ts';

export interface AgentFilesResult {
  written: string[];
  removed: string[];
  unchanged: string[];
  skipped: string[];
}

const DNS1123_LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;
const AGENT_FILE = /^skua-.+\.md$/;
const YAML_UNSAFE_BREAKS = /[\u0085\u2028\u2029]/g;

export function renderAgentFile(p: Profile): string {
  const description =
    `Remote skua agent, profile ${p.name}: ${p.description} Models: ${p.models.join(', ')}. ` +
    'Runs in the skua cluster on the pushed HEAD of origin and returns a report or schema-checked JSON; it may push skua/<run> and open a PR.';
  const quoted = JSON.stringify(description).replace(
    YAML_UNSAFE_BREAKS,
    c => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`,
  );
  return [
    '---',
    `name: skua-${p.name}`,
    `description: ${quoted}`,
    `model: skua/${p.name}`,
    'tools: []',
    '---',
    '',
    'Relay for a remote skua run. The remote agent does the work; this session mirrors it.',
    '',
  ].join('\n');
}

function readOptional(path: string): string | undefined {
  try {
    return readFileSync(path, 'utf8');
  } catch {
    return undefined;
  }
}

export function syncAgentFiles(
  agentsDir: string,
  profiles: Profile[],
): AgentFilesResult {
  const result: AgentFilesResult = {
    written: [],
    removed: [],
    unchanged: [],
    skipped: [],
  };
  mkdirSync(agentsDir, {recursive: true});
  const keep = new Set<string>();
  for (const p of profiles) {
    if (!p.ready) continue;
    if (!DNS1123_LABEL.test(p.name)) {
      result.skipped.push(p.name);
      continue;
    }
    const name = `skua-${p.name}.md`;
    if (keep.has(name)) continue;
    keep.add(name);
    const content = renderAgentFile(p);
    if (readOptional(join(agentsDir, name)) === content) {
      result.unchanged.push(name);
      continue;
    }
    writeAtomic(agentsDir, name, content, 0o644);
    result.written.push(name);
  }
  for (const entry of readdirSync(agentsDir, {withFileTypes: true})) {
    if (
      !AGENT_FILE.test(entry.name) ||
      keep.has(entry.name) ||
      entry.isDirectory()
    )
      continue;
    unlinkSync(join(agentsDir, entry.name));
    result.removed.push(entry.name);
  }
  return result;
}

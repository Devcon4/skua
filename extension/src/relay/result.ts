import type {Run, RunOutputs} from '../contract/wire.ts';
import type {ResolvedWorkspace} from './workspace.ts';

export interface ToolLike {
  name: string;
  parameters: unknown;
}

const SCHEMA_MARKER = 'Structured output matching the schema:\n';
const OMP_LOOSE_DATA = 'Structured JSON output';
const STEP_RESULT_MAX_BYTES = 32 * 1024;

interface SchemaNode {
  type?: unknown;
  properties?: unknown;
  description?: unknown;
}

function isSchemaNode(v: unknown): v is SchemaNode {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function isPropertyMap(v: unknown): v is Record<string, unknown> {
  return isSchemaNode(v);
}

function yieldProperties(
  tools: readonly ToolLike[] | undefined,
): Record<string, unknown> | undefined {
  const params = tools?.find(t => t.name === 'yield')?.parameters;
  if (!isSchemaNode(params) || !isPropertyMap(params.properties))
    return undefined;
  return params.properties;
}

function declaresOwnShape(data: SchemaNode, description: string): boolean {
  return (
    (data.type !== undefined || data.properties !== undefined) &&
    !description.startsWith(OMP_LOOSE_DATA)
  );
}

function schemaInDescription(description: string): unknown {
  if (!description.startsWith(SCHEMA_MARKER)) return undefined;
  try {
    return JSON.parse(description.slice(SCHEMA_MARKER.length));
  } catch {
    return undefined;
  }
}

export function schemaFromYieldTool(
  tools: readonly ToolLike[] | undefined,
): unknown {
  const data = yieldProperties(tools)?.['data'];
  if (!isSchemaNode(data)) return undefined;
  const description =
    typeof data.description === 'string' ? data.description : '';
  if (declaresOwnShape(data, description)) return data;
  return schemaInDescription(description);
}

export function yieldAcceptsError(
  tools: readonly ToolLike[] | undefined,
): boolean {
  const props = yieldProperties(tools);
  return props !== undefined && Object.hasOwn(props, 'error');
}

export function prUrl(
  outputs: RunOutputs,
  ws: ResolvedWorkspace | undefined,
): string | undefined {
  const pr = outputs.pullRequest;
  if (typeof pr === 'string') return pr.startsWith('http') ? pr : undefined;
  if (typeof pr !== 'number') return undefined;
  return ws === undefined
    ? `PR #${String(pr)}`
    : `https://${ws.host}/${ws.workspace.repo}/pull/${String(pr)}`;
}

function renderResult(result: unknown): string | undefined {
  if (result === undefined || result === null) return undefined;
  return typeof result === 'string' ? result : JSON.stringify(result, null, 2);
}

function outputLines(
  outputs: RunOutputs,
  ws: ResolvedWorkspace | undefined,
): string[] {
  const lines: string[] = [];
  if (outputs.branch !== undefined) lines.push(`Branch: ${outputs.branch}`);
  const url = prUrl(outputs, ws);
  if (url !== undefined) lines.push(`PR: ${url}`);
  return lines;
}

export function finalText(run: Run, ws: ResolvedWorkspace | undefined): string {
  const blocks: string[] = [];
  if (run.phase === 'Failed')
    blocks.push(`Remote run ${run.name} failed: ${run.reason}.`);
  const result = renderResult(run.result);
  if (result !== undefined) blocks.push(result);
  const lines = outputLines(run.outputs, ws);
  if (lines.length > 0) blocks.push(lines.join('\n'));
  return blocks.join('\n\n');
}

export function followUpPrompt(prev: Run, texts: string[]): string {
  const result = renderResult(prev.result) ?? 'null';
  return `Previous skua run ${prev.name} (${prev.phase}/${prev.reason}) result:\n${result}\nOutputs: ${JSON.stringify(prev.outputs)}\n\nFollow-up:\n${texts.join('\n\n')}`;
}

export function firstPrompt(
  batchContext: string | undefined,
  assignments: string[],
): string {
  const body = assignments.join('\n\n');
  return batchContext === undefined ? body : `${batchContext}\n\n${body}`;
}

function isUtf8Continuation(byte: number | undefined): boolean {
  return byte !== undefined && (byte & 0xc0) === 0x80;
}

export function truncateStepResult(s: string): string {
  const bytes = new TextEncoder().encode(s);
  if (bytes.length <= STEP_RESULT_MAX_BYTES) return s;
  let cut = STEP_RESULT_MAX_BYTES;
  while (cut > 0 && isUtf8Continuation(bytes[cut])) cut--;
  const kept = new TextDecoder().decode(bytes.subarray(0, cut));
  return `${kept}\n[truncated ${String(bytes.length - cut)} bytes]`;
}

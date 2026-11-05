import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {cp, mkdir, mkdtemp, readFile, rm, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import {fileURLToPath} from 'node:url';
import type {Profile} from '../../src/contract/wire.ts';
import {loadConfig, saveConfig} from '../../src/support/auth/config.ts';
import type {SkuaConfigInput} from '../../src/support/auth/config.ts';
import {discover} from '../../src/support/auth/discovery.ts';
import {tokenSet} from '../../src/support/auth/idtoken.ts';
import {saveToken} from '../../src/support/auth/token.ts';
import {syncAgentFiles} from '../../src/support/profiles/agents.ts';
import {writeCachedProfiles} from '../../src/support/profiles/cache.ts';
import {startFakeApi} from '../fakes/api.ts';
import type {FakeApi, FakeApiOptions, FakeRunSnapshot} from '../fakes/api.ts';
import {startFakeIssuer} from '../fakes/oidc.ts';
import type {FakeIssuer, FakeIssuerOptions} from '../fakes/oidc.ts';
import {startOmp} from './driver.ts';
import type {Omp} from './driver.ts';

export const CLIENT_ID = 'skua-omp';
const ORIGIN = 'https://github.com/org/test.git';
const ENVELOPES = ['<system-reminder>', '<system-notice>', '<irc'];

const EXTENSION_DIR = fileURLToPath(new URL('../../', import.meta.url));
const PACKAGE_FILES = ['index.ts', 'package.json', 'omp-range.json', 'src'];

export interface HarnessOptions {
  api?: FakeApiOptions;
  issuer?: FakeIssuerOptions;
  task?: Record<string, unknown>;
  config?: Omit<SkuaConfigInput, 'endpoint' | 'issuer' | 'clientId'>;
  loggedIn?: boolean;
  cachedProfiles?: Profile[];
}

export interface TaskItem {
  agent: string;
  name: string;
  task: string;
  outputSchema?: unknown;
}

export interface Harness {
  root: string;
  agentDir: string;
  packageDir: string;
  agentsDir: string;
  cwd: string;
  headSha: string;
  api: FakeApi;
  issuer: FakeIssuer;
  start(opts?: {args?: string[]; env?: Record<string, string>}): Promise<Omp>;
  runs(): FakeRunSnapshot[];
  close(): Promise<void>;
}

function git(cwd: string, ...args: string[]): Promise<string> {
  const {promise, resolve, reject} = Promise.withResolvers<string>();
  execFile('git', args, {cwd}, (err, stdout) => {
    if (err !== null) {
      reject(err);
      return;
    }
    resolve(stdout.trim());
  });
  return promise;
}

async function gitRepo(dir: string): Promise<string> {
  await mkdir(dir, {recursive: true});
  await git(dir, 'init', '-q', '-b', 'main');
  await git(dir, 'config', 'user.email', 't@example.com');
  await git(dir, 'config', 'user.name', 't');
  await git(dir, 'config', 'commit.gpgsign', 'false');
  await writeFile(join(dir, 'README.md'), 'test repo\n');
  await git(dir, 'add', 'README.md');
  await git(dir, 'commit', '-q', '-m', 'init');
  await git(dir, 'remote', 'add', 'origin', ORIGIN);
  await git(dir, 'update-ref', 'refs/remotes/origin/main', 'HEAD');
  return git(dir, 'rev-parse', 'HEAD');
}

export async function createHarness(
  opts: HarnessOptions = {},
): Promise<Harness> {
  const root = await mkdtemp(join(tmpdir(), 'skua-rpc-'));
  const agentDir = join(root, 'agent');
  const packageDir = join(root, 'pkg');
  const agentsDir = join(packageDir, 'agents');
  const cwd = join(root, 'repo');
  const omps: Omp[] = [];
  const api = await startFakeApi(opts.api);
  const issuer = await startFakeIssuer({clientId: CLIENT_ID, ...opts.issuer});
  try {
    await mkdir(agentDir, {recursive: true});
    const settings = {
      modelRoles: {
        default: 'script/parent',
        smol: 'script/local',
        slow: 'script/local',
      },
      task: {
        agentModelOverrides: {scout: 'script/local', task: 'script/local'},
        agentIdleTtlMs: 5000,
        ...opts.task,
      },
    };
    await writeFile(
      join(agentDir, 'config.yml'),
      `${JSON.stringify(settings, null, 2)}\n`,
    );
    for (const f of PACKAGE_FILES)
      await cp(join(EXTENSION_DIR, f), join(packageDir, f), {recursive: true});
    const headSha = await gitRepo(cwd);
    const loggedIn = opts.loggedIn ?? true;
    if (loggedIn || opts.config !== undefined)
      saveConfig(agentDir, {
        endpoint: api.url,
        issuer: issuer.url,
        clientId: CLIENT_ID,
        ...opts.config,
      });
    if (loggedIn) {
      const cfg = loadConfig(agentDir);
      assert.ok(cfg);
      saveToken(
        agentDir,
        tokenSet(cfg, await discover(cfg.issuer), issuer.mint(), undefined),
      );
      if (opts.cachedProfiles !== undefined) {
        writeCachedProfiles(agentDir, api.url, opts.cachedProfiles);
        syncAgentFiles(agentsDir, opts.cachedProfiles);
      }
    }
    return {
      root,
      agentDir,
      packageDir,
      agentsDir,
      cwd,
      headSha,
      api,
      issuer,
      start: async (startOpts = {}) => {
        const omp = startOmp({...startOpts, packageDir, agentDir, cwd});
        omps.push(omp);
        await omp.waitFrame(f => f.type === 'ready', 30_000);
        await omp.data('set_subagent_subscription', {level: 'progress'});
        return omp;
      },
      runs: () => api.runs(),
      close: async () => {
        try {
          checkInvariants(api.runs(), omps);
        } finally {
          await Promise.all(omps.map(o => o.kill()));
          await Promise.all([api.close(), issuer.close()]);
          await rm(root, {recursive: true, force: true});
        }
      },
    };
  } catch (err) {
    await Promise.all([api.close(), issuer.close()]);
    await rm(root, {recursive: true, force: true});
    throw err;
  }
}

function checkInvariants(runs: FakeRunSnapshot[], omps: Omp[]): void {
  const keys = runs.map(r => `${r.owner}\n${r.clientKey}`);
  assert.equal(
    new Set(keys).size,
    keys.length,
    `duplicate (owner, clientKey) runs: ${JSON.stringify(runs.map(r => r.clientKey))}`,
  );
  for (const r of runs) {
    for (const text of [r.prompt, ...r.steers.map(s => s.message)]) {
      for (const e of ENVELOPES)
        assert.ok(
          !text.includes(e),
          `run ${r.name} received ${e}: ${text.slice(0, 300)}`,
        );
    }
  }
  for (const o of omps) {
    const retry = o.frames.find(f => f.type === 'auto_retry_start');
    assert.equal(
      retry,
      undefined,
      `auto_retry_start appeared: ${JSON.stringify(retry)}`,
    );
  }
}

export function script(
  calls:
    | {tool: string; args: Record<string, unknown>}
    | {tool: string; args: Record<string, unknown>}[],
): string {
  return `SCRIPT:${JSON.stringify(calls)}`;
}

export function batch(
  tasks: TaskItem[],
  context = 'Shared batch context: repo org/test, issue 142.',
): string {
  return script({
    tool: 'task',
    args: {
      context,
      tasks: tasks.map(t => ({solutionSpace: 'fixed by the test', ...t})),
    },
  });
}

export function writeAgent(id: string, content: string): string {
  return script({tool: 'write', args: {path: `agent://${id}`, content}});
}

export async function prompt(omp: Omp, message: string): Promise<number> {
  const mark = omp.mark();
  await omp.data('prompt', {message});
  return mark;
}

export async function promptSettled(
  omp: Omp,
  message: string,
  timeoutMs: number,
): Promise<number> {
  const mark = await prompt(omp, message);
  await omp.waitSettled(timeoutMs, mark);
  return mark;
}

export async function command(
  omp: Omp,
  line: string,
  timeoutMs = 30_000,
): Promise<string[]> {
  const mark = await prompt(omp, line);
  await omp.waitFrame(f => f.type === 'prompt_result', timeoutMs, mark);
  return omp.frames
    .slice(mark)
    .filter(f => f.type === 'extension_ui_request' && f.method === 'notify')
    .map(f => String(f.message));
}

export function lifecycle(omp: Omp, id: string): string[] {
  const statuses: string[] = [];
  for (const f of omp.frames) {
    const p = f.payload;
    if (
      f.type === 'subagent_lifecycle' &&
      typeof p === 'object' &&
      p !== null &&
      'id' in p &&
      p.id === id &&
      'status' in p
    )
      statuses.push(String(p.status));
  }
  return statuses;
}

export function sessionFileOf(omp: Omp, id: string): string | undefined {
  for (const f of omp.frames) {
    const p = f.payload;
    if (
      f.type === 'subagent_lifecycle' &&
      typeof p === 'object' &&
      p !== null &&
      'id' in p &&
      p.id === id &&
      'sessionFile' in p &&
      typeof p.sessionFile === 'string'
    )
      return p.sessionFile;
  }
  return undefined;
}

export function abortRequests(h: Harness, run: string): number {
  return h.api
    .requests()
    .filter(
      r => r.method === 'POST' && r.path === `/v1alpha1/runs/${run}/abort`,
    ).length;
}

export function taskResults(omp: Omp, id: string, since = 0): string[] {
  const out: string[] = [];
  for (const f of omp.frames.slice(since)) {
    const m = f.message;
    if (
      f.type !== 'message_end' ||
      typeof m !== 'object' ||
      m === null ||
      !('content' in m)
    )
      continue;
    const content =
      typeof m.content === 'string' ? m.content : JSON.stringify(m.content);
    if (content.includes(`<task-result id="${id}"`)) out.push(content);
  }
  return out;
}

export function resultOutput(taskResult: string): string {
  const output = /<output>\n([\s\S]*?)\n<\/output>/.exec(taskResult)?.[1];
  assert.ok(output !== undefined, `no <output> in ${taskResult}`);
  return output;
}

export interface ContentBlock {
  type: string;
  text?: string;
  id?: string;
  name?: string;
  arguments?: Record<string, unknown>;
}
export interface AgentMessage {
  role: string;
  content?: string | ContentBlock[];
  stopReason?: string;
  toolCallId?: string;
  toolName?: string;
  isError?: boolean;
}

export async function childMessages(
  omp: Omp,
  id: string,
): Promise<AgentMessage[]> {
  return (
    await omp.data<{messages: AgentMessage[]}>('get_subagent_messages', {
      subagentId: id,
    })
  ).messages;
}

export function toolCalls(
  m: AgentMessage,
): {id: string; name: string; arguments: Record<string, unknown>}[] {
  if (m.role !== 'assistant' || !Array.isArray(m.content)) return [];
  return m.content.flatMap(b =>
    b.type === 'toolCall'
      ? [{id: b.id ?? '', name: b.name ?? '', arguments: b.arguments ?? {}}]
      : [],
  );
}

export function stepIds(messages: AgentMessage[]): string[] {
  return messages.flatMap(m =>
    toolCalls(m)
      .filter(c => c.name === 'skua_step')
      .map(c => c.id),
  );
}

export function messageText(m: AgentMessage): string {
  if (typeof m.content === 'string') return m.content;
  if (!Array.isArray(m.content)) return '';
  return m.content
    .flatMap(b => (b.type === 'text' && b.text !== undefined ? [b.text] : []))
    .join('\n');
}

export async function childSessionId(
  sessionFile: string | undefined,
): Promise<string> {
  assert.ok(sessionFile, 'the subagent has a session file');
  const text = await readFile(sessionFile, 'utf8');
  for (const line of text.split('\n').slice(0, 5)) {
    const entry: unknown = line === '' ? undefined : JSON.parse(line);
    if (
      typeof entry === 'object' &&
      entry !== null &&
      'type' in entry &&
      entry.type === 'session' &&
      'id' in entry &&
      typeof entry.id === 'string'
    )
      return entry.id;
  }
  throw new Error(`no session header in ${sessionFile}: ${text.slice(0, 400)}`);
}

export async function waitFor<T>(
  what: string,
  probe: () => T | undefined | Promise<T | undefined>,
  timeoutMs: number,
  everyMs = 200,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const v = await probe();
    if (v !== undefined) return v;
    if (Date.now() > deadline)
      throw new Error(
        `timed out after ${String(timeoutMs)} ms waiting for ${what}`,
      );
    await delay(everyMs);
  }
}

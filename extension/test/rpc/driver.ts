import {spawn} from 'node:child_process';
import type {ChildProcessByStdio} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {createInterface} from 'node:readline';
import type {Readable, Writable} from 'node:stream';
import {setTimeout as delay} from 'node:timers/promises';

export type Frame = Record<string, unknown> & {
  _t: number;
  type?: unknown;
  id?: unknown;
  success?: unknown;
  error?: unknown;
  data?: unknown;
  method?: unknown;
  message?: unknown;
  payload?: unknown;
};

export interface Subagent {
  id: string;
  agent: string;
  status: string;
  sessionFile?: string;
  [key: string]: unknown;
}

export interface StartOptions {
  packageDir: string;
  agentDir: string;
  cwd: string;
  args?: string[];
  env?: Record<string, string>;
}

export interface Omp {
  readonly frames: Frame[];
  readonly startedAt: number;
  readonly exited: Promise<number | null>;
  stderr(): string;
  mark(): number;
  cmd(
    type: string,
    args?: Record<string, unknown>,
    timeoutMs?: number,
  ): Promise<Frame>;
  data<T>(
    type: string,
    args?: Record<string, unknown>,
    timeoutMs?: number,
  ): Promise<T>;
  waitFrame(
    pred: (f: Frame) => boolean,
    timeoutMs: number,
    since?: number,
  ): Promise<Frame>;
  waitSettled(timeoutMs: number, since?: number): Promise<void>;
  subagents(): Promise<Subagent[]>;
  waitSubagent(
    id: string,
    status: string | ((s: string) => boolean),
    timeoutMs: number,
  ): Promise<Subagent>;
  notifications(): string[];
  kill(signal?: NodeJS.Signals): Promise<void>;
  closeStdin(): void;
}

export const SCRIPT_PROVIDER_DIR = fileURLToPath(
  new URL('../fakes/script-provider/', import.meta.url),
);

const DEAD_PROXY = 'http://127.0.0.1:9';
const LOOPBACK_ONLY: Record<string, string> = {
  HTTP_PROXY: DEAD_PROXY,
  HTTPS_PROXY: DEAD_PROXY,
  ALL_PROXY: DEAD_PROXY,
  http_proxy: DEAD_PROXY,
  https_proxy: DEAD_PROXY,
  all_proxy: DEAD_PROXY,
  NO_PROXY: '127.0.0.1,localhost',
  no_proxy: '127.0.0.1,localhost',
};

export function startOmp(opts: StartOptions): Omp {
  const bin = process.env['OMP_BIN'] ?? 'omp';
  const argv = [
    '--mode',
    'rpc',
    '-e',
    opts.packageDir,
    '-e',
    SCRIPT_PROVIDER_DIR,
    '--model',
    'script/parent',
    ...(opts.args ?? []),
  ];
  const env: Record<string, string> = {
    ...providerFreeEnv(),
    ...LOOPBACK_ONLY,
    PI_CODING_AGENT_DIR: opts.agentDir,
    ...opts.env,
  };
  const proc: ChildProcessByStdio<Writable, Readable, Readable> = spawn(
    bin,
    argv,
    {
      cwd: opts.cwd,
      env,
      detached: true,
      stdio: ['pipe', 'pipe', 'pipe'],
    },
  );
  const startedAt = Date.now();
  const frames: Frame[] = [];
  const stderr: string[] = [];
  const waiters = new Map<string, (f: Frame) => void>();
  const listeners = new Set<() => void>();
  let nextId = 1;
  let exitCode: number | null | undefined;

  const exit = Promise.withResolvers<number | null>();
  const exited = exit.promise;
  proc.once('exit', code => {
    exitCode = code;
    exit.resolve(code);
    for (const l of listeners) l();
  });
  proc.stdin.on('error', () => undefined);
  proc.stderr.setEncoding('utf8');
  proc.stderr.on('data', (chunk: string) => {
    stderr.push(chunk);
  });
  createInterface({input: proc.stdout, crlfDelay: Infinity}).on(
    'line',
    line => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(line);
      } catch {
        parsed = {type: 'raw', raw: line.slice(0, 300)};
      }
      if (
        typeof parsed !== 'object' ||
        parsed === null ||
        Array.isArray(parsed)
      )
        return;
      const frame: Frame = {...parsed, _t: Date.now() - startedAt};
      frames.push(frame);
      if (frame.type === 'response' && typeof frame.id === 'string') {
        const w = waiters.get(frame.id);
        waiters.delete(frame.id);
        w?.(frame);
      }
      for (const l of listeners) l();
    },
  );

  const waitFrame = (
    pred: (f: Frame) => boolean,
    timeoutMs: number,
    since = 0,
  ): Promise<Frame> => {
    const {promise, resolve, reject} = Promise.withResolvers<Frame>();
    let scanned = since;
    const check = (): void => {
      for (; scanned < frames.length; scanned++) {
        const frame = frames[scanned];
        if (frame !== undefined && pred(frame)) {
          done();
          resolve(frame);
          return;
        }
      }
      if (exitCode !== undefined) {
        done();
        reject(
          new Error(
            `omp exited (${String(exitCode)}) before the awaited frame\n${stderr.join('').slice(-2000)}`,
          ),
        );
      }
    };
    const timer = setTimeout(() => {
      done();
      reject(
        new Error(
          `timed out after ${String(timeoutMs)} ms waiting for an omp frame; last frames ${JSON.stringify(frames.slice(-8).map(f => f.type))}\n${stderr.join('').slice(-2000)}`,
        ),
      );
    }, timeoutMs);
    const done = (): void => {
      clearTimeout(timer);
      listeners.delete(check);
    };
    listeners.add(check);
    check();
    return promise;
  };

  const cmd = async (
    type: string,
    args: Record<string, unknown> = {},
    timeoutMs = 60_000,
  ): Promise<Frame> => {
    const id = `r${String(nextId++)}`;
    const {promise, resolve, reject} = Promise.withResolvers<Frame>();
    const timer = setTimeout(() => {
      waiters.delete(id);
      reject(
        new Error(`omp did not answer ${type} within ${String(timeoutMs)} ms`),
      );
    }, timeoutMs);
    waiters.set(id, f => {
      clearTimeout(timer);
      resolve(f);
    });
    proc.stdin.write(`${JSON.stringify({...args, id, type})}\n`);
    return promise;
  };

  const data = async <T>(
    type: string,
    args: Record<string, unknown> = {},
    timeoutMs = 60_000,
  ): Promise<T> => {
    const r = await cmd(type, args, timeoutMs);
    if (r.success !== true)
      throw new Error(`omp ${type} failed: ${String(r.error)}`);
    return r.data as T;
  };

  const subagents = async (): Promise<Subagent[]> =>
    (await data<{subagents: Subagent[]}>('get_subagents')).subagents;

  return {
    frames,
    startedAt,
    exited,
    stderr: () => stderr.join(''),
    mark: () => frames.length,
    cmd,
    data,
    waitFrame,
    waitSettled: async (timeoutMs, since = frames.length) => {
      await waitFrame(f => f.type === 'session_settled', timeoutMs, since);
    },
    subagents,
    waitSubagent: async (id, status, timeoutMs) => {
      const match =
        typeof status === 'string' ? (s: string) => s === status : status;
      const deadline = Date.now() + timeoutMs;
      for (;;) {
        const found = (await subagents()).find(s => s.id === id);
        if (found !== undefined && match(found.status)) return found;
        if (Date.now() > deadline)
          throw new Error(
            `subagent ${id} did not reach the awaited status; last ${found?.status ?? 'absent'}`,
          );
        await delay(250);
      }
    },
    notifications: () =>
      frames
        .filter(
          f =>
            f.type === 'extension_ui_request' &&
            f.method === 'notify' &&
            typeof f.message === 'string',
        )
        .map(f => String(f.message)),
    kill: async (signal = 'SIGKILL') => {
      if (exitCode !== undefined || proc.pid === undefined) return;
      try {
        process.kill(-proc.pid, signal);
      } catch {
        return;
      }
      await exited;
    },
    closeStdin: () => {
      proc.stdin.end();
    },
  };
}

const PROVIDER_ENV =
  /(_API_KEY|_AUTH_TOKEN|_ACCESS_TOKEN)$|^(ANTHROPIC|OPENAI|AZURE|GEMINI|GOOGLE|OPENROUTER|XAI|GROQ|MISTRAL|DEEPSEEK|CEREBRAS|COPILOT|GITHUB|GH|AWS|VERTEX|OLLAMA|LITELLM)_/;

function providerFreeEnv(): Record<string, string> {
  const env: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (
      v !== undefined &&
      !PROVIDER_ENV.test(k) &&
      k !== 'PI_CODING_AGENT_DIR' &&
      k !== 'BROWSER'
    )
      env[k] = v;
  }
  return env;
}

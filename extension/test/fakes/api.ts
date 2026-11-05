import {createHash, randomBytes} from 'node:crypto';
import {createServer} from 'node:http';
import type {IncomingMessage, ServerResponse} from 'node:http';
import {buffer} from 'node:stream/consumers';
import {setTimeout as delay} from 'node:timers/promises';
import {
  parseBearer,
  parseCreateBody,
  parseJson,
  parseProfileInput,
  parseRunPath,
  parseSteerMessage,
} from './api-requests.ts';
import type {BearerClaims, CreateRunRequest} from './api-requests.ts';
import {
  cancelledOutcome,
  createAnswerFails,
  heartbeatStopsAfterStep,
  opensWithSilence,
  scenarioOf,
  scenarioOutcome,
  stepCount,
  stepTool,
  stepsUseTools,
  streamDropsAfterToolEnd,
} from './api-scenarios.ts';
import type {RunOutcome} from './api-scenarios.ts';
import {closeServer, listenOnLoopback} from './loopback-server.ts';

export interface FakeProfile {
  name: string;
  description: string;
  models: string[];
  ready: boolean;
}
export interface FakeProfileInput {
  name: string;
  description?: string | undefined;
  models?: string[] | undefined;
  ready?: boolean | undefined;
}
export type FakePhase = 'Pending' | 'Running' | 'Succeeded' | 'Failed';
export interface FakeWorkspace {
  repo: string;
  baseRef: string;
}
export interface FakeOutputs {
  branch?: string;
  pullRequest?: number | string;
}
export interface FakeSteer {
  message: string;
  receivedAtStep: number;
  appliedAtStep: number | null;
}
export interface RateLimit {
  path: string;
  times: number;
  retryAfterS: number;
}

export interface FakeRunSnapshot {
  name: string;
  profile: string;
  clientKey: string;
  owner: string;
  phase: FakePhase;
  reason: string;
  prompt: string;
  schema: unknown;
  workspace: FakeWorkspace | undefined;
  steers: FakeSteer[];
  aborted: boolean;
  lastSeq: number;
}

export interface FakeRequest {
  method: string;
  path: string;
  status: number;
  lastEventId: string | undefined;
  exp: number | undefined;
  bytes: number;
}

export interface FakeApiOptions {
  port?: number | undefined;
  stepMs?: number | undefined;
  steps?: number | undefined;
  gapMs?: number | undefined;
  hbMs?: number | undefined;
  profilesDelayMs?: number | undefined;
  expireStreams?: boolean | undefined;
  rateLimit?: RateLimit | undefined;
}

export interface FakeApi {
  url: string;
  close(): Promise<void>;
  runs(): FakeRunSnapshot[];
  requests(): FakeRequest[];
  setProfile(p: FakeProfileInput): void;
  restart(): void;
}

interface RunEventRecord {
  seq: number;
  type: string;
  data: Record<string, unknown>;
}

interface EventStream {
  res: ServerResponse;
  connection: number;
  closed: boolean;
  timers: NodeJS.Timeout[];
}

interface RunState extends RunOutcome {
  name: string;
  profile: string;
  clientKey: string;
  owner: string;
  prompt: string;
  schema: unknown;
  workspace: FakeWorkspace | undefined;
  scenario: string;
  createdAt: string;
  events: RunEventRecord[];
  lastSeq: number;
  step: number;
  stepStartSeq: number;
  steers: FakeSteer[];
  pendingSteers: FakeSteer[];
  aborted: boolean;
  streams: Set<EventStream>;
  muted: Set<EventStream>;
  connections: number;
  stop: AbortController;
}

interface Caller {
  owner: string;
  exp: number | undefined;
}

interface Exchange {
  req: IncomingMessage;
  res: ServerResponse;
  entry: FakeRequest;
  body: Buffer;
}

interface CallerExchange extends Exchange {
  caller: Caller;
}

interface Rejection {
  status: number;
  error: string;
}

type ExchangeRoute = (x: Exchange) => void;
type CallerRoute = (x: CallerExchange) => void;
type RunRoute = (x: CallerExchange, run: RunState) => void;

const DEFAULT_MODELS = [
  'anthropic/claude-sonnet-5',
  'anthropic/claude-haiku-4-5',
];
const MAX_TIMER_MS = 2_147_483_647;
const DROP_AFTER_WRITE_MS = 100;
const API_PREFIX = '/v1alpha1/';
const PROFILES_ROUTE = 'GET /v1alpha1/profiles';

function runIsTerminal(run: RunState): boolean {
  return run.phase === 'Succeeded' || run.phase === 'Failed';
}

function endEventDropped(run: RunState): boolean {
  return runIsTerminal(run) && !run.events.some(e => e.type === 'end');
}

function streamWritable(s: EventStream): boolean {
  return !s.closed && !s.res.destroyed && !s.res.writableEnded;
}

function tokenExpired(bearer: BearerClaims): boolean {
  return bearer.exp !== undefined && bearer.exp * 1000 <= Date.now();
}

function routeOf(entry: FakeRequest): string {
  return `${entry.method} ${entry.path}`;
}

function lastEventIdOf(req: IncomingMessage): number {
  const from = Number(req.headers['last-event-id'] ?? 0);
  return Number.isFinite(from) ? from : 0;
}

function ownerOf(bearer: BearerClaims): string {
  return createHash('sha256')
    .update(`${bearer.iss}\n${bearer.sub}`)
    .digest('hex')
    .slice(0, 40);
}

function outcomeOf(run: RunState): RunOutcome {
  return {
    phase: run.phase,
    reason: run.reason,
    result: run.result,
    outputs: run.outputs,
  };
}

function runJson(run: RunState): Record<string, unknown> {
  return {
    name: run.name,
    profile: run.profile,
    clientKey: run.clientKey,
    phase: run.phase,
    reason: run.reason,
    result: run.result,
    outputs: run.outputs,
    createdAt: run.createdAt,
  };
}

function runSnapshot(r: RunState): FakeRunSnapshot {
  return {
    name: r.name,
    profile: r.profile,
    clientKey: r.clientKey,
    owner: r.owner,
    phase: r.phase,
    reason: r.reason,
    prompt: r.prompt,
    schema: r.schema,
    workspace: r.workspace,
    steers: r.steers.map(s => ({...s})),
    aborted: r.aborted,
    lastSeq: r.lastSeq,
  };
}

function eventFrame(e: RunEventRecord): string {
  return `id: ${String(e.seq)}\nevent: ${e.type}\ndata: ${JSON.stringify(e.data)}\n\n`;
}

function json(
  x: Exchange,
  status: number,
  body: unknown,
  headers: Record<string, string> = {},
): void {
  x.entry.status = status;
  x.res.writeHead(status, {'content-type': 'application/json', ...headers});
  x.res.end(JSON.stringify(body));
}

function ignoreResponseError(): undefined {
  return undefined;
}

function newRun(
  request: CreateRunRequest,
  owner: string,
  serial: number,
): RunState {
  return {
    name: `${request.profile}-${serial.toString(36)}${randomBytes(2).toString('hex')}`,
    profile: request.profile,
    clientKey: request.clientKey,
    owner,
    prompt: request.prompt,
    schema: request.schema,
    workspace: request.workspace,
    scenario: scenarioOf(request.prompt),
    phase: 'Pending',
    reason: '',
    result: null,
    outputs: {},
    createdAt: new Date().toISOString(),
    events: [],
    lastSeq: 0,
    step: 0,
    stepStartSeq: 1,
    steers: [],
    pendingSteers: [],
    aborted: false,
    streams: new Set(),
    muted: new Set(),
    connections: 0,
    stop: new AbortController(),
  };
}

export async function startFakeApi(
  opts: FakeApiOptions = {},
): Promise<FakeApi> {
  const stepMs = opts.stepMs ?? 4000;
  const stepsOpt = opts.steps ?? 6;
  const gapMs = opts.gapMs ?? 240000;
  const hbMs = opts.hbMs ?? 15000;
  const profilesDelayMs = opts.profilesDelayMs ?? 0;
  const expireStreams = opts.expireStreams ?? true;
  const rateLimit = opts.rateLimit;

  const profiles = new Map<string, FakeProfile>();
  const runs = new Map<string, RunState>();
  const byOwnerKey = new Map<string, RunState>();
  const requestLog: FakeRequest[] = [];
  const closing = new AbortController();
  let rateLimited = 0;
  let runCounter = 0;

  const setProfile = (p: FakeProfileInput): void => {
    const prev = profiles.get(p.name);
    profiles.set(p.name, {
      name: p.name,
      description:
        p.description ?? prev?.description ?? `${p.name} remote agent.`,
      models: p.models ?? prev?.models ?? [...DEFAULT_MODELS],
      ready: p.ready ?? prev?.ready ?? true,
    });
  };
  setProfile({
    name: 'generic',
    description: 'Generic remote agent: git, gh, shells, omp.',
    models: [...DEFAULT_MODELS],
    ready: true,
  });
  setProfile({
    name: 'golang',
    description: 'Go toolchain remote agent.',
    models: [...DEFAULT_MODELS],
    ready: true,
  });

  const clearStreamTimers = (s: EventStream): void => {
    for (const t of s.timers) clearTimeout(t);
    s.timers = [];
  };

  const detach = (run: RunState, s: EventStream): void => {
    s.closed = true;
    clearStreamTimers(s);
    run.streams.delete(s);
    run.muted.delete(s);
  };

  const endStream = (run: RunState, s: EventStream): void => {
    detach(run, s);
    if (!s.res.writableEnded) s.res.end();
  };

  const destroyStream = (run: RunState, s: EventStream): void => {
    detach(run, s);
    s.res.destroy();
  };

  const destroyAllStreams = (run: RunState): void => {
    for (const s of [...run.streams, ...run.muted]) destroyStream(run, s);
  };

  const send = (run: RunState, s: EventStream, e: RunEventRecord): void => {
    if (!streamWritable(s)) return;
    const frame = eventFrame(e);
    if (e.type === 'end') {
      s.res.write(frame);
      endStream(run, s);
      return;
    }
    if (
      e.type === 'tool_end' &&
      streamDropsAfterToolEnd(run.scenario, s.connection)
    ) {
      detach(run, s);
      s.res.write(frame, () =>
        setTimeout(() => s.res.destroy(), DROP_AFTER_WRITE_MS),
      );
      return;
    }
    s.res.write(frame);
  };

  const emit = (
    run: RunState,
    type: string,
    data: Record<string, unknown>,
  ): void => {
    const e: RunEventRecord = {seq: ++run.lastSeq, type, data};
    run.events.push(e);
    for (const s of [...run.streams]) send(run, s, e);
  };

  const finish = (run: RunState, outcome: RunOutcome): void => {
    run.phase = outcome.phase;
    run.reason = outcome.reason;
    run.result = outcome.result;
    run.outputs = outcome.outputs;
    emit(run, 'end', {...outcome});
  };

  const beginStep = (run: RunState, k: number): void => {
    run.step = k;
    run.stepStartSeq = run.lastSeq + 1;
    for (const s of run.pendingSteers.splice(0)) {
      s.appliedAtStep = k;
      emit(run, 'text', {
        delta: `[remote agent received steer at step ${String(k)}: ${s.message}]`,
      });
    }
  };

  const pause = async (run: RunState, ms: number): Promise<boolean> => {
    try {
      await delay(ms, undefined, {signal: run.stop.signal});
    } catch {
      return false;
    }
    return !runIsTerminal(run);
  };

  const muteStreams = (run: RunState): void => {
    for (const s of run.streams) {
      clearStreamTimers(s);
      run.muted.add(s);
    }
    run.streams.clear();
  };

  const openingSilence = async (run: RunState): Promise<boolean> => {
    if (!opensWithSilence(run.scenario)) return true;
    emit(run, 'text', {delta: 'Thinking quietly for a long time.'});
    return pause(run, gapMs);
  };

  const emitToolStep = (run: RunState, i: number): void => {
    const tool = stepTool(run.scenario, i);
    const id = `call-${String(i)}`;
    emit(run, 'text', {delta: `Step ${String(i)}: inspecting the code. `});
    emit(run, 'tool_start', {id, name: tool.name, args: tool.args});
    emit(run, 'tool_update', {id, partial: tool.partial});
    emit(run, 'tool_end', {id, result: tool.result, isError: false});
  };

  const playStep = async (run: RunState, i: number): Promise<boolean> => {
    if (!stepsUseTools(run.scenario)) {
      emit(run, 'text', {
        delta: `Step ${String(i)}: thinking without tools. `,
      });
      return true;
    }
    emitToolStep(run, i);
    if (!heartbeatStopsAfterStep(run.scenario, i)) return true;
    muteStreams(run);
    return pause(run, gapMs);
  };

  const script = async (run: RunState): Promise<void> => {
    run.phase = 'Running';
    emit(run, 'status', {phase: 'Running'});
    if (!(await openingSilence(run))) return;
    const n = stepCount(run.scenario, stepsOpt);
    for (let i = 1; i <= n; i++) {
      if (!(await pause(run, stepMs))) return;
      beginStep(run, i);
      if (!(await playStep(run, i))) return;
    }
    beginStep(run, n + 1);
    finish(run, scenarioOutcome(run.scenario, run.name));
  };

  const restart = (): void => {
    for (const run of runs.values()) {
      destroyAllStreams(run);
      run.events = runIsTerminal(run)
        ? []
        : run.events.filter(e => e.seq >= run.stepStartSeq);
    }
  };

  const snapshot = (): FakeRunSnapshot[] => [...runs.values()].map(runSnapshot);

  const startEventStream = (x: CallerExchange, run: RunState): EventStream => {
    x.entry.status = 200;
    x.res.writeHead(200, {
      'content-type': 'text/event-stream',
      'cache-control': 'no-cache',
      connection: 'keep-alive',
    });
    x.res.flushHeaders();
    const s: EventStream = {
      res: x.res,
      connection: ++run.connections,
      closed: false,
      timers: [],
    };
    x.res.on('close', () => {
      detach(run, s);
    });
    return s;
  };

  const replayAfter = (
    run: RunState,
    s: EventStream,
    after: number,
  ): boolean => {
    for (const e of run.events) {
      if (e.seq > after) send(run, s, e);
      if (s.closed) return false;
    }
    return true;
  };

  const startHeartbeat = (s: EventStream): void => {
    s.timers.push(
      setInterval(() => {
        if (streamWritable(s)) s.res.write(': hb\n');
      }, hbMs),
    );
  };

  const scheduleExpiry = (
    run: RunState,
    s: EventStream,
    exp: number | undefined,
  ): void => {
    if (!expireStreams || exp === undefined) return;
    const ms = exp * 1000 - Date.now();
    if (ms > MAX_TIMER_MS) return;
    s.timers.push(
      setTimeout(
        () => {
          endStream(run, s);
        },
        Math.max(0, ms),
      ),
    );
  };

  const openEvents: RunRoute = (x, run) => {
    const after = lastEventIdOf(x.req);
    const s = startEventStream(x, run);
    if (endEventDropped(run)) {
      send(run, s, {seq: run.lastSeq, type: 'end', data: {...outcomeOf(run)}});
      return;
    }
    if (!replayAfter(run, s, after)) return;
    if (runIsTerminal(run)) {
      endStream(run, s);
      return;
    }
    run.streams.add(s);
    startHeartbeat(s);
    scheduleExpiry(run, s, x.caller.exp);
  };

  const getRun: RunRoute = (x, run) => {
    json(x, 200, runJson(run));
  };

  const steerRun: RunRoute = (x, run) => {
    const message = parseSteerMessage(parseJson(x.body.toString('utf8')));
    if (message === undefined) {
      json(x, 400, {error: 'body must be {message}'});
      return;
    }
    if (runIsTerminal(run)) {
      json(x, 409, {error: `run ${run.name} is ${run.phase}`});
      return;
    }
    const steer: FakeSteer = {
      message,
      receivedAtStep: run.step,
      appliedAtStep: null,
    };
    run.steers.push(steer);
    run.pendingSteers.push(steer);
    json(x, 202, {});
  };

  const abortRun: RunRoute = (x, run) => {
    run.aborted = true;
    if (!runIsTerminal(run)) {
      finish(run, cancelledOutcome());
      run.stop.abort();
    }
    json(x, 202, {});
  };

  const runRoutes = new Map<string, RunRoute>([
    ['GET ', getRun],
    ['GET /events', openEvents],
    ['POST /steer', steerRun],
    ['POST /abort', abortRun],
  ]);

  const answerProfiles = async (x: CallerExchange): Promise<void> => {
    if (profilesDelayMs > 0) {
      try {
        await delay(profilesDelayMs, undefined, {signal: closing.signal});
      } catch {
        return;
      }
    }
    json(x, 200, [...profiles.values()]);
  };

  const listRuns: CallerRoute = x => {
    json(
      x,
      200,
      [...runs.values()].filter(r => r.owner === x.caller.owner).map(runJson),
    );
  };

  const profileRejection = (name: string): Rejection | undefined => {
    const profile = profiles.get(name);
    if (!profile) return {status: 404, error: `unknown profile ${name}`};
    if (!profile.ready)
      return {status: 409, error: `profile ${name} is not Ready`};
    return undefined;
  };

  const answerCreated = (x: CallerExchange, run: RunState): void => {
    if (createAnswerFails(run.scenario)) {
      json(x, 503, {error: 'upstream unavailable'});
      return;
    }
    json(x, 201, runJson(run));
  };

  const createRun: CallerRoute = x => {
    const parsed = parseCreateBody(parseJson(x.body.toString('utf8')));
    if (!parsed.ok) {
      json(x, 400, {error: parsed.error});
      return;
    }
    const request = parsed.value;
    const key = `${x.caller.owner}\n${request.clientKey}`;
    const existing = byOwnerKey.get(key);
    if (existing) {
      json(x, 200, runJson(existing));
      return;
    }
    const rejection = profileRejection(request.profile);
    if (rejection) {
      json(x, rejection.status, {error: rejection.error});
      return;
    }
    const run = newRun(request, x.caller.owner, ++runCounter);
    runs.set(run.name, run);
    byOwnerKey.set(key, run);
    answerCreated(x, run);
    void script(run);
  };

  const collectionRoutes = new Map<string, CallerRoute>([
    ['GET /v1alpha1/runs', listRuns],
    ['POST /v1alpha1/runs', createRun],
  ]);

  const routeToRun = (x: CallerExchange): void => {
    const target = parseRunPath(x.entry.path);
    if (target === undefined) {
      json(x, 404, {error: 'not found'});
      return;
    }
    const run = runs.get(target.name);
    if (run?.owner !== x.caller.owner) {
      json(x, 404, {error: `no run ${target.name}`});
      return;
    }
    const route = runRoutes.get(`${x.entry.method} ${target.sub}`);
    if (!route) {
      json(x, 405, {error: 'method not allowed'});
      return;
    }
    route(x, run);
  };

  const handleApi = async (x: CallerExchange): Promise<void> => {
    const route = routeOf(x.entry);
    if (route === PROFILES_ROUTE) {
      await answerProfiles(x);
      return;
    }
    const collectionRoute = collectionRoutes.get(route);
    if (collectionRoute) {
      collectionRoute(x);
      return;
    }
    routeToRun(x);
  };

  const upsertProfile: ExchangeRoute = x => {
    const input = parseProfileInput(parseJson(x.body.toString('utf8')));
    if (!input) {
      json(x, 400, {error: 'name is required'});
      return;
    }
    setProfile(input);
    json(x, 200, profiles.get(input.name));
  };

  const adminRoutes = new Map<string, ExchangeRoute>([
    ['POST /admin/profiles', upsertProfile],
    [
      'GET /admin/runs',
      x => {
        json(x, 200, snapshot());
      },
    ],
    [
      'POST /admin/restart',
      x => {
        restart();
        json(x, 200, {});
      },
    ],
  ]);

  const takeRateLimitSlot = (path: string): RateLimit | undefined => {
    if (rateLimit?.path !== path || rateLimited >= rateLimit.times)
      return undefined;
    rateLimited++;
    return rateLimit;
  };

  const admitCaller = (
    x: Exchange,
    bearer: BearerClaims | undefined,
  ): Caller | undefined => {
    if (!bearer) {
      json(x, 401, {error: 'missing or invalid bearer token'});
      return undefined;
    }
    if (tokenExpired(bearer)) {
      json(x, 401, {error: 'token expired'});
      return undefined;
    }
    const limit = takeRateLimitSlot(x.entry.path);
    if (limit) {
      json(
        x,
        429,
        {error: 'rate limited'},
        {'retry-after': String(limit.retryAfterS)},
      );
      return undefined;
    }
    return {owner: ownerOf(bearer), exp: bearer.exp};
  };

  const recordRequest = (
    req: IncomingMessage,
    body: Buffer,
    bearer: BearerClaims | undefined,
  ): FakeRequest => {
    const lastEventId = req.headers['last-event-id'];
    const entry: FakeRequest = {
      method: req.method ?? 'GET',
      path: new URL(req.url ?? '/', 'http://fake').pathname,
      status: 0,
      lastEventId: typeof lastEventId === 'string' ? lastEventId : undefined,
      exp: bearer?.exp,
      bytes: body.length,
    };
    requestLog.push(entry);
    return entry;
  };

  const handle = async (
    req: IncomingMessage,
    res: ServerResponse,
  ): Promise<void> => {
    res.on('error', ignoreResponseError);
    const body = await buffer(req);
    const bearer = parseBearer(req.headers.authorization);
    const x: Exchange = {
      req,
      res,
      body,
      entry: recordRequest(req, body, bearer),
    };
    const adminRoute = adminRoutes.get(routeOf(x.entry));
    if (adminRoute) {
      adminRoute(x);
      return;
    }
    if (!x.entry.path.startsWith(API_PREFIX)) {
      json(x, 404, {error: 'not found'});
      return;
    }
    const caller = admitCaller(x, bearer);
    if (!caller) return;
    await handleApi({...x, caller});
  };

  const server = createServer((req, res) => {
    handle(req, res).catch((err: unknown) => {
      if (!res.headersSent) {
        res.writeHead(500, {'content-type': 'application/json'});
        res.end(
          JSON.stringify({
            error: err instanceof Error ? err.message : String(err),
          }),
        );
      }
    });
  });
  const url = await listenOnLoopback(server, opts.port);

  return {
    url,
    runs: snapshot,
    requests: () => requestLog.map(r => ({...r})),
    setProfile,
    restart,
    close: async () => {
      closing.abort();
      for (const run of runs.values()) {
        run.stop.abort();
        destroyAllStreams(run);
      }
      await closeServer(server);
    },
  };
}

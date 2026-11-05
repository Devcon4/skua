import assert from 'node:assert/strict';
import {mkdtempSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {afterAll, test} from 'vitest';
import type {Run} from '../contract/wire.ts';
import type {Api} from '../integration/skuaapi/client.ts';
import {saveConfig} from '../support/auth/config.ts';
import type {TokenSource} from '../support/auth/token.ts';
import type {CommandDeps, LiveRun, NotifyLevel} from './deps.ts';
import {runSkuaCommand} from './dispatch.ts';
import {USAGE} from './usage.ts';

const roots: string[] = [];

afterAll(() => {
  for (const r of roots) rmSync(r, {recursive: true, force: true});
});

function agentDir(): string {
  const root = mkdtempSync(join(tmpdir(), 'skua-commands-'));
  roots.push(root);
  return root;
}

function unexpected(): never {
  throw new Error('unexpected call');
}

function b64url(v: unknown): string {
  return Buffer.from(JSON.stringify(v)).toString('base64url');
}

function run(name: string, phase: Run['phase'], reason: string): Run {
  return {
    name,
    profile: 'generic',
    clientKey: `sid:${name}`,
    phase,
    reason,
    result: null,
    outputs: {},
    createdAt: '2026-11-03T10:00:00Z',
  };
}

function harness(
  dir: string,
  opts: {runs?: Run[]; live?: LiveRun[]} = {},
): {deps: CommandDeps; notes: {text: string; level: NotifyLevel}[]} {
  const notes: {text: string; level: NotifyLevel}[] = [];
  const api: Api = {
    profiles: unexpected,
    createRun: unexpected,
    getRun: unexpected,
    listRuns: () =>
      opts.runs === undefined
        ? Promise.reject(new Error('unexpected call'))
        : Promise.resolve(opts.runs),
    steer: unexpected,
    abort: unexpected,
    followEvents: unexpected,
  };
  const tokens: TokenSource = {
    getToken: unexpected,
    forceRefresh: unexpected,
    status: unexpected,
    clear: unexpected,
  };
  const deps: CommandDeps = {
    agentDir: dir,
    agentsDir: join(dir, 'agents'),
    omp: {version: '18.16.0', check: {ok: true}},
    notify: (text, level) => notes.push({text, level}),
    services: () => ({tokens, api, relay: {live: () => opts.live ?? []}}),
    sync: unexpected,
    startRefresh: unexpected,
    openUrl: unexpected,
    login: unexpected,
  };
  return {deps, notes};
}

test('login without values and no stored config answers with the usage error', async () => {
  const {deps, notes} = harness(agentDir());
  await runSkuaCommand('login', deps);
  assert.equal(notes.length, 1);
  const [note] = notes;
  assert.ok(note);
  assert.equal(note.level, 'error');
  assert.match(
    note.text,
    /login needs <endpoint> <issuer> <clientId> the first time/,
  );
  assert.ok(note.text.endsWith(USAGE));
});

test('login --device without values and no stored config answers with the usage error', async () => {
  const {deps, notes} = harness(agentDir());
  await runSkuaCommand('login --device', deps);
  assert.equal(notes.length, 1);
  const [note] = notes;
  assert.ok(note);
  assert.equal(note.level, 'error');
  assert.ok(note.text.endsWith(USAGE));
});

test('abort without a run answers with usage', async () => {
  const {deps, notes} = harness(agentDir());
  await runSkuaCommand('abort', deps);
  assert.deepEqual(notes, [
    {text: `skua: abort takes exactly one run name\n${USAGE}`, level: 'error'},
  ]);
});

test('an unknown subcommand and a bare /skua answer with usage', async () => {
  const {deps, notes} = harness(agentDir());
  await runSkuaCommand('frobnicate', deps);
  await runSkuaCommand('', deps);
  assert.deepEqual(notes, [
    {text: `skua: unknown subcommand frobnicate\n${USAGE}`, level: 'error'},
    {text: USAGE, level: 'info'},
  ]);
  for (const sub of ['login', 'sync', 'list', 'abort <run>', 'status'])
    assert.match(USAGE, new RegExp(`^  ${sub.replace(/[<>]/g, '\\$&')}`, 'm'));
});

test('a successful login starts the profile refresh and names the profiles', async () => {
  const dir = agentDir();
  const {deps, notes} = harness(dir);
  const started: string[] = [];
  deps.startRefresh = cfg => started.push(cfg.endpoint);
  deps.login = ({cfg}) =>
    Promise.resolve({
      issuer: cfg.issuer,
      clientId: cfg.clientId,
      idToken: `${b64url({alg: 'none'})}.${b64url({iss: cfg.issuer, sub: 'dev-1', email: 'dev@example.test', exp: 4102444800})}.`,
      expiresAt: 4102444800,
    });
  deps.sync = () =>
    Promise.resolve({
      ok: true,
      profiles: [],
      files: {
        written: ['skua-generic.md', 'skua-golang.md'],
        unchanged: [],
        removed: [],
        skipped: [],
      },
    });
  await runSkuaCommand(
    'login https://skua.test https://issuer.test skua-omp',
    deps,
  );
  assert.deepEqual(started, ['https://skua.test']);
  assert.deepEqual(notes, [
    {
      text: 'skua: logged in as dev@example.test at https://skua.test; profiles: generic, golang',
      level: 'info',
    },
  ]);
});

test('a failed login starts no profile refresh', async () => {
  const {deps, notes} = harness(agentDir());
  deps.login = () =>
    Promise.reject(new Error('issuer refused the login: access_denied'));
  await runSkuaCommand(
    'login https://skua.test https://issuer.test skua-omp',
    deps,
  );
  assert.deepEqual(notes, [
    {
      text: 'skua login failed: issuer refused the login: access_denied',
      level: 'error',
    },
  ]);
});

test('list marks the runs relayed in this process', async () => {
  const dir = agentDir();
  saveConfig(dir, {
    endpoint: 'https://skua.test',
    issuer: 'https://issuer.test',
    clientId: 'skua-omp',
  });
  const {deps, notes} = harness(dir, {
    runs: [run('run-a', 'Running', ''), run('run-b', 'Succeeded', 'Completed')],
    live: [{sessionId: 'sid', run: 'run-a', followUp: 0}],
  });
  await runSkuaCommand('list', deps);
  assert.equal(notes.length, 1);
  const [note] = notes;
  assert.ok(note);
  assert.equal(note.level, 'info');
  assert.deepEqual(note.text.split('\n'), [
    'skua runs:',
    'run-a  generic  Running  -  2026-11-03T10:00:00Z  relayed here',
    'run-b  generic  Succeeded  Completed  2026-11-03T10:00:00Z',
  ]);
});

test('list without a stored config says how to log in', async () => {
  const {deps, notes} = harness(agentDir());
  await runSkuaCommand('list', deps);
  assert.deepEqual(notes, [
    {
      text: 'skua: not configured. Run /skua login <endpoint> <issuer> <clientId>.',
      level: 'error',
    },
  ]);
});

test('sync names the current and removed profiles and when the task description lists new ones', async () => {
  const dir = agentDir();
  saveConfig(dir, {
    endpoint: 'https://skua.test',
    issuer: 'https://issuer.test',
    clientId: 'skua-omp',
  });
  const {deps, notes} = harness(dir);
  deps.sync = () =>
    Promise.resolve({
      ok: true,
      profiles: [],
      files: {
        written: ['skua-golang.md'],
        unchanged: ['skua-generic.md'],
        removed: ['skua-rust.md'],
        skipped: [],
      },
    });
  await runSkuaCommand('sync', deps);
  assert.deepEqual(notes, [
    {
      text: 'skua sync: 2 profiles (generic, golang); removed rust. New agents are dispatchable now; the task tool description lists them after the next base-prompt rebuild.',
      level: 'info',
    },
  ]);
});

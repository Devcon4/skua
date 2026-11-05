import assert from 'node:assert/strict';
import {existsSync, readFileSync, statSync} from 'node:fs';
import {writeFile} from 'node:fs/promises';
import {createServer} from 'node:net';
import type {AddressInfo} from 'node:net';
import {join} from 'node:path';
import {test} from 'vitest';
import type {FakeIssuer} from '../fakes/oidc.ts';
import type {Omp} from './driver.ts';
import {CLIENT_ID, command, createHarness, prompt} from './harness.ts';
import type {Harness} from './harness.ts';

const DEVICE_GRANT = 'urn:ietf:params:oauth:grant-type:device_code';

const BROWSER_SCRIPT = `const res = await fetch(process.argv.at(-1), { redirect: "manual" });
const location = res.headers.get("location");
if (location === null) process.exit(1);
process.exit((await fetch(location)).ok ? 0 : 1);
`;

const FORGED_STATE_SCRIPT = `import { writeFileSync } from "node:fs";
const res = await fetch(process.argv.at(-1), { redirect: "manual" });
const callback = new URL(res.headers.get("location") ?? "");
const forged = new URL(callback);
forged.searchParams.set("state", "forged");
const cb = await fetch(forged);
await cb.text();
writeFileSync(process.argv[2], String(cb.status));
process.exit((await fetch(callback)).ok ? 0 : 1);
`;

const ISSUER_ERROR_SCRIPT = `const u = new URL(process.argv.at(-1));
u.searchParams.set("response_type", "token");
await (await fetch(u)).text();
`;

async function browser(
  h: Harness,
  name: string,
  source: string,
  ...args: string[]
): Promise<string> {
  const path = join(h.root, name);
  await writeFile(path, source);
  return ['node', path, ...args].join(' ');
}

function loginLine(h: Harness, ...flags: string[]): string {
  return ['/skua login', h.api.url, h.issuer.url, CLIENT_ID, ...flags].join(
    ' ',
  );
}

function loggedInNote(h: Harness): string {
  return `skua: logged in as dev@example.test at ${h.api.url}; profiles: generic, golang`;
}

async function userCode(omp: Omp, since: number): Promise<string> {
  const notice = await omp.waitFrame(
    f =>
      f.type === 'extension_ui_request' &&
      f.method === 'notify' &&
      /confirm the code [A-Z]+-[A-Z]+/.test(String(f.message)),
    30_000,
    since,
  );
  const code = /confirm the code ([A-Z]+-[A-Z]+)/.exec(
    String(notice.message),
  )?.[1];
  assert.ok(code);
  return code;
}

test(
  '/skua login with a browser stores a private token and syncs profiles',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({loggedIn: false});
    try {
      const omp = await h.start({
        env: {BROWSER: await browser(h, 'browser.mjs', BROWSER_SCRIPT)},
      });
      const notes = await command(omp, loginLine(h), 60_000);
      assert.ok(notes.includes(loggedInNote(h)), JSON.stringify(notes));
      const authorize = new URL(
        notes
          .map(n => /visit (\S+)$/.exec(n)?.[1])
          .find(u => u !== undefined) ?? '',
      );
      assert.equal(authorize.searchParams.get('code_challenge_method'), 'S256');
      assert.match(
        authorize.searchParams.get('redirect_uri') ?? '',
        /^http:\/\/127\.0\.0\.1:\d+\/callback$/,
      );
      assert.equal(
        authorize.searchParams.get('scope'),
        'openid profile email offline_access',
      );
      assert.deepEqual(h.issuer.tokenRequests(), [
        {grantType: 'authorization_code', status: 200, error: undefined},
      ]);
      const token = join(h.agentDir, 'skua', 'token.json');
      assert.equal(statSync(token).mode & 0o777, 0o600);
      const {idToken} = JSON.parse(readFileSync(token, 'utf8')) as {
        idToken: string;
      };
      for (const note of notes) assert.ok(!note.includes(idToken));
      assert.ok(
        existsSync(join(h.agentsDir, 'skua-generic.md')) &&
          existsSync(join(h.agentsDir, 'skua-golang.md')),
      );
      const status = await command(omp, '/skua status');
      assert.ok(
        status.some(n => n.includes('fake-user')),
        JSON.stringify(status),
      );
    } finally {
      await h.close();
    }
  },
);

test(
  'a callback with the wrong state is refused and the genuine callback still completes the login',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({loggedIn: false});
    try {
      const forgedStatus = join(h.root, 'forged-status');
      const omp = await h.start({
        env: {
          BROWSER: await browser(
            h,
            'forge.mjs',
            FORGED_STATE_SCRIPT,
            forgedStatus,
          ),
        },
      });
      const notes = await command(omp, loginLine(h), 60_000);
      assert.ok(notes.includes(loggedInNote(h)), JSON.stringify(notes));
      assert.equal(readFileSync(forgedStatus, 'utf8'), '500');
      assert.deepEqual(h.issuer.tokenRequests(), [
        {grantType: 'authorization_code', status: 200, error: undefined},
      ]);
    } finally {
      await h.close();
    }
  },
);

test(
  'an issuer error on the callback fails the login',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({loggedIn: false});
    try {
      const omp = await h.start({
        env: {BROWSER: await browser(h, 'refused.mjs', ISSUER_ERROR_SCRIPT)},
      });
      const notes = await command(omp, loginLine(h), 60_000);
      assert.ok(
        notes.some(
          n =>
            n.startsWith('skua login failed: issuer refused the login') &&
            n.includes('unsupported_response_type'),
        ),
        JSON.stringify(notes),
      );
      assert.ok(!existsSync(join(h.agentDir, 'skua', 'token.json')));
    } finally {
      await h.close();
    }
  },
);

test(
  'the browser flow times out after loginTimeoutSeconds without a callback',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({
      loggedIn: false,
      config: {loginTimeoutSeconds: 1},
    });
    try {
      const omp = await h.start({env: {BROWSER: 'true'}});
      const notes = await command(omp, loginLine(h), 60_000);
      assert.ok(
        notes.includes('skua login failed: no login callback within 1 s'),
        JSON.stringify(notes),
      );
      assert.deepEqual(h.issuer.tokenRequests(), []);
    } finally {
      await h.close();
    }
  },
);

test(
  '/skua login falls back to the device flow when no browser starts',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({loggedIn: false, issuer: {interval: 1}});
    try {
      const omp = await h.start({env: {BROWSER: 'false'}});
      const mark = await prompt(omp, loginLine(h));
      h.issuer.approveDevice(await userCode(omp, mark));
      await omp.waitFrame(f => f.type === 'prompt_result', 60_000, mark);
      const notes = omp.notifications();
      assert.ok(notes.includes(loggedInNote(h)), JSON.stringify(notes));
      assert.ok(
        h.issuer
          .tokenRequests()
          .some(r => r.grantType === DEVICE_GRANT && r.status === 200),
      );
      const status = await command(omp, '/skua status');
      assert.ok(
        status.some(n => n.includes('fake-user')),
        JSON.stringify(status),
      );
    } finally {
      await h.close();
    }
  },
);

test(
  '/skua login falls back to the device flow when the loopback port is taken',
  {timeout: 120_000},
  async () => {
    const blocker = createServer();
    const listening = Promise.withResolvers<undefined>();
    blocker.listen(0, '127.0.0.1', () => {
      listening.resolve(undefined);
    });
    await listening.promise;
    const h = await createHarness({
      loggedIn: false,
      issuer: {interval: 1},
      config: {loopbackPort: (blocker.address() as AddressInfo).port},
    });
    try {
      const opened = join(h.root, 'opened');
      const omp = await h.start({env: {BROWSER: `touch ${opened}`}});
      const mark = await prompt(omp, loginLine(h));
      h.issuer.approveDevice(await userCode(omp, mark));
      await omp.waitFrame(f => f.type === 'prompt_result', 60_000, mark);
      assert.ok(
        omp.notifications().includes(loggedInNote(h)),
        JSON.stringify(omp.notifications()),
      );
      assert.ok(!existsSync(opened));
    } finally {
      await h.close();
      blocker.close();
    }
  },
);

test(
  'slow_down raises the device polling interval by 5 s',
  {timeout: 120_000},
  async () => {
    const deviceFlow: {issuer?: FakeIssuer; code: string} = {code: ''};
    const secondPollTooSoon = (): number => {
      if (
        deviceFlow.issuer?.tokenRequests().length !== 1 ||
        deviceFlow.code === ''
      )
        return Date.now();
      deviceFlow.issuer.approveDevice(deviceFlow.code);
      return Date.now() - 600;
    };
    const h = await createHarness({
      loggedIn: false,
      issuer: {interval: 1, now: secondPollTooSoon},
    });
    deviceFlow.issuer = h.issuer;
    try {
      const omp = await h.start();
      const begin = Date.now();
      const mark = await prompt(omp, loginLine(h, '--device'));
      deviceFlow.code = await userCode(omp, mark);
      await omp.waitFrame(f => f.type === 'prompt_result', 60_000, mark);
      assert.ok(
        omp.notifications().includes(loggedInNote(h)),
        JSON.stringify(omp.notifications()),
      );
      assert.ok(
        Date.now() - begin >= 7000,
        `login took ${String(Date.now() - begin)} ms, want 1 s + 6 s of polling waits`,
      );
      assert.deepEqual(
        h.issuer.tokenRequests().map(r => r.error),
        ['authorization_pending', 'slow_down', undefined],
      );
    } finally {
      await h.close();
    }
  },
);

test(
  '/skua login --device stops when the device code expires',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({
      loggedIn: false,
      issuer: {interval: 1, deviceExpiresInS: 2},
    });
    try {
      const omp = await h.start();
      const notes = await command(omp, loginLine(h, '--device'), 60_000);
      assert.ok(
        notes.some(
          n =>
            n.startsWith('skua login failed:') &&
            n.includes('the device code expired'),
        ),
        JSON.stringify(notes),
      );
      assert.ok(!existsSync(join(h.agentDir, 'skua', 'token.json')));
    } finally {
      await h.close();
    }
  },
);

test(
  '/skua login without a browser and without a device flow names both flows',
  {timeout: 120_000},
  async () => {
    const h = await createHarness({loggedIn: false, issuer: {noDevice: true}});
    try {
      const omp = await h.start({env: {BROWSER: 'false'}});
      const notes = await command(omp, loginLine(h), 60_000);
      assert.ok(
        notes.some(
          n =>
            n.startsWith('skua login failed:') &&
            n.includes('browser') &&
            n.includes('device'),
        ),
        JSON.stringify(notes),
      );
    } finally {
      await h.close();
    }
  },
);

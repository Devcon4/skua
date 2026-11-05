import assert from 'node:assert/strict';
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
  mkdirSync,
} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {test} from 'vitest';
import {configPath, loadConfig, saveConfig} from './config.ts';

function tempAgentDir(t: {onTestFinished(fn: () => void): void}): string {
  const dir = mkdtempSync(join(tmpdir(), 'skua-config-'));
  t.onTestFinished(() => {
    rmSync(dir, {recursive: true, force: true});
  });
  return dir;
}

const base = {
  endpoint: 'http://127.0.0.1:32080',
  issuer: 'http://dex.127.0.0.1.nip.io:32000',
  clientId: 'skua-omp',
};

test('a missing config file loads as undefined', t => {
  assert.equal(loadConfig(tempAgentDir(t)), undefined);
});

test('defaults fill missing fields', t => {
  const agentDir = tempAgentDir(t);
  saveConfig(agentDir, base);
  assert.deepEqual(loadConfig(agentDir), {
    ...base,
    scopes: ['openid', 'profile', 'email', 'offline_access'],
    loopbackPort: 0,
    heartbeatTimeoutMs: 45_000,
    profileRefreshMs: 600_000,
    loginTimeoutSeconds: 300,
  });
  assert.deepEqual(
    JSON.parse(readFileSync(configPath(agentDir), 'utf8')),
    base,
  );
});

test('stored optional fields override the defaults', t => {
  const agentDir = tempAgentDir(t);
  saveConfig(agentDir, {
    ...base,
    scopes: ['openid'],
    loopbackPort: 8765,
    heartbeatTimeoutMs: 1500,
    profileRefreshMs: 5000,
    loginTimeoutSeconds: 30,
  });
  const cfg = loadConfig(agentDir);
  assert.deepEqual(cfg?.scopes, ['openid']);
  assert.equal(cfg.loopbackPort, 8765);
  assert.equal(cfg.heartbeatTimeoutMs, 1500);
  assert.equal(cfg.profileRefreshMs, 5000);
  assert.equal(cfg.loginTimeoutSeconds, 30);
});

test('a changed endpoint reports endpointChanged', t => {
  const agentDir = tempAgentDir(t);
  assert.deepEqual(saveConfig(agentDir, base), {endpointChanged: false});
  assert.deepEqual(saveConfig(agentDir, {...base, clientId: 'other'}), {
    endpointChanged: false,
  });
  assert.deepEqual(
    saveConfig(agentDir, {...base, endpoint: 'https://skua.example.test'}),
    {endpointChanged: true},
  );
  assert.equal(loadConfig(agentDir)?.endpoint, 'https://skua.example.test');
});

test('the skua dir is private', t => {
  const agentDir = tempAgentDir(t);
  saveConfig(agentDir, base);
  assert.equal(statSync(join(agentDir, 'skua')).mode & 0o777, 0o700);
});

test('an invalid config file is refused', t => {
  const agentDir = tempAgentDir(t);
  mkdirSync(join(agentDir, 'skua'));
  writeFileSync(
    configPath(agentDir),
    JSON.stringify({endpoint: 'x', issuer: 3}),
  );
  assert.throws(
    () => loadConfig(agentDir),
    /needs endpoint, issuer and clientId/,
  );
  assert.deepEqual(saveConfig(agentDir, base), {endpointChanged: true});
  assert.equal(loadConfig(agentDir)?.clientId, 'skua-omp');
});

test('loginTimeoutSeconds must be a positive integer', t => {
  const agentDir = tempAgentDir(t);
  for (const loginTimeoutSeconds of [0, -5, 1.5]) {
    assert.throws(
      () => saveConfig(agentDir, {...base, loginTimeoutSeconds}),
      /valid optional fields/,
    );
  }
  mkdirSync(join(agentDir, 'skua'));
  writeFileSync(
    configPath(agentDir),
    JSON.stringify({...base, loginTimeoutSeconds: '60'}),
  );
  assert.throws(() => loadConfig(agentDir), /valid optional fields/);
});

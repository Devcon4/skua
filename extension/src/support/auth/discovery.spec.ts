import assert from 'node:assert/strict';
import {test} from 'vitest';
import {startFakeIssuer} from '../../../test/fakes/oidc.ts';
import type {FakeIssuer, FakeIssuerOptions} from '../../../test/fakes/oidc.ts';
import {OidcError, discover} from './discovery.ts';

async function withIssuer(
  opts: FakeIssuerOptions,
  fn: (issuer: FakeIssuer) => Promise<void>,
): Promise<void> {
  const issuer = await startFakeIssuer(opts);
  try {
    await fn(issuer);
  } finally {
    await issuer.close();
  }
}

test('discovery fails without S256', async () => {
  await withIssuer({noS256: true}, async issuer => {
    await assert.rejects(
      discover(issuer.url),
      (err: unknown) =>
        err instanceof OidcError &&
        err.code === 'no_s256' &&
        err.message.includes('S256'),
    );
  });
});

test('discovery fails when the issuer differs from the configured value', async () => {
  await withIssuer({}, async issuer => {
    await assert.rejects(
      discover(`${issuer.url}/`),
      (err: unknown) =>
        err instanceof OidcError && err.code === 'issuer_mismatch',
    );
  });
});

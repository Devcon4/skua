import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {describe, it} from 'vitest';
import {
  checkOmpVersion,
  compareVersions,
  isOmpRange,
  parseVersion,
} from './version.ts';

const range = {min: '18.16.0', maxExclusive: '18.17.0'};

function outOfRange(version: string): string {
  const check = checkOmpVersion(version, range);
  assert.equal(check.ok, false);
  return check.message;
}

describe('parseVersion', () => {
  it('parses a release version', () => {
    assert.deepEqual(parseVersion('18.16.0'), [18, 16, 0]);
  });

  it('rejects prereleases and garbage', () => {
    for (const s of [
      '18.16.0-beta.1',
      '18.16',
      'v18.16.0',
      '',
      'omp/18.16.0',
      '18.016.0',
      'a.b.c',
    ]) {
      assert.equal(parseVersion(s), undefined, s);
    }
  });
});

describe('compareVersions', () => {
  it('orders by major, minor, patch', () => {
    assert.equal(compareVersions([18, 16, 0], [18, 16, 0]), 0);
    assert.equal(compareVersions([18, 16, 1], [18, 16, 0]), 1);
    assert.equal(compareVersions([18, 15, 9], [18, 16, 0]), -1);
    assert.equal(compareVersions([19, 0, 0], [18, 99, 99]), 1);
  });
});

describe('checkOmpVersion', () => {
  it('accepts a version inside the range', () => {
    assert.deepEqual(checkOmpVersion('18.16.7', range), {ok: true});
  });

  it('accepts a version equal to min', () => {
    assert.deepEqual(checkOmpVersion('18.16.0', range), {ok: true});
  });

  it('rejects a version equal to maxExclusive', () => {
    const message = outOfRange('18.17.0');
    assert.match(message, /18\.17\.0/);
    assert.match(message, />=18\.16\.0 <18\.17\.0/);
    assert.match(message, /extension\/README\.md/);
  });

  it('rejects a newer major', () => {
    assert.match(outOfRange('19.0.0'), /omp 19\.0\.0/);
  });

  it('rejects an older patch', () => {
    const below = {min: '18.16.1', maxExclusive: '18.17.0'};
    const check = checkOmpVersion('18.16.0', below);
    assert.equal(check.ok, false);
    assert.match(check.message, /omp 18\.16\.0 .*>=18\.16\.1 <18\.17\.0/);
  });

  it('rejects a prerelease with a message and no throw', () => {
    const message = outOfRange('18.16.0-beta.1');
    assert.match(message, /18\.16\.0-beta\.1/);
    assert.match(message, />=18\.16\.0 <18\.17\.0/);
    assert.match(message, /extension\/README\.md/);
  });

  it('rejects a garbage string with a message and no throw', () => {
    const message = outOfRange('not a version');
    assert.match(message, /not a version/);
    assert.match(message, /extension\/README\.md/);
  });
});

describe('omp-range.json', () => {
  it('is a valid range pinned to the tested version', () => {
    const raw: unknown = JSON.parse(
      readFileSync(new URL('../../omp-range.json', import.meta.url), 'utf8'),
    );
    assert.ok(isOmpRange(raw));
    assert.deepEqual(raw, range);
  });
});

export type Version = [number, number, number];

export interface OmpRange {
  min: string;
  maxExclusive: string;
}

export type VersionCheck = {ok: true} | {ok: false; message: string};

export function parseVersion(s: string): Version | undefined {
  const m = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(s.trim());
  if (!m) return undefined;
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

function compareParts(a: number, b: number): number {
  if (a === b) return 0;
  return a < b ? -1 : 1;
}

export function compareVersions(a: Version, b: Version): number {
  const byPart = [
    compareParts(a[0], b[0]),
    compareParts(a[1], b[1]),
    compareParts(a[2], b[2]),
  ];
  return byPart.find(order => order !== 0) ?? 0;
}

export function isOmpRange(v: unknown): v is OmpRange {
  if (typeof v !== 'object' || v === null) return false;
  const r = v as Partial<Record<keyof OmpRange, unknown>>;
  return (
    typeof r.min === 'string' &&
    typeof r.maxExclusive === 'string' &&
    parseVersion(r.min) !== undefined &&
    parseVersion(r.maxExclusive) !== undefined
  );
}

export function checkOmpVersion(
  version: string,
  range: OmpRange,
): VersionCheck {
  const span = `>=${range.min} <${range.maxExclusive}`;
  const hint = 'See extension/README.md.';
  const v = parseVersion(version);
  const min = parseVersion(range.min);
  const max = parseVersion(range.maxExclusive);
  if (!min || !max) {
    return {
      ok: false,
      message: `skua: the supported omp range ${span} is not valid, so omp ${version} cannot be checked. ${hint}`,
    };
  }
  if (!v) {
    return {
      ok: false,
      message: `skua: omp version "${version}" is not a release version; the skua extension is tested on omp ${span}. ${hint}`,
    };
  }
  if (compareVersions(v, min) < 0 || compareVersions(v, max) >= 0) {
    return {
      ok: false,
      message: `skua: omp ${version} is outside the tested range ${span}; the skua extension may not work. ${hint}`,
    };
  }
  return {ok: true};
}

import {accessSync, constants, statSync} from 'node:fs';
import {delimiter, join} from 'node:path';
import type {OpenUrl} from '../support/auth/login.ts';

export type OpenUrlExec = (
  command: string,
  args: string[],
) => Promise<{code: number}>;

const BROWSER_COMMANDS = ['xdg-open', 'open', 'wslview'];

function isExecutableFile(path: string): boolean {
  try {
    accessSync(path, constants.X_OK);
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

function findOnPath(
  name: string,
  path: string | undefined,
): string | undefined {
  for (const dir of (path ?? '').split(delimiter)) {
    if (dir.length === 0) continue;
    const candidate = join(dir, name);
    if (isExecutableFile(candidate)) return candidate;
  }
  return undefined;
}

/** Opens `url` with `$BROWSER` (run through `sh -c`, so it may carry arguments), else the first of xdg-open, open, wslview on `PATH`; `false` when none exists or the command exits non-zero. */
export function createOpenUrl(opts: {
  exec: OpenUrlExec;
  env: Record<string, string | undefined>;
}): OpenUrl {
  return async url => {
    try {
      const browser = opts.env['BROWSER'];
      if (browser !== undefined && browser.trim().length > 0) {
        return (
          (await opts.exec('sh', ['-c', `${browser} "$1"`, 'sh', url])).code ===
          0
        );
      }
      for (const name of BROWSER_COMMANDS) {
        const bin = findOnPath(name, opts.env['PATH']);
        if (bin !== undefined) return (await opts.exec(bin, [url])).code === 0;
      }
      return false;
    } catch {
      return false;
    }
  };
}

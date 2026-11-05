import {randomBytes} from 'node:crypto';
import {renameSync, rmSync, writeFileSync} from 'node:fs';
import {join} from 'node:path';

export function writeAtomic(
  dir: string,
  name: string,
  content: string,
  mode: number,
): void {
  const tmp = join(dir, `.${name}.${randomBytes(6).toString('hex')}.tmp`);
  try {
    writeFileSync(tmp, content, {mode});
    renameSync(tmp, join(dir, name));
  } catch (err) {
    rmSync(tmp, {force: true});
    throw err;
  }
}

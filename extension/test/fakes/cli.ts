import {parseArgs} from 'node:util';
import {startFakeApi} from './api.ts';
import {startFakeIssuer} from './oidc.ts';

const USAGE = `Usage: node extension/test/fakes/cli.ts [options]
Starts the fake skua-api and the fake OIDC issuer on 127.0.0.1 and prints {"api", "issuer", "clientId"} as one JSON line.
  --port N               fake skua-api port (default: ephemeral)
  --issuer-port N        fake issuer port (default: ephemeral)
  --step-ms N            delay between remote steps (default 4000)
  --steps N              step count for the steps, nohb and drop scenarios (default 6)
  --gap-ms N             silence length for the long and nohb scenarios (default 240000)
  --hb-ms N              heartbeat interval on event streams (default 15000)
  --profiles-delay-ms N  delay before GET /v1alpha1/profiles answers (default 0)
  --no-expire-streams    keep event streams open past the bearer's exp
  --id-token-ttl-s N     id_token lifetime from the issuer (default 3600)
  --client-id ID         public client id the issuer accepts (default skua-omp)`;

const {values} = parseArgs({
  options: {
    port: {type: 'string'},
    'issuer-port': {type: 'string'},
    'step-ms': {type: 'string'},
    steps: {type: 'string'},
    'gap-ms': {type: 'string'},
    'hb-ms': {type: 'string'},
    'profiles-delay-ms': {type: 'string'},
    'no-expire-streams': {type: 'boolean'},
    'id-token-ttl-s': {type: 'string'},
    'client-id': {type: 'string'},
    help: {type: 'boolean'},
  },
});

if (values.help) {
  console.log(USAGE);
  process.exit(0);
}

function num(flag: string, v: string | undefined): number | undefined {
  if (v === undefined) return undefined;
  const n = Number(v);
  if (!Number.isInteger(n) || n < 0) {
    console.error(
      `--${flag} must be a non-negative integer, got ${v}\n${USAGE}`,
    );
    process.exit(2);
  }
  return n;
}

const clientId = values['client-id'] ?? 'skua-omp';
const api = await startFakeApi({
  port: num('port', values.port),
  stepMs: num('step-ms', values['step-ms']),
  steps: num('steps', values.steps),
  gapMs: num('gap-ms', values['gap-ms']),
  hbMs: num('hb-ms', values['hb-ms']),
  profilesDelayMs: num('profiles-delay-ms', values['profiles-delay-ms']),
  expireStreams: !values['no-expire-streams'],
});
const issuer = await startFakeIssuer({
  port: num('issuer-port', values['issuer-port']),
  clientId,
  idTokenTtlS: num('id-token-ttl-s', values['id-token-ttl-s']),
});
console.log(JSON.stringify({api: api.url, issuer: issuer.url, clientId}));

const stop = async (): Promise<void> => {
  await Promise.all([api.close(), issuer.close()]);
  process.exit(0);
};
process.once('SIGINT', () => void stop());
process.once('SIGTERM', () => void stop());

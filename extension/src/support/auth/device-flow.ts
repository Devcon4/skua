import {OAuthError} from '@oh-my-pi/pi-ai/error';
import {pollOAuthDeviceCodeFlow} from '@oh-my-pi/pi-ai/oauth/device-code';
import type {SkuaConfig} from './config.ts';
import {OidcError, optionalString, postForm} from './discovery.ts';
import type {IssuerMetadata} from './discovery.ts';
import {tokenSet} from './idtoken.ts';
import type {TokenSet} from './idtoken.ts';
import type {Notify} from './login.ts';

const DEVICE_GRANT = 'urn:ietf:params:oauth:grant-type:device_code';

const DEVICE_EXPIRED = 'the device code expired before the login was approved';
const DEFAULT_DEVICE_CODE_LIFETIME_SECONDS = 600;

type DeviceAuthorizationResponse = Partial<
  Record<
    | 'device_code'
    | 'user_code'
    | 'verification_uri'
    | 'verification_uri_complete'
    | 'interval'
    | 'expires_in',
    unknown
  >
>;

interface DeviceAuthorization {
  deviceCode: string;
  userCode: string;
  verificationUri: string;
  verificationUriComplete: string | undefined;
  pollSchedule: {intervalSeconds?: number; expiresInSeconds: number};
}

function positiveNumber(v: unknown): number | undefined {
  return typeof v === 'number' && v > 0 ? v : undefined;
}

function pollSchedule(
  start: DeviceAuthorizationResponse,
): DeviceAuthorization['pollSchedule'] {
  const expiresInSeconds =
    positiveNumber(start.expires_in) ?? DEFAULT_DEVICE_CODE_LIFETIME_SECONDS;
  const intervalSeconds = positiveNumber(start.interval);
  if (intervalSeconds === undefined) return {expiresInSeconds};
  return {intervalSeconds, expiresInSeconds};
}

function deviceAuthorization(
  start: DeviceAuthorizationResponse,
): DeviceAuthorization {
  const deviceCode = optionalString(start.device_code);
  const userCode = optionalString(start.user_code);
  const verificationUri = optionalString(start.verification_uri);
  if (!deviceCode || !userCode || !verificationUri) {
    throw new OidcError(
      'device_authorization',
      'device authorization response lacks device_code, user_code or verification_uri',
    );
  }
  return {
    deviceCode,
    userCode,
    verificationUri,
    verificationUriComplete: optionalString(start.verification_uri_complete),
    pollSchedule: pollSchedule(start),
  };
}

function deviceCodeInstructions(device: DeviceAuthorization): string {
  if (device.verificationUriComplete)
    return `skua login: visit ${device.verificationUriComplete} and confirm the code ${device.userCode}`;
  return `skua login: visit ${device.verificationUri} and enter the code ${device.userCode}`;
}

type DevicePollPending = {status: 'pending'} | {status: 'slow_down'};

function pollStatusOrThrow(err: OidcError): DevicePollPending {
  switch (err.code) {
    case 'authorization_pending':
      return {status: 'pending'};
    case 'slow_down':
      return {status: 'slow_down'};
    case 'expired_token':
      throw new OidcError('expired_token', DEVICE_EXPIRED, err.status);
    case 'access_denied':
      throw new OidcError(
        'access_denied',
        'the device login was denied',
        err.status,
      );
    default:
      throw err;
  }
}

export async function deviceFlow(
  cfg: SkuaConfig,
  meta: IssuerMetadata,
  notify: Notify,
): Promise<TokenSet> {
  if (!meta.deviceAuthorizationEndpoint) {
    throw new OidcError(
      'no_device',
      `issuer ${meta.issuer} does not advertise device_authorization_endpoint`,
    );
  }
  const start: DeviceAuthorizationResponse = await postForm(
    meta.deviceAuthorizationEndpoint,
    {client_id: cfg.clientId, scope: cfg.scopes.join(' ')},
  );
  const device = deviceAuthorization(start);
  notify(deviceCodeInstructions(device));
  try {
    return await pollOAuthDeviceCodeFlow<TokenSet>({
      ...device.pollSchedule,
      poll: async () => {
        try {
          const body = await postForm(meta.tokenEndpoint, {
            grant_type: DEVICE_GRANT,
            device_code: device.deviceCode,
            client_id: cfg.clientId,
          });
          return {
            status: 'complete',
            value: tokenSet(cfg, meta, body, undefined),
          };
        } catch (err) {
          if (!(err instanceof OidcError)) throw err;
          return pollStatusOrThrow(err);
        }
      },
    });
  } catch (err) {
    if (err instanceof OAuthError && err.kind === 'timeout')
      throw new OidcError('expired_token', DEVICE_EXPIRED);
    throw err;
  }
}

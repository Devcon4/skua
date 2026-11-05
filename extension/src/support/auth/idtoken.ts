import type {SkuaConfig} from './config.ts';
import {OidcError, isObject, optionalString} from './discovery.ts';
import type {IssuerMetadata} from './discovery.ts';

export interface TokenSet {
  issuer: string;
  clientId: string;
  idToken: string;
  refreshToken?: string;
  /** The id_token `exp` claim, in epoch seconds. */
  expiresAt: number;
}

export interface IdTokenClaims {
  iss: string;
  sub: string;
  aud: string[];
  exp: number;
  email: string | undefined;
  nonce: string | undefined;
}

type TokenResponse = Partial<Record<'id_token' | 'refresh_token', unknown>>;

type IdTokenPayload = Partial<Record<keyof IdTokenClaims, unknown>>;

function audienceOf(aud: unknown): string[] {
  if (typeof aud === 'string') return [aud];
  if (Array.isArray(aud))
    return aud.filter((a: unknown) => typeof a === 'string');
  return [];
}

/** Decodes the id_token payload without verifying the signature; skua-api verifies it. */
export function decodeIdToken(idToken: string): IdTokenClaims {
  const encoded = idToken.split('.')[1];
  let decoded: unknown;
  try {
    decoded = JSON.parse(
      Buffer.from(encoded ?? '', 'base64url').toString('utf8'),
    );
  } catch {
    throw new OidcError('id_token', 'id_token payload is not JSON');
  }
  if (!isObject(decoded))
    throw new OidcError('id_token', 'id_token payload is not a JSON object');
  const claims: IdTokenPayload = decoded;
  const aud = audienceOf(claims.aud);
  if (
    typeof claims.iss !== 'string' ||
    typeof claims.sub !== 'string' ||
    typeof claims.exp !== 'number'
  ) {
    throw new OidcError('id_token', 'id_token lacks iss, sub or exp');
  }
  return {
    iss: claims.iss,
    sub: claims.sub,
    aud,
    exp: claims.exp,
    email: optionalString(claims.email),
    nonce: optionalString(claims.nonce),
  };
}

export function tokenSet(
  cfg: SkuaConfig,
  meta: IssuerMetadata,
  body: TokenResponse,
  nonce: string | undefined,
): TokenSet {
  const idToken = optionalString(body.id_token);
  if (!idToken)
    throw new OidcError('id_token', 'token response has no id_token');
  const claims = decodeIdToken(idToken);
  if (claims.iss !== meta.issuer)
    throw new OidcError(
      'id_token',
      `id_token iss ${claims.iss} is not the configured issuer ${meta.issuer}`,
    );
  if (!claims.aud.includes(cfg.clientId))
    throw new OidcError(
      'id_token',
      `id_token aud does not contain client id ${cfg.clientId}`,
    );
  if (nonce !== undefined && claims.nonce !== nonce)
    throw new OidcError(
      'id_token',
      'id_token nonce does not match the login request',
    );
  const set: TokenSet = {
    issuer: cfg.issuer,
    clientId: cfg.clientId,
    idToken,
    expiresAt: claims.exp,
  };
  const refreshToken = optionalString(body.refresh_token);
  if (refreshToken) set.refreshToken = refreshToken;
  return set;
}

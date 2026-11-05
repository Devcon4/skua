export interface IssuerMetadata {
  issuer: string;
  authorizationEndpoint: string;
  tokenEndpoint: string;
  deviceAuthorizationEndpoint: string | undefined;
}

/** Login, discovery and token endpoint failure; `code` is the issuer's OAuth `error` value or one of this module's names. */
export class OidcError extends Error {
  readonly code: string;
  readonly status: number | undefined;
  constructor(code: string, message: string, status?: number) {
    super(message);
    this.name = 'OidcError';
    this.code = code;
    this.status = status;
  }
}

const REQUEST_TIMEOUT_MS = 15_000;

export function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

type OAuthErrorResponse = Partial<
  Record<'error' | 'error_description', unknown>
>;

type DiscoveryDocument = Partial<
  Record<
    | 'issuer'
    | 'code_challenge_methods_supported'
    | 'authorization_endpoint'
    | 'token_endpoint'
    | 'device_authorization_endpoint',
    unknown
  >
>;

function issuerRefusal(
  body: OAuthErrorResponse,
  status: number,
): OidcError | undefined {
  if (typeof body.error !== 'string') return undefined;
  const description =
    typeof body.error_description === 'string'
      ? `: ${body.error_description}`
      : '';
  return new OidcError(
    body.error,
    `issuer returned ${body.error}${description}`,
    status,
  );
}

async function getJson(url: string): Promise<unknown> {
  let res: Response;
  try {
    res = await fetch(url, {
      headers: {accept: 'application/json'},
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (err) {
    throw new OidcError('network', `cannot reach ${url}: ${errText(err)}`);
  }
  if (!res.ok)
    throw new OidcError(
      'discovery',
      `${url} answered ${String(res.status)}`,
      res.status,
    );
  try {
    return await res.json();
  } catch {
    throw new OidcError('discovery', `${url} did not return JSON`, res.status);
  }
}

export async function postForm(
  url: string,
  form: Record<string, string>,
): Promise<Record<string, unknown>> {
  let res: Response;
  try {
    res = await fetch(url, {
      method: 'POST',
      headers: {
        'content-type': 'application/x-www-form-urlencoded',
        accept: 'application/json',
      },
      body: new URLSearchParams(form),
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (err) {
    throw new OidcError('network', `cannot reach ${url}: ${errText(err)}`);
  }
  let body: unknown;
  try {
    body = await res.json();
  } catch {
    body = undefined;
  }
  if (res.ok && isObject(body)) return body;
  if (res.status >= 500)
    throw new OidcError(
      'server',
      `${url} answered ${String(res.status)}`,
      res.status,
    );
  const refusal = isObject(body) ? issuerRefusal(body, res.status) : undefined;
  if (refusal) throw refusal;
  throw new OidcError(
    'token_endpoint',
    `${url} answered ${String(res.status)} without an OAuth error`,
    res.status,
  );
}

export function optionalString(v: unknown): string | undefined {
  return typeof v === 'string' && v.length > 0 ? v : undefined;
}

export async function discover(issuer: string): Promise<IssuerMetadata> {
  const url = `${issuer.replace(/\/+$/, '')}/.well-known/openid-configuration`;
  const json = await getJson(url);
  if (!isObject(json))
    throw new OidcError('discovery', `${url} is not a JSON object`);
  const doc: DiscoveryDocument = json;
  if (doc.issuer !== issuer) {
    throw new OidcError(
      'issuer_mismatch',
      `issuer metadata names ${String(doc.issuer)}, expected the configured issuer ${issuer}`,
    );
  }
  const methods = doc.code_challenge_methods_supported;
  if (!Array.isArray(methods) || !methods.includes('S256')) {
    throw new OidcError(
      'no_s256',
      `issuer ${issuer} does not list S256 in code_challenge_methods_supported; skua login needs PKCE with S256`,
    );
  }
  const authorizationEndpoint = optionalString(doc.authorization_endpoint);
  const tokenEndpoint = optionalString(doc.token_endpoint);
  if (!authorizationEndpoint || !tokenEndpoint) {
    throw new OidcError(
      'discovery',
      `issuer ${issuer} metadata lacks authorization_endpoint or token_endpoint`,
    );
  }
  return {
    issuer,
    authorizationEndpoint,
    tokenEndpoint,
    deviceAuthorizationEndpoint: optionalString(
      doc.device_authorization_endpoint,
    ),
  };
}

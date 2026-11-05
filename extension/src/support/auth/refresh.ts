import type {SkuaConfig} from './config.ts';
import {discover, postForm} from './discovery.ts';
import {tokenSet} from './idtoken.ts';
import type {TokenSet} from './idtoken.ts';

export async function refresh(
  cfg: SkuaConfig,
  refreshToken: string,
): Promise<TokenSet> {
  const meta = await discover(cfg.issuer);
  const body = await postForm(meta.tokenEndpoint, {
    grant_type: 'refresh_token',
    refresh_token: refreshToken,
    client_id: cfg.clientId,
  });
  return tokenSet(cfg, meta, body, undefined);
}

import { publicProviderCatalog } from '../../../lib/provider-proxy.mjs';

const HEADERS = Object.freeze({
  'cache-control': 'public, max-age=300',
  'content-type': 'application/json; charset=utf-8',
  'x-content-type-options': 'nosniff',
});

export async function GET() {
  return new Response(JSON.stringify({ providers: publicProviderCatalog() }), {
    status: 200,
    headers: HEADERS,
  });
}

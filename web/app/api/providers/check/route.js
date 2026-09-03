import {
  buildProviderProbe,
  normalizeProbePayload,
  normalizeProviderKey,
  providerFailure,
} from '../../../../lib/provider-proxy.mjs';

export const runtime = 'nodejs';
export const maxDuration = 30;

const HEADERS = Object.freeze({
  'cache-control': 'no-store, max-age=0',
  'content-type': 'application/json; charset=utf-8',
  pragma: 'no-cache',
  'x-content-type-options': 'nosniff',
});

function json(status, value) {
  return new Response(JSON.stringify(value), { status, headers: HEADERS });
}

function errorResponse(status, message, details = {}) {
  return json(status, { error: { message, ...details } });
}

function sameOriginRequest(request) {
  const fetchSite = request.headers.get('sec-fetch-site');
  if (fetchSite && fetchSite !== 'same-origin' && fetchSite !== 'none') return false;
  const origin = request.headers.get('origin');
  return !origin || origin === new URL(request.url).origin;
}

export async function handleProviderCheck(request, fetchImpl = fetch) {
  if (!sameOriginRequest(request)) return errorResponse(403, 'Cross-site requests are not allowed.');
  if (!request.headers.get('content-type')?.toLowerCase().startsWith('application/json')) {
    return errorResponse(415, 'Send a JSON request.');
  }

  let apiKey;
  let payload;
  try {
    apiKey = normalizeProviderKey(request.headers.get('authorization'));
    const body = await request.text();
    if (new TextEncoder().encode(body).byteLength > 4096) {
      return errorResponse(413, 'The connection test is too large.');
    }
    payload = normalizeProbePayload(JSON.parse(body));
  } catch (error) {
    return errorResponse(400, error instanceof Error ? error.message : 'The connection test is invalid.');
  }

  const upstream = buildProviderProbe(payload, apiKey);
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 20_000);
  const abortUpstream = () => controller.abort();
  request.signal.addEventListener('abort', abortUpstream, { once: true });
  try {
    const response = await fetchImpl(upstream.url, { ...upstream.init, signal: controller.signal });
    if (!response.ok) {
      try { await response.body?.cancel(); } catch { /* nothing to retain */ }
      const failure = providerFailure(response.status);
      return errorResponse(response.status === 429 ? 429 : 502, failure.message, failure);
    }
    try { await response.body?.cancel(); } catch { /* the status is sufficient */ }
    return json(200, { ok: true, provider: payload.provider, model: payload.model });
  } catch (error) {
    const timedOut = error instanceof Error && error.name === 'AbortError';
    return errorResponse(502, timedOut ? 'The provider took too long to answer.' : 'The provider could not be reached.', {
      code: timedOut ? 'provider_timeout' : 'provider_unreachable',
      retryable: true,
    });
  } finally {
    clearTimeout(timeout);
    request.signal.removeEventListener('abort', abortUpstream);
  }
}

export async function POST(request) {
  return handleProviderCheck(request);
}

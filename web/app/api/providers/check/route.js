import {
  buildProviderProbe,
  extractProviderOutput,
  normalizeProbePayload,
  normalizeProviderKey,
  providerFailure,
} from '../../../../lib/provider-proxy.mjs';
import { ephemeralClientIdentity } from '../../../../lib/client-identity.mjs';
import { RequestCapacity } from '../../../../lib/request-capacity.mjs';
import { readBoundedText } from '../../../../lib/bounded-body.mjs';

export const runtime = 'nodejs';
export const maxDuration = 30;

const HEADERS = Object.freeze({
  'cache-control': 'no-store, max-age=0',
  'content-type': 'application/json; charset=utf-8',
  pragma: 'no-cache',
  'x-content-type-options': 'nosniff',
});

const checkCapacity = new RequestCapacity({
  maxConcurrent: 1,
  maxRequests: 5,
  windowMs: 60_000,
  maxClients: 2048,
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
  try {
    apiKey = normalizeProviderKey(request.headers.get('authorization'));
  } catch (error) {
    return errorResponse(401, error instanceof Error ? error.message : 'Load a provider key.');
  }

  if (Number(request.headers.get('content-length')) > 4096) {
    return errorResponse(413, 'The connection test is too large.');
  }
  const release = checkCapacity.acquire(ephemeralClientIdentity(request));
  if (!release) {
    return errorResponse(429, 'Too many connection tests were requested. Try again in a minute.', {
      code: 'connection_check_limited',
      retryable: true,
    });
  }
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 20_000);
  const abortUpstream = () => controller.abort();
  request.signal.addEventListener('abort', abortUpstream, { once: true });
  if (request.signal.aborted) abortUpstream();
  try {
    let value;
    try {
      value = JSON.parse(await readBoundedText(request.body, 4096, { signal: controller.signal }));
    } catch (error) {
      return errorResponse(error instanceof RangeError ? 413 : 400,
        error instanceof RangeError ? 'The connection test is too large.' : 'The connection test must contain valid JSON.');
    }
    let payload;
    try { payload = normalizeProbePayload(value); } catch (error) {
      return errorResponse(400, error.message);
    }
    const upstream = buildProviderProbe(payload, apiKey);
    controller.signal.throwIfAborted();
    const response = await fetchImpl(upstream.url, { ...upstream.init, signal: controller.signal });
    if (!response.ok) {
      try { await response.body?.cancel(); } catch { /* nothing to retain */ }
      const failure = providerFailure(response.status);
      return errorResponse(response.status === 429 ? 429 : 502, failure.message, failure);
    }
    try {
      const raw = await readBoundedText(response.body, 64 * 1024, { signal: controller.signal });
      extractProviderOutput(payload.provider, JSON.parse(raw));
    } catch {
      return errorResponse(502, 'The provider did not return readable text. Try another model or check its output limit.', {
        code: 'provider_output_missing', retryable: false,
      });
    }
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
    release();
  }
}

export async function POST(request) {
  return handleProviderCheck(request);
}

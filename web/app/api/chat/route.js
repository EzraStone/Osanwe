import {
  buildUpstreamRequest,
  extractProviderOutput,
  normalizeChatPayload,
  normalizeProviderKey,
  requestIsTooLarge,
  safeProviderError,
} from '../../../lib/provider-proxy.mjs';

const RESPONSE_HEADERS = Object.freeze({
  'cache-control': 'no-store, max-age=0',
  'content-type': 'application/json; charset=utf-8',
  pragma: 'no-cache',
  'x-content-type-options': 'nosniff',
});

function json(status, value) {
  return new Response(JSON.stringify(value), { status, headers: RESPONSE_HEADERS });
}

function sameOriginRequest(request) {
  const fetchSite = request.headers.get('sec-fetch-site');
  if (fetchSite && fetchSite !== 'same-origin' && fetchSite !== 'none') return false;
  const origin = request.headers.get('origin');
  return !origin || origin === new URL(request.url).origin;
}

export async function handleChatRequest(request, fetchImpl = fetch) {
  if (!sameOriginRequest(request)) return json(403, { error: 'Cross-site requests are not allowed.' });
  if (!request.headers.get('content-type')?.toLowerCase().startsWith('application/json')) {
    return json(415, { error: 'Send a JSON request.' });
  }

  let apiKey;
  try {
    apiKey = normalizeProviderKey(request.headers.get('authorization'));
  } catch (error) {
    return json(401, { error: error instanceof Error ? error.message : 'Connect a provider key.' });
  }

  let rawBody;
  try {
    rawBody = await request.text();
  } catch {
    return json(400, { error: 'The request body could not be read.' });
  }
  if (requestIsTooLarge(request, rawBody)) {
    return json(413, { error: 'The conversation is too large for this beta.' });
  }

  let payload;
  try {
    payload = normalizeChatPayload(JSON.parse(rawBody));
  } catch (error) {
    return json(400, { error: error instanceof Error ? error.message : 'The request is invalid.' });
  }

  const upstream = buildUpstreamRequest(payload, apiKey);
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 60_000);
  request.signal.addEventListener('abort', () => controller.abort(), { once: true });

  let response;
  try {
    response = await fetchImpl(upstream.url, { ...upstream.init, signal: controller.signal });
  } catch (error) {
    clearTimeout(timeout);
    const message = error instanceof Error && error.name === 'AbortError'
      ? 'The provider took too long to answer.'
      : 'The provider could not be reached.';
    return json(502, { error: message });
  }
  clearTimeout(timeout);

  if (!response.ok) {
    // Do not reflect the provider body: it can include account or request details.
    try { await response.body?.cancel(); } catch { /* nothing to retain */ }
    return json(response.status === 429 ? 429 : 502, { error: safeProviderError(response.status) });
  }

  try {
    const output = extractProviderOutput(payload.provider, await response.json());
    return json(200, { output });
  } catch (error) {
    return json(502, {
      error: error instanceof Error ? error.message : 'The provider returned an unreadable response.',
    });
  }
}

export async function POST(request) {
  return handleChatRequest(request);
}

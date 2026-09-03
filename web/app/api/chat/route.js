import {
  MAX_REQUEST_BYTES,
  buildUpstreamRequest,
  extractProviderOutput,
  normalizeChatPayload,
  normalizeProviderKey,
  providerFailure,
  providerStyle,
  requestIsTooLarge,
} from '../../../lib/provider-proxy.mjs';
import { normalizeProviderStream } from '../../../lib/provider-stream.mjs';
import { RequestCapacity } from '../../../lib/request-capacity.mjs';
import { ephemeralClientIdentity } from '../../../lib/client-identity.mjs';

export const runtime = 'nodejs';
export const maxDuration = 60;

const JSON_HEADERS = Object.freeze({
  'cache-control': 'no-store, max-age=0',
  'content-type': 'application/json; charset=utf-8',
  pragma: 'no-cache',
  'x-content-type-options': 'nosniff',
});

const STREAM_HEADERS = Object.freeze({
  'cache-control': 'no-store, max-age=0',
  'content-type': 'text/event-stream; charset=utf-8',
  pragma: 'no-cache',
  'x-accel-buffering': 'no',
  'x-content-type-options': 'nosniff',
});

const chatCapacity = new RequestCapacity({
  maxConcurrent: 3,
  maxRequests: 30,
  windowMs: 60_000,
  maxClients: 2048,
});

function json(status, value) {
  return new Response(JSON.stringify(value), { status, headers: JSON_HEADERS });
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

async function readBoundedText(body, limit) {
  if (!body || typeof body.getReader !== 'function') return '';
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let bytes = 0;
  let text = '';
  try {
    while (true) {
      const item = await reader.read();
      if (item.done) break;
      bytes += item.value.byteLength;
      if (bytes > limit) {
        try { await reader.cancel(); } catch { /* cleanup only */ }
        throw new RangeError('body too large');
      }
      text += decoder.decode(item.value, { stream: true });
    }
    text += decoder.decode();
    return text;
  } finally {
    try { reader.releaseLock(); } catch { /* cleanup only */ }
  }
}

function providerStream(output) {
  const body = [
    `data: ${JSON.stringify({ type: 'content_block_delta', delta: { type: 'text_delta', text: output } })}\n\n`,
    `data: ${JSON.stringify({ type: 'message_stop' })}\n\n`,
  ].join('');
  return new Response(body, { status: 200, headers: STREAM_HEADERS });
}

export async function handleChatRequest(request, fetchImpl = fetch) {
  if (!sameOriginRequest(request)) return errorResponse(403, 'Cross-site requests are not allowed.');
  if (!request.headers.get('content-type')?.toLowerCase().startsWith('application/json')) {
    return errorResponse(415, 'Send a JSON request.');
  }
  if (requestIsTooLarge(request)) {
    return errorResponse(413, 'The conversation is too large for this beta.');
  }

  let apiKey;
  try {
    apiKey = normalizeProviderKey(request.headers.get('authorization'));
  } catch (error) {
    return errorResponse(401, error instanceof Error ? error.message : 'Load a provider key.');
  }

  const release = chatCapacity.acquire(ephemeralClientIdentity(request));
  if (!release) return errorResponse(429, 'Too many requests are active from this connection. Try again shortly.');
  let streamOwnsCleanup = false;

  try {
    let rawBody;
    try {
      rawBody = await readBoundedText(request.body, MAX_REQUEST_BYTES);
    } catch (error) {
      const message = error instanceof RangeError
        ? 'The conversation is too large for this beta.'
        : 'The request body could not be read.';
      return errorResponse(error instanceof RangeError ? 413 : 400, message);
    }

    let payload;
    try {
      payload = normalizeChatPayload(JSON.parse(rawBody));
    } catch (error) {
      return errorResponse(400, error instanceof Error ? error.message : 'The request is invalid.');
    }

    const upstream = buildUpstreamRequest(payload, apiKey);
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 60_000);
    const abortUpstream = () => controller.abort();
    request.signal.addEventListener('abort', abortUpstream, { once: true });

    try {
      const response = await fetchImpl(upstream.url, { ...upstream.init, signal: controller.signal });
      if (!response.ok) {
        try { await response.body?.cancel(); } catch { /* nothing to retain */ }
        const failure = providerFailure(response.status);
        return errorResponse(response.status === 429 ? 429 : 502, failure.message, {
          code: failure.code,
          retryable: failure.retryable,
        });
      }

      if (response.headers.get('content-type')?.toLowerCase().includes('text/event-stream')) {
        const cleanup = () => {
          clearTimeout(timeout);
          request.signal.removeEventListener('abort', abortUpstream);
          release();
        };
        const body = normalizeProviderStream(providerStyle(payload.provider), response.body, {
          maxBytes: 2 * 1024 * 1024,
          onFinalize: cleanup,
        });
        streamOwnsCleanup = true;
        return new Response(body, { status: 200, headers: STREAM_HEADERS });
      }

      let value;
      try {
        const rawResponse = await readBoundedText(response.body, 1024 * 1024);
        value = JSON.parse(rawResponse);
      } catch (error) {
        if (error instanceof RangeError) return errorResponse(502, 'The provider response was unexpectedly large.');
        throw error;
      }
      return providerStream(extractProviderOutput(payload.provider, value));
    } catch (error) {
      const message = error instanceof Error && error.name === 'AbortError'
        ? 'The provider took too long to answer.'
        : error instanceof Error && error.message
          ? error.message
          : 'The provider could not be reached.';
      return errorResponse(502, message);
    } finally {
      if (!streamOwnsCleanup) {
        clearTimeout(timeout);
        request.signal.removeEventListener('abort', abortUpstream);
      }
    }
  } finally {
    if (!streamOwnsCleanup) release();
  }
}

export async function POST(request) {
  return handleChatRequest(request);
}

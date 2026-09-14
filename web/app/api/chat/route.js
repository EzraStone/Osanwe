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
import { readBoundedText } from '../../../lib/bounded-body.mjs';
import { hostedPauseResponse } from '../../../lib/hosted-availability.mjs';

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

function providerStream(output) {
  const body = [
    `data: ${JSON.stringify({ type: 'content_block_delta', delta: { type: 'text_delta', text: output } })}\n\n`,
    `data: ${JSON.stringify({ type: 'message_stop' })}\n\n`,
  ].join('');
  return new Response(body, { status: 200, headers: STREAM_HEADERS });
}

export async function handleChatRequest(request, fetchImpl = fetch) {
  const paused = hostedPauseResponse();
  if (paused) return paused;
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
      rawBody = await readBoundedText(request.body, MAX_REQUEST_BYTES, { signal: request.signal });
    } catch (error) {
      const message = error instanceof RangeError
        ? 'The conversation is too large for this beta.'
        : 'The request body could not be read.';
      return errorResponse(error instanceof RangeError ? 413 : 400, message);
    }

    let payload;
    let value;
    try { value = JSON.parse(rawBody); } catch {
      return errorResponse(400, 'The request must contain valid JSON.');
    }
    try {
      payload = normalizeChatPayload(value);
    } catch (error) {
      return errorResponse(400, error instanceof Error ? error.message : 'The request is invalid.');
    }

    const upstream = buildUpstreamRequest(payload, apiKey);
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 60_000);
    const abortUpstream = () => controller.abort();
    request.signal.addEventListener('abort', abortUpstream, { once: true });
    if (request.signal.aborted) abortUpstream();

    try {
      controller.signal.throwIfAborted();
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
          controller.abort();
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

      let output;
      try {
        const rawResponse = await readBoundedText(response.body, 1024 * 1024, { signal: controller.signal });
        let responseValue;
        try { responseValue = JSON.parse(rawResponse); } catch {
          return errorResponse(502, 'The provider returned an unreadable response.');
        }
        try { output = extractProviderOutput(payload.provider, responseValue); } catch (error) {
          return errorResponse(502, error.message);
        }
      } catch (error) {
        if (error instanceof RangeError) return errorResponse(502, 'The provider response was unexpectedly large.');
        throw error;
      }
      return providerStream(output);
    } catch (error) {
      const message = error instanceof Error && error.name === 'AbortError'
        ? 'The provider took too long to answer.'
        : 'The provider could not be reached.';
      return errorResponse(502, message);
    } finally {
      if (!streamOwnsCleanup) {
        controller.abort();
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

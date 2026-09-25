import { validateProviderKey } from './credentials.js';
import { readResponseText } from './response-body.js';

let providerCache = null;

async function providerCatalog(fetchImpl = globalThis.fetch, force = false) {
  if (!force && providerCache) return providerCache;
  const response = await fetchImpl('/api/providers', {
    headers: { accept: 'application/json' },
    cache: 'no-store',
  });
  if (!response.ok) throw await responseError(response, 'provider catalog request failed');
  const value = await response.json();
  providerCache = Array.isArray(value.providers) ? value.providers : [];
  return providerCache;
}

export async function loadStatus(fetchImpl = globalThis.fetch) {
  const providers = await providerCatalog(fetchImpl);
  const origin = typeof location === 'object' && location.origin ? location.origin : 'this hosted page';
  return {
    paying: 'byok',
    endpoint: origin,
    upstream: 'Selected in Settings',
    retained: 'no server conversation history',
    api_style: 'hosted',
    providers,
    build: { version: 'Hosted beta', commit: 'browser' },
    privacy: {
      gateway_content_access: 'prompt_and_answer_visible_in_transit',
      operator_separation: 'not_provided_by_hosted_byok',
      conversation_history: 'not_intentionally_retained_by_osanwe',
    },
  };
}

export async function loadModels(provider = 'groq', fetchImpl = globalThis.fetch, force = false) {
  const providers = await providerCatalog(fetchImpl, force);
  const selected = providers.find((item) => item && item.id === provider);
  const models = selected && Array.isArray(selected.models) ? selected.models : [];
  return {
    data: models.map((id) => ({
      id,
      type: 'model',
      capabilities: { text: true, streaming: true, tools: false, images: false },
      limits: { max_request_bytes: 65536, max_output_tokens: 2048 },
      osanwe: {
        provider_account: 'your_provider_account',
        relay_content_access: 'not_applicable',
        gateway_content_access: 'prompt_and_answer_visible_in_transit',
        conversation_history: 'not_intentionally_retained_by_osanwe',
        address_separation: 'not_provided_by_hosted_byok',
        provider_retention: 'see_provider_policy',
        provider_training: 'see_provider_policy',
        provider_identity: selected?.label || provider,
      },
    })),
  };
}

export async function activateInviteBook() {
  throw new Error('Invitation files are available only in the local relay client.');
}

function normalizeMessages(messages) {
  if (!Array.isArray(messages) || messages.length === 0) throw new TypeError('at least one message is required');
  return messages.map((message) => {
    if (!message || (message.role !== 'user' && message.role !== 'assistant')) {
      throw new TypeError('message roles must be user or assistant');
    }
    if (typeof message.content !== 'string') throw new TypeError('message content must be text');
    return { role: message.role, content: message.content };
  });
}

export async function sendMessages(input, {
  signal,
  fetchImpl = globalThis.fetch,
  apiKey = '',
  provider = 'groq',
  mode = 'chat',
} = {}) {
  if (typeof input.model !== 'string' || !input.model.trim()) throw new TypeError('a model is required');
  validateProviderKey(apiKey);
  const response = await fetchImpl('/api/chat', {
    method: 'POST',
    redirect: 'error',
    cache: 'no-store',
    credentials: 'omit',
    signal,
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream, application/json',
    },
    body: JSON.stringify({
      provider,
      model: input.model.trim(),
      mode,
      messages: normalizeMessages(input.messages),
    }),
  });
  if (!response.ok) throw await responseError(response, `request failed with status ${response.status}`);
  return response;
}

export async function testProviderConnection({ provider, model, apiKey }, fetchImpl = globalThis.fetch) {
  if (typeof provider !== 'string' || !provider) throw new TypeError('a provider is required');
  if (typeof model !== 'string' || !model.trim()) throw new TypeError('a model is required');
  validateProviderKey(apiKey);
  const response = await fetchImpl('/api/providers/check', {
    method: 'POST',
    redirect: 'error',
    cache: 'no-store',
    credentials: 'omit',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      accept: 'application/json',
    },
    body: JSON.stringify({ provider, model: model.trim() }),
  });
  if (!response.ok) throw await responseError(response, `connection test failed with status ${response.status}`);
  return response.json();
}

export async function responseError(response, fallback) {
  const text = await readResponseText(response).catch(() => '');
  try {
    const parsed = JSON.parse(text);
    const error = parsed && parsed.error;
    const message = typeof error === 'string' ? error : error && error.message;
    if (typeof message === 'string' && message && message.length <= 800) {
      const result = new Error(message);
      result.status = response.status;
      if (error && typeof error === 'object') {
        if (typeof error.code === 'string') result.code = error.code;
        if (typeof error.retryable === 'boolean') result.retryable = error.retryable;
      }
      return result;
    }
  } catch {
    // Edge error pages may contain identifiers or markup. Do not echo them.
  }
  const messages = {
    401: 'The provider rejected that API key. Check the provider selected in Settings.',
    403: 'This request was blocked. Check your provider access and try again.',
    429: 'Too many requests. Wait a minute before trying again. Shared Wi-Fi may share this limit.',
    503: 'AI connections are temporarily unavailable. Please try again later.',
    504: 'The request timed out. Please try again.',
  };
  const result = new Error(messages[response.status] || fallback);
  result.status = response.status;
  result.retryable = [429, 502, 503, 504].includes(response.status);
  return result;
}

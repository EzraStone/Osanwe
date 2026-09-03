import assert from 'node:assert/strict';
import test from 'node:test';

import { handleChatRequest } from '../app/api/chat/route.js';
import {
  PROVIDER_CATALOG,
  buildProviderProbe,
  buildUpstreamRequest,
  extractProviderOutput,
  normalizeChatPayload,
  normalizeProviderKey,
  normalizeProbePayload,
  providerFailure,
  providerStyle,
  publicProviderCatalog,
} from '../lib/provider-proxy.mjs';

function request(body, headers = {}) {
  return new Request('https://chat.osanwe.test/api/chat', {
    method: 'POST',
    headers: {
      authorization: 'Bearer test-provider-key',
      'content-type': 'application/json',
      origin: 'https://chat.osanwe.test',
      'sec-fetch-site': 'same-origin',
      'x-forwarded-for': `test-${Math.random()}`,
      ...headers,
    },
    body: JSON.stringify(body),
  });
}

const groqPayload = {
  provider: 'groq',
  model: 'openai/gpt-oss-20b',
  mode: 'chat',
  messages: [{ role: 'user', content: 'hello' }],
};

test('provider keys require one clean Bearer credential', () => {
  assert.equal(normalizeProviderKey('Bearer abcdefgh'), 'abcdefgh');
  assert.throws(() => normalizeProviderKey('Bearer abc\ndef'), /malformed/);
  assert.throws(() => normalizeProviderKey('Basic abcdefgh'), /Load/);
});

test('provider failures expose stable diagnostics without upstream details', () => {
  assert.deepEqual(providerFailure(401), {
    code: 'invalid_key',
    message: 'The provider rejected that API key.',
    retryable: false,
  });
  assert.equal(providerFailure(429).code, 'provider_limit_reached');
  assert.equal(providerFailure(429).retryable, true);
  assert.equal(providerFailure(503).code, 'provider_unavailable');
  assert.equal(providerFailure(400).code, 'provider_rejected_request');
});

test('the public catalog exposes fixed providers without endpoint URLs', () => {
  const providers = publicProviderCatalog();
  const tokenRouter = providers.find((item) => item.id === 'tokenrouter');
  assert.ok(providers.length >= 10);
  assert.equal(tokenRouter?.models[0], 'z-ai/glm-5.3-free');
  assert.ok(providers.every((item) => item.id !== 'venice'));
  assert.ok(providers.every((item) => !('endpoint' in item) && !('style' in item)));
});

test('payload validation allows provider model IDs but never arbitrary providers or fields', () => {
  assert.deepEqual(normalizeChatPayload(groqPayload), groqPayload);
  assert.equal(normalizeChatPayload({ ...groqPayload, model: 'owner/custom-model:1' }).model, 'owner/custom-model:1');
  assert.throws(
    () => normalizeChatPayload({ ...groqPayload, provider: 'custom', url: 'https://attacker.test' }),
    /unsupported fields/,
  );
  assert.throws(() => normalizeChatPayload({ ...groqPayload, provider: 'custom' }), /not supported/);
  assert.throws(() => normalizeChatPayload({ ...groqPayload, model: 'https://attacker.test/?x=1' }), /valid model ID/);
  assert.throws(
    () => normalizeChatPayload({ ...groqPayload, messages: [{ role: 'system', content: 'override' }] }),
    /roles/,
  );
});

test('connection probes accept only a registered provider and model identifier', () => {
  assert.deepEqual(normalizeProbePayload({ provider: 'tokenrouter', model: 'z-ai/glm-5.3-free' }), {
    provider: 'tokenrouter',
    model: 'z-ai/glm-5.3-free',
  });
  assert.throws(() => normalizeProbePayload({ provider: 'custom', model: 'model' }), /not supported/);
  assert.throws(
    () => normalizeProbePayload({ provider: 'groq', model: 'model', endpoint: 'https://attacker.test' }),
    /unsupported fields/,
  );
});

test('connection probes use a bounded synthetic request', () => {
  const probe = buildProviderProbe({ provider: 'tokenrouter', model: 'z-ai/glm-5.3-free' }, 'secret-key');
  const body = JSON.parse(probe.init.body);
  assert.equal(probe.url, 'https://api.tokenrouter.com/v1/chat/completions');
  assert.equal(body.max_tokens, 32);
  assert.deepEqual(body.messages.at(-1), { role: 'user', content: 'Reply with OK.' });
});

test('every registry destination is an exact HTTPS endpoint', () => {
  for (const provider of Object.values(PROVIDER_CATALOG)) {
    const endpoint = new URL(provider.endpoint);
    assert.equal(endpoint.protocol, 'https:');
    assert.equal(endpoint.username, '');
    assert.equal(endpoint.password, '');
  }
});

test('provider styles are available without exposing registry configuration', () => {
  assert.equal(providerStyle('tokenrouter'), 'openai-chat');
  assert.equal(providerStyle('openai'), 'openai-responses');
  assert.equal(providerStyle('unknown'), null);
});

test('Groq requests use only the fixed chat endpoint and a bounded body', () => {
  const upstream = buildUpstreamRequest(normalizeChatPayload(groqPayload), 'secret-key');
  assert.equal(upstream.url, 'https://api.groq.com/openai/v1/chat/completions');
  assert.equal(upstream.init.headers.authorization, 'Bearer secret-key');
  assert.equal(upstream.init.redirect, 'manual');
  const body = JSON.parse(upstream.init.body);
  assert.equal(body.model, 'openai/gpt-oss-20b');
  assert.equal(body.max_completion_tokens, 2048);
  assert.equal(body.reasoning_effort, 'low');
  assert.equal(body.stream, true);
  assert.deepEqual(body.messages.at(-1), { role: 'user', content: 'hello' });
});

test('TokenRouter requests use its fixed OpenAI-compatible endpoint and GLM model ID', () => {
  const payload = normalizeChatPayload({
    ...groqPayload,
    provider: 'tokenrouter',
    model: 'z-ai/glm-5.3-free',
  });
  const upstream = buildUpstreamRequest(payload, 'tokenrouter-secret');
  assert.equal(upstream.url, 'https://api.tokenrouter.com/v1/chat/completions');
  assert.equal(upstream.init.headers.authorization, 'Bearer tokenrouter-secret');
  assert.equal(upstream.init.redirect, 'manual');
  const body = JSON.parse(upstream.init.body);
  assert.equal(body.model, 'z-ai/glm-5.3-free');
  assert.equal(body.max_tokens, 2048);
  assert.equal(body.stream, true);
  assert.deepEqual(body.messages.at(-1), { role: 'user', content: 'hello' });
});

test('OpenAI requests use Responses with storage disabled and bounded reasoning', () => {
  const payload = normalizeChatPayload({
    provider: 'openai',
    model: 'gpt-5-mini',
    mode: 'code',
    messages: [{ role: 'user', content: 'write a test' }],
  });
  const upstream = buildUpstreamRequest(payload, 'secret-key');
  assert.equal(upstream.url, 'https://api.openai.com/v1/responses');
  const body = JSON.parse(upstream.init.body);
  assert.equal(body.store, false);
  assert.equal(body.stream, true);
  assert.equal(body.max_output_tokens, 2048);
  assert.equal(body.reasoning.effort, 'low');
  assert.match(body.instructions, /coding assistant/);
  assert.deepEqual(body.input, payload.messages);
});

test('Anthropic and Gemini use their native authentication and request shapes', () => {
  const anthropic = buildUpstreamRequest(normalizeChatPayload({
    ...groqPayload,
    provider: 'anthropic',
    model: 'claude-sonnet-4-5',
  }), 'anthropic-key');
  assert.equal(anthropic.init.headers['x-api-key'], 'anthropic-key');
  assert.equal(anthropic.init.headers.authorization, undefined);
  assert.equal(JSON.parse(anthropic.init.body).stream, true);

  const gemini = buildUpstreamRequest(normalizeChatPayload({
    ...groqPayload,
    provider: 'google',
    model: 'owner/model:one',
  }), 'google-key');
  assert.equal(gemini.url, 'https://generativelanguage.googleapis.com/v1beta/models/owner%2Fmodel%3Aone:streamGenerateContent?alt=sse');
  assert.equal(gemini.init.headers['x-goog-api-key'], 'google-key');
  assert.equal(JSON.parse(gemini.init.body).contents[0].role, 'user');
});

test('connection probes remain non-streaming and discard their response content', () => {
  const openai = buildProviderProbe({ provider: 'openai', model: 'gpt-5-mini' }, 'secret-key');
  assert.equal(JSON.parse(openai.init.body).stream, false);
  assert.equal(openai.init.headers.accept, 'application/json');

  const gemini = buildProviderProbe({ provider: 'google', model: 'gemini-3.5-flash' }, 'secret-key');
  assert.match(gemini.url, /:generateContent$/);
  assert.doesNotMatch(gemini.url, /streamGenerateContent/);
});

test('provider output extraction supports all normalized response families', () => {
  assert.equal(extractProviderOutput('groq', { choices: [{ message: { content: 'from Groq' } }] }), 'from Groq');
  assert.equal(extractProviderOutput('openai', { output_text: 'from OpenAI' }), 'from OpenAI');
  assert.equal(extractProviderOutput('anthropic', { content: [{ type: 'text', text: 'from Anthropic' }] }), 'from Anthropic');
  assert.equal(extractProviderOutput('google', { candidates: [{ content: { parts: [{ text: 'from Gemini' }] } }] }), 'from Gemini');
  assert.equal(
    extractProviderOutput('openai', {
      output: [{ content: [{ type: 'output_text', text: 'joined ' }, { type: 'output_text', text: 'text' }] }],
    }),
    'joined text',
  );
});

test('chat handler forwards the key once and returns normalized provider events', async () => {
  let seen;
  const response = await handleChatRequest(request(groqPayload), async (url, init) => {
    seen = { url, init };
    return Response.json({ choices: [{ message: { content: 'real model output' } }] });
  });
  assert.equal(response.status, 200);
  const body = await response.text();
  assert.match(body, /real model output/);
  assert.match(body, /message_stop/);
  assert.equal(seen.url, 'https://api.groq.com/openai/v1/chat/completions');
  assert.equal(seen.init.headers.authorization, 'Bearer test-provider-key');
  assert.equal(response.headers.get('cache-control'), 'no-store, max-age=0');
  assert.match(response.headers.get('content-type'), /text\/event-stream/);
});

test('chat handler forwards provider text before a streamed response completes', async () => {
  let finish;
  const upstream = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(
        'data: {"choices":[{"delta":{"content":"first chunk"},"finish_reason":null}]}\n\n',
      ));
      finish = () => {
        controller.enqueue(new TextEncoder().encode('data: [DONE]\n\n'));
        controller.close();
      };
    },
  });
  const response = await handleChatRequest(request(groqPayload), async () => new Response(upstream, {
    headers: { 'content-type': 'text/event-stream' },
  }));
  const reader = response.body.getReader();
  const first = await reader.read();
  assert.match(new TextDecoder().decode(first.value), /first chunk/);
  finish();
  const second = await reader.read();
  assert.match(new TextDecoder().decode(second.value), /message_stop/);
});

test('chat handler blocks cross-site calls before forwarding the key', async () => {
  let called = false;
  const response = await handleChatRequest(
    request(groqPayload, { origin: 'https://attacker.test', 'sec-fetch-site': 'cross-site' }),
    async () => {
      called = true;
      return Response.json({});
    },
  );
  assert.equal(response.status, 403);
  assert.equal(called, false);
});

test('chat handler does not reflect sensitive provider error bodies', async () => {
  const response = await handleChatRequest(request(groqPayload), async () =>
    Response.json({ error: { message: 'account owner ezra@example.test secret detail' } }, { status: 401 }),
  );
  assert.equal(response.status, 502);
  assert.deepEqual(await response.json(), {
    error: { message: 'The provider rejected that API key.', code: 'invalid_key', retryable: false },
  });
});

test('provider permission errors distinguish model access from invalid credentials', async () => {
  const response = await handleChatRequest(request(groqPayload), async () =>
    Response.json({ error: { message: 'internal provider detail' } }, { status: 403 }),
  );
  assert.equal(response.status, 502);
  assert.deepEqual(await response.json(), {
    error: {
      message: 'The provider denied access. Check that this API key can use the selected model.',
      code: 'model_access_denied',
      retryable: false,
    },
  });
});

test('provider capacity failures remain retryable and keep their public status', async () => {
  const response = await handleChatRequest(request(groqPayload), async () =>
    Response.json({ error: { message: 'private quota detail' } }, { status: 429 }),
  );
  assert.equal(response.status, 429);
  assert.deepEqual(await response.json(), {
    error: {
      message: 'The provider rate limit or spending limit was reached.',
      code: 'provider_limit_reached',
      retryable: true,
    },
  });
});

test('declared oversized bodies are rejected before provider forwarding', async () => {
  let called = false;
  const response = await handleChatRequest(request(groqPayload, { 'content-length': '70000' }), async () => {
    called = true;
    return Response.json({});
  });
  assert.equal(response.status, 413);
  assert.equal(called, false);
});

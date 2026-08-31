import assert from 'node:assert/strict';
import test from 'node:test';

import { handleChatRequest } from '../app/api/chat/route.js';
import {
  PROVIDER_CATALOG,
  buildUpstreamRequest,
  extractProviderOutput,
  normalizeChatPayload,
  normalizeProviderKey,
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

test('the public catalog exposes fixed providers without endpoint URLs', () => {
  const providers = publicProviderCatalog();
  assert.ok(providers.length >= 10);
  assert.ok(providers.some((item) => item.id === 'venice'));
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

test('every registry destination is an exact HTTPS endpoint', () => {
  for (const provider of Object.values(PROVIDER_CATALOG)) {
    const endpoint = new URL(provider.endpoint);
    assert.equal(endpoint.protocol, 'https:');
    assert.equal(endpoint.username, '');
    assert.equal(endpoint.password, '');
  }
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
  assert.equal(body.stream, false);
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

  const gemini = buildUpstreamRequest(normalizeChatPayload({
    ...groqPayload,
    provider: 'google',
    model: 'owner/model:one',
  }), 'google-key');
  assert.equal(gemini.url, 'https://generativelanguage.googleapis.com/v1beta/models/owner%2Fmodel%3Aone:generateContent');
  assert.equal(gemini.init.headers['x-goog-api-key'], 'google-key');
  assert.equal(JSON.parse(gemini.init.body).contents[0].role, 'user');
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
  assert.deepEqual(await response.json(), { error: { message: 'The provider rejected that API key.' } });
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

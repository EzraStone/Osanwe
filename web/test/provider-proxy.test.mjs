import assert from 'node:assert/strict';
import test from 'node:test';

import { handleChatRequest } from '../app/api/chat/route.js';
import {
  buildUpstreamRequest,
  extractProviderOutput,
  normalizeChatPayload,
  normalizeProviderKey,
} from '../lib/provider-proxy.mjs';

function request(body, headers = {}) {
  return new Request('https://chat.osanwe.test/api/chat', {
    method: 'POST',
    headers: {
      authorization: 'Bearer test-provider-key',
      'content-type': 'application/json',
      origin: 'https://chat.osanwe.test',
      'sec-fetch-site': 'same-origin',
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
  assert.throws(() => normalizeProviderKey('Basic abcdefgh'), /Connect/);
});

test('payload validation rejects arbitrary providers, models, and fields', () => {
  assert.deepEqual(normalizeChatPayload(groqPayload), groqPayload);
  assert.throws(
    () => normalizeChatPayload({ ...groqPayload, provider: 'custom', url: 'https://attacker.test' }),
    /unsupported fields/,
  );
  assert.throws(() => normalizeChatPayload({ ...groqPayload, model: 'unpriced-model' }), /not available/);
  assert.throws(
    () => normalizeChatPayload({ ...groqPayload, messages: [{ role: 'system', content: 'override' }] }),
    /roles/,
  );
});

test('Groq requests use only the fixed chat endpoint and a bounded body', () => {
  const upstream = buildUpstreamRequest(normalizeChatPayload(groqPayload), 'secret-key');
  assert.equal(upstream.url, 'https://api.groq.com/openai/v1/chat/completions');
  assert.equal(upstream.init.headers.authorization, 'Bearer secret-key');
  assert.equal(upstream.init.redirect, 'manual');
  const body = JSON.parse(upstream.init.body);
  assert.equal(body.model, 'openai/gpt-oss-20b');
  assert.equal(body.max_completion_tokens, 2048);
  assert.equal(body.stream, false);
  assert.deepEqual(body.messages.at(-1), { role: 'user', content: 'hello' });
});

test('OpenAI requests use Responses with storage disabled', () => {
  const payload = normalizeChatPayload({
    provider: 'openai',
    model: 'gpt-5.6-luna',
    mode: 'code',
    messages: [{ role: 'user', content: 'write a test' }],
  });
  const upstream = buildUpstreamRequest(payload, 'secret-key');
  assert.equal(upstream.url, 'https://api.openai.com/v1/responses');
  const body = JSON.parse(upstream.init.body);
  assert.equal(body.store, false);
  assert.equal(body.max_output_tokens, 2048);
  assert.match(body.instructions, /coding assistant/);
  assert.deepEqual(body.input, payload.messages);
});

test('provider output extraction supports Groq and OpenAI response shapes', () => {
  assert.equal(
    extractProviderOutput('groq', { choices: [{ message: { content: 'from Groq' } }] }),
    'from Groq',
  );
  assert.equal(extractProviderOutput('openai', { output_text: 'from OpenAI' }), 'from OpenAI');
  assert.equal(
    extractProviderOutput('openai', {
      output: [{ content: [{ type: 'output_text', text: 'joined ' }, { type: 'output_text', text: 'text' }] }],
    }),
    'joined text',
  );
});

test('chat handler forwards the key once and returns real provider text', async () => {
  let seen;
  const response = await handleChatRequest(request(groqPayload), async (url, init) => {
    seen = { url, init };
    return Response.json({ choices: [{ message: { content: 'real model output' } }] });
  });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { output: 'real model output' });
  assert.equal(seen.url, 'https://api.groq.com/openai/v1/chat/completions');
  assert.equal(seen.init.headers.authorization, 'Bearer test-provider-key');
  assert.equal(response.headers.get('cache-control'), 'no-store, max-age=0');
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
  assert.deepEqual(await response.json(), { error: 'The provider rejected that API key.' });
});

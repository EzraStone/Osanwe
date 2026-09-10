import assert from 'node:assert/strict';
import test from 'node:test';
import { normalizeChatPayload, normalizeProbePayload, buildUpstreamRequest } from '../lib/provider-proxy.mjs';
const payload = { provider: 'tokenrouter', model: 'z-ai/glm-5.3-free', mode: 'chat', messages: [{ role: 'user', content: 'hello' }] };

test('callers may lower but not raise the server output ceiling', () => {
  for (const limit of [0, -1, 2049, 1.5, '32', null]) {
    assert.throws(() => normalizeChatPayload({ ...payload, max_output_tokens: limit }));
  }
  for (const provider of ['tokenrouter', 'groq', 'openai', 'anthropic', 'google']) {
    const normalized = normalizeChatPayload({ ...payload, provider, max_output_tokens: 64 });
    const body = JSON.parse(buildUpstreamRequest(normalized, 'synthetic-key').init.body);
    assert.equal(body.max_tokens ?? body.max_completion_tokens ?? body.max_output_tokens ?? body.generation_config?.max_output_tokens, 64);
  }
});

test('inherited object properties cannot become provider endpoints', () => {
  for (const provider of ['constructor', '__proto__', 'toString', ['groq']]) {
    assert.throws(() => normalizeChatPayload({ ...payload, provider }), /provider is not supported/);
    assert.throws(() => normalizeProbePayload({ provider, model: payload.model }), /provider is not supported/);
  }
});

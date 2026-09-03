import assert from 'node:assert/strict';
import test from 'node:test';

import {
  encodeNormalizedEvent,
  normalizeProviderEvent,
  normalizeProviderStream,
} from '../lib/provider-stream.mjs';

test('OpenAI-compatible stream deltas become Osanwe text events', () => {
  const events = normalizeProviderEvent('openai-chat', {
    event: 'message',
    data: JSON.stringify({ choices: [{ delta: { content: 'hello' }, finish_reason: null }] }),
  });
  assert.deepEqual(events, [{
    type: 'content_block_delta',
    delta: { type: 'text_delta', text: 'hello' },
  }]);
});

test('OpenAI-compatible finish markers stop the normalized stream', () => {
  assert.deepEqual(normalizeProviderEvent('openai-chat', { event: 'message', data: '[DONE]' }), [
    { type: 'message_stop' },
  ]);
  assert.deepEqual(normalizeProviderEvent('openai-chat', {
    event: 'message',
    data: JSON.stringify({ choices: [{ delta: {}, finish_reason: 'stop' }] }),
  }), [{ type: 'message_stop' }]);
});

test('Anthropic content deltas and stop events use the same client protocol', () => {
  assert.deepEqual(normalizeProviderEvent('anthropic', {
    event: 'content_block_delta',
    data: JSON.stringify({ type: 'content_block_delta', delta: { type: 'text_delta', text: 'answer' } }),
  }), [{ type: 'content_block_delta', delta: { type: 'text_delta', text: 'answer' } }]);
  assert.deepEqual(normalizeProviderEvent('anthropic', {
    event: 'message_stop',
    data: JSON.stringify({ type: 'message_stop' }),
  }), [{ type: 'message_stop' }]);
});

test('OpenAI Responses text and completion events are normalized', () => {
  assert.deepEqual(normalizeProviderEvent('openai-responses', {
    event: 'response.output_text.delta',
    data: JSON.stringify({ type: 'response.output_text.delta', delta: 'response text' }),
  }), [{ type: 'content_block_delta', delta: { type: 'text_delta', text: 'response text' } }]);
  assert.deepEqual(normalizeProviderEvent('openai-responses', {
    event: 'response.completed',
    data: JSON.stringify({ type: 'response.completed', response: { id: 'discarded' } }),
  }), [{ type: 'message_stop' }]);
});

test('Gemini streaming candidates expose only text and completion', () => {
  const events = normalizeProviderEvent('gemini', {
    event: 'message',
    data: JSON.stringify({
      candidates: [{
        content: { parts: [{ text: 'Gemini text' }, { thoughtSignature: 'discarded' }] },
        finishReason: 'STOP',
      }],
      usageMetadata: { promptTokenCount: 12 },
    }),
  });
  assert.deepEqual(events, [
    { type: 'content_block_delta', delta: { type: 'text_delta', text: 'Gemini text' } },
    { type: 'message_stop' },
  ]);
});

test('normalized events use one data record and never preserve provider fields', () => {
  const encoded = encodeNormalizedEvent({
    type: 'content_block_delta',
    delta: { type: 'text_delta', text: 'safe text' },
  });
  assert.equal(encoded, 'data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"safe text"}}\n\n');
  assert.doesNotMatch(encoded, /request_id|usage|account/);
});

test('provider stream emits text before the upstream response completes', async () => {
  let finish;
  const upstream = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode('data: {"choices":[{"delta":{"content":"first"},"finish_reason":null}]}\n\n'));
      finish = () => {
        controller.enqueue(new TextEncoder().encode('data: [DONE]\n\n'));
        controller.close();
      };
    },
  });
  const reader = normalizeProviderStream('openai-chat', upstream).getReader();
  const first = await reader.read();
  assert.match(new TextDecoder().decode(first.value), /first/);
  finish();
  const rest = await reader.read();
  assert.match(new TextDecoder().decode(rest.value), /message_stop/);
});

test('provider stream emits only one terminal event', async () => {
  const upstream = new Response([
    'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n',
    'data: [DONE]\n\n',
  ].join('')).body;
  const response = new Response(normalizeProviderStream('openai-chat', upstream));
  const body = await response.text();
  assert.equal(body.match(/message_stop/g)?.length, 1);
});

test('provider stream finalizes its request lifecycle once', async () => {
  let finalized = 0;
  const upstream = new Response('data: [DONE]\n\n').body;
  const response = new Response(normalizeProviderStream('openai-chat', upstream, {
    onFinalize() { finalized += 1; },
  }));
  await response.text();
  assert.equal(finalized, 1);
});

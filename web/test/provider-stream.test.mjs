import assert from 'node:assert/strict';
import test from 'node:test';

import { encodeNormalizedEvent, normalizeProviderEvent } from '../lib/provider-stream.mjs';

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

test('normalized events use one data record and never preserve provider fields', () => {
  const encoded = encodeNormalizedEvent({
    type: 'content_block_delta',
    delta: { type: 'text_delta', text: 'safe text' },
  });
  assert.equal(encoded, 'data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"safe text"}}\n\n');
  assert.doesNotMatch(encoded, /request_id|usage|account/);
});

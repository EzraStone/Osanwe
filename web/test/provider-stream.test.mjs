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

test('normalized events use one data record and never preserve provider fields', () => {
  const encoded = encodeNormalizedEvent({
    type: 'content_block_delta',
    delta: { type: 'text_delta', text: 'safe text' },
  });
  assert.equal(encoded, 'data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"safe text"}}\n\n');
  assert.doesNotMatch(encoded, /request_id|usage|account/);
});

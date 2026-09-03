import assert from 'node:assert/strict';
import test from 'node:test';

import { SSEDecoder } from '../lib/sse-decoder.mjs';

test('SSE decoder survives arbitrary chunk boundaries', () => {
  const decoder = new SSEDecoder();
  const events = [];
  events.push(...decoder.push('event: response.output_text.delta\r'));
  events.push(...decoder.push('\ndata: {"delta":"hel'));
  events.push(...decoder.push('lo"}\r\n\r\n'));
  events.push(...decoder.finish());
  assert.deepEqual(events, [{
    event: 'response.output_text.delta',
    data: '{"delta":"hello"}',
  }]);
});

test('SSE decoder joins data lines and ignores comments', () => {
  const decoder = new SSEDecoder();
  const events = decoder.push(': keepalive\n\ndata: first\ndata: second\n\n');
  assert.deepEqual(events, [{ event: 'message', data: 'first\nsecond' }]);
});

test('SSE decoder accepts a final unterminated event', () => {
  const decoder = new SSEDecoder();
  assert.deepEqual(decoder.push('data: [DONE]'), []);
  assert.deepEqual(decoder.finish(), [{ event: 'message', data: '[DONE]' }]);
});

test('SSE decoder enforces its byte boundary', () => {
  const decoder = new SSEDecoder({ maxBytes: 8 });
  assert.throws(() => decoder.push('data: too much'), /too large/);
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { readProviderTextStream } from '../public/client/assets/sse.js';

test('browser streams cancel when the byte limit is exceeded', async () => {
  let cancelled = false;
  const body = new ReadableStream({ start(c) { c.enqueue(new Uint8Array(11)); }, cancel() { cancelled = true; } });
  await assert.rejects(readProviderTextStream(body, () => {}, { maxBytes: 10 }), /safety limit/);
  assert.equal(cancelled, true);
  assert.equal(body.locked, false);
});

test('terminal-only and whitespace-only answers are failures, not blank success', async () => {
  for (const wire of ['data: null\n\ndata: [DONE]\n\n', 'data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"   "}}\n\ndata: {"type":"message_stop"}\n\n']) {
    await assert.rejects(readProviderTextStream(new Response(wire).body), /without readable text/);
  }
});

test('complete visible text succeeds and truncated text is not complete', async () => {
  const wire = 'data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}\n\n';
  let text = '';
  await readProviderTextStream(new Response(wire + 'data: [DONE]\n\n').body, value => { text += value; });
  assert.equal(text, 'hello');
  await assert.rejects(readProviderTextStream(new Response(wire).body), /before the provider confirmed/);
});

test('browser streams do not wait forever for a stalled answer', async () => {
  let cancelled = false;
  const body = new ReadableStream({ cancel() { cancelled = true; } });
  await assert.rejects(readProviderTextStream(body, () => {}, { timeoutMs: 10 }), /timed out/);
  assert.equal(cancelled, true);
});

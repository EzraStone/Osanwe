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

test('browser streams do not wait forever for a stalled answer', async () => {
  let cancelled = false;
  const body = new ReadableStream({ cancel() { cancelled = true; } });
  await assert.rejects(readProviderTextStream(body, () => {}, { timeoutMs: 10 }), /timed out/);
  assert.equal(cancelled, true);
});

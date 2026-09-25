import test from 'node:test';
import assert from 'node:assert/strict';
import { readResponseText } from '../public/client/assets/response-body.js';

test('browser JSON reader counts bytes and cancels oversized responses', async () => {
  let cancelled = false;
  const body = new ReadableStream({
    start(c) { c.enqueue(new TextEncoder().encode('éé')); },
    cancel() { cancelled = true; },
  });
  await assert.rejects(readResponseText(new Response(body), { maxBytes: 3 }), RangeError);
  assert.equal(cancelled, true);
  assert.equal(await readResponseText(new Response('é')), 'é');
});

test('browser JSON reader times out a stalled body and releases it', async () => {
  let cancelled = false;
  const body = new ReadableStream({ cancel() { cancelled = true; } });
  await assert.rejects(readResponseText(new Response(body), { timeoutMs: 10 }), /too long/);
  assert.equal(cancelled, true);
  assert.equal(body.locked, false);
});

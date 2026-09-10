import assert from 'node:assert/strict';
import test from 'node:test';
import { readBoundedText } from '../lib/bounded-body.mjs';

test('body reader counts UTF-8 bytes and preserves split characters', async () => {
  const bytes = new TextEncoder().encode('é');
  const body = new ReadableStream({ start(c) { c.enqueue(bytes.slice(0, 1)); c.enqueue(bytes.slice(1)); c.close(); } });
  assert.equal(await readBoundedText(body, 2), 'é');
  await assert.rejects(readBoundedText(new Response('é').body, 1), RangeError);
});

test('body reader cancels an oversized source without draining it', async () => {
  let cancelled = false;
  const body = new ReadableStream({ start(c) { c.enqueue(new Uint8Array(9)); }, cancel() { cancelled = true; } });
  await assert.rejects(readBoundedText(body, 8), RangeError);
  assert.equal(cancelled, true);
  assert.equal(body.locked, false);
});

test('body reader bounds a source that never sends or finishes', async () => {
  let cancelled = false;
  const body = new ReadableStream({ cancel() { cancelled = true; } });
  await assert.rejects(readBoundedText(body, 8, { timeoutMs: 10 }), { name: 'AbortError' });
  assert.equal(cancelled, true);
  assert.equal(body.locked, false);
});

test('body reader honors an already aborted request', async () => {
  await assert.rejects(readBoundedText(new Response('small').body, 8, { signal: AbortSignal.abort() }), { name: 'AbortError' });
});

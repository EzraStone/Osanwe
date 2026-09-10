import assert from 'node:assert/strict';
import test from 'node:test';
import { handleChatRequest } from '../app/api/chat/route.js';
import { handleProviderCheck } from '../app/api/providers/check/route.js';
const secret = 'synthetic-private-detail';
const probe = { provider: 'tokenrouter', model: 'z-ai/glm-5.3-free' };
const chat = { ...probe, mode: 'chat', messages: [{ role: 'user', content: 'Synthetic prompt.' }] };
function request(body, extra = {}) {
  return new Request('https://chat.osanwe.test/api/chat', {
    method: 'POST', headers: { authorization: 'Bearer synthetic-test-key', 'content-type': 'application/json', 'x-forwarded-for': String(Math.random()) },
    body, duplex: 'half', ...extra,
  });
}

for (const [name, handler, payload] of [['chat', handleChatRequest, chat], ['check', handleProviderCheck, probe]]) {
  test(`${name} does not reflect JSON fragments or upstream exception details`, async () => {
    for (const [body, fetcher] of [
      [`{"${secret}`, () => { throw new Error('must not call'); }],
      [JSON.stringify(payload), () => { throw new Error(secret); }],
      [JSON.stringify(payload), () => new Response(secret)],
    ]) {
      const response = await handler(request(body), fetcher);
      assert.ok(response.status >= 400);
      assert.doesNotMatch(await response.text(), new RegExp(secret));
    }
  });
  test(`${name} refuses an aborted request without using a key upstream`, async () => {
    let called = false;
    const response = await handler(request(JSON.stringify(payload), { signal: AbortSignal.abort() }), () => { called = true; });
    assert.ok(response.status >= 400);
    assert.equal(called, false);
  });
  test(`${name} cancels an oversized chunked request before contacting a provider`, async () => {
    let cancelled = false;
    let called = false;
    const body = new ReadableStream({ start(c) { c.enqueue(new Uint8Array(65537)); }, cancel() { cancelled = true; } });
    const response = await handler(request(body), () => { called = true; });
    assert.equal(response.status, 413);
    assert.equal(cancelled, true);
    assert.equal(called, false);
  });
}

test('cancelling a hosted answer aborts the provider request', async () => {
  let upstreamSignal;
  const response = await handleChatRequest(request(JSON.stringify(chat)), async (_, init) => {
    upstreamSignal = init.signal;
    return new Response(new ReadableStream(), { headers: { 'content-type': 'text/event-stream' } });
  });
  await response.body.cancel();
  assert.equal(upstreamSignal.aborted, true);
});

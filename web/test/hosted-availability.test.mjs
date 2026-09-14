import assert from 'node:assert/strict';
import test from 'node:test';
import { hostedPauseResponse } from '../lib/hosted-availability.mjs';
import { handleChatRequest } from '../app/api/chat/route.js';
import { handleProviderCheck } from '../app/api/providers/check/route.js';

test('the hosted pause defaults to current behavior and permits explicit reopening', () => {
  for (const setting of [undefined, '0', 'false']) {
    assert.equal(hostedPauseResponse({ OSANWE_HOSTED_API_PAUSED: setting }), null);
  }
});

test('a configured pause or unrecognized setting fails closed without reflecting configuration', async () => {
  for (const setting of ['1', 'true', '', 'False', 'unexpected-private-value']) {
    const response = hostedPauseResponse({ OSANWE_HOSTED_API_PAUSED: setting });
    assert.equal(response.status, 503);
    assert.match(response.headers.get('cache-control'), /no-store/);
    assert.equal(response.headers.get('retry-after'), '300');
    const value = await response.json();
    assert.equal(value.error.code, 'hosted_api_paused');
    assert.equal(value.error.retryable, false);
    assert.doesNotMatch(JSON.stringify(value), /unexpected-private-value/);
  }
});

test('both handlers stop before reading credentials, parsing bodies, or making provider calls', async () => {
  const previous = process.env.OSANWE_HOSTED_API_PAUSED;
  process.env.OSANWE_HOSTED_API_PAUSED = '1';
  try {
    const unreadableRequest = new Proxy({}, { get() { throw new Error('Request must not be inspected'); } });
    for (const handler of [handleChatRequest, handleProviderCheck]) {
      const response = await handler(unreadableRequest, () => { throw new Error('Provider must not be called'); });
      assert.equal(response.status, 503);
      assert.equal((await response.json()).error.code, 'hosted_api_paused');
    }
  } finally {
    if (previous === undefined) delete process.env.OSANWE_HOSTED_API_PAUSED;
    else process.env.OSANWE_HOSTED_API_PAUSED = previous;
  }
});

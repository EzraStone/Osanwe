import test from 'node:test';
import assert from 'node:assert/strict';
import { responseError } from '../public/client/assets/api.js';

test('edge HTML and raw error identifiers stay out of public messages', async () => {
  for (const status of [401, 403, 429, 500, 503, 504]) {
    const error = await responseError(new Response('<html>private-edge-id</html>', { status }), 'Request failed.');
    assert.doesNotMatch(error.message, /private-edge-id|html/);
    assert.equal(error.status, status);
    if (status === 429) { assert.match(error.message, /Wait a minute/); assert.equal(error.retryable, true); }
  }
});

test('oversized error messages are replaced instead of filling the chat', async () => {
  const error = await responseError(Response.json({ error: { message: 'x'.repeat(900) } }, { status: 500 }), 'Request failed.');
  assert.equal(error.message, 'Request failed.');
});

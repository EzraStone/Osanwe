import assert from 'node:assert/strict';
import test from 'node:test';

import { handleProviderCheck } from '../app/api/providers/check/route.js';

function request(body, headers = {}) {
  return new Request('https://chat.osanwe.test/api/providers/check', {
    method: 'POST',
    headers: {
      authorization: 'Bearer test-provider-key',
      'content-type': 'application/json',
      origin: 'https://chat.osanwe.test',
      'sec-fetch-site': 'same-origin',
      ...headers,
    },
    body: JSON.stringify(body),
  });
}

const payload = { provider: 'tokenrouter', model: 'z-ai/glm-5.3-free' };

test('provider check verifies the selected fixed endpoint without returning content', async () => {
  let seen;
  const response = await handleProviderCheck(request(payload), async (url, init) => {
    seen = { url, init };
    return Response.json({ choices: [{ message: { content: 'OK' } }] });
  });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { ok: true, ...payload });
  assert.equal(seen.url, 'https://api.tokenrouter.com/v1/chat/completions');
  assert.equal(seen.init.headers.authorization, 'Bearer test-provider-key');
  assert.equal(JSON.parse(seen.init.body).max_tokens, 32);
});

test('provider check reports invalid credentials without reflecting provider details', async () => {
  const response = await handleProviderCheck(request(payload), async () =>
    Response.json({ error: { message: 'private account detail' } }, { status: 401 }),
  );
  assert.equal(response.status, 502);
  assert.deepEqual(await response.json(), {
    error: {
      message: 'The provider rejected that API key.',
      code: 'invalid_key',
      retryable: false,
    },
  });
});

test('provider check refuses cross-site requests before forwarding credentials', async () => {
  let called = false;
  const response = await handleProviderCheck(
    request(payload, { origin: 'https://attacker.test', 'sec-fetch-site': 'cross-site' }),
    async () => { called = true; return Response.json({}); },
  );
  assert.equal(response.status, 403);
  assert.equal(called, false);
});

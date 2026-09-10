import assert from 'node:assert/strict';
import test from 'node:test';

import { handleChatRequest } from '../app/api/chat/route.js';
import { handleProviderCheck } from '../app/api/providers/check/route.js';
import { readProviderTextStream } from '../public/client/assets/sse.js';

const provider = process.env.OSANWE_LIVE_PROVIDER;
const model = process.env.OSANWE_LIVE_MODEL;
const key = process.env.OSANWE_LIVE_API_KEY;
const confirmed = process.env.OSANWE_LIVE_CONFIRM === 'YES';
const enabled = Boolean(provider && model && key && confirmed);
const checkEnabled = enabled && process.env.OSANWE_LIVE_CHECK_CONFIRM === 'YES';

test('opt-in live provider smoke test returns visible text', { skip: !enabled }, async () => {
  const request = new Request('https://chat.osanwe.test/api/chat', {
    method: 'POST',
    headers: {
      authorization: `Bearer ${key}`,
      'content-type': 'application/json',
      origin: 'https://chat.osanwe.test',
      'sec-fetch-site': 'same-origin',
      'x-forwarded-for': 'live-smoke-test',
    },
    body: JSON.stringify({
      provider,
      model,
      mode: 'chat',
      messages: [{ role: 'user', content: 'Reply with exactly: osanwe live test' }],
    }),
  });
  const response = await handleChatRequest(request);
  assert.equal(response.status, 200, 'Provider request did not succeed.');
  let answer = '';
  await readProviderTextStream(response.body, (text) => { answer += text; });
  assert.ok(answer.trim().length > 0, 'No readable answer was returned.');
});

test('opt-in live provider connection check accepts the selected key and model', { skip: !checkEnabled }, async () => {
  const request = new Request('https://chat.osanwe.test/api/providers/check', {
    method: 'POST',
    headers: {
      authorization: `Bearer ${key}`,
      'content-type': 'application/json',
      origin: 'https://chat.osanwe.test',
      'sec-fetch-site': 'same-origin',
    },
    body: JSON.stringify({ provider, model }),
  });
  const response = await handleProviderCheck(request);
  const body = await response.text();
  assert.equal(response.status, 200, 'Provider check did not succeed.');
  assert.deepEqual(JSON.parse(body), { ok: true, provider, model });
});

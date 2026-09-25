import test from 'node:test';
import assert from 'node:assert/strict';
import { testProviderConnection } from '../public/client/assets/api.js';
const input = { provider: 'test', model: 'model', apiKey: 'synthetic' };

test('a successful HTTP status alone cannot mark a provider connection verified', async () => {
  for (const body of [{}, { ok: false }, { ok: true, provider: 'other', model: 'model' }, { ok: true, provider: 'test', model: 'other' }]) {
    await assert.rejects(testProviderConnection(input, async () => Response.json(body)), /did not verify/);
  }
  await assert.rejects(testProviderConnection(input, async () => new Response('<html>error</html>')), /unreadable/);
});

test('connection verification returns only its public outcome', async () => {
  const result = await testProviderConnection(input, async () => Response.json({ ok: true, provider: 'test', model: 'model', extra: 'not-for-ui' }));
  assert.deepEqual(result, { ok: true, provider: 'test', model: 'model' });
});

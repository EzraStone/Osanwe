import test from 'node:test';
import assert from 'node:assert/strict';
import { validateProviderKey } from '../public/client/assets/credentials.js';
import { sendMessages, testProviderConnection } from '../public/client/assets/api.js';

test('invalid credentials never reach a browser request', async () => {
  for (const key of ['', 'has space', 'a\nb', 'a\tb', 'é', 'x'.repeat(4097), null]) {
    assert.throws(() => validateProviderKey(key), /API key/);
    const fetchImpl = () => assert.fail('invalid key was sent');
    await assert.rejects(sendMessages({ model: 'test', messages: [] }, { apiKey: key, fetchImpl }));
    await assert.rejects(testProviderConnection({ provider: 'test', model: 'test', apiKey: key }, fetchImpl));
  }
  assert.equal(validateProviderKey('synthetic-key_123'), 'synthetic-key_123');
});

test('credentialed requests refuse redirects, cookies, and browser caching', async () => {
  const fetchImpl = async (_url, options) => {
    assert.equal(options.redirect, 'error');
    assert.equal(options.cache, 'no-store');
    assert.equal(options.credentials, 'omit');
    return Response.json({ ok: true, provider: 'test', model: 'test' });
  };
  await sendMessages({ model: 'test', messages: [{ role: 'user', content: 'synthetic' }] }, { apiKey: 'test-key', fetchImpl });
  await testProviderConnection({ provider: 'test', model: 'test', apiKey: 'test-key' }, fetchImpl);
});

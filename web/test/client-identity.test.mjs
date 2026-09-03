import assert from 'node:assert/strict';
import test from 'node:test';

import { ephemeralClientIdentity } from '../lib/client-identity.mjs';

function request(headers = {}) {
  return new Request('https://chat.osanwe.test/api/chat', { headers });
}

test('ephemeral client identity is stable only within the current server process', () => {
  const first = ephemeralClientIdentity(request({ 'x-forwarded-for': '192.0.2.4, 198.51.100.1' }));
  const second = ephemeralClientIdentity(request({ 'x-forwarded-for': '192.0.2.4' }));
  assert.equal(first, second);
  assert.equal(first.length, 24);
  assert.doesNotMatch(first, /192\.0\.2\.4/);
});

test('ephemeral client identity prefers the platform-authenticated edge address', () => {
  const cloudflare = ephemeralClientIdentity(request({
    'cf-connecting-ip': '203.0.113.8',
    'x-forwarded-for': '192.0.2.4',
  }));
  const expected = ephemeralClientIdentity(request({ 'cf-connecting-ip': '203.0.113.8' }));
  assert.equal(cloudflare, expected);
});

test('ephemeral client identity has a bounded local fallback', () => {
  assert.equal(ephemeralClientIdentity(request()), ephemeralClientIdentity(request()));
});

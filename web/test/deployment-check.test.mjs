import test from 'node:test';
import assert from 'node:assert/strict';
import { checkHostedDeployment } from '../scripts/check-hosted-deployment.mjs';

test('deployment checks use only bounded credential-free GETs', async () => {
  let calls = 0;
  const report = await checkHostedDeployment('http://127.0.0.1:3100', async (url, options) => {
    calls++;
    assert.equal(options.method, 'GET');
    assert.equal(options.redirect, 'error');
    assert.equal(options.credentials, 'omit');
    assert.equal(options.headers, undefined);
    const headers = {
      'x-content-type-options': 'nosniff',
      'content-security-policy': "frame-ancestors 'none'; frame-src 'none'; connect-src 'self'; connect-src 'none'",
      'connection-allowlist': '(response-origin);webrtc=block',
    };
    const body = url.endsWith('/api/providers') ? JSON.stringify({ providers: [{ id: 'test', label: 'Test', models: [] }] }) : 'id="cancelProviderCheck" source.textContent = code';
    return new Response(body, { headers });
  });
  assert.equal(calls, 3);
  assert.equal(report.passed, true);
  assert.equal(report.providerRequests, 0);
});

test('deployment checks reject arbitrary targets and report missing isolation', async () => {
  for (const base of ['https://example.com', 'http://user:pass@localhost', 'https://osanwe.vercel.app/client', 'https://osanwe.vercel.app/?key=no']) {
    await assert.rejects(checkHostedDeployment(base, () => assert.fail('unsafe destination')));
  }
  const report = await checkHostedDeployment('http://localhost:3100', async () => new Response('not a valid deployment'));
  assert.equal(report.passed, false);
});

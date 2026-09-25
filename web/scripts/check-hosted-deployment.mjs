import { fileURLToPath } from 'node:url';
import { readResponseText } from '../public/client/assets/response-body.js';
import { validateCatalog } from '../public/client/assets/provider-catalog.js';

export async function checkHostedDeployment(base, fetchImpl = fetch) {
  const url = new URL(base);
  const local = url.protocol === 'http:' && ['127.0.0.1', 'localhost'].includes(url.hostname);
  if ((!local && url.origin !== 'https://osanwe.vercel.app') || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error('Use the Osanwe production origin or a loopback origin, without credentials or a path.');
  }
  const results = [];
  for (const path of ['/client', '/client/assets/runner.html', '/api/providers']) {
    const response = await fetchImpl(url.origin + path, {
      method: 'GET', redirect: 'error', credentials: 'omit', cache: 'no-store', signal: AbortSignal.timeout(15000),
    });
    const text = await readResponseText(response, { maxBytes: 256 * 1024 });
    const csp = response.headers.get('content-security-policy') || '';
    let passed = response.ok && response.headers.get('x-content-type-options') === 'nosniff';
    if (path === '/client') {
      passed &&= /frame-ancestors 'none'/.test(csp) && /connect-src 'self'/.test(csp) && text.includes('id="cancelProviderCheck"');
    } else if (path.endsWith('runner.html')) {
      passed &&= /frame-src 'none'/.test(csp) && /connect-src 'none'/.test(csp) && text.includes('source.textContent = code') &&
        (response.headers.get('connection-allowlist') || '').includes('webrtc=block');
    } else {
      try { validateCatalog(JSON.parse(text)); } catch { passed = false; }
    }
    results.push({ path, status: response.status, passed: Boolean(passed) });
  }
  return { checkedAt: new Date().toISOString(), origin: url.origin, providerRequests: 0, passed: results.every(item => item.passed), results };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const report = await checkHostedDeployment(process.argv[2] || 'http://127.0.0.1:3100');
    console.log(JSON.stringify(report, null, 2));
    if (!report.passed) process.exitCode = 1;
  } catch {
    console.error('Deployment verification could not finish. Check the allowed origin, reachability, and response limits. No provider request was made.');
    process.exitCode = 1;
  }
}

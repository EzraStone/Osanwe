import { readBoundedText } from '../lib/bounded-body.mjs';
import { buildProviderProbe, extractProviderOutput, normalizeProviderKey } from '../lib/provider-proxy.mjs';
import { readProviderTextStream } from '../public/client/assets/sse.js';

const provider = 'tokenrouter';
const model = 'z-ai/glm-5.3-free';
const pricingUrl = 'https://www.tokenrouter.com/models/z-ai/glm-5.3-free/';
const base = new URL(process.argv[2] || 'https://osanwe.vercel.app');
if (base.origin !== 'https://osanwe.vercel.app' && !(['localhost', '127.0.0.1'].includes(base.hostname) && base.protocol === 'http:')) {
  throw new Error('Only the production Osanwe host or a loopback development server is allowed.');
}
// No keys, prompts, replies, account identifiers, or raw exception messages in reports.
const report = { date: new Date().toISOString(), provider, model, host: base.origin, synthetic: true, requestLimit: 6, results: [] };
let key = '';
let calls = 0;
async function post(url, init) {
  if (++calls > 6) throw new Error('request_limit');
  return fetch(url, { ...init, redirect: 'manual' });
}
async function hosted(path, payload, signal, credential = key) {
  return post(new URL(path, base), {
    method: 'POST', signal,
    headers: { authorization: `Bearer ${credential}`, 'content-type': 'application/json', origin: base.origin },
    body: JSON.stringify(payload),
  });
}
async function run(name, operation) {
  const start = performance.now();
  try {
    const details = await operation();
    report.results.push({ name, passed: true, elapsedMs: Math.round(performance.now() - start), ...details });
    return true;
  } catch {
    report.results.push({ name, passed: false, elapsedMs: Math.round(performance.now() - start) });
    return false;
  }
}
async function chat(messages, cancel = false) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 55_000);
  let answer = '';
  let firstTextMs = null;
  const start = performance.now();
  try {
    const response = await hosted('/api/chat', { provider, model, mode: 'chat', messages, max_output_tokens: 512 }, controller.signal);
    if (!response.ok) { await response.body?.cancel(); throw new Error('http_failure'); }
    let bytes = 0;
    const bounded = response.body.pipeThrough(new TransformStream({ transform(chunk, target) {
      bytes += chunk.byteLength;
      if (bytes > 256 * 1024) throw new Error('response_limit');
      target.enqueue(chunk);
    } }));
    try {
      await readProviderTextStream(bounded, (text) => {
        answer += text;
        if (text.trim() && firstTextMs === null) firstTextMs = Math.round(performance.now() - start);
        if (cancel && firstTextMs !== null) controller.abort();
      });
    } catch {
      if (!(cancel && firstTextMs !== null && controller.signal.aborted)) throw new Error('stream_failure');
    }
    if (!answer.trim()) throw new Error('empty_answer');
    return { firstTextMs, visibleCharacters: answer.trim().length, ...(cancel ? { clientCancelled: controller.signal.aborted } : {}) };
  } finally { clearTimeout(timer); controller.abort(); }
}

try {
  // Never infer zero price from a model suffix. Fail closed if published pricing changes.
  const priceResponse = await fetch(pricingUrl, { signal: AbortSignal.timeout(10_000), redirect: 'error' });
  if (!priceResponse.ok) throw new Error('pricing_unavailable');
  const pricing = (await readBoundedText(priceResponse.body, 256 * 1024)).replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');
  if (!/Input price:\s*\$0\.0+ per 1M tokens/.test(pricing) || !/Output price:\s*\$0\.0+ per 1M tokens/.test(pricing)) {
    throw new Error('zero_price_not_verified');
  }
  report.zeroPublishedPriceVerified = true;
  for await (const chunk of process.stdin) {
    key += chunk.toString();
    if (key.length > 1024) throw new Error('key_too_large');
  }
  key = normalizeProviderKey(`Bearer ${key.trim()}`);
  const probe = buildProviderProbe({ provider, model }, key);
  const directPassed = await run('direct_readable_probe', async () => {
    const response = await post(probe.url, { ...probe.init, signal: AbortSignal.timeout(25_000) });
    if (!response.ok) { await response.body?.cancel(); throw new Error('probe_failed'); }
    extractProviderOutput(provider, JSON.parse(await readBoundedText(response.body, 65536)));
    return {};
  });
  // Stop on the first failed prerequisite. No retries and no substitution of a paid model.
  if (!directPassed) throw new Error('direct_prerequisite_failed');
  const hostedPassed = await run('hosted_readable_probe', async () => {
    const response = await hosted('/api/providers/check', { provider, model }, AbortSignal.timeout(30_000));
    const value = JSON.parse(await readBoundedText(response.body, 4096));
    if (!response.ok || value.ok !== true) throw new Error('check_failed');
    return {};
  });
  if (!hostedPassed) throw new Error('hosted_prerequisite_failed');
  await run('normal_answer', () => chat([{ role: 'user', content: 'In one sentence, describe a blue triangle.' }]));
  await run('follow_up_context', () => chat([
    { role: 'user', content: 'Remember this synthetic label: blue triangle.' },
    { role: 'assistant', content: 'The synthetic label is blue triangle.' },
    { role: 'user', content: 'What label did I give you? Reply in one sentence.' },
  ]));
  await run('long_answer_cancel', () => chat([{ role: 'user', content: 'List 100 numbered imaginary garden names, one per line.' }], true));
  await run('invalid_key_is_rejected', async () => {
    const response = await hosted('/api/chat', { provider, model, mode: 'chat', messages: [{ role: 'user', content: 'Hello.' }] }, AbortSignal.timeout(25_000), 'intentionally-invalid-synthetic-key');
    const value = JSON.parse(await readBoundedText(response.body, 4096));
    if (response.ok || value.error?.code !== 'invalid_key') throw new Error('invalid_key_not_rejected');
    return {};
  });
} catch {
  report.stopped = true;
} finally {
  key = '';
  report.requestsSent = calls;
  report.passed = !report.stopped && report.results.length === 6 && report.results.every((result) => result.passed);
  console.log(JSON.stringify(report, null, 2));
  process.exitCode = report.passed ? 0 : 1;
}

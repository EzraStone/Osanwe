import test from 'node:test';
import assert from 'node:assert/strict';
import { getEventListeners } from 'node:events';
import { sendMessages } from '../public/client/assets/api.js';

const input = { model: 'test-model', messages: [{ role: 'user', content: 'A synthetic prompt.' }] };
const apiKey = 'synthetic-not-a-real-key';
let moduleSequence = 0;

function freshCatalogAPI() {
  return import(`../public/client/assets/api.js?catalog-lifecycle=${++moduleSequence}`);
}

function catalog(version) {
  return { providers: [
    { id: 'groq', label: `Groq ${version}`, models: [`groq-${version}`] },
    { id: 'tokenrouter', label: 'TokenRouter', models: [`tokenrouter-${version}`] },
  ] };
}

function pendingFetch() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { fetch: () => promise, resolve: value => resolve(Response.json(value)) };
}

test('model refresh returns the same provider definitions used for its model choices', async () => {
  const api = await freshCatalogAPI();
  await api.loadStatus(async () => Response.json(catalog('old')));
  const updated = catalog('new');
  updated.providers.push({ id: 'new-provider', label: 'New provider', models: ['new-model'] });
  const result = await api.loadModels('groq', async () => Response.json(updated), true);
  assert.deepEqual(result.providers, updated.providers);
  assert.equal(result.data[0].id, result.providers[0].models[0]);
  assert.equal(result.data[0].osanwe.provider_identity, result.providers[0].label);
  assert.deepEqual(await api.loadProviderCatalog(() => { throw new Error('Unexpected network request'); }), updated.providers);
});

test('an older catalog response cannot roll the shared provider cache backward', async () => {
  const api = await freshCatalogAPI();
  await api.loadStatus(async () => Response.json(catalog('initial')));
  const older = pendingFetch(), newer = pendingFetch();
  const oldRequest = api.loadModels('groq', older.fetch, true);
  const newRequest = api.loadModels('groq', newer.fetch, true);
  newer.resolve(catalog('new'));
  assert.equal((await newRequest).data[0].id, 'groq-new');
  older.resolve(catalog('old'));
  assert.equal((await oldRequest).data[0].id, 'groq-old');
  const nextProvider = await api.loadModels('tokenrouter', () => { throw new Error('Unexpected network request'); });
  assert.equal(nextProvider.data[0].id, 'tokenrouter-new');
  assert.equal((await api.loadStatus()).providers[0].label, 'Groq new');
});

test('a stale catalog cannot become authoritative after the newest refresh fails validation', async () => {
  const api = await freshCatalogAPI();
  await api.loadStatus(async () => Response.json(catalog('initial')));
  const older = pendingFetch();
  const oldRequest = api.loadModels('groq', older.fetch, true);
  await assert.rejects(api.loadModels('groq', async () => Response.json({ providers: [] }), true), /unavailable/);
  older.resolve(catalog('stale'));
  await oldRequest;
  assert.equal((await api.loadModels('groq')).data[0].id, 'groq-initial');
});

test('chat has a header-arrival deadline and reports a safe retryable timeout', async () => {
  let requestSignal, calls = 0;
  await assert.rejects(sendMessages(input, {
    apiKey,
    headerTimeoutMs: 5,
    fetchImpl: (_url, { signal }) => {
      calls++;
      requestSignal = signal;
      return new Promise((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true });
      });
    },
  }), error => {
    assert.equal(error.name, 'TimeoutError');
    assert.equal(error.retryable, true);
    assert.match(error.message, /timed out before the server answered/);
    assert.doesNotMatch(error.message, /synthetic-not-a-real-key/);
    return true;
  });
  assert.equal(requestSignal.aborted, true);
  assert.equal(calls, 1);
});

test('user cancellation while waiting for headers stays an abort and releases its listener', async () => {
  const user = new AbortController();
  const pending = sendMessages(input, {
    apiKey,
    signal: user.signal,
    fetchImpl: (_url, { signal }) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(signal.reason), { once: true });
    }),
  });
  user.abort();
  await assert.rejects(pending, { name: 'AbortError' });
  assert.equal(getEventListeners(user.signal, 'abort').length, 0);
});

test('an already-aborted caller reaches fetch with an aborted request signal', async () => {
  const user = new AbortController();
  user.abort();
  await assert.rejects(sendMessages(input, {
    apiKey,
    signal: user.signal,
    fetchImpl: async (_url, { signal }) => {
      assert.equal(signal.aborted, true);
      throw signal.reason;
    },
  }), { name: 'AbortError' });
  assert.equal(getEventListeners(user.signal, 'abort').length, 0);
});

test('receiving headers clears only the deadline, preserving Stop for the response stream', async () => {
  const user = new AbortController();
  let requestSignal;
  const response = await sendMessages(input, {
    apiKey,
    signal: user.signal,
    headerTimeoutMs: 5,
    fetchImpl: async (_url, { signal }) => {
      requestSignal = signal;
      return new Response(new ReadableStream({
        start(controller) {
          signal.addEventListener('abort', () => controller.error(signal.reason), { once: true });
        },
      }));
    },
  });
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(requestSignal.aborted, false);
  const reader = response.body.getReader();
  const reading = reader.read();
  user.abort();
  await assert.rejects(reading, { name: 'AbortError' });
  assert.equal(requestSignal.aborted, true);
  reader.releaseLock();
});

test('chat keeps structured safe server errors and does not retry them', async () => {
  const user = new AbortController();
  let calls = 0;
  await assert.rejects(sendMessages(input, {
    apiKey,
    signal: user.signal,
    fetchImpl: async () => {
      calls++;
      return Response.json({ error: { message: 'Provider temporarily busy.', code: 'provider_busy', retryable: true } }, { status: 503 });
    },
  }), error => {
    assert.equal(error.message, 'Provider temporarily busy.');
    assert.equal(error.code, 'provider_busy');
    assert.equal(error.retryable, true);
    assert.equal(error.status, 503);
    return true;
  });
  assert.equal(calls, 1);
  assert.equal(getEventListeners(user.signal, 'abort').length, 0);
});

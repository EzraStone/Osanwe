import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { loadModels, loadStatus, sendMessages } from '../public/client/assets/api.js';

const catalog = {
  providers: [
    { id: 'groq', label: 'Groq', models: ['openai/gpt-oss-20b'] },
    { id: 'venice', label: 'Venice AI', models: ['venice-uncensored'] },
  ],
};

function catalogFetch() {
  return Promise.resolve(Response.json(catalog));
}

test('hosted status and model suggestions come only from the same-origin registry', async () => {
  const status = await loadStatus(catalogFetch);
  assert.equal(status.paying, 'byok');
  assert.equal(status.providers[1].label, 'Venice AI');
  assert.deepEqual((await loadModels('groq', catalogFetch)).data.map((item) => item.id), ['openai/gpt-oss-20b']);
});

test('hosted client sends provider, model, mode, and messages to one fixed endpoint', async () => {
  let seen;
  const response = new Response('data: {"type":"message_stop"}\n\n', {
    status: 200,
    headers: { 'content-type': 'text/event-stream' },
  });
  await sendMessages({ model: 'venice-uncensored', messages: [{ role: 'user', content: 'hello' }] }, {
    provider: 'venice',
    mode: 'code',
    apiKey: 'venice-test-key',
    fetchImpl: async (url, options) => {
      seen = { url, options };
      return response;
    },
  });
  assert.equal(seen.url, '/api/chat');
  assert.equal(seen.options.headers.authorization, 'Bearer venice-test-key');
  assert.deepEqual(JSON.parse(seen.options.body), {
    provider: 'venice',
    model: 'venice-uncensored',
    mode: 'code',
    messages: [{ role: 'user', content: 'hello' }],
  });
});

test('the hosted shell retains the original navigation and runnable code display', async () => {
  const html = await readFile(new URL('../public/client/index.html', import.meta.url), 'utf8');
  assert.match(html, /id="chatTab"/);
  assert.match(html, /id="codeTab"/);
  assert.match(html, /id="settingsBtn"/);
  assert.match(html, /id="providerSelect"/);
  assert.match(html, /id="runnerPreview"/);
  assert.match(html, /sandbox="allow-scripts"/);
  assert.doesNotMatch(html, /allow-same-origin/);
});

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  loadModels,
  loadStatus,
  responseError,
  sendMessages,
  testProviderConnection,
} from '../public/client/assets/api.js';

const catalog = {
  providers: [
    { id: 'groq', label: 'Groq', models: ['openai/gpt-oss-20b'] },
    { id: 'tokenrouter', label: 'TokenRouter', models: ['z-ai/glm-5.3-free'] },
  ],
};

function catalogFetch() {
  return Promise.resolve(Response.json(catalog));
}

test('hosted status and model suggestions come only from the same-origin registry', async () => {
  const status = await loadStatus(catalogFetch);
  assert.equal(status.paying, 'byok');
  assert.equal(status.providers[1].label, 'TokenRouter');
  assert.deepEqual((await loadModels('groq', catalogFetch)).data.map((item) => item.id), ['openai/gpt-oss-20b']);
});

test('hosted client sends provider, model, mode, and messages to one fixed endpoint', async () => {
  let seen;
  const response = new Response('data: {"type":"message_stop"}\n\n', {
    status: 200,
    headers: { 'content-type': 'text/event-stream' },
  });
  await sendMessages({ model: 'z-ai/glm-5.3-free', messages: [{ role: 'user', content: 'hello' }] }, {
    provider: 'tokenrouter',
    mode: 'code',
    apiKey: 'tokenrouter-test-key',
    fetchImpl: async (url, options) => {
      seen = { url, options };
      return response;
    },
  });
  assert.equal(seen.url, '/api/chat');
  assert.equal(seen.options.headers.authorization, 'Bearer tokenrouter-test-key');
  assert.deepEqual(JSON.parse(seen.options.body), {
    provider: 'tokenrouter',
    model: 'z-ai/glm-5.3-free',
    mode: 'code',
    messages: [{ role: 'user', content: 'hello' }],
  });
});

test('hosted client retains safe provider diagnostics for the interface', async () => {
  const response = Response.json({
    error: { message: 'The provider rate limit was reached.', code: 'provider_limit_reached', retryable: true },
  }, { status: 429 });
  const error = await responseError(response, 'fallback');
  assert.equal(error.message, 'The provider rate limit was reached.');
  assert.equal(error.code, 'provider_limit_reached');
  assert.equal(error.retryable, true);
  assert.equal(error.status, 429);
});

test('provider connection test sends only the selected provider, model, and transient key', async () => {
  let seen;
  const result = await testProviderConnection({
    provider: 'tokenrouter',
    model: 'z-ai/glm-5.3-free',
    apiKey: 'tokenrouter-test-key',
  }, async (url, options) => {
    seen = { url, options };
    return Response.json({ ok: true, provider: 'tokenrouter', model: 'z-ai/glm-5.3-free' });
  });
  assert.equal(seen.url, '/api/providers/check');
  assert.equal(seen.options.headers.authorization, 'Bearer tokenrouter-test-key');
  assert.deepEqual(JSON.parse(seen.options.body), {
    provider: 'tokenrouter',
    model: 'z-ai/glm-5.3-free',
  });
  assert.equal(result.ok, true);
});

test('the hosted shell retains the original navigation and runnable code display', async () => {
  const html = await readFile(new URL('../public/client/index.html', import.meta.url), 'utf8');
  const css = await readFile(new URL('../public/client/assets/app.css', import.meta.url), 'utf8');
  assert.match(html, /id="chatTab"/);
  assert.match(html, /id="codeTab"/);
  assert.match(html, /id="settingsBtn"/);
  assert.match(html, /id="providerSelect"/);
  assert.match(html, /id="testProviderKey"/);
  assert.match(html, /32-token maximum/);
  assert.match(html, /id="runnerPreview"/);
  assert.match(html, /sandbox="allow-scripts"/);
  assert.doesNotMatch(html, /allow-same-origin/);
  assert.match(html, /href="\/client\/assets\/app\.css"/);
  assert.match(html, /src="\/client\/assets\/app\.js"/);
  assert.match(html, /src="\/client\/assets\/runner\.html"/);
  assert.doesNotMatch(html, /(?:href|src)="assets\//);
  assert.match(css, /\.settings-inner \.provider-config-grid>label\{grid-column:1;/);
  assert.match(css, /\.provider-config-grid select,\.provider-model-control\{grid-column:1; width:100%; min-width:0\}/);
  assert.match(css, /scrollbar-color:var\(--scroll-thumb\) transparent/);
  assert.match(css, /\*::-webkit-scrollbar-thumb/);
  assert.match(css, /#testProviderKey/);
  assert.match(css, /\.provider-test-note/);
  const app = await readFile(new URL('../public/client/assets/app.js', import.meta.url), 'utf8');
  assert.match(app, /thread\.addEventListener\("scroll"/);
  assert.match(app, /scrollConversation\(true\)/);
  assert.match(app, /testProviderConnection/);
  assert.match(app, /Test the connection before closing Settings/);
  assert.match(app, /if\(runnable\)loadRunnerCode\(runnable\.language,runnable\.code,true,null,false\)/);
});

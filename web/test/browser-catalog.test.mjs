import test from 'node:test';
import assert from 'node:assert/strict';
import { validateCatalog } from '../public/client/assets/provider-catalog.js';
import { loadModels } from '../public/client/assets/api.js';
const provider = { id: 'test', label: 'Test', models: ['model'] };

test('browser catalogs reject malformed provider and model lists', () => {
  for (const value of [null, {}, { providers: [] }, { providers: [null] }, { providers: [provider, provider] },
    { providers: [{ ...provider, models: ['<script>'] }] }, { providers: [{ ...provider, label: '' }] }]) {
    assert.throws(() => validateCatalog(value), /unavailable/);
  }
});

test('catalog loading has a deadline and cannot redirect or use cookies', async () => {
  await loadModels('test', async (_url, options) => {
    assert.ok(options.signal instanceof AbortSignal);
    assert.equal(options.redirect, 'error');
    assert.equal(options.credentials, 'omit');
    return Response.json({ providers: [provider] });
  }, true);
});

test('catalogs retain only public fields and deduplicate suggestions', () => {
  assert.deepEqual(validateCatalog({ providers: [{ ...provider, models: ['model', 'model'], secret: 'unused' }] }), [provider]);
});

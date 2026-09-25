import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

test('hidden and back-cache pages release access and stop generated code', async () => {
  const source = await readFile(new URL('../public/client/assets/app.js', import.meta.url), 'utf8');
  assert.match(source, /pagehide[\s\S]*?forgetProviderKey\(\);resetRunnerFrame/);
  assert.match(source, /pageshow[\s\S]*?event.persisted[\s\S]*?forgetProviderKey\(\);render\(\);refresh\(\)/);
  assert.match(source, /function forgetProviderKey\(\)\{\s*cancelConnectionCheck\(\);stopActiveRequest\(\)/);
});

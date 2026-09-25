import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

test('Enter does not submit while an input-method editor is composing', async () => {
  const source = await readFile(new URL('../public/client/assets/app.js', import.meta.url), 'utf8');
  assert.match(source, /e.key==="Enter"&&!e.shiftKey&&!e.isComposing&&e.keyCode!==229/);
});

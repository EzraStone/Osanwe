import assert from 'node:assert/strict';
import test from 'node:test';

import { parseCodeFences, selectRunnableCode } from '../public/client/assets/code.js';

test('a generated web response becomes one automatic preview bundle', () => {
  const parts = parseCodeFences([
    'Here is the interface.',
    '```html',
    '<!doctype html><html><head></head><body><button id="save">Save</button></body></html>',
    '```',
    '```css',
    'button { color: rebeccapurple; }',
    '```',
    '```javascript',
    'document.querySelector("#save").disabled = true;',
    '```',
  ].join('\n'));

  const runnable = selectRunnableCode(parts);
  assert.equal(runnable.language, 'html');
  assert.match(runnable.code, /<style>[\s\S]*rebeccapurple[\s\S]*<\/style>/);
  assert.match(runnable.code, /<script>[\s\S]*querySelector[\s\S]*<\/script>/);
});

test('standalone generated JavaScript automatically targets the console runner', () => {
  const runnable = selectRunnableCode(parseCodeFences('```javascript\nconsole.log("ready");\n```'));
  assert.deepEqual(runnable, { language: 'javascript', code: 'console.log("ready");\n' });
});

test('explanations and unsupported languages never execute automatically', () => {
  assert.equal(selectRunnableCode(parseCodeFences('No runnable code here.')), null);
  assert.equal(selectRunnableCode(parseCodeFences('```python\nprint("hello")\n```')), null);
});

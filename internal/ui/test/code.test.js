import assert from "node:assert/strict";
import test from "node:test";

import { normalizeRunnerLanguage, parseCodeFences } from "../assets/code.js";

test("runner languages are intentionally limited", () => {
  assert.equal(normalizeRunnerLanguage("JavaScript"), "javascript");
  assert.equal(normalizeRunnerLanguage("node"), "javascript");
  assert.equal(normalizeRunnerLanguage("html preview"), "html");
  assert.equal(normalizeRunnerLanguage("python"), "");
  assert.equal(normalizeRunnerLanguage("shell"), "");
});

test("generated fenced code stays separate from surrounding prose", () => {
  assert.deepEqual(
    parseCodeFences("Before\n```js\nconsole.log('ok')\n```\nAfter"),
    [
      { kind: "text", content: "Before\n" },
      { kind: "code", content: "console.log('ok')\n", language: "js", runnerLanguage: "javascript" },
      { kind: "text", content: "\nAfter" },
    ],
  );
});

test("unsupported fences remain displayable but not runnable", () => {
  const parts = parseCodeFences("```python\nprint('no local process')\n```");
  assert.equal(parts[0].kind, "code");
  assert.equal(parts[0].runnerLanguage, "");
});

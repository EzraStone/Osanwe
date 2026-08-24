import assert from "node:assert/strict";
import test from "node:test";

import { buildPreviewBundle, normalizeRunnerLanguage, parseCodeFences } from "../assets/code.js";

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

test("HTML, CSS, and JavaScript fences become one runnable web preview", () => {
  const parts = parseCodeFences([
    "```html",
    "<!doctype html><html><head><title>Demo</title></head><body><button id=go>Go</button></body></html>",
    "```",
    "```css",
    "button { color: rebeccapurple; }",
    "```",
    "```js",
    "document.querySelector('#go').onclick = () => { document.body.dataset.ran = 'yes'; };",
    "```",
  ].join("\n"));
  const bundle = buildPreviewBundle(parts);
  assert.equal(bundle.language, "html");
  assert.match(bundle.code, /<style>[\s\S]*rebeccapurple[\s\S]*<\/style>/);
  assert.match(bundle.code, /<script>[\s\S]*dataset\.ran[\s\S]*<\/script>/);
  assert.ok(bundle.code.indexOf("<style>") < bundle.code.indexOf("</head>"));
  assert.ok(bundle.code.indexOf("<script>") < bundle.code.indexOf("</body>"));
});

test("preview bundling ignores closing-tag text inside generated scripts", () => {
  const parts = parseCodeFences([
    "```html",
    "<!doctype html><html><head><script>const headText = '</head>';</script></head><body><script>const bodyText = '</body>';</script><main>Safe</main></body></html>",
    "```",
    "```css",
    "main { color: green; }",
    "```",
    "```js",
    "document.querySelector('main').dataset.ready = 'yes';",
    "```",
  ].join("\n"));
  const bundle = buildPreviewBundle(parts);
  assert.match(bundle.code, /const headText = '<\/head>';<\/script><style>/);
  assert.match(bundle.code, /const bodyText = '<\/body>';<\/script><main>Safe<\/main><script>/);
  assert.ok(bundle.code.lastIndexOf("<style>") < bundle.code.lastIndexOf("</head>"));
  assert.ok(bundle.code.lastIndexOf("<script>") < bundle.code.lastIndexOf("</body>"));
});

test("a response without HTML does not pretend to be a visual bundle", () => {
  assert.equal(buildPreviewBundle(parseCodeFences("```js\nconsole.log('hello')\n```")), null);
});

import assert from "node:assert/strict";
import test from "node:test";

import { connectionSnippets } from "../assets/snippets.js";

test("token snippets use only a disposable placeholder", () => {
  const snippets = connectionSnippets({ endpoint: "127.0.0.1:8080", paying: "tokens", model: "model-a" });
  assert.match(snippets.shell.code, /ANTHROPIC_API_KEY=osanwe/);
  assert.doesNotMatch(JSON.stringify(snippets), /YOUR_PROVIDER_API_KEY/);
  assert.doesNotMatch(snippets.curl.code, /x-api-key/);
  assert.match(snippets.curl.code, /"model":"model-a"/);
  assert.match(snippets.python.note, /strict text-only Anthropic Messages subset/);
});

test("BYOK snippets require the real key and disclose transient handling", () => {
  const snippets = connectionSnippets({ endpoint: "127.0.0.1:8080", paying: "your own key", model: "model-a" });
  for (const snippet of Object.values(snippets)) {
    assert.match(`${snippet.code} ${snippet.note}`, /YOUR_PROVIDER_API_KEY|provider key/i);
    assert.match(snippet.note, /handles? and forwards|handled transiently/i);
  }
  assert.match(snippets.curl.code, /x-api-key: YOUR_PROVIDER_API_KEY/);
  assert.doesNotMatch(JSON.stringify(snippets), /discarded locally/);
});

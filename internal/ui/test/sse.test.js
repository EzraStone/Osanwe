import assert from "node:assert/strict";
import test from "node:test";

import { anthropicTextDelta, SSEParser } from "../assets/sse.js";

test("SSE parsing survives arbitrary chunk boundaries", () => {
  const parser = new SSEParser();
  assert.deepEqual(parser.push('data: {"type":"content_'), []);
  assert.deepEqual(parser.push('block_delta"}\n\n'), ['{"type":"content_block_delta"}']);
});

test("SSE parsing accepts CRLF and multiple data lines", () => {
  const parser = new SSEParser();
  assert.deepEqual(parser.push("event: note\r\ndata: first\r\ndata: second\r\n\r\n"), ["first\nsecond"]);
});

test("comments and events without data are ignored", () => {
  const parser = new SSEParser();
  assert.deepEqual(parser.push(": heartbeat\n\nevent: ping\n\n"), []);
  assert.deepEqual(parser.finish(), []);
});

test("a final unterminated data event is available at EOF", () => {
  const parser = new SSEParser();
  parser.push("data: final");
  assert.deepEqual(parser.finish(), ["final"]);
});

test("Anthropic text deltas are normalized", () => {
  assert.deepEqual(
    anthropicTextDelta('{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}'),
    { done: false, text: "hello" },
  );
  assert.deepEqual(anthropicTextDelta('{"type":"message_stop"}'), { done: true, text: "" });
  assert.deepEqual(anthropicTextDelta("[DONE]"), { done: true, text: "" });
  assert.deepEqual(anthropicTextDelta("not json"), { done: false, text: "" });
});

test("provider error events become local errors", () => {
  assert.throws(
    () => anthropicTextDelta('{"type":"error","error":{"message":"capacity"}}'),
    /capacity/,
  );
});

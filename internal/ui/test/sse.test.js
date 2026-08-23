import assert from "node:assert/strict";
import test from "node:test";

import { anthropicTextDelta, readAnthropicTextStream, SSEParser } from "../assets/sse.js";

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

function responseBody(parts, { cancelError = null } = {}) {
  const chunks = parts.map((part) => new TextEncoder().encode(part));
  let index = 0;
  return {
	getReader() {
	  return {
		async read() {
		  if (index >= chunks.length) return { done: true, value: undefined };
		  return { done: false, value: chunks[index++] };
		},
		async cancel() {
		  if (cancelError) throw cancelError;
		},
		releaseLock() {},
	  };
	},
  };
}

test("stream reader accepts split text and message_stop", async () => {
  const text = [];
  await readAnthropicTextStream(responseBody([
	'data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hel',
	'lo"}}\n\ndata: {"type":"message_stop"}\n\n',
  ]), (part) => text.push(part));
  assert.equal(text.join(""), "hello");
});

test("stream reader accepts OpenAI DONE terminal marker", async () => {
  await readAnthropicTextStream(responseBody(["data: [DONE]\n\n"]));
});

test("partial or empty EOF is never marked complete", async () => {
  await assert.rejects(
	readAnthropicTextStream(responseBody(['data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}\n\n'])),
	/ended before the provider confirmed/,
  );
  await assert.rejects(readAnthropicTextStream(responseBody([])), /ended before the provider confirmed/);
});

test("unterminated terminal data at EOF is accepted", async () => {
  await readAnthropicTextStream(responseBody(['data: {"type":"message_stop"}']));
});

test("provider errors still reject the stream", async () => {
  await assert.rejects(
	readAnthropicTextStream(responseBody(['data: {"type":"error","error":{"message":"overloaded"}}\n\n'])),
	/overloaded/,
  );
});

test("cleanup failure cannot invalidate a terminal event", async () => {
  await readAnthropicTextStream(responseBody(
	["data: {\"type\":\"message_stop\"}\n\nignored"],
	{ cancelError: new Error("cancel failed") },
  ));
});

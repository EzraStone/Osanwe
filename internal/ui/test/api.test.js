import assert from "node:assert/strict";
import test from "node:test";

import { activateInviteBook, buildMessageBody, buildOpenAIMessageBody, loadModels, loadStatus, responseError, sendMessages } from "../assets/api.js";

test("message bodies preserve complete multi-turn context", () => {
  assert.deepEqual(
    buildMessageBody({
      model: "model-a",
      messages: [
        { role: "user", content: "one" },
        { role: "assistant", content: "two" },
        { role: "user", content: "three" },
      ],
    }),
    {
      model: "model-a",
      max_tokens: 2048,
      stream: true,
      messages: [
        { role: "user", content: "one" },
        { role: "assistant", content: "two" },
        { role: "user", content: "three" },
      ],
    },
  );
});

test("invalid message bodies fail before spending a request", () => {
  assert.throws(() => buildMessageBody({ model: "", messages: [] }), /model/);
  assert.throws(() => buildMessageBody({ model: "m", messages: [] }), /message/);
  assert.throws(
    () => buildMessageBody({ model: "m", messages: [{ role: "system", content: "x" }] }),
    /roles/,
  );
});

test("OpenAI-compatible bodies keep the same bounded chat fields", () => {
  assert.deepEqual(
    buildOpenAIMessageBody({ model: "stealth/ox-alpha", messages: [{ role: "user", content: "hello" }] }),
    {
      model: "stealth/ox-alpha",
      max_tokens: 2048,
      stream: true,
      messages: [{ role: "user", content: "hello" }],
    },
  );
});

test("mode instructions use each provider's supported system field", () => {
  const input = {
    model: "m",
    system: "  Act as a coding assistant.  ",
    messages: [{ role: "user", content: "review this" }],
  };
  assert.equal(buildMessageBody(input).system, "Act as a coding assistant.");
  assert.deepEqual(buildOpenAIMessageBody(input).messages, [
    { role: "system", content: "Act as a coding assistant." },
    { role: "user", content: "review this" },
  ]);
  assert.throws(
    () => buildMessageBody({ model: "m", system: 4, messages: input.messages }),
    /system must be text/,
  );
});

test("status and model reads use only their exact local endpoints", async () => {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push([url, options]);
    return { ok: true, json: async () => (url === "/v1/models" ? { data: [{ id: "m" }] } : { paying: "tokens" }) };
  };
  assert.equal((await loadStatus(fetchImpl)).paying, "tokens");
  assert.equal((await loadModels(fetchImpl)).data[0].id, "m");
  assert.deepEqual(calls.map(([url]) => url), ["/_osanwe/status", "/v1/models"]);
});

test("free test activation posts the selected JSON only to the local activation endpoint", async () => {
  let seen;
  await activateInviteBook('{"schema_version":2}', async (url, options) => {
    seen = { url, options };
    return { ok: true };
  });
  assert.equal(seen.url, "/_osanwe/activate");
  assert.equal(seen.options.method, "POST");
  assert.equal(seen.options.headers["content-type"], "application/json");
  assert.equal(seen.options.body, '{"schema_version":2}');
  await assert.rejects(activateInviteBook(""), /non-empty JSON/);
});

test("sendMessages cannot add an arbitrary endpoint or request field", async () => {
  let seen;
  const fetchImpl = async (url, options) => {
    seen = { url, options };
    return { ok: true };
  };
  await sendMessages(
    { model: "m", messages: [{ role: "user", content: "hello" }], ignored: "secret" },
    { fetchImpl },
  );
  assert.equal(seen.url, "/v1/messages");
  assert.deepEqual(Object.keys(JSON.parse(seen.options.body)).sort(), ["max_tokens", "messages", "model", "stream"]);
});

test("sendMessages passes the caller's exact abort signal to fetch", async () => {
  const controller = new AbortController();
  let seenSignal;
  await sendMessages(
	{ model: "m", messages: [{ role: "user", content: "hello" }] },
	{ signal: controller.signal, fetchImpl: async (_url, options) => {
	  seenSignal = options.signal;
	  return { ok: true };
	} },
  );
  assert.equal(seenSignal, controller.signal);
});

test("OpenAI-compatible BYOK uses the exact chat endpoint and bearer credential", async () => {
  let seen;
  await sendMessages(
    { model: "stealth/ox-alpha", messages: [{ role: "user", content: "hello" }] },
    {
      apiStyle: "openai",
      apiKey: "sk-or-test",
      fetchImpl: async (url, options) => {
        seen = { url, options };
        return { ok: true };
      },
    },
  );
  assert.equal(seen.url, "/v1/chat/completions");
  assert.equal(seen.options.headers.authorization, "Bearer sk-or-test");
  assert.equal(seen.options.headers["x-api-key"], undefined);
  assert.equal(JSON.parse(seen.options.body).model, "stealth/ox-alpha");
});

test("provider keys with header control characters fail before fetch", async () => {
  let called = false;
  await assert.rejects(
    sendMessages(
      { model: "m", messages: [{ role: "user", content: "hello" }] },
      { apiStyle: "openai", apiKey: "bad\nkey", fetchImpl: async () => { called = true; } },
    ),
    /malformed/,
  );
  assert.equal(called, false);
});

test("structured and plain API errors remain readable", async () => {
  assert.match(
    (await responseError({ text: async () => '{"error":{"message":"budget full"}}' }, "fallback")).message,
    /budget full/,
  );
  assert.equal((await responseError({ text: async () => "plain refusal" }, "fallback")).message, "plain refusal");
  assert.equal((await responseError({ text: async () => "" }, "fallback")).message, "fallback");
});

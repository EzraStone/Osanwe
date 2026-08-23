import assert from "node:assert/strict";
import test from "node:test";

import { buildMessageBody, loadModels, loadStatus, responseError, sendMessages } from "../assets/api.js";

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

test("structured and plain API errors remain readable", async () => {
  assert.match(
    (await responseError({ text: async () => '{"error":{"message":"budget full"}}' }, "fallback")).message,
    /budget full/,
  );
  assert.equal((await responseError({ text: async () => "plain refusal" }, "fallback")).message, "plain refusal");
  assert.equal((await responseError({ text: async () => "" }, "fallback")).message, "fallback");
});

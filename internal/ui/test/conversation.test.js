import assert from "node:assert/strict";
import test from "node:test";

import {
  appendTurn,
  assertConversation,
  conversationTitle,
  conversationTurnText,
  createConversation,
  exportConversation,
  toRequestMessages,
} from "../assets/conversation.js";

test("a conversation starts empty and versioned", () => {
  const conversation = createConversation({ id: "conversation-1", model: "model-a", now: 10 });
  assert.deepEqual(conversation, {
    schema: 1,
    id: "conversation-1",
    model: "model-a",
    createdAt: 10,
    updatedAt: 10,
    turns: [],
  });
});

test("stopped turns remain visibly partial after restore", () => {
  assert.equal(
    conversationTurnText({ role: "assistant", content: "Partial words", status: "stopped" }),
    "Partial words\n\nStopped — partial response. Stopping cannot recall text already sent to the provider or restore a spent token.",
  );
  assert.match(
    conversationTurnText({ role: "assistant", content: "", status: "stopped" }),
    /Stopped before response text arrived.*cannot recall.*spent token/,
  );
});

test("completed turns become provider context in order", () => {
  const conversation = createConversation({ id: "conversation-2", now: 1 });
  appendTurn(conversation, "user", "First question", { now: 2 });
  appendTurn(conversation, "assistant", "First answer", { now: 3 });
  appendTurn(conversation, "user", "Follow-up", { now: 4 });
  appendTurn(conversation, "assistant", "unfinished", { now: 5, status: "streaming" });

  assert.deepEqual(toRequestMessages(conversation), [
    { role: "user", content: "First question" },
    { role: "assistant", content: "First answer" },
    { role: "user", content: "Follow-up" },
  ]);
});

test("a title comes from local user text and stays bounded", () => {
  const conversation = createConversation({ id: "conversation-3", now: 1 });
  appendTurn(conversation, "assistant", "Welcome", { now: 2 });
  appendTurn(conversation, "user", `  ${"private ".repeat(12)}question  `, { now: 3 });
  const title = conversationTitle(conversation);
  assert.equal(title.length, 54);
  assert.match(title, /…$/);
  assert.doesNotMatch(title, /\s{2}/);
});

test("invalid persisted records fail closed", () => {
  assert.throws(() => assertConversation({ schema: 99, turns: [] }), /unsupported/);
  assert.throws(
    () => assertConversation({ schema: 1, id: "x", model: "", turns: [{ role: "system", content: "x", status: "complete" }] }),
    /turn is invalid/,
  );
  const conversation = createConversation({ id: "conversation-4", now: 1 });
  assert.throws(() => appendTurn(conversation, "system", "hidden"), /role/);
});

test("exports are versioned plaintext conversation data only", () => {
  const conversation = createConversation({ id: "conversation-5", model: "model-a", now: 1 });
  appendTurn(conversation, "user", "my words", { now: 2 });
  const exported = exportConversation(conversation, new Date("2026-08-22T12:00:00Z"));
  assert.equal(exported.format, "osanwe-conversation");
  assert.equal(exported.version, 1);
  assert.equal(exported.exported_at, "2026-08-22T12:00:00.000Z");
  assert.equal(exported.conversation.turns[0].content, "my words");
  assert.deepEqual(Object.keys(exported).sort(), ["conversation", "exported_at", "format", "version"]);
  assert.doesNotMatch(JSON.stringify(exported), /token|receipt|api.?key|credential/i);
});

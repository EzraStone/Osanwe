import assert from "node:assert/strict";
import test from "node:test";

import {
  appendTurn,
  assertConversation,
  conversationTitle,
  createConversation,
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

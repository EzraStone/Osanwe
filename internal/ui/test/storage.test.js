import assert from "node:assert/strict";
import test from "node:test";

import { appendTurn, createConversation } from "../assets/conversation.js";
import { conversationStore, MemoryConversationStore } from "../assets/storage.js";

test("ephemeral storage returns copies in recent order", async () => {
  const store = new MemoryConversationStore();
  const older = createConversation({ id: "conversation-old", now: 1 });
  const newer = createConversation({ id: "conversation-new", now: 2 });
  appendTurn(older, "user", "private", { now: 3 });
  appendTurn(newer, "user", "newer", { now: 4 });
  await store.put(older);
  await store.put(newer);

  const listed = await store.list();
  assert.deepEqual(listed.map((item) => item.id), ["conversation-new", "conversation-old"]);
  listed[0].turns[0].content = "mutated copy";
  assert.equal((await store.get("conversation-new")).turns[0].content, "newer");
});

test("ephemeral storage supports complete deletion", async () => {
  const store = conversationStore("ephemeral");
  const first = createConversation({ id: "conversation-one", now: 1 });
  const second = createConversation({ id: "conversation-two", now: 2 });
  await store.put(first);
  await store.put(second);
  await store.delete(first.id);
  assert.equal(await store.get(first.id), null);
  assert.equal((await store.list()).length, 1);
  await store.clear();
  assert.deepEqual(await store.list(), []);
});

test("invalid conversations are never persisted", async () => {
  const store = new MemoryConversationStore();
  await assert.rejects(() => store.put({ schema: 99, id: "bad", turns: [] }), /unsupported/);
});

test("device mode fails visibly when IndexedDB is unavailable", () => {
  assert.throws(() => conversationStore("device", null), /unavailable/);
});

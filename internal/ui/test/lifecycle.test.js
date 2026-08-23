import assert from "node:assert/strict";
import test from "node:test";

import { createConversation } from "../assets/conversation.js";
import { ConversationLifecycle } from "../assets/lifecycle.js";
import { MemoryConversationStore } from "../assets/storage.js";

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

test("delete waits for an active put and wins permanently", async () => {
  const entered = deferred();
  const release = deferred();
  const records = new Map();
  const store = {
    async put(value) {
      entered.resolve();
      await release.promise;
      records.set(value.id, value);
    },
    async delete(id) { records.delete(id); },
  };
  const lifecycle = new ConversationLifecycle();
  const conversation = createConversation({ id: "conversation-race", now: 1 });

  const writing = lifecycle.persist(store, conversation);
  await entered.promise;
  const deleting = lifecycle.delete(store, conversation.id);
  release.resolve();
  await Promise.all([writing, deleting]);

  assert.equal(records.has(conversation.id), false);
  assert.equal(await lifecycle.persist(store, conversation), false);
  assert.equal(records.has(conversation.id), false);
});

test("clear invalidates queued snapshots before they can run", async () => {
  const gate = deferred();
  const store = new MemoryConversationStore();
  const lifecycle = new ConversationLifecycle();
  const originalPut = store.put.bind(store);
  let first = true;
  store.put = async (value) => {
    if (first) {
      first = false;
      await gate.promise;
    }
    return originalPut(value);
  };
  const firstConversation = createConversation({ id: "conversation-first", now: 1 });
  const queuedConversation = createConversation({ id: "conversation-queued", now: 2 });

  const firstWrite = lifecycle.persist(store, firstConversation);
  const queuedWrite = lifecycle.persist(store, queuedConversation);
  const clearing = lifecycle.clear(store);
  gate.resolve();
  await Promise.all([firstWrite, queuedWrite, clearing]);

  assert.deepEqual(await store.list(), []);
  assert.equal(await queuedWrite, false);
});

test("a failed write cannot prevent a later delete", async () => {
  let deleted = false;
  const lifecycle = new ConversationLifecycle();
  const conversation = createConversation({ id: "conversation-failure", now: 1 });
  const store = {
    async put() { throw new Error("disk full"); },
    async delete() { deleted = true; },
  };

  await assert.rejects(lifecycle.persist(store, conversation), /disk full/);
  await lifecycle.delete(store, conversation.id);
  assert.equal(deleted, true);
});

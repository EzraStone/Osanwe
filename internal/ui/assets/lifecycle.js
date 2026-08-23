import { assertConversation } from "./conversation.js";

function snapshot(conversation) {
  assertConversation(conversation);
  return JSON.parse(JSON.stringify(conversation));
}

// ConversationLifecycle serializes writes with destructive operations. A
// deletion becomes effective when it is requested, not when IndexedDB happens
// to finish it, so a late stream cannot put the same record back afterwards.
export class ConversationLifecycle {
  #tail = Promise.resolve();
  #generation = 0;
  #deleted = new Set();

  persist(store, conversation) {
    const record = snapshot(conversation);
    const generation = this.#generation;
    return this.#enqueue(async () => {
      if (generation !== this.#generation || this.#deleted.has(record.id)) return false;
      await store.put(record);
      return true;
    });
  }

  delete(store, id) {
    if (typeof id !== "string" || !id) return Promise.reject(new TypeError("conversation id is invalid"));
    this.#deleted.add(id);
    return this.#enqueue(async () => {
      await store.delete(id);
      return true;
    });
  }

  clear(store) {
    this.#generation += 1;
    this.#deleted.clear();
    return this.#enqueue(async () => {
      await store.clear();
      return true;
    });
  }

  idle() {
    return this.#tail;
  }

  #enqueue(operation) {
    const result = this.#tail.then(operation, operation);
    // A failed browser write is reported by the caller, but it must not poison
    // later deletion work in the queue.
    this.#tail = result.catch(() => undefined);
    return result;
  }
}

import { assertConversation } from "./conversation.js";

const DATABASE = "osanwe-conversations";
const VERSION = 1;
const STORE = "conversations";

function copyConversation(conversation) {
  assertConversation(conversation);
  return JSON.parse(JSON.stringify(conversation));
}

export class MemoryConversationStore {
  #records = new Map();

  async list() {
    return [...this.#records.values()]
      .map(copyConversation)
      .sort((left, right) => right.updatedAt - left.updatedAt);
  }

  async get(id) {
    const value = this.#records.get(id);
    return value ? copyConversation(value) : null;
  }

  async put(conversation) {
    const value = copyConversation(conversation);
    this.#records.set(value.id, value);
    return copyConversation(value);
  }

  async delete(id) {
    this.#records.delete(id);
  }

  async clear() {
    this.#records.clear();
  }
}

export class IndexedDBConversationStore {
  #factory;
  #database;

  constructor(factory = globalThis.indexedDB) {
    if (!factory || typeof factory.open !== "function") throw new Error("device-only history is unavailable");
    this.#factory = factory;
  }

  async #open() {
    if (this.#database) return this.#database;
    this.#database = await new Promise((resolve, reject) => {
      const request = this.#factory.open(DATABASE, VERSION);
      request.onupgradeneeded = () => {
        const database = request.result;
        if (!database.objectStoreNames.contains(STORE)) database.createObjectStore(STORE, { keyPath: "id" });
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error || new Error("opening device-only history failed"));
      request.onblocked = () => reject(new Error("device-only history upgrade is blocked by another tab"));
    });
    return this.#database;
  }

  async list() {
    const values = await this.#request("readonly", (store) => store.getAll());
    return values
      .flatMap((value) => {
        try { return [copyConversation(value)]; } catch { return []; }
      })
      .sort((left, right) => right.updatedAt - left.updatedAt);
  }

  async get(id) {
    const value = await this.#request("readonly", (store) => store.get(id));
    if (!value) return null;
    return copyConversation(value);
  }

  async put(conversation) {
    const value = copyConversation(conversation);
    await this.#request("readwrite", (store) => store.put(value));
    return copyConversation(value);
  }

  async delete(id) {
    await this.#request("readwrite", (store) => store.delete(id));
  }

  async clear() {
    await this.#request("readwrite", (store) => store.clear());
  }

  async #request(mode, operation) {
    const database = await this.#open();
    return new Promise((resolve, reject) => {
      const transaction = database.transaction(STORE, mode);
      const request = operation(transaction.objectStore(STORE));
      let result;
      request.onsuccess = () => { result = request.result; };
      request.onerror = () => reject(request.error || new Error("device-only history request failed"));
      transaction.oncomplete = () => resolve(result);
      transaction.onerror = () => reject(transaction.error || new Error("device-only history transaction failed"));
      transaction.onabort = () => reject(transaction.error || new Error("device-only history transaction was aborted"));
    });
  }
}

export function conversationStore(mode, factory = globalThis.indexedDB) {
  return mode === "device" ? new IndexedDBConversationStore(factory) : new MemoryConversationStore();
}

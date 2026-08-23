const ROLES = new Set(["user", "assistant"]);
const STATUSES = new Set(["complete", "streaming", "stopped", "error"]);

function defaultID() {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === "function") {
    return globalThis.crypto.randomUUID();
  }
  throw new Error("secure conversation identifiers are unavailable");
}

export function createConversation({ id = defaultID(), model = "", now = Date.now() } = {}) {
  if (typeof id !== "string" || id.length < 8) throw new TypeError("conversation id is invalid");
  if (typeof model !== "string") throw new TypeError("conversation model is invalid");
  if (!Number.isSafeInteger(now) || now < 0) throw new TypeError("conversation time is invalid");
  return { schema: 1, id, model, createdAt: now, updatedAt: now, turns: [] };
}

export function appendTurn(conversation, role, content, options = {}) {
  assertConversation(conversation);
  if (!ROLES.has(role)) throw new TypeError("turn role must be user or assistant");
  if (typeof content !== "string") throw new TypeError("turn content must be text");
  const status = options.status || "complete";
  if (!STATUSES.has(status)) throw new TypeError("turn status is invalid");
  const now = options.now === undefined ? Date.now() : options.now;
  if (!Number.isSafeInteger(now) || now < 0) throw new TypeError("turn time is invalid");
  const turn = {
    id: options.id || `${conversation.id}:${conversation.turns.length + 1}`,
    role,
    content,
    status,
    createdAt: now,
  };
  conversation.turns.push(turn);
  conversation.updatedAt = now;
  return turn;
}

export function toRequestMessages(conversation) {
  assertConversation(conversation);
  return conversation.turns
    .filter((turn) => turn.status === "complete" && turn.content.trim())
    .map((turn) => ({ role: turn.role, content: turn.content }));
}

export function conversationTitle(conversation, fallback = "New conversation") {
  assertConversation(conversation);
  const first = conversation.turns.find((turn) => turn.role === "user" && turn.content.trim());
  if (!first) return fallback;
  const oneLine = first.content.trim().replace(/\s+/g, " ");
  return oneLine.length > 54 ? `${oneLine.slice(0, 53)}…` : oneLine;
}

export function exportConversation(conversation, exportedAt = new Date()) {
  assertConversation(conversation);
  if (!(exportedAt instanceof Date) || Number.isNaN(exportedAt.getTime())) {
    throw new TypeError("export time is invalid");
  }
  return {
    format: "osanwe-conversation",
    version: 1,
    exported_at: exportedAt.toISOString(),
    conversation: JSON.parse(JSON.stringify(conversation)),
  };
}

export function assertConversation(value) {
  if (!value || typeof value !== "object" || value.schema !== 1) {
    throw new TypeError("conversation schema is unsupported");
  }
  if (typeof value.id !== "string" || typeof value.model !== "string" || !Array.isArray(value.turns)) {
    throw new TypeError("conversation record is invalid");
  }
  for (const turn of value.turns) {
    if (!turn || !ROLES.has(turn.role) || typeof turn.content !== "string" || !STATUSES.has(turn.status)) {
      throw new TypeError("conversation turn is invalid");
    }
  }
  return value;
}

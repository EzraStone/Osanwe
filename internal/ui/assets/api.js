const PREFIX = "/_osanwe/";

export async function loadStatus(fetchImpl = globalThis.fetch) {
  const response = await fetchImpl(`${PREFIX}status`, { headers: { accept: "application/json" } });
  if (!response.ok) throw await responseError(response, "status request failed");
  return response.json();
}

export async function loadModels(fetchImpl = globalThis.fetch) {
  const response = await fetchImpl("/v1/models", { headers: { accept: "application/json" } });
  if (!response.ok) throw await responseError(response, "model catalog request failed");
  const catalog = await response.json();
  return Array.isArray(catalog.data) ? catalog : { data: [] };
}

export function buildMessageBody({ model, messages, maxTokens = 2048, stream = true }) {
  if (typeof model !== "string" || !model.trim()) throw new TypeError("a model is required");
  if (!Array.isArray(messages) || messages.length === 0) throw new TypeError("at least one message is required");
  const normalized = messages.map((message) => {
    if (!message || (message.role !== "user" && message.role !== "assistant")) {
      throw new TypeError("message roles must be user or assistant");
    }
    if (typeof message.content !== "string") throw new TypeError("message content must be text");
    return { role: message.role, content: message.content };
  });
  if (!Number.isSafeInteger(maxTokens) || maxTokens < 1) throw new TypeError("maxTokens must be positive");
  return { model, max_tokens: maxTokens, stream: Boolean(stream), messages: normalized };
}

export async function sendMessages(input, { signal, fetchImpl = globalThis.fetch } = {}) {
  const response = await fetchImpl("/v1/messages", {
    method: "POST",
    signal,
    headers: { "content-type": "application/json", "anthropic-version": "2023-06-01" },
    body: JSON.stringify(buildMessageBody(input)),
  });
  if (!response.ok) throw await responseError(response, `request failed with status ${response.status}`);
  return response;
}

export async function responseError(response, fallback) {
  const text = await response.text();
  try {
    const parsed = JSON.parse(text);
    const message = parsed && parsed.error && parsed.error.message;
    if (typeof message === "string" && message) return new Error(message);
  } catch {
    // The provider may return plain text. It is still more useful than a generic status.
  }
  return new Error(text.trim() || fallback);
}

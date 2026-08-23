export class SSEParser {
  #buffer = "";

  push(chunk) {
    if (typeof chunk !== "string") throw new TypeError("SSE chunks must be text");
    this.#buffer += chunk;
    const events = [];
    let boundary;
    while ((boundary = this.#buffer.match(/\r?\n\r?\n/))) {
      const block = this.#buffer.slice(0, boundary.index);
      this.#buffer = this.#buffer.slice(boundary.index + boundary[0].length);
      const data = eventData(block);
      if (data !== null) events.push(data);
    }
    return events;
  }

  finish() {
    if (!this.#buffer) return [];
    const block = this.#buffer;
    this.#buffer = "";
    const data = eventData(block);
    return data === null ? [] : [data];
  }
}

function eventData(block) {
  const values = [];
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith(":")) continue;
    if (line === "data") {
      values.push("");
    } else if (line.startsWith("data:")) {
      const value = line.slice(5);
      values.push(value.startsWith(" ") ? value.slice(1) : value);
    }
  }
  return values.length ? values.join("\n") : null;
}

export function anthropicTextDelta(payload) {
  if (payload === "[DONE]") return { done: true, text: "" };
  let event;
  try {
    event = JSON.parse(payload);
  } catch {
    return { done: false, text: "" };
  }
  if (event.type === "error" && event.error) {
    throw new Error(event.error.message || "the provider returned an error");
  }
  const delta = event.type === "content_block_delta" ? event.delta || {} : {};
  return {
    done: event.type === "message_stop",
    text: delta.type === "text_delta" && typeof delta.text === "string" ? delta.text : "",
  };
}

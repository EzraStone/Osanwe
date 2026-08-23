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

// readAnthropicTextStream resolves only after the provider emits an explicit
// terminal event. A clean TCP EOF is not proof that a partial answer is
// complete, so an interrupted stream remains excluded from future context.
export async function readAnthropicTextStream(body, onText = () => {}) {
  if (!body || typeof body.getReader !== "function") throw new TypeError("a readable response body is required");
  if (typeof onText !== "function") throw new TypeError("onText must be a function");

  const reader = body.getReader();
  const decoder = new TextDecoder();
  const parser = new SSEParser();
  let sawTerminal = false;
  let reachedEOF = false;

  const consume = (payloads) => {
    for (const payload of payloads) {
      if (sawTerminal) break;
      const delta = anthropicTextDelta(payload);
      if (delta.text) onText(delta.text);
      if (delta.done) sawTerminal = true;
    }
  };

  try {
    while (!sawTerminal) {
      const item = await reader.read();
      if (item.done) {
        reachedEOF = true;
        consume(parser.push(decoder.decode()));
        consume(parser.finish());
        break;
      }
      consume(parser.push(decoder.decode(item.value, { stream: true })));
    }
    if (!sawTerminal) {
      throw new Error("The response ended before the provider confirmed it was complete.");
    }
  } finally {
    // Stop bytes after a terminal event and release the reader. Cancellation
    // is cleanup, never evidence that a valid terminal event was incomplete.
    if (!reachedEOF) {
      try { await reader.cancel(); } catch {}
    }
    try { reader.releaseLock(); } catch {}
  }
}

const DEFAULT_MAX_BYTES = 2 * 1024 * 1024;

function eventFromBlock(block) {
  let event = 'message';
  const data = [];
  for (const line of block.split(/\r?\n/)) {
    if (!line || line.startsWith(':')) continue;
    const separator = line.indexOf(':');
    const field = separator === -1 ? line : line.slice(0, separator);
    let value = separator === -1 ? '' : line.slice(separator + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') event = value || 'message';
    if (field === 'data') data.push(value);
  }
  if (data.length === 0) return null;
  return { event, data: data.join('\n') };
}

export class SSEDecoder {
  constructor({ maxBytes = DEFAULT_MAX_BYTES } = {}) {
    this.maxBytes = maxBytes;
    this.bytes = 0;
    this.buffer = '';
    this.decoder = new TextDecoder();
  }

  push(chunk) {
    const value = typeof chunk === 'string' ? new TextEncoder().encode(chunk) : chunk;
    if (!(value instanceof Uint8Array)) throw new TypeError('SSE chunks must be bytes or text.');
    this.bytes += value.byteLength;
    if (this.bytes > this.maxBytes) throw new RangeError('provider stream too large');
    this.buffer += this.decoder.decode(value, { stream: true });
    return this.#drain(false);
  }

  finish() {
    this.buffer += this.decoder.decode();
    return this.#drain(true);
  }

  #drain(atEnd) {
    const events = [];
    while (true) {
      const match = /\r?\n\r?\n/.exec(this.buffer);
      if (!match) break;
      const block = this.buffer.slice(0, match.index);
      this.buffer = this.buffer.slice(match.index + match[0].length);
      const event = eventFromBlock(block);
      if (event) events.push(event);
    }
    if (atEnd && this.buffer) {
      const event = eventFromBlock(this.buffer);
      this.buffer = '';
      if (event) events.push(event);
    }
    return events;
  }
}

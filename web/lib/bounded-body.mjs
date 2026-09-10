// Bound bytes while reading, not after buffering an untrusted body.
export async function readBoundedText(body, limit, { signal, timeoutMs = 10_000 } = {}) {
  if (!body) return '';
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let bytes = 0;
  let text = '';
  let timer;
  let interrupt;
  const interrupted = new Promise((_, reject) => {
    interrupt = () => {
      reject(new DOMException('Body read interrupted.', 'AbortError'));
      void reader.cancel().catch(() => {});
    };
    timer = setTimeout(interrupt, timeoutMs);
    signal?.addEventListener('abort', interrupt, { once: true });
    if (signal?.aborted) interrupt();
  });
  try {
    while (true) {
      const item = await Promise.race([interrupted, reader.read()]);
      if (item.done) break;
      bytes += item.value.byteLength;
      if (bytes > limit) {
        void reader.cancel().catch(() => {});
        throw new RangeError('Body exceeds the byte limit.');
      }
      text += decoder.decode(item.value, { stream: true });
    }
    return text + decoder.decode();
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', interrupt);
    reader.releaseLock();
  }
}

// Limits apply to error/catalog JSON too, not just generated answers.
export async function readResponseText(response, { maxBytes = 16384, timeoutMs = 10000 } = {}) {
  if (!response.body) return '';
  const reader = response.body.getReader();
  let timer, finished = false, bytes = 0, text = '';
  const decoder = new TextDecoder();
  const deadline = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error('The response took too long. Try again.')), timeoutMs);
  });
  try {
    while (true) {
      const item = await Promise.race([reader.read(), deadline]);
      if (item.done) { finished = true; return text + decoder.decode(); }
      bytes += item.value.byteLength;
      if (bytes > maxBytes) throw new RangeError('The response exceeded the browser safety limit.');
      text += decoder.decode(item.value, { stream: true });
    }
  } finally {
    clearTimeout(timer);
    if (!finished) reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

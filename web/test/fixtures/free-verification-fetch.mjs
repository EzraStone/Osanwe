// Loaded only by the subprocess unit test. No network is used.
let calls = 0;
globalThis.fetch = async (url, init = {}) => {
  const address = url instanceof Request ? url.url : url instanceof URL ? url.href : url;
  if (address === 'https://www.tokenrouter.com/models/z-ai/glm-5.3-free/') {
    return new Response(`Input price: $${process.env.TEST_PAID_PRICE ? '1.0000' : '0.0000'} per 1M tokens Output price: $0.0000 per 1M tokens`);
  }
  if (++calls > 6 || init.redirect !== 'manual') throw new Error('unexpected request');
  const payload = JSON.parse(init.body);
  if (payload.model !== 'z-ai/glm-5.3-free') throw new Error('paid fallback');
  if (address === 'https://api.tokenrouter.com/v1/chat/completions') {
    return Response.json(process.env.TEST_EMPTY_PROBE ? {} : { choices: [{ message: { content: 'synthetic-private-output' } }] });
  }
  if (address === 'https://osanwe.vercel.app/api/providers/check') return Response.json({ ok: true });
  if (address !== 'https://osanwe.vercel.app/api/chat') throw new Error('unexpected destination');
  if (init.headers.authorization === 'Bearer intentionally-invalid-synthetic-key') {
    return Response.json({ error: { code: 'invalid_key' } }, { status: 502 });
  }
  if (payload.max_output_tokens !== 512) throw new Error('unbounded request');
  const text = 'synthetic-private-output blue triangle';
  const delta = `data: ${JSON.stringify({ type: 'content_block_delta', delta: { type: 'text_delta', text } })}\n\n`;
  if (payload.messages[0].content.includes('100 numbered')) {
    return new Response(new ReadableStream({ start(c) {
      c.enqueue(new TextEncoder().encode(delta));
      init.signal.addEventListener('abort', () => c.error(new DOMException('Stopped', 'AbortError')), { once: true });
    } }));
  }
  return new Response(delta + 'data: {"type":"message_stop"}\n\n');
};

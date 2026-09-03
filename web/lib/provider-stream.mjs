function textDelta(text) {
  if (typeof text !== 'string' || text.length === 0) return [];
  return [{ type: 'content_block_delta', delta: { type: 'text_delta', text } }];
}

function stop() {
  return [{ type: 'message_stop' }];
}

function parseObject(data) {
  try {
    const value = JSON.parse(data);
    return value !== null && typeof value === 'object' && !Array.isArray(value) ? value : null;
  } catch {
    return null;
  }
}

function openAIChatEvent(event) {
  if (event.data === '[DONE]') return stop();
  const value = parseObject(event.data);
  if (!value) return [];
  const choice = Array.isArray(value.choices) ? value.choices[0] : null;
  if (!choice || typeof choice !== 'object') return [];
  const content = choice.delta && typeof choice.delta === 'object' ? choice.delta.content : '';
  const events = textDelta(content);
  if (choice.finish_reason) events.push(...stop());
  return events;
}

function anthropicEvent(event) {
  if (event.event === 'message_stop') return stop();
  const value = parseObject(event.data);
  if (!value) return [];
  if (value.type === 'message_stop') return stop();
  if (value.type !== 'content_block_delta') return [];
  return textDelta(value.delta && typeof value.delta === 'object' ? value.delta.text : '');
}

function openAIResponsesEvent(event) {
  const value = parseObject(event.data);
  if (!value) return [];
  const type = typeof value.type === 'string' ? value.type : event.event;
  if (type === 'response.output_text.delta') return textDelta(value.delta);
  if (type === 'response.completed') return stop();
  return [];
}

function geminiEvent(event) {
  const value = parseObject(event.data);
  if (!value) return [];
  const candidate = Array.isArray(value.candidates) ? value.candidates[0] : null;
  if (!candidate || typeof candidate !== 'object') return [];
  const parts = candidate.content && Array.isArray(candidate.content.parts) ? candidate.content.parts : [];
  const text = parts
    .map((part) => (part && typeof part === 'object' && typeof part.text === 'string' ? part.text : ''))
    .join('');
  const events = textDelta(text);
  if (candidate.finishReason) events.push(...stop());
  return events;
}

export function normalizeProviderEvent(providerStyle, event) {
  if (providerStyle === 'openai-chat') return openAIChatEvent(event);
  if (providerStyle === 'anthropic') return anthropicEvent(event);
  if (providerStyle === 'openai-responses') return openAIResponsesEvent(event);
  if (providerStyle === 'gemini') return geminiEvent(event);
  return [];
}

export function encodeNormalizedEvent(event) {
  return `data: ${JSON.stringify(event)}\n\n`;
}

export function normalizeProviderStream(providerStyle, upstream, { maxBytes } = {}) {
  if (!upstream || typeof upstream.getReader !== 'function') {
    throw new TypeError('The provider response did not contain a readable stream.');
  }
  const reader = upstream.getReader();
  const decoder = new SSEDecoder({ maxBytes });
  const encoder = new TextEncoder();
  let stopped = false;

  function write(events, controller) {
    for (const event of events) {
      if (event.type === 'message_stop') {
        if (stopped) continue;
        stopped = true;
      }
      if (stopped && event.type !== 'message_stop') continue;
      controller.enqueue(encoder.encode(encodeNormalizedEvent(event)));
    }
  }

  return new ReadableStream({
    async start(controller) {
      try {
        while (true) {
          const item = await reader.read();
          if (item.done) break;
          for (const event of decoder.push(item.value)) {
            write(normalizeProviderEvent(providerStyle, event), controller);
          }
        }
        for (const event of decoder.finish()) {
          write(normalizeProviderEvent(providerStyle, event), controller);
        }
        controller.close();
      } catch (error) {
        controller.error(error);
      } finally {
        try { reader.releaseLock(); } catch { /* stream cleanup only */ }
      }
    },
    async cancel(reason) {
      try { await reader.cancel(reason); } catch { /* cancellation is best effort */ }
    },
  });
}
import { SSEDecoder } from './sse-decoder.mjs';

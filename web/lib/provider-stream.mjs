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

export function normalizeProviderEvent(providerStyle, event) {
  if (providerStyle === 'openai-chat') return openAIChatEvent(event);
  if (providerStyle === 'anthropic') return anthropicEvent(event);
  if (providerStyle === 'openai-responses') return openAIResponsesEvent(event);
  return [];
}

export function encodeNormalizedEvent(event) {
  return `data: ${JSON.stringify(event)}\n\n`;
}

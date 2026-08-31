const MAX_KEY_LENGTH = 512;
const MAX_REQUEST_BYTES = 64 * 1024;
const MAX_MESSAGES = 32;
const MAX_MESSAGE_CHARACTERS = 16 * 1024;
const MAX_CONVERSATION_CHARACTERS = 56 * 1024;
const MAX_OUTPUT_TOKENS = 2048;

export const PROVIDER_CATALOG = Object.freeze({
  groq: Object.freeze({
    endpoint: 'https://api.groq.com/openai/v1/chat/completions',
    models: Object.freeze(['openai/gpt-oss-20b', 'openai/gpt-oss-120b']),
  }),
  openai: Object.freeze({
    endpoint: 'https://api.openai.com/v1/responses',
    models: Object.freeze(['gpt-5.6-luna', 'gpt-5.6-terra', 'gpt-5.6-sol']),
  }),
});

const MODE_INSTRUCTIONS = Object.freeze({
  chat:
    'You are Osanwë, a thoughtful general-purpose assistant. Be accurate, direct, and honest about uncertainty. Do not claim access to tools, files, or current information that was not provided.',
  code:
    'You are Osanwë Code, a focused coding assistant. Help analyze, write, review, debug, and explain code. Prefer concise, directly usable answers. You cannot access the user’s files, terminal, or deployed systems, so state that limitation when it matters.',
});

function plainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function exactKeys(value, allowed) {
  return Object.keys(value).every((key) => allowed.includes(key));
}

export function normalizeProviderKey(authorization) {
  if (typeof authorization !== 'string' || !authorization.startsWith('Bearer ')) {
    throw new TypeError('Connect a provider API key in Settings.');
  }
  const key = authorization.slice('Bearer '.length);
  const hasUnsafeCharacter = Array.from(key).some((character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint <= 32 || codePoint === 127;
  });
  if (
    key.length < 8 ||
    key.length > MAX_KEY_LENGTH ||
    key !== key.trim() ||
    hasUnsafeCharacter
  ) {
    throw new TypeError('The provider API key is malformed.');
  }
  return key;
}

export function normalizeChatPayload(value) {
  if (!plainObject(value) || !exactKeys(value, ['provider', 'model', 'mode', 'messages'])) {
    throw new TypeError('The request contains unsupported fields.');
  }

  const { provider, model, mode, messages } = value;
  const providerConfig = PROVIDER_CATALOG[provider];
  if (!providerConfig) throw new TypeError('The selected provider is not supported.');
  if (typeof model !== 'string' || !providerConfig.models.includes(model)) {
    throw new TypeError('The selected model is not available for this provider.');
  }
  if (mode !== 'chat' && mode !== 'code') throw new TypeError('The selected mode is not supported.');
  if (!Array.isArray(messages) || messages.length < 1 || messages.length > MAX_MESSAGES) {
    throw new TypeError(`A conversation must contain between 1 and ${MAX_MESSAGES} messages.`);
  }

  let totalCharacters = 0;
  const normalizedMessages = messages.map((message) => {
    if (!plainObject(message) || !exactKeys(message, ['role', 'content'])) {
      throw new TypeError('Each message may contain only a role and text content.');
    }
    if (message.role !== 'user' && message.role !== 'assistant') {
      throw new TypeError('Message roles must be user or assistant.');
    }
    if (
      typeof message.content !== 'string' ||
      !message.content.trim() ||
      message.content.length > MAX_MESSAGE_CHARACTERS
    ) {
      throw new TypeError(`Each message must contain 1–${MAX_MESSAGE_CHARACTERS} characters of text.`);
    }
    totalCharacters += message.content.length;
    return { role: message.role, content: message.content };
  });

  if (totalCharacters > MAX_CONVERSATION_CHARACTERS) {
    throw new TypeError('The conversation is too large for this beta. Start a new conversation.');
  }
  if (normalizedMessages.at(-1)?.role !== 'user') {
    throw new TypeError('The final conversation message must be from the user.');
  }

  return { provider, model, mode, messages: normalizedMessages };
}

export function buildUpstreamRequest(payload, apiKey) {
  const config = PROVIDER_CATALOG[payload.provider];
  const instructions = MODE_INSTRUCTIONS[payload.mode];
  const headers = {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    accept: 'application/json',
  };

  if (payload.provider === 'groq') {
    return {
      url: config.endpoint,
      init: {
        method: 'POST',
        headers,
        redirect: 'manual',
        body: JSON.stringify({
          model: payload.model,
          messages: [{ role: 'system', content: instructions }, ...payload.messages],
          max_completion_tokens: MAX_OUTPUT_TOKENS,
          stream: false,
        }),
      },
    };
  }

  return {
    url: config.endpoint,
    init: {
      method: 'POST',
      headers,
      redirect: 'manual',
      body: JSON.stringify({
        model: payload.model,
        instructions,
        input: payload.messages,
        max_output_tokens: MAX_OUTPUT_TOKENS,
        store: false,
      }),
    },
  };
}

export function extractProviderOutput(provider, value) {
  if (!plainObject(value)) throw new TypeError('The provider returned an unreadable response.');

  if (provider === 'groq') {
    const content = value.choices?.[0]?.message?.content;
    if (typeof content === 'string' && content.trim()) return content;
  } else {
    if (typeof value.output_text === 'string' && value.output_text.trim()) return value.output_text;
    if (Array.isArray(value.output)) {
      const parts = [];
      for (const item of value.output) {
        if (!plainObject(item) || !Array.isArray(item.content)) continue;
        for (const part of item.content) {
          if (plainObject(part) && part.type === 'output_text' && typeof part.text === 'string') {
            parts.push(part.text);
          }
        }
      }
      const joined = parts.join('');
      if (joined.trim()) return joined;
    }
  }

  throw new TypeError('The provider returned no text output.');
}

export function safeProviderError(status) {
  if (status === 401 || status === 403) return 'The provider rejected that API key.';
  if (status === 404) return 'The selected model is not available for this provider account.';
  if (status === 408) return 'The provider timed out before answering.';
  if (status === 429) return 'The provider rate limit or spending limit was reached.';
  if (status >= 500) return 'The provider is temporarily unavailable.';
  return 'The provider rejected this request.';
}

export function requestIsTooLarge(request, rawBody) {
  const declared = Number(request.headers.get('content-length') || 0);
  if (Number.isFinite(declared) && declared > MAX_REQUEST_BYTES) return true;
  return new TextEncoder().encode(rawBody).byteLength > MAX_REQUEST_BYTES;
}

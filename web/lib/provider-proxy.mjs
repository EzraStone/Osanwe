export const MAX_REQUEST_BYTES = 64 * 1024;

const MAX_KEY_LENGTH = 512;
const MAX_MESSAGES = 32;
const MAX_MESSAGE_CHARACTERS = 16 * 1024;
const MAX_CONVERSATION_CHARACTERS = 56 * 1024;
const MAX_OUTPUT_TOKENS = 2048;
const MODEL_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,159}$/;

export const PROVIDER_CATALOG = Object.freeze({
  groq: Object.freeze({
    label: 'Groq',
    style: 'openai-chat',
    endpoint: 'https://api.groq.com/openai/v1/chat/completions',
    models: Object.freeze(['openai/gpt-oss-20b', 'openai/gpt-oss-120b']),
  }),
  openai: Object.freeze({
    label: 'OpenAI',
    style: 'openai-responses',
    endpoint: 'https://api.openai.com/v1/responses',
    models: Object.freeze(['gpt-5-mini', 'gpt-4.1-mini']),
  }),
  anthropic: Object.freeze({
    label: 'Anthropic',
    style: 'anthropic',
    endpoint: 'https://api.anthropic.com/v1/messages',
    models: Object.freeze(['claude-sonnet-4-5', 'claude-haiku-4-5']),
  }),
  google: Object.freeze({
    label: 'Google Gemini',
    style: 'gemini',
    endpoint: 'https://generativelanguage.googleapis.com/v1beta/models/',
    models: Object.freeze(['gemini-3.5-flash', 'gemini-3.5-pro']),
  }),
  openrouter: Object.freeze({
    label: 'OpenRouter',
    style: 'openai-chat',
    endpoint: 'https://openrouter.ai/api/v1/chat/completions',
    models: Object.freeze(['openai/gpt-5-mini', 'anthropic/claude-sonnet-4.5']),
  }),
  tokenrouter: Object.freeze({
    label: 'TokenRouter',
    style: 'openai-chat',
    endpoint: 'https://api.tokenrouter.com/v1/chat/completions',
    models: Object.freeze([
      'z-ai/glm-5.3-free',
      'z-ai/glm-5.3-flash',
      'z-ai/glm-5.3',
      'z-ai/glm-5.2',
    ]),
  }),
  xai: Object.freeze({
    label: 'xAI',
    style: 'openai-chat',
    endpoint: 'https://api.x.ai/v1/chat/completions',
    models: Object.freeze(['grok-4', 'grok-code-fast-1']),
  }),
  mistral: Object.freeze({
    label: 'Mistral AI',
    style: 'openai-chat',
    endpoint: 'https://api.mistral.ai/v1/chat/completions',
    models: Object.freeze(['mistral-small-latest', 'mistral-large-latest']),
  }),
  deepseek: Object.freeze({
    label: 'DeepSeek',
    style: 'openai-chat',
    endpoint: 'https://api.deepseek.com/chat/completions',
    models: Object.freeze(['deepseek-v4-flash', 'deepseek-v4-pro']),
  }),
  together: Object.freeze({
    label: 'Together AI',
    style: 'openai-chat',
    endpoint: 'https://api.together.ai/v1/chat/completions',
    models: Object.freeze(['Qwen/Qwen3.5-9B', 'meta-llama/Llama-3.3-70B-Instruct-Turbo']),
  }),
  fireworks: Object.freeze({
    label: 'Fireworks AI',
    style: 'openai-chat',
    endpoint: 'https://api.fireworks.ai/inference/v1/chat/completions',
    models: Object.freeze([
      'accounts/fireworks/models/gpt-oss-20b',
      'accounts/fireworks/models/llama-v3p3-70b-instruct',
    ]),
  }),
});

const MODE_INSTRUCTIONS = Object.freeze({
  chat:
    'You are Osanwë, a thoughtful general-purpose assistant. Be accurate, direct, and honest about uncertainty. Do not claim access to tools, files, or current information that was not provided.',
  code:
    'You are Osanwë Code, a focused coding assistant. Help analyze, write, review, debug, and explain code. Prefer concise, directly usable answers. You cannot access the user’s files, terminal, or deployed systems, so state that limitation when it matters. When a self-contained browser preview is useful, return fenced HTML, CSS, and JavaScript.',
});

function plainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function exactKeys(value, allowed) {
  return Object.keys(value).every((key) => allowed.includes(key));
}

export function publicProviderCatalog() {
  return Object.entries(PROVIDER_CATALOG).map(([id, provider]) => ({
    id,
    label: provider.label,
    models: [...provider.models],
  }));
}

export function normalizeProviderKey(authorization) {
  if (typeof authorization !== 'string' || !authorization.startsWith('Bearer ')) {
    throw new TypeError('Load a provider API key in Settings.');
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
  if (!PROVIDER_CATALOG[provider]) throw new TypeError('The selected provider is not supported.');
  if (typeof model !== 'string' || !MODEL_ID_PATTERN.test(model)) {
    throw new TypeError('Enter a valid model ID from the selected provider.');
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

export function normalizeProbePayload(value) {
  if (!plainObject(value) || !exactKeys(value, ['provider', 'model'])) {
    throw new TypeError('The connection test contains unsupported fields.');
  }
  if (!PROVIDER_CATALOG[value.provider]) {
    throw new TypeError('The selected provider is not supported.');
  }
  if (typeof value.model !== 'string' || !MODEL_ID_PATTERN.test(value.model)) {
    throw new TypeError('Enter a valid model ID from the selected provider.');
  }
  return { provider: value.provider, model: value.model };
}

function bearerHeaders(apiKey) {
  return {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    accept: 'text/event-stream',
  };
}

function openAIChatRequest(payload, apiKey, config, instructions) {
  const body = {
    model: payload.model,
    messages: [{ role: 'system', content: instructions }, ...payload.messages],
    max_tokens: MAX_OUTPUT_TOKENS,
    stream: true,
  };

  if (payload.provider === 'groq') {
    delete body.max_tokens;
    body.max_completion_tokens = MAX_OUTPUT_TOKENS;
    if (payload.model.startsWith('openai/gpt-oss-')) body.reasoning_effort = 'low';
  }
  return {
    url: config.endpoint,
    init: {
      method: 'POST',
      headers: bearerHeaders(apiKey),
      redirect: 'manual',
      body: JSON.stringify(body),
    },
  };
}

export function buildUpstreamRequest(payload, apiKey) {
  const config = PROVIDER_CATALOG[payload.provider];
  const instructions = MODE_INSTRUCTIONS[payload.mode];

  if (config.style === 'openai-chat') {
    return openAIChatRequest(payload, apiKey, config, instructions);
  }

  if (config.style === 'openai-responses') {
    const body = {
      model: payload.model,
      instructions,
      input: payload.messages,
      max_output_tokens: MAX_OUTPUT_TOKENS,
      store: false,
      stream: true,
    };
    if (/^(gpt-[56]|o\d)/.test(payload.model)) body.reasoning = { effort: 'low' };
    return {
      url: config.endpoint,
      init: {
        method: 'POST',
        headers: bearerHeaders(apiKey),
        redirect: 'manual',
        body: JSON.stringify(body),
      },
    };
  }

  if (config.style === 'anthropic') {
    return {
      url: config.endpoint,
      init: {
        method: 'POST',
        headers: {
          'x-api-key': apiKey,
          'anthropic-version': '2023-06-01',
          'content-type': 'application/json',
          accept: 'text/event-stream',
        },
        redirect: 'manual',
        body: JSON.stringify({
          model: payload.model,
          system: instructions,
          messages: payload.messages,
          max_tokens: MAX_OUTPUT_TOKENS,
          stream: true,
        }),
      },
    };
  }

  const contents = payload.messages.map((message) => ({
    role: message.role === 'assistant' ? 'model' : 'user',
    parts: [{ text: message.content }],
  }));
  return {
    url: `${config.endpoint}${encodeURIComponent(payload.model)}:streamGenerateContent?alt=sse`,
    init: {
      method: 'POST',
      headers: {
        'x-goog-api-key': apiKey,
        'content-type': 'application/json',
        accept: 'text/event-stream',
      },
      redirect: 'manual',
      body: JSON.stringify({
        system_instruction: { parts: [{ text: instructions }] },
        contents,
        generation_config: { max_output_tokens: MAX_OUTPUT_TOKENS },
      }),
    },
  };
}

export function buildProviderProbe(payload, apiKey) {
  const normalized = normalizeProbePayload(payload);
  const request = buildUpstreamRequest({
    ...normalized,
    mode: 'chat',
    messages: [{ role: 'user', content: 'Reply with OK.' }],
  }, apiKey);
  const body = JSON.parse(request.init.body);
  if ('max_completion_tokens' in body) body.max_completion_tokens = 32;
  if ('max_output_tokens' in body) body.max_output_tokens = 32;
  if ('max_tokens' in body) body.max_tokens = 32;
  if (body.generation_config) body.generation_config.max_output_tokens = 32;
  if ('stream' in body) body.stream = false;
  const url = request.url.replace(':streamGenerateContent?alt=sse', ':generateContent');
  return {
    ...request,
    url,
    init: {
      ...request.init,
      headers: { ...request.init.headers, accept: 'application/json' },
      body: JSON.stringify(body),
    },
  };
}

export function providerStyle(provider) {
  return PROVIDER_CATALOG[provider]?.style || null;
}

function textParts(value) {
  if (typeof value === 'string') return value;
  if (!Array.isArray(value)) return '';
  return value
    .map((part) => {
      if (!plainObject(part)) return '';
      return typeof part.text === 'string' ? part.text : '';
    })
    .join('');
}

export function extractProviderOutput(provider, value) {
  if (!plainObject(value)) throw new TypeError('The provider returned an unreadable response.');
  const style = PROVIDER_CATALOG[provider]?.style;

  if (style === 'openai-chat') {
    const content = textParts(value.choices?.[0]?.message?.content);
    if (content.trim()) return content;
  } else if (style === 'anthropic') {
    const content = textParts(value.content);
    if (content.trim()) return content;
  } else if (style === 'gemini') {
    const content = textParts(value.candidates?.[0]?.content?.parts);
    if (content.trim()) return content;
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
    if (value.status === 'incomplete') {
      throw new TypeError('The provider used the output limit before returning visible text. Try again or choose a non-reasoning model.');
    }
  }

  throw new TypeError('The provider returned no text output.');
}

export function providerFailure(status) {
  if (status === 401) {
    return { code: 'invalid_key', message: 'The provider rejected that API key.', retryable: false };
  }
  if (status === 403) {
    return {
      code: 'model_access_denied',
      message: 'The provider denied access. Check that this API key can use the selected model.',
      retryable: false,
    };
  }
  if (status === 402) {
    return { code: 'credit_unavailable', message: 'The provider account has no available credit.', retryable: false };
  }
  if (status === 404) {
    return {
      code: 'model_unavailable',
      message: 'The selected model is not available for this provider account.',
      retryable: false,
    };
  }
  if (status === 408 || status === 504) {
    return { code: 'provider_timeout', message: 'The provider timed out before answering.', retryable: true };
  }
  if (status === 413) {
    return {
      code: 'provider_request_too_large',
      message: 'The provider says this conversation is too large.',
      retryable: false,
    };
  }
  if (status === 429) {
    return {
      code: 'provider_limit_reached',
      message: 'The provider rate limit or spending limit was reached.',
      retryable: true,
    };
  }
  if (status >= 500) {
    return {
      code: 'provider_unavailable',
      message: 'The provider is temporarily unavailable.',
      retryable: true,
    };
  }
  return {
    code: 'provider_rejected_request',
    message: 'The provider rejected this request. Check the model ID and account permissions.',
    retryable: false,
  };
}

export function safeProviderError(status) {
  return providerFailure(status).message;
}

export function requestIsTooLarge(request, byteLength = 0) {
  const declared = Number(request.headers.get('content-length') || 0);
  if (Number.isFinite(declared) && declared > MAX_REQUEST_BYTES) return true;
  return byteLength > MAX_REQUEST_BYTES;
}

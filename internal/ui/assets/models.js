const UNKNOWN = "unknown";

export function normalizeCatalog(value) {
  if (!value || !Array.isArray(value.data)) return [];
  const seen = new Set();
  const models = [];
  for (const raw of value.data) {
    if (!raw || typeof raw.id !== "string" || !raw.id.trim() || seen.has(raw.id)) continue;
    seen.add(raw.id);
    const capabilities = raw.capabilities || {};
    const limits = raw.limits || {};
    const privacy = raw.osanwe || {};
    models.push({
      id: raw.id,
      type: raw.type === "model" ? "model" : UNKNOWN,
      capabilities: {
        text: capabilities.text === true,
        streaming: capabilities.streaming === true,
        tools: capabilities.tools === true,
        images: capabilities.images === true,
      },
      limits: {
        maxRequestBytes: positiveInteger(limits.max_request_bytes),
        maxOutputTokens: positiveInteger(limits.max_output_tokens),
      },
      privacy: {
        providerAccount: textOrUnknown(privacy.provider_account),
        relayContentAccess: textOrUnknown(privacy.relay_content_access),
        gatewayContentAccess: textOrUnknown(privacy.gateway_content_access),
        conversationHistory: textOrUnknown(privacy.conversation_history),
        addressSeparation: textOrUnknown(privacy.address_separation),
        providerRetention: textOrUnknown(privacy.provider_retention),
        providerTraining: textOrUnknown(privacy.provider_training),
      },
    });
  }
  return models;
}

function positiveInteger(value) {
  return Number.isSafeInteger(value) && value > 0 ? value : null;
}

function textOrUnknown(value) {
  return typeof value === "string" && value.trim() ? value : UNKNOWN;
}

export function modelFacts(model, status, retentionMode = "ephemeral") {
  return [
    ["Input", model.capabilities.text ? "Text" : "Not reported"],
    ["Streaming", model.capabilities.streaming ? "Available" : "Not reported"],
    ["Tools", model.capabilities.tools ? "Available" : "Not available"],
    ["Images", model.capabilities.images ? "Available" : "Not available"],
    ["Output limit", model.limits.maxOutputTokens ? `${model.limits.maxOutputTokens.toLocaleString()} tokens` : "Unknown"],
    ["Network address", status && status.relay ? "Stops at a verified relay" : "No active relay reported"],
    ["Provider account", status && status.paying === "tokens" ? "Gateway account; not your provider account" : "Your provider account"],
    ["Gateway access", humanize(model.privacy.gatewayContentAccess)],
    ["Osanwë history", retentionMode === "device" ? "Saved on this device" : "Ephemeral in this page"],
    ["Provider retention", humanize(model.privacy.providerRetention)],
    ["Provider training", humanize(model.privacy.providerTraining)],
  ];
}

export function humanize(value) {
  if (!value || value === UNKNOWN) return "Unknown";
  return value.replaceAll("_", " ").replace(/^./, (letter) => letter.toUpperCase());
}

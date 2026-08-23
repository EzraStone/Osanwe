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
		providerIdentity: textOrUnknown(privacy.provider_identity),
		policySource: httpsURLOrUnknown(privacy.policy_source),
		policyCheckedAt: dateOrUnknown(privacy.policy_checked_at),
      },
	  lifecycle: {
		experimental: Boolean(raw.lifecycle && raw.lifecycle.experimental === true),
		expiresAt: timestampOrUnknown(raw.lifecycle && raw.lifecycle.expires_at),
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

function httpsURLOrUnknown(value) {
  if (typeof value !== "string" || !value.trim()) return UNKNOWN;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && parsed.username === "" && parsed.password === "" ? parsed.href : UNKNOWN;
  } catch {
    return UNKNOWN;
  }
}

function dateOrUnknown(value) {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return UNKNOWN;
  const parsed = new Date(`${value}T00:00:00Z`);
  return Number.isNaN(parsed.valueOf()) || parsed.toISOString().slice(0, 10) !== value ? UNKNOWN : value;
}

function timestampOrUnknown(value) {
  if (typeof value !== "string" || !value.trim()) return UNKNOWN;
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? UNKNOWN : parsed.toISOString();
}

export function modelFacts(model, status, retentionMode = "ephemeral") {
  return [
    ["Input", model.capabilities.text ? "Text" : "Not reported"],
    ["Streaming", model.capabilities.streaming ? "Available" : "Not reported"],
    ["Tools", model.capabilities.tools ? "Available" : "Not available"],
    ["Images", model.capabilities.images ? "Available" : "Not available"],
    ["Output limit", model.limits.maxOutputTokens ? `${model.limits.maxOutputTokens.toLocaleString()} tokens` : "Unknown"],
	["Relay verification", relayVerificationLabel(status)],
    ["Provider account", status && status.paying === "tokens" ? "Gateway account; not your provider account" : "Your provider account"],
    ["Gateway access", humanize(model.privacy.gatewayContentAccess)],
    ["Osanwë history", retentionMode === "device" ? "Saved on this device" : "Ephemeral in this page"],
    ["Provider retention", humanize(model.privacy.providerRetention)],
    ["Provider training", humanize(model.privacy.providerTraining)],
	["Provider identity", humanize(model.privacy.providerIdentity)],
	["Policy checked", model.privacy.policyCheckedAt === UNKNOWN ? "Unknown" : model.privacy.policyCheckedAt],
	["Policy source", policySourceLabel(model.privacy.policySource)],
	["Lifecycle", lifecycleLabel(model.lifecycle)],
  ];
}

export function relayVerificationLabel(status) {
  if (!status || !status.relay) return "No relay reported";
  if (status.relay.verification === "connected_pin_matched") return "Pin matched on a successful connection";
  if (status.relay.verification === "pin_configured") return "Pin configured; live connection verification is not reported";
  // Schema compatibility: older clients exposed only key_matched.
  if (status.relay.key_matched === true) return "Pin matched on a successful connection";
  return "Relay verification unknown";
}

export function buildIdentityLabel(build) {
  if (!build || typeof build.version !== "string" || !build.version.trim()) return "Unknown";
  const release = build.version.trim().slice(0, 64);
  if (typeof build.commit !== "string" || !build.commit.trim() || build.commit === "unknown") return release;
  return `${release} · ${build.commit.trim().slice(0, 12)}`;
}

export function lifecycleLabel(lifecycle) {
  if (!lifecycle || lifecycle.experimental !== true) return "No temporary expiry reported";
  if (!lifecycle.expiresAt || lifecycle.expiresAt === UNKNOWN) return "Experimental; expiry unknown";
  return `Experimental; expires ${new Date(lifecycle.expiresAt).toISOString().replace("T", " ").replace(".000Z", " UTC")}`;
}

export function policySourceLabel(value) {
  if (!value || value === UNKNOWN) return "Unknown";
  try {
    return new URL(value).hostname;
  } catch {
    return "Unknown";
  }
}

export function humanize(value) {
  if (!value || value === UNKNOWN) return "Unknown";
  return value.replaceAll("_", " ").replace(/^./, (letter) => letter.toUpperCase());
}

import assert from "node:assert/strict";
import test from "node:test";

import { buildIdentityLabel, humanize, lifecycleLabel, modelFacts, normalizeCatalog, policySourceLabel, relayVerificationLabel } from "../assets/models.js";

test("model metadata is normalized without inventing policy", () => {
  const [model] = normalizeCatalog({
    schema_version: 1,
    data: [{
      id: "model-a",
      type: "model",
      capabilities: { text: true, streaming: true, tools: false, images: false },
      limits: { max_request_bytes: 1048576, max_output_tokens: 4096 },
      osanwe: { provider_account: "pooled", provider_retention: "unknown" },
    }],
  });
  assert.equal(model.id, "model-a");
  assert.deepEqual(model.capabilities, { text: true, streaming: true, tools: false, images: false });
  assert.equal(model.limits.maxOutputTokens, 4096);
  assert.equal(model.privacy.providerRetention, "unknown");
  assert.equal(model.privacy.providerTraining, "unknown");
	assert.equal(model.privacy.providerIdentity, "unknown");
	assert.deepEqual(model.lifecycle, { experimental: false, expiresAt: "unknown" });
});

test("sourced provider policy and temporary lifecycle are normalized", () => {
  const [model] = normalizeCatalog({
	schema_version: 2,
	data: [{
	  id: "stealth/ox-alpha",
	  osanwe: {
		provider_retention: "retained",
		provider_training: "unknown",
		provider_identity: "undisclosed",
		policy_source: "https://openrouter.ai/stealth/ox-alpha",
		policy_checked_at: "2026-08-22",
	  },
	  lifecycle: { experimental: true, expires_at: "2026-08-29T00:00:00Z" },
	}],
  });
  assert.equal(model.privacy.providerIdentity, "undisclosed");
  assert.equal(model.privacy.policyCheckedAt, "2026-08-22");
  assert.equal(policySourceLabel(model.privacy.policySource), "openrouter.ai");
  assert.equal(model.lifecycle.expiresAt, "2026-08-29T00:00:00.000Z");
  assert.match(lifecycleLabel(model.lifecycle), /Experimental; expires 2026-08-29 00:00:00 UTC/);
});

test("invalid policy dates, links, and lifecycle times stay unknown", () => {
  const [model] = normalizeCatalog({ data: [{
	id: "m",
	osanwe: { policy_source: "http://insecure.example", policy_checked_at: "2026-02-31" },
	lifecycle: { experimental: true, expires_at: "eventually" },
  }] });
  assert.equal(model.privacy.policySource, "unknown");
  assert.equal(model.privacy.policyCheckedAt, "unknown");
  assert.equal(model.lifecycle.expiresAt, "unknown");
  assert.equal(lifecycleLabel(model.lifecycle), "Experimental; expiry unknown");
});

test("legacy catalogs remain usable with unknown labels", () => {
  const [model] = normalizeCatalog({ data: [{ id: "legacy-model", type: "model" }] });
  assert.equal(model.id, "legacy-model");
  assert.equal(model.capabilities.text, false);
  assert.equal(model.limits.maxOutputTokens, null);
  assert.equal(model.privacy.gatewayContentAccess, "unknown");
});

test("invalid and duplicate catalog entries are ignored", () => {
  assert.deepEqual(normalizeCatalog(null), []);
  assert.deepEqual(
    normalizeCatalog({ data: [{ id: "m" }, { id: "m" }, { id: "" }, { nope: true }] }).map((model) => model.id),
    ["m"],
  );
});

test("privacy facts distinguish account and local retention state", () => {
  const [model] = normalizeCatalog({ data: [{ id: "m", osanwe: { gateway_content_access: "plaintext_until_attested_execution" } }] });
	const tokenFacts = new Map(modelFacts(model, { paying: "tokens", relay: { verification: "connected_pin_matched" } }, "device"));
  assert.match(tokenFacts.get("Provider account"), /not your/);
	assert.equal(tokenFacts.get("Relay verification"), "Pin matched on a successful connection");
  assert.equal(tokenFacts.get("Osanwë history"), "Saved on this device");
  assert.match(tokenFacts.get("Gateway access"), /Plaintext until attested execution/);

  const byokFacts = new Map(modelFacts(model, { paying: "your own key" }, "ephemeral"));
  assert.equal(byokFacts.get("Provider account"), "Your provider account");
  assert.equal(byokFacts.get("Provider retention"), "Unknown");
});

test("relay wording distinguishes a connection from a configured pin", () => {
  assert.equal(
	relayVerificationLabel({ relay: { verification: "connected_pin_matched", key_matched: true } }),
	"Pin matched on a successful connection",
  );
  assert.equal(
	relayVerificationLabel({ relay: { verification: "pin_configured", key_matched: false } }),
	"Pin configured; live connection verification is not reported",
  );
  assert.equal(relayVerificationLabel({}), "No relay reported");
  assert.equal(relayVerificationLabel({ relay: { key_matched: true } }), "Pin matched on a successful connection");
  assert.doesNotMatch(
	new Map(modelFacts(normalizeCatalog({ data: [{ id: "m" }] })[0], { relay: { verification: "pin_configured" } })).get("Relay verification"),
	/verified relay/i,
  );
});

test("build identity is concise and does not invent missing release data", () => {
  assert.equal(buildIdentityLabel({ version: "v0.2.0", commit: "0123456789abcdef", date: "2026-08-22" }), "v0.2.0 · 0123456789ab");
  assert.equal(buildIdentityLabel({ version: "dev", commit: "unknown" }), "dev");
  assert.equal(buildIdentityLabel(null), "Unknown");
});

test("machine labels become readable without changing their meaning", () => {
  assert.equal(humanize("ciphertext_only"), "Ciphertext only");
  assert.equal(humanize("unknown"), "Unknown");
});

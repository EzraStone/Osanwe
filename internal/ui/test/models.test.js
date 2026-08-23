import assert from "node:assert/strict";
import test from "node:test";

import { humanize, modelFacts, normalizeCatalog } from "../assets/models.js";

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
  const tokenFacts = new Map(modelFacts(model, { paying: "tokens", relay: {} }, "device"));
  assert.match(tokenFacts.get("Provider account"), /not your/);
  assert.equal(tokenFacts.get("Network address"), "Stops at a verified relay");
  assert.equal(tokenFacts.get("Osanwë history"), "Saved on this device");
  assert.match(tokenFacts.get("Gateway access"), /Plaintext until attested execution/);

  const byokFacts = new Map(modelFacts(model, { paying: "your own key" }, "ephemeral"));
  assert.equal(byokFacts.get("Provider account"), "Your provider account");
  assert.equal(byokFacts.get("Provider retention"), "Unknown");
});

test("machine labels become readable without changing their meaning", () => {
  assert.equal(humanize("ciphertext_only"), "Ciphertext only");
  assert.equal(humanize("unknown"), "Unknown");
});

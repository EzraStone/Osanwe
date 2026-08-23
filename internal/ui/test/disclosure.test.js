import assert from "node:assert/strict";
import test from "node:test";

import { disclosureNarrative } from "../assets/disclosure.js";

function text(lines) { return lines.flat().join(" "); }

test("a configured pin and zero completed requests make no past-tense routing claim", () => {
  const narrative = text(disclosureNarrative({
    paying: "tokens",
    relay: { verification: "pin_configured" },
    requests: { total: 0 },
  }, false));
  assert.match(narrative, /configured/);
  assert.match(narrative, /not reported yet/);
  assert.doesNotMatch(narrative, /words passed|address stopped|model answered|completed a request through/);
});

test("a locally completed request and connected pin allow bounded past tense", () => {
  const narrative = text(disclosureNarrative({
    paying: "tokens",
    relay: { verification: "connected_pin_matched" },
  }, true));
  assert.match(narrative, /completed a request through a pinned relay/);
  assert.match(narrative, /gateway and model provider could read/);
  assert.match(narrative, /does not promise complete anonymity/);
});

test("BYOK disclosure admits credential handling and provider account linkage", () => {
  const narrative = text(disclosureNarrative({ paying: "your own key" }, false));
  assert.match(narrative, /handles and forwards that credential transiently/);
  assert.match(narrative, /associate requests with the account/);
});

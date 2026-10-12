import assert from "node:assert/strict";
import test from "node:test";

import { ageLabel, disclosureNarrative, keyMismatchSummary } from "../assets/disclosure.js";

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

test("a relay that presented an unpublished key is named first, with a bounded claim", () => {
  const lines = disclosureNarrative({
    paying: "tokens",
    relay: { verification: "connected_pin_matched" },
    directory: { key_mismatch: { nickname: "ranger-one", address: "203.0.113.9:8443", seconds_ago: 150 } },
  }, false);
  const first = lines[0].join("");
  assert.match(first, /presented a key the directory did not publish/);
  assert.match(first, /ranger-one \(203\.0\.113\.9:8443\), 2 minutes ago/);
  assert.match(first, /refused that connection before sending anything through it/);
  assert.match(first, /impersonating the relay/);
});

test("no key mismatch, no warning", () => {
  for (const status of [{}, { directory: {} }, { directory: { key_mismatch: null } }, { directory: { key_mismatch: { address: "" } } }]) {
    assert.doesNotMatch(text(disclosureNarrative(status, false)), /did not publish/);
    assert.equal(keyMismatchSummary(status), null);
  }
});

test("a mismatch without a nickname is named by address", () => {
  assert.equal(keyMismatchSummary({ directory: { key_mismatch: { address: "203.0.113.9:8443", seconds_ago: 5 } } }),
    "203.0.113.9:8443, just now");
});

test("ages read as plain words", () => {
  for (const [seconds, want] of [[0, "just now"], [59, "just now"], [60, "1 minute ago"], [119, "1 minute ago"],
    [120, "2 minutes ago"], [3599, "59 minutes ago"], [3600, "1 hour ago"], [7300, "2 hours ago"],
    [-5, "just now"], [NaN, "just now"], [undefined, "just now"]]) {
    assert.equal(ageLabel(seconds), want, `ageLabel(${seconds})`);
  }
});

// ageLabel turns a count of seconds into the plain words the panel uses.
export function ageLabel(seconds) {
  const s = Number.isFinite(seconds) && seconds > 0 ? Math.floor(seconds) : 0;
  if (s < 60) return "just now";
  if (s < 3600) {
    const m = Math.floor(s / 60);
    return `${m} minute${m === 1 ? "" : "s"} ago`;
  }
  const h = Math.floor(s / 3600);
  return `${h} hour${h === 1 ? "" : "s"} ago`;
}

// keyMismatchSummary names the relay that most recently presented a key the
// directory did not publish, and when, or returns null if none has.
export function keyMismatchSummary(status) {
  const m = status && status.directory && status.directory.key_mismatch;
  if (!m || typeof m.address !== "string" || !m.address) return null;
  const name = typeof m.nickname === "string" && m.nickname ? `${m.nickname} (${m.address})` : m.address;
  return `${name}, ${ageLabel(m.seconds_ago)}`;
}

export function disclosureNarrative(status, completedHere = false) {
  const relay = status && status.relay;
  const connected = relay && (relay.verification === "connected_pin_matched" ||
    (relay.verification === undefined && relay.key_matched === true));
  const tokenMode = status && status.paying === "tokens";
  const lines = [];

  // First, because it is the one line here that may describe an attack rather
  // than a limitation. The connection was refused, so the claim is bounded.
  const mismatch = keyMismatchSummary(status);
  if (mismatch) {
    lines.push(["A listed relay presented a key the directory did not publish. ",
      `This client refused that connection before sending anything through it: ${mismatch}. ` +
      "It can mean the operator changed the key without republishing, or that something is impersonating the relay."]);
  }

  if (completedHere && connected) {
    lines.push(["This page completed a request through a pinned relay. ",
      "The relay could see the source address, timing, traffic volume, and allowed destination, but not the encrypted prompt or answer text."]);
  } else if (completedHere) {
    lines.push(["This page completed a model request. ",
      "The current status does not prove a successful pinned relay connection, so this panel does not claim one."]);
  } else if (connected) {
    lines.push(["A successful pinned relay connection has been observed. ",
      "No model request from this page has completed yet; when one does, the relay is designed to carry encrypted content while seeing network metadata."]);
  } else if (relay) {
    lines.push(["A relay address and pin are configured. ",
      "A successful live connection is not reported yet, and no model request from this page has completed."]);
  } else {
    lines.push(["No relay connection is currently reported. ",
      "This panel cannot claim that an address or request content was separated."]);
  }

  if (tokenMode && completedHere) {
    lines.push(["The request used the gateway's provider account rather than yours. ",
      "The gateway and model provider could read the words, and this client cannot verify that relay and gateway have independent operators."]);
  } else if (tokenMode) {
    lines.push(["Token mode is configured to use the gateway's provider account. ",
      "If you send a request, the gateway and model provider can read the words; independent relay and gateway operation is not verified by this client."]);
  } else {
    lines.push(["Bring-your-own-key mode uses your provider account. ",
      "The local client handles and forwards that credential transiently, and the provider can associate requests with the account."]);
  }
  lines.push(["Writing style and request timing can still identify a person. ",
    "Osanwë reduces specific links in the path; it does not promise complete anonymity."]);
  return lines;
}

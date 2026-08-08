#!/usr/bin/env bash
#
# Runs the whole Osanwë network on one machine and narrates what happens.
#
#   ./demo/run.sh
#
# No API key, no VPS, no accounts. A mock provider stands in for Anthropic so
# every other component is the real thing: the real relay, the real client, the
# real directory authority, real signatures, real TLS.
#
# What it demonstrates, in order:
#   1. a relay starts and publishes a signed descriptor
#   2. the authority refuses it, because submission is default-deny
#   3. the operator admits the relay and it publishes successfully
#   4. the client fetches a signed consensus and picks a relay from it
#   5. a request reaches the provider through the relay, and streams
#   6. the relay could not read any of it

set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
WORK="$(mktemp -d)"
trap 'echo; echo "cleaning up..."; kill $(jobs -p) 2>/dev/null || true; wait 2>/dev/null || true; rm -rf "$WORK"' EXIT

# ── presentation helpers ──────────────────────────────────────────────────
if [ -t 1 ]; then
  B=$'\033[1m'; DIM=$'\033[2m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'
else
  B=""; DIM=""; G=""; Y=""; R=""; N=""
fi
step() { echo; echo "${B}── $* ${N}"; }
note() { echo "   ${DIM}$*${N}"; }
good() { echo "   ${G}✓${N} $*"; }
bad()  { echo "   ${R}✗${N} $*"; }
warn() { echo "   ${Y}!${N} $*"; }

wait_for_port() {
  local hostport=$1 tries=${2:-50}
  local host=${hostport%:*} port=${hostport##*:}
  for _ in $(seq "$tries"); do
    if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then exec 3>&- ; return 0; fi
    sleep 0.2
  done
  bad "nothing came up on $hostport"; return 1
}

wait_for_relay_consensus() {
  for _ in $(seq 50); do
    if curl -fsS http://127.0.0.1:18900/consensus | grep -q '^relay '; then
      return 0
    fi
    sleep 0.2
  done
  bad "the next directory epoch did not include the admitted relay"; return 1
}

PROMPT="the-secret-question-swordfish-42"
APIKEY="sk-ant-demo-key-do-not-use-me"

# ── build ─────────────────────────────────────────────────────────────────
step "Building"
go build -o "$WORK/ranger"  ./cmd/ranger
go build -o "$WORK/bearer"  ./cmd/bearer
go build -o "$WORK/council" ./cmd/council
go build -o "$WORK/mockprovider" ./demo/mockprovider
good "ranger, bearer, council built"

mkdir -p "$WORK/relay" "$WORK/authority" "$WORK/descriptors"

# ── 0. the provider ───────────────────────────────────────────────────────
step "0. Starting a stand-in provider"
"$WORK/mockprovider" \
  -addr 127.0.0.1:0 \
  -cert-out "$WORK/provider.crt" \
  -addr-out "$WORK/provider.addr" \
  -tap "$WORK/relay-carried.bin" \
  >"$WORK/provider.log" 2>&1 &
sleep 1
PROVIDER=$(cat "$WORK/provider.addr")
wait_for_port "$PROVIDER"
good "provider on $PROVIDER (stands in for api.anthropic.com)"
note "recording every byte it receives to relay-carried.bin, before TLS is removed"
note "those bytes are exactly what the relay forwarded"

# ── 1. the relay ──────────────────────────────────────────────────────────
step "1. Starting a relay"
SECRET=$("$WORK/ranger" -gen-secret)
OSANWE_RANGER_SECRET="$SECRET" "$WORK/ranger" \
  -dir "$WORK/relay" \
  -addr 127.0.0.1:18443 \
  -metrics 127.0.0.1:18464 \
  -allow "$PROVIDER" \
  >"$WORK/ranger.log" 2>&1 &
wait_for_port 127.0.0.1:18443
RELAY_PIN=$("$WORK/ranger" -dir "$WORK/relay" -pin)
RELAY_ID=$("$WORK/ranger" -dir "$WORK/relay" -identity)
good "relay on 127.0.0.1:18443"
note "TLS pin  $RELAY_PIN"
note "identity $RELAY_ID"
note "it will carry traffic only to $PROVIDER, and to nothing else"

# ── 2. the authority ──────────────────────────────────────────────────────
step "2. Starting a directory authority"
: > "$WORK/accept.txt"   # deliberately empty
"$WORK/council" \
  -identity "$WORK/authority/council.key" \
  -descriptors "$WORK/descriptors" \
  -accept "$WORK/accept.txt" \
  -addr 127.0.0.1:18900 \
  -rebuild 5s -lifetime 1h -unhealthy-after 2 \
  >"$WORK/council.log" 2>&1 &
wait_for_port 127.0.0.1:18900
AUTHORITY=$("$WORK/council" -identity "$WORK/authority/council.key" -key)
good "authority on 127.0.0.1:18900"
note "key $AUTHORITY"
note "its accept list is empty, so it will publish nobody"

# ── 3. publication is default-deny ────────────────────────────────────────
step "3. The relay tries to publish, and is refused"
if "$WORK/ranger" -dir "$WORK/relay" -allow "$PROVIDER" \
     -nickname northrelay -advertise 127.0.0.1:18443 \
     -publish http://127.0.0.1:18900/publish >"$WORK/publish1.log" 2>&1; then
  bad "publication succeeded, but the accept list is empty"
else
  good "refused, as it should be"
  note "$(grep -o 'not admitted.*accept list' "$WORK/publish1.log" | head -1 || true)"
fi
note "an open endpoint would let anyone register relays; a directory listing a"
note "thousand attacker-run relays has handed over almost every client"

# ── 4. admit it, and publish ──────────────────────────────────────────────
step "4. The operator admits the relay, which then publishes"
echo "$RELAY_ID  northrelay, demo operator" > "$WORK/accept.txt"
note "added the fingerprint to the accept list (no restart needed)"
"$WORK/ranger" -dir "$WORK/relay" -allow "$PROVIDER" \
  -nickname northrelay -advertise 127.0.0.1:18443 \
  -contact ops@example.com \
  -publish http://127.0.0.1:18900/publish 2>&1 | sed 's/^/   /'
good "accepted for the next consensus epoch"

step "4b. Replaying the same descriptor"
# Descriptor timestamps have one-second resolution, so wait before minting a
# fresh one; otherwise it is already the same age as the one just published.
sleep 1.1
"$WORK/ranger" -dir "$WORK/relay" -allow "$PROVIDER" -nickname northrelay \
  -advertise 127.0.0.1:18443 -descriptor "$WORK/replay.desc" >/dev/null 2>&1
curl -s -o /dev/null -w "   fresh submission: %{http_code}  (accepted)\n" \
  -X POST --data-binary @"$WORK/replay.desc" http://127.0.0.1:18900/publish
curl -s -o /dev/null -w "   same one again:   %{http_code}  (refused)\n" \
  -X POST --data-binary @"$WORK/replay.desc" http://127.0.0.1:18900/publish
good "409 on replay, so an old descriptor cannot roll the relay backwards"

# A directory authority freezes the first body it signs in each epoch. Wait for
# the next five-second boundary instead of asking it to equivocate immediately
# after a descriptor changes.
wait_for_relay_consensus

# ── 5. the consensus ──────────────────────────────────────────────────────
step "5. What the authority is publishing"
curl -s http://127.0.0.1:18900/consensus | head -3 | sed 's/^/   /'
note "..."
curl -s http://127.0.0.1:18900/consensus | grep '^signature' | cut -c1-72 | sed 's/^/   /'
good "$(curl -s http://127.0.0.1:18900/consensus | grep -c '^relay ') relay in the consensus, signed by the authority"
note "council probed it first: a relay that is down, or presenting a key that"
note "does not match its descriptor, is dropped rather than advertised"

# ── 6. the client ─────────────────────────────────────────────────────────
step "6. Starting the client, which discovers the relay itself"
OSANWE_SECRET="$SECRET" "$WORK/bearer" \
  -addr 127.0.0.1:18080 \
  -directory http://127.0.0.1:18900/consensus \
  -authority "$AUTHORITY" \
  -threshold 1 \
  -upstream "https://$PROVIDER" \
  -upstream-ca "$WORK/provider.crt" \
  >"$WORK/bearer.log" 2>&1 &
wait_for_port 127.0.0.1:18080
# Greps against a log are matched against wording, which drifts. Failing soft
# keeps a renamed message from killing the demo, which is exactly how this
# script broke once and stayed broken.
grep -o 'relays available from the directory.*' "$WORK/bearer.log" | head -1 | sed 's/^/   /' || true
good "client listening on http://127.0.0.1:18080"
note "no relay address was configured; it was chosen from the signed consensus"
note "and no relay is chosen until the first request, so none is named yet"

# ── 7. a request ──────────────────────────────────────────────────────────
step "7. Sending a request, the way any tool would"
echo "   ${DIM}export ANTHROPIC_BASE_URL=http://127.0.0.1:18080${N}"
echo
curl -s http://127.0.0.1:18080/v1/messages \
  -H "x-api-key: $APIKEY" \
  -H "content-type: application/json" \
  -d "{\"model\":\"demo\",\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}]}" \
  | sed 's/^/   /'
echo
good "the reply came back through the relay"
note "what the provider saw:"
grep -o 'provider saw:.*' "$WORK/provider.log" | tail -1 | sed 's/^/     /'
note "remote_addr is the relay, not this machine, and no forwarded-for header"

# ── 8. streaming ──────────────────────────────────────────────────────────
step "8. Streaming, arriving token by token"
curl -sN http://127.0.0.1:18080/v1/messages \
  -H "x-api-key: $APIKEY" \
  -H "content-type: application/json" \
  -d "{\"model\":\"demo\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}]}" \
  | grep --line-buffered '^data: .*text_delta' \
  | sed -u 's/.*"text":"\([^"]*\)".*/\1/' \
  | tr -d '\n'
echo; echo
good "words appeared one at a time, so nothing on the path buffered the stream"

# ── 9. the point of all of it ─────────────────────────────────────────────
step "9. What the relay could actually read"
CARRIED=$(wc -c < "$WORK/relay-carried.bin")
note "the relay forwarded $CARRIED bytes; searching them for the conversation"
echo
FAILED=0
for pair in "the prompt:$PROMPT" "the API key:$APIKEY" "the reply:swordfish"; do
  label=${pair%%:*}; needle=${pair#*:}
  if grep -qa -- "$needle" "$WORK/relay-carried.bin"; then
    bad "$label was readable in the relay's traffic"
    FAILED=1
  else
    good "$label was not recoverable"
  fi
done

# A control. Absence proves nothing unless the capture really is the traffic.
if head -c 3 "$WORK/relay-carried.bin" | od -An -tx1 | grep -q '16 03'; then
  good "capture begins with a TLS handshake record, so it really is the wire"
else
  warn "capture does not look like TLS; the absences above may be meaningless"
fi

step "Relay's own view"
curl -s http://127.0.0.1:18464/metrics | grep -E 'tunnels_total|bytes_to|auth_failed|policy_denied' | grep -v '^#' | sed 's/^/   /'
note "counters only, and no record of who talked to which provider"
note "one tunnel served both requests, because the client reuses the connection"

echo
if [ "$FAILED" -eq 0 ]; then
  echo "${B}${G}Done.${N} The relay carried the whole conversation and could not read any of it."
else
  echo "${B}${R}Done, with failures above.${N}"
  exit 1
fi
echo
echo "To point this at the real Anthropic API instead of the mock provider:"
echo "  - run ranger with  -allow api.anthropic.com"
echo "  - run bearer without -upstream and without -upstream-ca"
echo "  - export ANTHROPIC_BASE_URL=http://127.0.0.1:18080 and use your own key"
echo

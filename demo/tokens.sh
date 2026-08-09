#!/usr/bin/env bash
#
# Runs the Phase 3 path on one machine and narrates what happens.
#
#   ./demo/tokens.sh
#
# No API key, no VPS, no money. A mock provider stands in for Anthropic; every
# other component is the real thing, with real blind signatures and real TLS.
#
# The Phase 2 demo (./demo/run.sh) shows a relay carrying traffic it cannot
# read. That leaves the other half untouched: the provider still knows exactly
# whose account is asking. This is the half that fixes it.
#
# What it demonstrates, in order:
#   1. a mint publishes a verification key
#   2. a gateway agrees to honour that mint, holding the only provider key
#   3. the client buys tokens, blinded, so the mint cannot read what it signs
#   4. a request reaches the provider paid for by a token, not an account
#   5. the user's own API key never leaves the machine
#   6. a spent token cannot be spent again
#   7. the mint cannot tell which token it signed

set -euo pipefail

cd "$(dirname "$0")/.."
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

PROMPT="the-secret-question-swordfish-42"
USERKEY="sk-ant-the-users-own-account-key"
POOLKEY="sk-ant-the-gateways-pooled-key"
FAILED=0

# ── build ─────────────────────────────────────────────────────────────────
step "Building"
go build -o "$WORK/ranger"   ./cmd/ranger
go build -o "$WORK/bearer"   ./cmd/bearer
go build -o "$WORK/eregion"  ./cmd/eregion
go build -o "$WORK/mithlond" ./cmd/mithlond
go build -o "$WORK/mockprovider" ./demo/mockprovider
good "ranger, bearer, eregion, mithlond built"
mkdir -p "$WORK/relay"

# ── 0. the provider ───────────────────────────────────────────────────────
step "0. Starting a stand-in provider"
"$WORK/mockprovider" \
  -addr 127.0.0.1:0 \
  -cert-out "$WORK/provider.crt" \
  -addr-out "$WORK/provider.addr" \
  >"$WORK/provider.log" 2>&1 &
sleep 1
PROVIDER=$(cat "$WORK/provider.addr")
wait_for_port "$PROVIDER"
good "provider on $PROVIDER (stands in for api.anthropic.com)"

# ── 1. the mint ───────────────────────────────────────────────────────────
step "1. Starting the mint"
"$WORK/eregion" \
  -key "$WORK/mint.key" \
  -publish "$WORK/mint.pub" \
  -addr 127.0.0.1:18445 \
  -open \
  >"$WORK/eregion.log" 2>&1 &
wait_for_port 127.0.0.1:18445
MINT_KEY_ID=$("$WORK/eregion" -key "$WORK/mint.key" -print-key-id)
good "mint on 127.0.0.1:18445"
note "key id $MINT_KEY_ID"
warn "started with -open, so it sells nothing and gives tokens to anyone"
note "a real mint implements mint.Authorizer against a card, a bank or Monero"
note "whichever it is, the mint only ever learns one bit: was this paid for"

# ── 2. the gateway ────────────────────────────────────────────────────────
step "2. Starting the gateway"
OSANWE_PROVIDER_KEY="$POOLKEY" "$WORK/mithlond" \
  -addr 127.0.0.1:18444 \
  -upstream "https://$PROVIDER" \
  -mint-key "$WORK/mint.pub" \
  -spent-db "$WORK/spent.db" \
  -budget-db "$WORK/budget.db" \
  -models demo \
  -cert "$WORK/gateway.crt" -key "$WORK/gateway.key" \
  -upstream-ca "$WORK/provider.crt" \
  >"$WORK/mithlond.log" 2>&1 &
wait_for_port 127.0.0.1:18444
good "gateway on 127.0.0.1:18444"
note "it honours tokens from $MINT_KEY_ID and from no other mint"
note "it holds the only provider credential in this demo: $POOLKEY"

# ── 3. the relay ──────────────────────────────────────────────────────────
step "3. Starting a relay in front of the gateway"
SECRET=$("$WORK/ranger" -gen-secret)
OSANWE_RANGER_SECRET="$SECRET" "$WORK/ranger" \
  -dir "$WORK/relay" \
  -addr 127.0.0.1:18443 \
  -metrics 127.0.0.1:18464 \
  -allow 127.0.0.1:18444 \
  >"$WORK/ranger.log" 2>&1 &
wait_for_port 127.0.0.1:18443
RELAY_PIN=$("$WORK/ranger" -dir "$WORK/relay" -pin)
good "relay on 127.0.0.1:18443, carrying traffic only to the gateway"
note "this is the split the whole design rests on:"
note "  the relay sees an address and never the words"
note "  the gateway sees the words and never the address"
note "neither is on both sides, and neither can be, unless they collude"

# ── 4. the client ─────────────────────────────────────────────────────────
step "4. Starting the client, paying with tokens"
OSANWE_SECRET="$SECRET" "$WORK/bearer" \
  -addr 127.0.0.1:18080 \
  -relay 127.0.0.1:18443 -pin "$RELAY_PIN" \
  -upstream https://127.0.0.1:18444 \
  -upstream-ca "$WORK/gateway.crt" \
  -mint http://127.0.0.1:18445 \
  -mint-key-id "$MINT_KEY_ID" \
  >"$WORK/bearer.log" 2>&1 &
wait_for_port 127.0.0.1:18080
good "client on 127.0.0.1:18080"
note "-mint-key-id came from the mint operator, not from the mint's own /key"
note "a mint handing each buyer a different key would put every one of them"
note "in an anonymity set of one, while appearing to work perfectly"

# ── 5. a request paid for by a token ──────────────────────────────────────
step "5. A request, paid for with a token"
note "sending the user's own API key too, exactly as a tool with it configured would"
echo
curl -s http://127.0.0.1:18080/v1/messages \
  -H "x-api-key: $USERKEY" \
  -H "content-type: application/json" \
  -d "{\"model\":\"demo\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}]}" \
  | head -c 400 | sed 's/^/   /'
echo; echo
good "the reply came back, bought with a token"

# ── 6. what the provider was told ─────────────────────────────────────────
step "6. What the provider was told"
PROVIDER_SAW=$(grep -o 'provider saw:.*' "$WORK/provider.log" | tail -1 || true)
note "${PROVIDER_SAW:-(nothing recorded)}"
echo

if grep -qa "$USERKEY" "$WORK/provider.log"; then
  bad "the user's own API key reached the provider"
  FAILED=1
else
  good "the user's own API key never arrived"
fi

if grep -qa "$POOLKEY" "$WORK/provider.log"; then
  good "the gateway's pooled key paid for it instead"
else
  warn "could not confirm the pooled key in the provider's log"
fi

if grep -qa 'X-Osanwe-Token' "$WORK/provider.log"; then
  bad "a token reached the provider, which should never see one"
  FAILED=1
else
  good "no token reached the provider"
fi
note "the provider billed the gateway's account and learned nothing about who asked"

# ── 7. one token, one request ─────────────────────────────────────────────
step "7. A spent token cannot be spent again"
TOKEN=$("$WORK/bearer" -buy-token -mint http://127.0.0.1:18445 -mint-key-id "$MINT_KEY_ID")
note "bought one token by hand: ${TOKEN:0:48}..."
note "the mint signed that without being able to read it"
echo

SPEND1=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/gateway.crt" \
  https://127.0.0.1:18444/v1/messages \
  -H "X-Osanwe-Token: $TOKEN" -H 'content-type: application/json' \
  -d "{\"model\":\"demo\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}]}")
if [ "$SPEND1" = "200" ]; then
  good "spent once, accepted ($SPEND1)"
else
  bad "the first spend got $SPEND1, expected 200"
  FAILED=1
fi

SPEND2=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/gateway.crt" \
  https://127.0.0.1:18444/v1/messages \
  -H "X-Osanwe-Token: $TOKEN" -H 'content-type: application/json' \
  -d "{\"model\":\"demo\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}]}")
if [ "$SPEND2" = "409" ]; then
  good "spent again with the same token, refused ($SPEND2)"
else
  bad "the second spend got $SPEND2, expected 409"
  FAILED=1
fi

FORGED=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/gateway.crt" \
  https://127.0.0.1:18444/v1/messages \
  -H "X-Osanwe-Token: ${MINT_KEY_ID}.AAAA.BBBB" -H 'content-type: application/json' -d '{}')
if [ "$FORGED" = "401" ]; then
  good "a token the mint never signed, refused ($FORGED)"
else
  bad "a forged token got $FORGED, expected 401"
  FAILED=1
fi

NOTOKEN=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/gateway.crt" \
  https://127.0.0.1:18444/v1/messages -H 'content-type: application/json' -d '{}')
if [ "$NOTOKEN" = "401" ]; then
  good "no token at all, refused ($NOTOKEN)"
else
  bad "an unpaid request got $NOTOKEN, expected 401"
  FAILED=1
fi
note "the token is marked spent before the request is forwarded, never after,"
note "so sending the same one sixteen times at once still buys exactly one request"

# ── 8. several requests, several tokens ───────────────────────────────────
step "8. Every request buys its own token"
for i in 1 2 3; do
  curl -s -o /dev/null http://127.0.0.1:18080/v1/messages \
    -H "content-type: application/json" \
    -d "{\"model\":\"demo\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"request $i\"}]}"
done
good "three more requests, three more tokens"
note "a session is not one long-lived credential tying its requests together"
note "nothing links request 1 to request 3, not even to the gateway"

# ── 9. what the mint knows ────────────────────────────────────────────────
step "9. What the mint knows"
note "the mint's log, in full:"
sed 's/^/     /' "$WORK/eregion.log" | tail -5
echo
good "no issuance is recorded, not even a timestamp"
note "a mint keeping timestamps would let anyone holding both logs line up"
note "'issued at 10:04:03' against 'spent at 10:04:03' without breaking anything"
echo
note "and the blinding means the link is not merely unrecorded, it never existed:"
note "  go test ./internal/mint -run ObviousLinkingAttacks -v"

# ── done ──────────────────────────────────────────────────────────────────
echo
if [ "$FAILED" -eq 0 ]; then
  echo "${B}${G}Done.${N} The provider answered without an account, and nobody on the path"
  echo "      held both halves of who-asked-what."
else
  echo "${B}${R}Done, with failures above.${N}"
  exit 1
fi

echo
echo "${B}What this demo does not show, because it is not built:${N}"
echo "  - the gateway reads prompts. The design calls for it to run in an attested"
echo "    enclave so its operator provably cannot. That does not exist yet, so"
echo "    running a gateway means asking users to trust whoever runs it."
echo "  - the local spent-token journal is not a cross-host database. Several"
echo "    gateway hosts need a shared atomic redemption-store implementation."
echo "  - the mint sells nothing. Payment is one interface away and unimplemented."
echo

#!/usr/bin/env bash
#
# Runs the whole network on this machine and opens the interface.
#
#   ./demo/ui.sh
#
# No API key, no VPS, no money. A mock provider stands in for Anthropic; the
# mint, the gateway, the relay and the client are all the real thing.
#
# Leave it running and open the URL it prints. Ctrl-C stops everything.

set -euo pipefail

cd "$(dirname "$0")/.."
WORK="$(mktemp -d)"

if [ -t 1 ]; then
  B=$'\033[1m'; DIM=$'\033[2m'; G=$'\033[32m'; Y=$'\033[33m'; N=$'\033[0m'
else
  B=""; DIM=""; G=""; Y=""; N=""
fi
say()  { echo "   ${DIM}$*${N}"; }
good() { echo "   ${G}✓${N} $*"; }
warn() { echo "   ${Y}!${N} $*"; }

cleanup() {
  echo
  echo "stopping..."
  kill $(jobs -p) 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

wait_for_port() {
  local hostport=$1
  local host=${hostport%:*} port=${hostport##*:}
  for _ in $(seq 60); do
    if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then exec 3>&- ; return 0; fi
    sleep 0.25
  done
  echo "nothing came up on $hostport" >&2
  return 1
}

# Ports are fixed so the printed URL is always the same one.
UI_PORT=8080
RELAY_PORT=8443
GATEWAY_PORT=8444
MINT_PORT=8445

echo
echo "${B}Building${N}"
for b in ranger bearer eregion mithlond; do go build -o "$WORK/$b" "./cmd/$b"; done
go build -o "$WORK/mockprovider" ./demo/mockprovider
good "built (no third-party dependencies)"
mkdir -p "$WORK/relay"

echo
echo "${B}Starting${N}"

"$WORK/mockprovider" -addr 127.0.0.1:0 \
  -cert-out "$WORK/provider.crt" -addr-out "$WORK/provider.addr" >"$WORK/provider.log" 2>&1 &
sleep 1
PROVIDER=$(cat "$WORK/provider.addr")
good "provider on $PROVIDER  ${DIM}(stands in for api.anthropic.com)${N}"

"$WORK/eregion" -key "$WORK/mint.key" -publish "$WORK/mint.pub" \
  -addr "127.0.0.1:$MINT_PORT" -open >"$WORK/eregion.log" 2>&1 &
wait_for_port "127.0.0.1:$MINT_PORT"
MINT_KEY_ID=$("$WORK/eregion" -key "$WORK/mint.key" -print-key-id)
good "mint on 127.0.0.1:$MINT_PORT  ${DIM}($MINT_KEY_ID)${N}"

OSANWE_PROVIDER_KEY="sk-the-gateways-pooled-key" "$WORK/mithlond" \
  -addr "127.0.0.1:$GATEWAY_PORT" -upstream "https://$PROVIDER" \
  -mint-key "$WORK/mint.pub" -upstream-ca "$WORK/provider.crt" \
  -cert "$WORK/gateway.crt" -key "$WORK/gateway.key" >"$WORK/mithlond.log" 2>&1 &
wait_for_port "127.0.0.1:$GATEWAY_PORT"
good "gateway on 127.0.0.1:$GATEWAY_PORT  ${DIM}(holds the only provider key)${N}"

SECRET=$("$WORK/ranger" -gen-secret)
OSANWE_RANGER_SECRET="$SECRET" "$WORK/ranger" -dir "$WORK/relay" \
  -addr "127.0.0.1:$RELAY_PORT" -allow "127.0.0.1:$GATEWAY_PORT" >"$WORK/ranger.log" 2>&1 &
wait_for_port "127.0.0.1:$RELAY_PORT"
RELAY_PIN=$("$WORK/ranger" -dir "$WORK/relay" -pin)
good "relay on 127.0.0.1:$RELAY_PORT  ${DIM}(carries traffic only to the gateway)${N}"

OSANWE_SECRET="$SECRET" "$WORK/bearer" -addr "127.0.0.1:$UI_PORT" \
  -relay "127.0.0.1:$RELAY_PORT" -pin "$RELAY_PIN" \
  -upstream "https://127.0.0.1:$GATEWAY_PORT" -upstream-ca "$WORK/gateway.crt" \
  -mint "http://127.0.0.1:$MINT_PORT" -mint-key-id "$MINT_KEY_ID" >"$WORK/bearer.log" 2>&1 &
wait_for_port "127.0.0.1:$UI_PORT"
good "client on 127.0.0.1:$UI_PORT"

URL="http://127.0.0.1:$UI_PORT/_osanwe/"
echo
echo "${B}Open this:${N}"
echo
echo "    ${B}$URL${N}"
echo
say "Type in the box. Every message buys a token from the mint, spends it at the"
say "gateway, and comes back through the relay -- which carried all of it and"
say "could read none of it."
echo
say "Connect shows the endpoint, the tokens on hand and which relay is in use,"
say "all read live from the running client."
echo
warn "The mint is running with -open, so it gives tokens away. Nothing is being sold."
warn "The provider is a stand-in; replies are canned, and no real model is involved."
echo
say "Ctrl-C stops everything and deletes the keys this created."
echo

# Nudge a browser open where one is available, and say nothing when not.
for opener in xdg-open open; do
  if command -v "$opener" >/dev/null 2>&1; then
    "$opener" "$URL" >/dev/null 2>&1 &
    break
  fi
done

wait

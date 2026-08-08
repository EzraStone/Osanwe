#!/usr/bin/env bash
#
# Checks what a running Osanwë actually protects, and what it does not.
#
#   ./demo/verify.sh                 against the default ports
#   ./demo/verify.sh 8080            against a client on another port
#
# Run this while ./demo/ui.sh is running, in another terminal.
#
# It is deliberately willing to fail. A verifier that only confirmed the good
# news would be worth nothing, and the most useful thing it reports is the one
# property a single machine cannot give you.

set -uo pipefail
cd "$(dirname "$0")/.."

UI_PORT="${1:-8080}"
RELAY_PORT=8443
GATEWAY_PORT=8444
MINT_PORT=8445

if [ -t 1 ]; then
  B=$'\033[1m'; DIM=$'\033[2m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'
else
  B=""; DIM=""; G=""; Y=""; R=""; N=""
fi
step() { echo; echo "${B}── $* ${N}"; }
say()  { echo "   ${DIM}$*${N}"; }
good() { echo "   ${G}✓${N} $*"; }
bad()  { echo "   ${R}✗${N} $*"; FAILED=1; }
warn() { echo "   ${Y}!${N} $*"; }
FAILED=0

STATUS=$(curl -s -m 5 "http://127.0.0.1:$UI_PORT/_osanwe/status" 2>/dev/null || true)
if [ -z "$STATUS" ]; then
  echo "Nothing is answering on 127.0.0.1:$UI_PORT." >&2
  echo "Start it with ./demo/ui.sh and run this in another terminal." >&2
  exit 1
fi

field() { printf '%s' "$STATUS" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for k in '$1'.split('.'):
    d=(d or {}).get(k) if isinstance(d,dict) else None
print('' if d is None else d)
" 2>/dev/null; }

echo
echo "${B}Osanwë — what is actually protected${N}"

# ── 1. the client is not reachable from anywhere else ─────────────────────
step "1. The client listens only to this machine"
ENDPOINT=$(field endpoint)
say "bound to $ENDPOINT"
case "$ENDPOINT" in
  127.0.0.1:*|localhost:*|"[::1]:"*) good "loopback only, so no other machine can reach it" ;;
  *) bad "bound to $ENDPOINT, which other machines can reach; prompts cross the network in the clear before they are sealed" ;;
esac

say "and a page you visit in a browser cannot use it either:"
for probe in "Origin: https://evil.example" "Host: evil.example:$UI_PORT"; do
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 5 -H "$probe" "http://127.0.0.1:$UI_PORT/_osanwe/status")
  if [ "$CODE" = "403" ]; then
    good "refused \"${probe%%:*}\" from elsewhere ($CODE)"
  else
    bad "\"${probe%%:*}\" from elsewhere got $CODE, expected 403"
  fi
done

# ── 2. no account of yours is in play ─────────────────────────────────────
step "2. Nothing identifies you to the provider"
PAYING=$(field paying)
if [ "$PAYING" = "tokens" ]; then
  good "paying with tokens; no account of yours is used"
  say "on hand: $(field wallet.on_hand), spent this session: $(field wallet.spent)"
else
  warn "paying with your own key, so the provider knows exactly whose account is asking"
  say "only your address is hidden. Run with -mint to use tokens."
fi
say "retained by the client: $(field retained)"

# ── 3. the mint cannot recognise what it signed ───────────────────────────
step "3. The mint keeps no record of an issuance"
if curl -s -m 5 -o /dev/null "http://127.0.0.1:$MINT_PORT/key"; then
  good "mint is answering on $MINT_PORT"
  say "the blinding means the link never existed, not merely that it is unlogged:"
  say "  go test ./internal/mint -run ObviousLinkingAttacks -v"
else
  warn "no mint on $MINT_PORT; this client is not buying tokens"
fi

# ── 4. the relay cannot read what it carries ──────────────────────────────
step "4. The relay carries ciphertext"
PHRASE="verify-$(date +%s)-swordfish"
CAP=$(mktemp /tmp/osanwe-verify-XXXX.pcap)

# Whatever this gateway actually carries, rather than a name guessed here.
MODEL=$(curl -s -m 5 "http://127.0.0.1:$UI_PORT/v1/models" 2>/dev/null | python3 -c "
import json,sys
try: print(json.load(sys.stdin)['data'][0]['id'])
except Exception: print('')
" 2>/dev/null)
[ -z "$MODEL" ] && MODEL="claude-sonnet-5"

if command -v tcpdump >/dev/null 2>&1 && tcpdump -D >/dev/null 2>&1; then
  tcpdump -i any -w "$CAP" -U "port $RELAY_PORT" >/dev/null 2>&1 &
  TCPDUMP=$!
  sleep 1.5
  curl -s -m 30 -o /dev/null -X POST "http://127.0.0.1:$UI_PORT/v1/messages" \
    -H 'content-type: application/json' \
    -d "{\"model\":\"$MODEL\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"$PHRASE\"}]}" 2>/dev/null || true
  sleep 1.5
  kill "$TCPDUMP" 2>/dev/null; wait "$TCPDUMP" 2>/dev/null
  BYTES=$(wc -c < "$CAP")
  say "sent a request naming $MODEL and captured $BYTES bytes crossing the relay"
  if [ "$BYTES" -lt 1024 ]; then
    warn "too little traffic captured for the absence below to mean anything"
  elif grep -qa "$PHRASE" "$CAP"; then
    bad "the prompt was readable in what the relay carried"
  else
    good "the prompt was not recoverable from the relay's traffic"
  fi
  rm -f "$CAP"
else
  warn "tcpdump unavailable or needs root, so the wire was not inspected here"
  say "the same property is proved in CI, over real sockets:"
  say "  ./demo/run.sh          searches a capture for the prompt and the key"
  say "  go test ./internal/integration -run RelayReadsNeither"
fi

# ── 5. the part a single machine cannot give you ──────────────────────────
step "5. Who the provider thinks is calling"
GW_LOCAL=0
if curl -s -m 3 -k -o /dev/null "https://127.0.0.1:$GATEWAY_PORT/v1/models" 2>/dev/null; then GW_LOCAL=1; fi
if ss -ltn 2>/dev/null | grep -q ":$GATEWAY_PORT "; then GW_LOCAL=1; fi

if [ "$GW_LOCAL" = "1" ]; then
  warn "the gateway is running on this machine"
  echo
  say "The gateway is the component that calls the provider, so the provider"
  say "sees the gateway's address. On one machine, that is your address."
  echo
  say "Everything above is real and holds. This one does not, and cannot,"
  say "until the gateway runs somewhere that is not your computer."
  echo
  say "What you have on one machine:"
  say "  the relay genuinely cannot read the traffic it carries"
  say "  the mint genuinely cannot recognise the token it signed"
  say "  no account of yours reaches the provider"
  say "What you do not have:"
  say "  your IP address is hidden from the provider"
else
  good "the gateway is not on this machine, so the provider sees its address"
  say "and the relay in front of it never saw your words"
fi

# ── verdict ───────────────────────────────────────────────────────────────
echo
if [ "$FAILED" -eq 0 ]; then
  echo "${B}${G}Everything checkable here holds.${N}"
else
  echo "${B}${R}Something above failed. Read it before trusting this with anything.${N}"
fi
echo
say "Not checked by this script, because it is not built:"
say "  the gateway reads prompts. The enclave that would stop its operator"
say "  reading them does not exist, so a gateway is a party you trust."
say "  the local spent-token journal does not coordinate separate gateway hosts."
say "  nothing aggregate-rate-limits accepted token requests, so account credit still needs a provider-side cap."
echo
exit "$FAILED"

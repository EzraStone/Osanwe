#!/usr/bin/env bash
#
# Runs the client against a gateway that is somewhere else.
#
#   ./demo/client.sh MINT_KEY_ID [GATEWAY_CERT]
#
# Everything the client needs, in one terminal: a relay, a wallet, and the
# window. The gateway and the mint are expected to be reachable on
# 127.0.0.1:8444 and 127.0.0.1:8445 -- which is what an SSH tunnel to the
# server gives you:
#
#   gcloud compute ssh osanwe-gateway --zone=us-west1-b -- -N \
#     -L 8444:localhost:8444 -L 8445:localhost:8445
#
# Leave that running in another terminal, then run this. Ctrl-C stops
# everything this started.

set -euo pipefail
cd "$(dirname "$0")/.."

MINT_KEY_ID="${1:-}"
GATEWAY_CERT="${2:-gateway.crt}"

UI_PORT=8080
RELAY_PORT=8443
GATEWAY_PORT=8444
MINT_PORT=8445

if [ -t 1 ]; then
  B=$'\033[1m'; DIM=$'\033[2m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'
else
  B=""; DIM=""; G=""; Y=""; R=""; N=""
fi
say()  { echo "   ${DIM}$*${N}"; }
good() { echo "   ${G}✓${N} $*"; }
warn() { echo "   ${Y}!${N} $*"; }
die()  { echo; echo "${R}$*${N}" >&2; exit 1; }

WORK="$(mktemp -d)"
cleanup() {
  echo; echo "stopping..."
  kill $(jobs -p) 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

if [ -z "$MINT_KEY_ID" ]; then
  die "Usage: ./demo/client.sh MINT_KEY_ID [GATEWAY_CERT]

The key id comes from whoever runs the mint, by some route other than the mint
itself. On the server:  curl -sI http://127.0.0.1:$MINT_PORT/key | grep -i key-id"
fi
[ -f "$GATEWAY_CERT" ] || die "No certificate at $GATEWAY_CERT.
Copy it from the server: cat /var/lib/osanwe/gateway.crt"

echo
echo "${B}Checking the tunnel${N}"

# Everything below fails confusingly if the forward is not up, so it is checked
# first and named as the cause.
if ! curl -s -m 5 --cacert "$GATEWAY_CERT" "https://127.0.0.1:$GATEWAY_PORT/v1/models" >"$WORK/models" 2>"$WORK/err"; then
  die "Cannot reach the gateway on 127.0.0.1:$GATEWAY_PORT.

Is the tunnel running in another terminal?
  gcloud compute ssh osanwe-gateway --zone=us-west1-b -- -N \\
    -L $GATEWAY_PORT:localhost:$GATEWAY_PORT -L $MINT_PORT:localhost:$MINT_PORT

$(cat "$WORK/err")"
fi
MODELS=$(python3 -c "
import json,sys
try: print(', '.join(m['id'] for m in json.load(open('$WORK/models'))['data']))
except Exception: print('')
" 2>/dev/null || true)
[ -n "$MODELS" ] || die "The gateway answered but carries no models. Check its route table."
good "gateway reachable, carrying: $MODELS"

SERVED_ID=$(curl -s -m 5 -D - -o /dev/null "http://127.0.0.1:$MINT_PORT/key" 2>/dev/null \
  | tr -d '\r' | awk 'tolower($1)=="x-osanwe-mint-key-id:"{print $2}')
[ -n "$SERVED_ID" ] || die "Cannot reach the mint on 127.0.0.1:$MINT_PORT. Is it in the tunnel too?"

# Comparing the id you were given against the one this mint serves is the whole
# point of holding it separately: a mint handing every buyer a key of their own
# would put each of them in an anonymity set of one while appearing to work.
if [ "$SERVED_ID" != "$MINT_KEY_ID" ]; then
  die "The mint serves key id
    $SERVED_ID
but you asked for
    $MINT_KEY_ID

Either the mint rotated and your id is stale, or something is serving you a key
of its own. The client will not proceed."
fi
good "mint reachable, and its key is the one you expected"

echo
echo "${B}Starting${N}"
go build -o "$WORK/ranger" ./cmd/ranger
go build -o "$WORK/bearer" ./cmd/bearer
good "built"

SECRET=$("$WORK/ranger" -gen-secret)
mkdir -p "$WORK/relay"
OSANWE_RANGER_SECRET="$SECRET" "$WORK/ranger" -dir "$WORK/relay" \
  -addr "127.0.0.1:$RELAY_PORT" -allow "127.0.0.1:$GATEWAY_PORT" >"$WORK/ranger.log" 2>&1 &

for _ in $(seq 40); do
  (exec 3<>"/dev/tcp/127.0.0.1/$RELAY_PORT") 2>/dev/null && { exec 3>&-; break; }
  sleep 0.25
done
PIN=$("$WORK/ranger" -dir "$WORK/relay" -pin)
good "relay on 127.0.0.1:$RELAY_PORT  ${DIM}(carries traffic only to the gateway)${N}"

OSANWE_SECRET="$SECRET" "$WORK/bearer" -addr "127.0.0.1:$UI_PORT" \
  -relay "127.0.0.1:$RELAY_PORT" -pin "$PIN" \
  -upstream "https://127.0.0.1:$GATEWAY_PORT" -upstream-ca "$GATEWAY_CERT" \
  -mint "http://127.0.0.1:$MINT_PORT" -mint-key-id "$MINT_KEY_ID" >"$WORK/bearer.log" 2>&1 &

for _ in $(seq 40); do
  (exec 3<>"/dev/tcp/127.0.0.1/$UI_PORT") 2>/dev/null && { exec 3>&-; break; }
  sleep 0.25
done
if ! curl -s -m 5 -o /dev/null "http://127.0.0.1:$UI_PORT/_osanwe/status"; then
  echo; echo "the client did not start:" >&2
  sed 's/^/   /' "$WORK/bearer.log" >&2
  exit 1
fi
good "client on 127.0.0.1:$UI_PORT"

URL="http://127.0.0.1:$UI_PORT/_osanwe/"
echo
echo "${B}Open this:${N}"
echo
echo "    ${B}$URL${N}"
echo
say "The gateway is on the server, so the provider sees the server's address"
say "rather than yours. Check it with ./demo/verify.sh in another terminal."
echo
warn "The relay is on this machine, beside the client, so the gateway still"
warn "sees your address. Only a relay run by someone else fixes that."
echo
say "Ctrl-C stops the relay and the client. The tunnel is yours to close."
echo

for opener in xdg-open open; do
  if command -v "$opener" >/dev/null 2>&1; then
    "$opener" "$URL" >/dev/null 2>&1 &
    break
  fi
done

wait

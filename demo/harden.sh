#!/usr/bin/env bash
#
# Checks what a token can and cannot buy, against a running client.
#
#   ./demo/harden.sh                 against a client on 8080
#   ./demo/harden.sh 8080            against a client on another port
#
# Run this while ./demo/ui.sh is running, in another terminal.
#
# The gateway is the only thing standing between one cheap token and the
# operator's provider account, and the property that matters is not just which
# requests are refused -- it is that a refused request costs nothing. A policy
# check that ran after redemption would pass every status assertion below and
# still let anyone drain a wallet with malformed requests. So the wallet is
# counted before and after, and the arithmetic is the real assertion.

set -uo pipefail
cd "$(dirname "$0")/.."

UI_PORT="${1:-8080}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [ -t 1 ]; then
  B=$'\033[1m'; DIM=$'\033[2m'; G=$'\033[32m'; R=$'\033[31m'; N=$'\033[0m'
else
  B=""; DIM=""; G=""; R=""; N=""
fi
step() { echo; echo "${B}── $* ${N}"; }
say()  { echo "   ${DIM}$*${N}"; }
good() { echo "   ${G}✓${N} $*"; }
bad()  { echo "   ${R}✗${N} $*"; FAILED=1; }
FAILED=0

wallet() {
  curl -s -m 5 "http://127.0.0.1:$UI_PORT/_osanwe/status" 2>/dev/null | python3 -c "
import json,sys
try: print(json.load(sys.stdin)['wallet']['spent'])
except Exception: print('')
" 2>/dev/null
}

BEFORE=$(wallet)
if [ -z "$BEFORE" ]; then
  echo "Nothing is answering on 127.0.0.1:$UI_PORT." >&2
  echo "Start it with ./demo/ui.sh and run this in another terminal." >&2
  exit 1
fi

MODEL=$(curl -s -m 5 "http://127.0.0.1:$UI_PORT/v1/models" 2>/dev/null | python3 -c "
import json,sys
try: print(json.load(sys.stdin)['data'][0]['id'])
except Exception: print('')
" 2>/dev/null)
[ -n "$MODEL" ] || { echo "The gateway carries no models." >&2; exit 1; }

echo
echo "${B}Osanwë — what a token can buy${N}"
say "model: $MODEL, spent so far: $BEFORE"

# want METHOD PATH WANTED_STATUS DESCRIPTION BODY
want() {
  local method="$1" path="$2" wanted="$3" desc="$4" body="${5:-}"
  local code
  if [ -n "$body" ]; then
    code=$(curl -s -o "$WORK/out" -w '%{http_code}' -m 20 -X "$method" \
      "http://127.0.0.1:$UI_PORT$path" -H 'content-type: application/json' -d "$body")
  else
    code=$(curl -s -o "$WORK/out" -w '%{http_code}' -m 20 -X "$method" "http://127.0.0.1:$UI_PORT$path")
  fi
  if [ "$code" = "$wanted" ]; then
    good "$desc ${DIM}($code)${N}"
  else
    bad "$desc: got $code, wanted $wanted"
    sed 's/^/       /' "$WORK/out" | head -2
  fi
}

step "1. One ordinary request is served"
want POST /v1/messages 200 "a plain text message is answered" \
  "{\"model\":\"$MODEL\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"

step "2. The provider account is not reachable through a token"
# This is the one that mattered. Before the allowlist, any path with a valid
# token was proxied with the pooled credential attached, so one token bought
# file upload, batches and fine-tuning on the operator's account.
for path in /v1/files /v1/batches /v1/fine_tuning/jobs /v1/organizations/me; do
  want POST "$path" 404 "POST $path is not exposed" \
    "{\"model\":\"$MODEL\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"x\"}]}"
done
# 405 rather than 404: the path exists, the method does not, and the refusal
# carries Allow: POST. The client refuses this one itself, before the gateway.
want GET /v1/messages 405 "GET /v1/messages is not a way in"

step "3. Cost has a ceiling"
want POST /v1/messages 422 "an enormous max_tokens is refused" \
  "{\"model\":\"$MODEL\",\"max_tokens\":9999999,\"messages\":[{\"role\":\"user\",\"content\":\"x\"}]}"
want POST /v1/messages 400 "a request naming no model is refused" \
  '{"max_tokens":8,"messages":[{"role":"user","content":"x"}]}'
want POST /v1/messages 404 "a model this gateway does not carry is refused" \
  '{"model":"gpt-9-ultra","max_tokens":8,"messages":[{"role":"user","content":"x"}]}'

step "4. The request shape is exactly what was priced"
want POST /v1/messages 400 "a duplicate JSON name is refused" \
  "{\"model\":\"$MODEL\",\"model\":\"other\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"x\"}]}"
want POST /v1/messages 422 "an unpriced top-level field is refused" \
  "{\"model\":\"$MODEL\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"x\"}],\"tools\":[{\"name\":\"sh\"}]}"
want POST /v1/messages 422 "a remote image, whose real cost is unbounded, is refused" \
  "{\"model\":\"$MODEL\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"image\",\"source\":{\"url\":\"http://example/x.png\"}}]}]}"
want POST /v1/messages 400 "a body that is not JSON is refused" 'not json at all'

step "5. The catalog is free"
want GET /v1/models 200 "the catalog answers"
say "a client should not spend a token to find out what is on offer, or to"
say "discover a typo"

step "6. What all of that cost"
AFTER=$(wallet)
USED=$(( AFTER - BEFORE ))
say "tokens spent across every request above: $USED"
if [ "$USED" -eq 1 ]; then
  good "exactly one, for the one request that was served"
  say "every refusal happened before redemption, which is the property that"
  say "keeps a wallet from being drained by malformed requests"
else
  bad "$USED tokens spent, expected 1"
  say "a refusal is costing the user money, so the policy check is running"
  say "after redemption rather than before it"
fi

echo
if [ "$FAILED" -eq 0 ]; then
  echo "${B}${G}A token buys one inference request and nothing else.${N}"
else
  echo "${B}${R}Something above failed. A token currently buys more than it should.${N}"
fi
echo
say "Not checked here, because it is not built:"
say "  nothing rate limits the gateway. Every check above bounds what ONE"
say "  token buys; none of them bound how many tokens one person may present."
echo
exit "$FAILED"

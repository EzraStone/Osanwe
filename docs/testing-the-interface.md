# Testing the interface

Three ways to run it, in increasing order of how much of it is real. Start at
the top; the first needs nothing but a Go toolchain.

## 1. Everything local, nothing real

```bash
./demo/ui.sh
```

Builds the four daemons, starts a mint, a gateway, a relay and the client, and
prints a URL. No API key, no server, no money. Ctrl-C stops everything and
deletes the keys it created.

A stand-in provider answers with canned text, so every *other* component is the
real one: real blind signatures, real TLS, real pinning, real double-spend
checks. What you are testing is the whole path except the model.

The mint runs with `-open`, meaning it gives tokens away. Nothing is sold.

## 2. Against a real provider

Same as above, but point the gateway at the real thing. In `demo/ui.sh`, change
the `mithlond` line to:

```bash
OSANWE_PROVIDER_KEY="sk-ant-..." "$WORK/mithlond" \
  -addr "127.0.0.1:$GATEWAY_PORT" \
  -upstream https://api.anthropic.com \
  -mint-key "$WORK/mint.pub" \
  -spent-db "$WORK/spent.db" \
  -budget-db "$WORK/budget.db" \
  -models YOUR_EXACT_MODEL_ID \
  -cert "$WORK/gateway.crt" -key "$WORK/gateway.key" &
```

Drop `-upstream-ca`, since Anthropic's certificate verifies against the system
roots, and change the relay's `-allow` to `api.anthropic.com:443`.

**Before doing this, know what you are agreeing to.** The gateway now holds a
real key that pays for every accepted token request. Its spent-token journal
survives restarts, but restoring an old copy would revive newer redemptions,
and a local file is not shared state for gateways on different machines. The paid request
surface is intentionally text-only; rich message blocks such as remote images,
files, tools, and cache-control are rejected before a token is spent.

## 3. Your own key, no mint

```bash
ranger -allow api.anthropic.com &
bearer -relay 127.0.0.1:8443 -pin sha256/...
```

Then open the printed URL. **Chat will be unavailable, on purpose.** There is no
key in the client to send with — your key lives in your tools, which is the
whole bring-your-own-key promise — so the window says so and points you at the
endpoint instead of inventing somewhere to paste a key.

Connect works normally, and is the useful half here: it shows the endpoint, the
relay in use, and whether its key matched what was published.

---

## What to actually poke at

Roughly two minutes, in the browser:

| Do this | Expect |
|---|---|
| Type a question and send | Words arrive one at a time, not in a lump at the end |
| Open **Connect** | Tokens on hand drops by one per message sent |
| Click the seal | Plain-language account of what happened, then the raw facts |
| Kill the relay, then send | The seal turns red and says the relay is not answering |
| Restart the relay, send again | It recovers on its own, with no restart of the client |
| Switch your OS between light and dark | Both are designed; neither is an inversion of the other |

The fourth row is the one worth doing deliberately. A privacy tool that fails
silently is worse than one that fails loudly, and the seal exists to make a
failure impossible to miss.

## What to check from a terminal

These are the properties you cannot see in a browser, because a browser will not
let you attempt them. Run them against a client on port 8080.

```bash
# A website you visited trying to read your state. Want 403.
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Origin: https://evil.example' http://127.0.0.1:8080/_osanwe/status

# A website trying to send a prompt as you. Want 403.
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  -H 'Origin: https://evil.example' -H 'content-type: application/json' \
  -d '{}' http://127.0.0.1:8080/v1/messages

# An attacker's own domain pointed at 127.0.0.1, which defeats every
# origin check on its own. Want 403.
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Host: evil.example:8080' http://127.0.0.1:8080/_osanwe/status

# A browser asking for a favicon. Want 404, and the wallet must not move:
# forwarding this would buy a token, spend it and collect a 404.
curl -s http://127.0.0.1:8080/_osanwe/status | grep on_hand
curl -s -o /dev/null -H 'Sec-Fetch-Dest: image' http://127.0.0.1:8080/favicon.ico
curl -s http://127.0.0.1:8080/_osanwe/status | grep on_hand

# An ordinary SDK, which sends no Origin at all. Want 200 -- requiring an
# Origin would lock out every real client and buy nothing.
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/_osanwe/status

# No cross-origin grant is ever issued. Want no output.
curl -sI http://127.0.0.1:8080/_osanwe/status | grep -i access-control
```

## What the automated tests already cover

```bash
go test ./...              # everything, including the checks above
./demo/run.sh              # the relay carries what it cannot read
./demo/tokens.sh           # buy a token, spend it, fail to spend it twice
./demo/harden.sh           # a token buys one request and nothing else
```

All three run in CI. They are the only thing that exercises the daemons as a
person runs them — real processes, real sockets — and the unit tests have
stayed green through several bugs that only those caught.

`harden.sh` needs a running client, so start `./demo/ui.sh` first. It drives
the endpoints a token must not reach and the request shapes it must not buy,
then checks the wallet: the whole run must cost exactly one token. That last
number is the point. Status codes alone cannot tell a refusal that costs
nothing from a refusal that happens after the token is taken, and only the
first is worth anything — the second still lets a malformed request drain a
wallet.

## What is deliberately not testable yet

- **The gateway reads prompts.** The design calls for it to run in an attested
  enclave so its operator provably cannot. That is not built, so running a
  gateway means asking users to trust whoever runs it.
- **The mint sells nothing.** Payment is one interface away and unimplemented.
- **Cross-host redemption storage is not shipped.** The local journal survives
  restarts and coordinates processes on one host. Several gateway hosts need a
  shared `mint.RedemptionStore` with an atomic claim operation.
- **Production route operations.** Exact model-to-provider routing is built and
  covered by tests, but operators still have to maintain the route table and
  provider credentials themselves; there is no automated marketplace or
  provider discovery service.

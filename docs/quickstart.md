# Quickstart

The smallest bring-your-own-key deployment uses two binaries: `ranger` runs on
a VPS somewhere else, and `bearer` runs on your machine.

**What this is designed to get you.** When `bearer` reaches the provider through
a correctly pinned remote relay, the provider connection carries the relay's
egress address rather than directly carrying your originating address, and the
relay receives provider TLS ciphertext rather than prompt text. **What it does
not get you:** anonymity. The provider still knows which account is asking and
sees the prompt; the relay sees your source address, timing, and volume; content,
timing, or collusion can still identify you. The account-unlinked token path is
pre-launch software; exercise it with `./demo/tokens.sh` locally, then read the
blockers and closed-network instructions in the [deployment guide](deploying.md).
[ADR 0001](decisions/0001-byok-first.md) records why BYOK was implemented first.

## Build

```bash
make build        # produces all six client and operator binaries
# Or build only the runnable BYOK pair:
make ranger bearer
```

Go 1.26 or newer. The blind-signature implementation uses Cloudflare CIRCL;
`go build` downloads the version pinned in `go.mod`.

## 1. Run a relay

On a VPS in a different region from you — that geographic separation is the
point, since a relay next door hides very little.

```bash
# A secret clients will present. Keep it; you have to give it to users.
export OSANWE_RANGER_SECRET=$(ranger -gen-secret)
echo "$OSANWE_RANGER_SECRET"

ranger -allow api.anthropic.com
```

On first start it generates a TLS identity, saves `ranger.crt` and `ranger.key`
beside itself, and prints a **pin**:

```
  pin: sha256/1hDnmVWIMaliQYmJhxf1j1Ycg6wGAu3c9Go6FBnGp5E=

  Give clients the address and this pin:
    bearer -relay <this-host>:8443 -pin sha256/1hDnmVWIMaliQYmJhxf1j1Ycg6wGAu3c9Go6FBnGp5E=
```

Keep `ranger.key`. If you lose it the relay generates a new identity, presents a
new pin, and every client stops trusting it until you redistribute the pin.

Destinations are **default-deny**. `-allow` is repeatable and comma-separated;
a bare host means port 443. A relay that forwarded anywhere would be an open
proxy, found by scanners within hours and turned into somebody's abuse relay,
which is why there is no "allow all".

## 2. Run the client

On your own machine:

```bash
export OSANWE_SECRET='<the relay secret>'

bearer -relay relay.example.com:8443 \
       -pin sha256/1hDnmVWIMaliQYmJhxf1j1Ycg6wGAu3c9Go6FBnGp5E=
```

Then point your tooling at it:

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
```

Compatible SDKs and tools can use that endpoint. Your tool sends its API key to
the loopback client over local HTTP; `bearer` necessarily handles it in
plaintext, then forwards it inside the provider TLS connection carried through
the relay. Osanwë does not intentionally persist or log the key. The relay sees
encrypted traffic rather than the credential or request content.

## Verifying it rather than trusting it

The narrow property to check is that a correctly configured relay receives no
plaintext prompt fragment:

```bash
# On the relay host, while a request is in flight:
sudo tcpdump -i any -w relay.pcap 'port 8443'
strings relay.pcap | grep -i 'your distinctive prompt text'   # finds nothing
```

The same property is asserted in CI. `internal/integration` captures every byte
crossing the relay and fails if a prompt, an API key or a response is
recoverable from it — with a control that fails the test if the capture is not
actually of encrypted traffic, so it cannot pass vacuously.

## Why two layers of TLS

```
your tool  ──plain──▶  bearer  ──TLS #1──▶  ranger  ──TLS #2──▶  provider
           (loopback,               (hides which               (hides the prompt
            never leaves             provider you use           from the relay)
            your machine)            from your ISP)
```

TLS #2 runs end to end from `bearer` to the provider, so the relay carries
ciphertext it has no key for. TLS #1 wraps that, because a `CONNECT` request
names its destination in the clear — without it, anyone watching your uplink
would read `CONNECT api.anthropic.com:443` and learn exactly which provider you
use.

`bearer` binds loopback and refuses to bind anything else unless you insist. The
hop from your tool to `bearer` is plaintext and is safe only because it never
leaves your machine.

## Flags worth knowing

| Flag | Where | Notes |
|---|---|---|
| `-allow HOST[:PORT]` | ranger | Required, repeatable. Default-deny |
| `-gen-secret` | ranger | Print a fresh secret and exit |
| `-pin` | ranger | Print this relay's pin and exit |
| `-log-destinations` | ranger | **Off by default.** See below |
| `-metrics ADDR` | ranger | Loopback by default; empty disables |
| `-upstream URL` | bearer | Another provider in BYOK mode; required and must name the gateway in token (`-mint`) mode |
| `-allow-exposed` | bearer | Binds a routable address. Puts prompts on the network in plaintext |

### On `-log-destinations`

Off by default, and that is a security decision. A relay recording *this address,
at this time, talked to this provider* is building exactly the correlation log
this network exists to prevent, and a seized or subpoenaed relay would hand it
over. Turn it on to debug an allowlist, then turn it off. The relay prints a
warning while it is on.

The metrics endpoint is cumulative counters only, with no per-connection detail,
so scraping it cannot reconstruct who talked to whom.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `relay ... rejected the credential (407)` | `OSANWE_SECRET` does not match the relay's |
| `relay ... will not carry traffic to X (403)` | The operator must add `-allow X` |
| `relay key mismatch` | The relay's identity changed, or something is impersonating it. Do not "fix" it by copying the new pin without asking the operator why it changed |
| `refusing to bind ...` | `bearer` only binds loopback unless told otherwise |
| `502` with `osanwe_tunnel_error` | The relay is unreachable. Requests fail closed rather than silently going direct |

## What is not built

This two-process quickstart deliberately stops at BYOK. The repository also
contains `checkout` (the fixed-price storefront), `eregion` (the blind-token
mint), `mithlond` (the token gateway), and `council` (the threshold directory
workflow). The gateway now has durable single-node aggregate limits, including
an optional conservative USD reservation when model prices are configured, and
the checkout and mint can create and consume self-hosted BTCPay invoices. Several
production pieces remain: the gateway is not isolated in an attested enclave,
neither state store spans several hosts, and the payment boundary has not had
an independent security review. See the
[deployment guide](deploying.md) and [payment guide](payments.md) for those boundaries.

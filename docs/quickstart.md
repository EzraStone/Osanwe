# Quickstart

The smallest bring-your-own-key deployment uses two binaries: `ranger` runs on
a VPS somewhere else, and `bearer` runs on your machine.

**What this gets you.** The provider stops learning your IP address and the
location it implies, and no relay operator can read your prompts. **What it does
not get you:** the provider still knows which account is asking, because you are
using your own API key. The account-unlinked token path is pre-launch software;
exercise it with `./demo/tokens.sh` locally, then read the blockers and closed-network
instructions in the [deployment guide](deploying.md).
[ADR 0001](decisions/0001-byok-first.md) records why BYOK shipped first.

## Build

```bash
make build        # produces all five client and operator binaries
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

That is the whole integration. Your SDK, editor plugin, scripts and agent
framework keep working — the API key still travels in the request, inside TLS,
straight to the provider. `bearer` never sees it in the clear and never stores
it.

## Verifying it rather than trusting it

The claim is that the relay cannot read your prompts. Check it:

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
contains `eregion` (the blind-token mint), `mithlond` (the token gateway), and
`council` (the threshold directory workflow), but several production pieces
remain: the mint has no real payment authorizer, the gateway is not yet isolated
in an attested enclave, aggregate rate/cost limiting is not implemented, and
the shipped durable redemption journal coordinates only processes on one host.
See [the deployment guide](deploying.md) for those boundaries.

# Standing up the first network

Getting from "the demos pass" to "somebody else can use this". Three pieces, in
the order they have to happen.

Read [`who-runs-what.md`](who-runs-what.md) first if you have not. Nothing here
is something a *user* does.

---

## Before anything: understand the spending boundary

`mithlond` now requires a durable fixed-window aggregate budget. It reserves
both one request and the caller's requested `max_tokens` before spending the
token or contacting the provider. The conservative defaults allow 100 requests
and reserve at most 100,000 output tokens per hour. A restart does not reset the
window, and concurrent requests cannot race past either ceiling.

This is a hard request/token ceiling, **not a dollar-denominated billing
oracle**. Input-token cost and provider-specific model prices are not known
exactly before dispatch. Two consequences for everything below:

- **Do not put an unlimited-spend provider account behind a public gateway.**
  Pair the local budget with the provider's own account budget or a prepaid
  balance small enough to lose.
- **Firewall the gateway to your relays.** This is not a workaround; it is the
  correct topology. A client reaches the gateway *through* a relay and never
  directly, so the gateway has no reason to accept a connection from anywhere
  else. It is the strongest control available today and it costs nothing.

The pooled credential is no longer attached to arbitrary provider paths. A
gateway answers exact, query-free `GET /v1/models` locally for free and pays only for exact
`POST /v1/messages` requests. The model must be in `-models` or the route
table, `max_tokens` must be present and no higher than
`-max-output-tokens`, the JSON body is capped by `-max-request-bytes`, and
unsupported top-level capabilities are rejected before redemption. The
defaults are 4,096 output tokens and a 1 MiB request body. These per-request
limits compose with `-budget-requests` and `-budget-output-tokens`; none of them
replaces the provider account's own dollar ceiling.

The accepted top-level Messages fields are `model`, `max_tokens`, `messages`,
`system`, `stream`, `temperature`, `top_p`, and `stop_sequences`. Their
provider-neutral types and sampling ranges are checked before spending. For
now, each message must contain only a `user` or `assistant` role and text
content as a JSON string; `system` must also be a string. Image, document,
file, tool, cache-control, and other rich content blocks are rejected without
spending. A short remote-media URL can make a provider fetch and tokenize far
more than the JSON byte limit, so rich blocks cannot safely be admitted until
their sources and prices are validated explicitly. Duplicate JSON object names
are rejected recursively, not just at the top level.

Bearer and gateway account for the token explicitly with
`X-Osanwe-Token-Outcome`. A policy refusal returns the token to the local
wallet, and so does a failure to establish a connection at all — a refused
dial or a DNS failure has no reading under which the provider did work, and
charging for the operator's outage would be charging for nothing. Once
dispatch may have begun, the outcome is `spent` even if the provider returns
5xx or the connection resets: HTTP cannot prove that the provider did not
already process and bill the request, so automatic refunds there would create
free retries. If a refund cannot be durably recorded the outcome is `spent`
rather than a token the gateway would refuse later. Bearer trusts this header only over its
TLS-authenticated configured gateway connection; provider-supplied values are
removed by the gateway. This is CA/hostname authentication, not a separate
SPKI-pin feature.

Both proxy hops use a positive request-header allowlist. In token mode,
`Accept` and `Content-Type` are rebuilt as `application/json`, and the gateway
sets its own provider API version; caller-supplied parameters or duplicate
values do not survive as identifiers. Bearer additionally allows the three
supported credential header shapes in BYOK mode, and the gateway adds its
pooled credential internally. Caller User-Agent, SDK/runtime headers, tracing
and request IDs, cookies, browser hints, forwarding metadata, and arbitrary
custom headers are removed. The Go HTTP stack's own default User-Agent is
suppressed as well. HTTP trailers are rejected at the gateway and stripped at
both proxy hops, and alternate URL spellings are rebuilt canonically before a
token or provider credential is attached.

Credential-shaped response headers and headers that exactly echo the pooled
credential are removed. The provider necessarily receives and therefore knows
its own credential; no streaming proxy can stop a malicious provider from
encoding that secret into arbitrary model output. The fixed endpoint/model
policy removes caller-selected debug endpoints, but the configured provider
remains part of the credential trust boundary.

---

## 1. One gateway, on a small server

### Choose the machine

Anything with 1 GB of memory. A GCP `e2-micro` in `us-west1`, `us-central1` or
`us-east1` is permanently free and enough. So is a $4/month VPS from any of the
usual places.

**Do not put it where your relay is**, later. For now it is the same person
either way; the point is to avoid building a habit you will have to unpick.

### Creating the free e2-micro, in the console

Four settings decide whether this is free or billed. Get them wrong and the
machine works exactly the same, which is why it is worth checking each one.

| Setting | Value | What goes wrong otherwise |
|---|---|---|
| Region | `us-west1`, `us-central1` or `us-east1` | Any other region bills the instance |
| Machine type | E2 series, `e2-micro` | `e2-small` and up are billed |
| Boot disk type | **Standard persistent disk** | Balanced and SSD are billed |
| Boot disk size | 30 GB or less | Above 30 GB is billed |

Console → Compute Engine → VM instances → **Create instance**.

1. **Name** it something you will recognise: `osanwe-gateway`.
2. **Region**: one of the three above. Any zone within it.
3. **Machine configuration**: series **E2**, preset **e2-micro** (2 vCPU, 1 GB).
   The picker defaults to a larger shape, so change it deliberately.
4. **Boot disk** → Change:
   - Operating system: **Ubuntu**
   - Version: **Ubuntu 24.04 LTS**, `x86/64, amd64` — not the *Minimal* build,
     and not Arm64. `e2-micro` is an Intel/AMD shape and will not boot an Arm
     image.
   - Boot disk type: **Standard persistent disk**
   - Size: **20 GB** is plenty
5. **Networking** → leave the firewall boxes unticked. "Allow HTTP traffic"
   opens port 80 to the world, which this does not need.
6. **Create**.

The instance list will show an external IP once it starts.

### Give it an address that will not move

An ephemeral IP changes whenever the instance stops. Clients pin a relay's
address and a gateway's name, so a moving address means reissuing configuration
to everyone holding it.

Console → VPC network → **IP addresses** → find the row for this instance →
**Reserve**. A static address is free while attached to a running instance and
billed when left unattached, so release it if you delete the machine.

### Do not open anything yet

A gateway accepts connections from relays. Until a relay exists there is no
address worth allowing, and an open port on a machine with a provider account
behind it and no rate limiting is the one mistake here that costs money.

For the first run, tunnel instead. This needs no firewall rule and exposes
nothing:

```bash
gcloud compute ssh osanwe-gateway --zone=us-west1-b -- -N -L 8444:localhost:8444
```

Leave that running. The gateway now answers on `127.0.0.1:8444` of your own
machine, over SSH, with port 8444 on the server still closed to the world.

Point a local relay at it and run the client as usual:

```bash
ranger -dir ./relay-data -addr 127.0.0.1:8443 -allow 127.0.0.1:8444
bearer -relay 127.0.0.1:8443 -pin sha256/... \
       -upstream https://127.0.0.1:8444 -upstream-ca gateway.crt \
       -mint http://127.0.0.1:8445 -mint-key-id mint-...
```

`-upstream` is mandatory whenever `-mint` enables token mode. Bearer will not
guess a gateway or fall back to a real provider: a mistaken default would send
the purchased bearer token to the wrong service and make its outcome
unknowable.

This is worth doing before anything else, because it is the first arrangement
that achieves something the laptop-only setup could not: **the gateway process
runs on the server, so the provider sees the server's address rather than your
home one.** `demo/verify.sh` will stop warning about step 5.

What it still does not give you: the relay is yours and sits beside the client,
so the gateway sees your home address. Fixing that needs a relay run by someone
else, which is section 3.

### Open only what is needed, once there is something to open

The default network allows SSH already. Add one narrow rule for the gateway,
scoped by tag so it applies to this machine and nothing else.

Console → VPC network → Firewall → **Create firewall rule**:

- Name: `osanwe-gateway`
- Targets: **Specified target tags**, tag `osanwe-gateway`
- Source IPv4 ranges: **your relay's address**, as `x.x.x.x/32` — not `0.0.0.0/0`
- Protocols and ports: TCP **8444**

Then add that tag to the instance: VM instances → your instance → Edit →
Network tags → `osanwe-gateway` → Save.

The source is the relay's address, and nothing else. If you want direct access
from your own machine before a relay exists, use the SSH tunnel above rather
than opening a port to your home address: home addresses usually change, and a
rule pointing at whoever holds that address next is worse than no rule.

`0.0.0.0/0` on a gateway with a provider account behind it and no rate limiting
is an open tab.

### The same thing from a terminal

```bash
gcloud compute instances create osanwe-gateway \
  --zone=us-central1-a \
  --machine-type=e2-micro \
  --image-family=ubuntu-2404-lts-amd64 \
  --image-project=ubuntu-os-cloud \
  --boot-disk-type=pd-standard \
  --boot-disk-size=20GB \
  --tags=osanwe-gateway

gcloud compute firewall-rules create osanwe-gateway \
  --allow=tcp:8444 --target-tags=osanwe-gateway \
  --source-ranges=RELAY_IP/32
```

### Getting in

Console → VM instances → **SSH** beside the instance. That opens a browser
terminal and needs no key setup.

### What the free tier does not cover

One `e2-micro` per month, 30 GB of standard disk, and **1 GB of outbound
traffic to the internet per month** from North America. Prompts and answers are
text, so a gateway serving a handful of people stays well inside that; a busy
one will not. Watch it in Billing → Reports before it surprises you.

### Decide about a name

The client verifies the gateway's TLS certificate. Two ways to satisfy that:

| | What it costs | What a user has to do |
|---|---|---|
| **A domain and a real certificate** | ~$10/year | nothing |
| A self-signed certificate | nothing | hold a copy of your CA file |

Take the domain. The second option means shipping a certificate alongside every
client, and re-shipping it when the certificate rotates. Ten dollars buys that
problem away.

### Install

```bash
sudo apt update && sudo apt install -y git curl
curl -fsSL https://go.dev/dl/go1.26.5.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
export PATH=$PATH:/usr/local/go/bin

sudo useradd --system --home /var/lib/osanwe --create-home osanwe
git clone https://github.com/EzraStone/Osanwe /tmp/osanwe && cd /tmp/osanwe
go build -o /usr/local/bin/mithlond ./cmd/mithlond
go build -o /usr/local/bin/eregion  ./cmd/eregion
```

Running as its own user matters more than it looks: the gateway holds the
provider credential for everybody, and a service account with nothing else on
it is the difference between one compromised process and one compromised
machine.

### Get a certificate

```bash
sudo apt install -y certbot
sudo certbot certonly --standalone -d gateway.example.com
sudo install -o osanwe -g osanwe \
  /etc/letsencrypt/live/gateway.example.com/fullchain.pem /var/lib/osanwe/gateway.crt
sudo install -o osanwe -g osanwe \
  /etc/letsencrypt/live/gateway.example.com/privkey.pem   /var/lib/osanwe/gateway.key
```

Certificates expire every 90 days. Add a renewal hook that copies them again
and restarts the gateway, or you will find out the hard way.

### Write the route table

```bash
sudo -u osanwe tee /var/lib/osanwe/routes.conf <<'EOF'
llama-3.1-8b-instant     openai  https://api.groq.com/openai  GROQ_API_KEY
llama-3.3-70b-versatile  openai  https://api.groq.com/openai  GROQ_API_KEY
EOF
```

The credential is not in this file — the last field names an environment
variable. That is deliberate, and it is what makes this file safe to commit.

### Put the credential somewhere only the service reads

```bash
sudo tee /etc/osanwe.env <<'EOF'
GROQ_API_KEY=gsk_...
EOF
sudo chmod 600 /etc/osanwe.env
sudo chown osanwe:osanwe /etc/osanwe.env
```

Not in the unit file, which is world-readable. Not on a command line, which is
visible in the process table to every user on the machine.

### Run it

```ini
# /etc/systemd/system/mithlond.service
[Unit]
Description=Osanwe gateway
After=network-online.target

[Service]
User=osanwe
WorkingDirectory=/var/lib/osanwe
EnvironmentFile=/etc/osanwe.env
ExecStart=/usr/local/bin/mithlond \
  -addr 0.0.0.0:8444 \
  -routes /var/lib/osanwe/routes.conf \
  -mint-key /var/lib/osanwe/mint.pub \
  -spent-db /var/lib/osanwe/spent.db \
  -budget-db /var/lib/osanwe/budget.db \
  -budget-window 1h \
  -budget-requests 100 \
  -budget-output-tokens 100000 \
  -cert /var/lib/osanwe/gateway.crt \
  -key /var/lib/osanwe/gateway.key
Restart=always
RestartSec=5

# The gateway needs a network and nothing else on this machine.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/osanwe

[Install]
WantedBy=multi-user.target
```

`-spent-db` is the gateway's double-spend boundary. A redemption is not
accepted until its journal entry has reached disk, so ordinary restarts do not
revive tokens. Keep it on a local filesystem, owned by the service account,
and back it up only as part of the gateway's live state. Restoring an older
copy revives every token redeemed after that copy was made.

Several gateway processes on the same host may point at the same path when
they run as the same OS user; local file locking makes each claim atomic. Treat
the journal and its companion lock as one live database: do not unlink,
replace, restore, rotate, or copy either path underneath running processes.
Stop every process first. Do not put them on NFS or another network filesystem
and assume its locking and `fsync` semantics are equivalent. Gateways on
different hosts need a shared implementation of `mint.RedemptionStore` whose
`Spend` is an atomic create-if-absent operation and whose `Refund` is durably
committed before it returns. That backend is not shipped; until it is, run one
gateway host per mint key.

`-budget-db` is a separate ACID database for the current aggregate window. Keep
it on the same kind of local, service-owned storage. Capacity is reserved using
the request's maximum possible output, then released only when the gateway can
prove no connection to the provider was made. A crash between reservation and
dispatch can therefore under-use the rest of that window, but cannot overspend
it. The database is intentionally single-process and single-host; a future
multi-host gateway needs a shared implementation of `gateway.Budget`.

The companion-lock format is new. When upgrading a gateway that previously
locked `spent.db` itself, stop **every** old process before starting this
version; old and new binaries do not coordinate on the same lock inode. There
is not yet a safe online migration or rebind tool.

The local format is append-only and there is no online compactor yet. Budget
disk for one small record per accepted request and alert on free space: a full
or corrupt journal makes the gateway return 503 before forwarding, by design.
Do not truncate or hand-edit it to recover space; removing a valid tail is
indistinguishable from restoring an old snapshot and revives those tokens.

### Firewall it to your relays, not to the world

```bash
# GCP: default-deny inbound already, so add one narrow rule.
gcloud compute firewall-rules create osanwe-gateway \
  --allow=tcp:8444 --source-ranges=RELAY_IP/32 --target-tags=osanwe-gateway

# Anywhere else:
sudo ufw default deny incoming
sudo ufw allow from RELAY_IP to any port 8444 proto tcp
sudo ufw allow 22/tcp
sudo ufw enable
```

Keep port 8444 restricted to relays. The aggregate budget limits financial
damage; it does not make direct connections preserve the network's anonymity
split.

---

## 2. One mint

It can share this machine at first. Bind it to loopback, so nothing outside
reaches it directly.

If this machine ran the pre-RFC mint, rotate its key before starting this
version. The old file has PEM type `PRIVATE KEY`; the RFC 9474 implementation
deliberately accepts only `OSANWE RSABSSA PRIVATE KEY`, because RFC 9474 forbids
reusing a signing key across blind-signature protocols. Move the legacy key and
public key aside, generate a fresh pair, publish the new key id, and treat every
legacy token as retired. This is intentionally a breaking security migration,
not an in-place conversion.

The protocol primitive now comes from Cloudflare CIRCL and produces the
standard randomized SHA-384 RSA-PSS form defined by RFC 9474. That removes the
project's hand-written RSA operation; it does not substitute for an independent
review of Osanwe's payment, key-rotation, and redemption integration before real
money is put behind it.

```bash
sudo -u osanwe eregion \
  -key /var/lib/osanwe/mint.key \
  -publish /var/lib/osanwe/mint.pub \
  -print-key-id
```

**Write down the key id it prints.** Clients need it, and they must get it from
you rather than from the mint — a mint that handed every buyer a different key
would put each of them in an anonymity set of one while appearing to work
perfectly. Put it on a web page, in a README, anywhere a user can compare
against.

For local demos, a unit may use `-addr 127.0.0.1:8445 -open`. `-open` gives a
token to anyone who asks and must never face the public.

For paid issuance, configure the built-in self-hosted BTCPay authorizer. It
requires a store-scoped view-invoices API key in `OSANWE_BTCPAY_API_KEY`, an
exact token price, and a separate durable `-receipts-db`. One settled invoice
can then issue exactly one token, including across restarts and concurrent
requests. Follow [the payment guide](payments.md) for the complete command and
the remaining checkout/TLS work.

### Separating them later

They should not stay together. The mint learns **who paid**; the gateway learns
**what was asked**. One machine holding both holds the link the whole design
exists to remove — one subpoena, one break-in, one careless backup.

Separating them is only:

1. Move `mint.key` to its own host, keep `mint.pub` on the gateway.
2. Point the mint's `-addr` at a public interface with its own certificate.
3. Give clients the mint's URL instead of a loopback one.

Do it as soon as there is anything worth protecting. Best done before you have
users, because afterwards it means rotating keys.

---

## 3. One relay run by somebody who isn't you

This is the hard one, and it is not a technical problem.

**Until an independent relay exists, the non-collusion assumption is false.**
Not weakened — false. If you run the relay and the gateway, one party sees both
the address and the words, which is precisely what the architecture claims
nobody does. Say so on the front page, in those words, until it stops being
true. A privacy claim whose assumption is quietly unmet is worse than no claim.

### What to send a volunteer

They need a machine with a public address and about 200 MB of memory. Send them
this:

```bash
git clone https://github.com/EzraStone/Osanwe && cd Osanwe
go build -o ranger ./cmd/ranger

SECRET=$(./ranger -gen-secret)          # send this back
./ranger -gen-secret > /dev/null        # note: keep yours secret

./ranger -dir ./relay-data \
  -addr 0.0.0.0:8443 \
  -allow gateway.example.com:8444       # this relay carries traffic to nowhere else

./ranger -dir ./relay-data -pin         # send this back
./ranger -dir ./relay-data -identity    # and this
```

Tell them plainly what they are agreeing to:

- Their machine forwards encrypted traffic it cannot read. They cannot see
  prompts, and there is nothing they could be compelled to hand over except
  which addresses connected.
- The destination allowlist means their relay reaches your gateway and nothing
  else. It cannot be turned into an open proxy.
- Bandwidth is the cost. Text is small; a busy relay is measured in gigabytes a
  month, not terabytes.

### What you need back

| | Used for |
|---|---|
| the relay's address | clients dial it |
| its TLS pin | clients verify they reached the right machine |
| its identity key | admitting it to a directory later |
| the secret it generated | clients authenticate to it |

Then open your gateway's firewall to that relay's address, and hand clients:

```bash
bearer -relay THEIR_ADDRESS:8443 -pin sha256/... \
       -upstream https://gateway.example.com:8444 \
       -mint https://mint.example.com -mint-key-id mint-...
```

### The credential problem you will hit at two relays

Every client holds one `OSANWE_SECRET`, and each relay operator sets their own.
With two independent relays a client can only use whichever one it shares a
secret with, so failover cannot reach the other.

Options, none free: one network-wide secret (worth what the least careful
operator makes it worth), a per-relay keyring on the client, or open relays
with rate limiting instead of a secret. This is written up in
[`directory.md`](directory.md) and remains undecided. It does not bite at one
relay, and it bites immediately at two.

---

## What to tell users while you are the only operator

Put it where they will see it before they trust it:

> The relay and the gateway are currently run by the same person. The design's
> security rests on those being different parties who do not collude, so that
> property does not hold yet. What does hold: your prompts are not readable in
> transit, and no account of yours reaches the provider.

Remove it the day an independent relay carries its first request, and not
before.

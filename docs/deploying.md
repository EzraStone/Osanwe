# Standing up the first network

Getting from "the demos pass" to "somebody else can use this". Three pieces, in
the order they have to happen.

Read [`who-runs-what.md`](who-runs-what.md) first if you have not. Nothing here
is something a *user* does.

---

## Before anything: the thing that is not built

**Nothing rate limits a gateway.** Anyone who reaches one spends the
operator's provider credit, without limit, until the account is empty.

Two consequences for everything below:

- **Do not put an unlimited-spend provider account behind a public gateway.**
  Use a provider whose own free tier caps you — Groq is the obvious one — or a
  prepaid balance small enough to lose.
- **Firewall the gateway to your relays.** This is not a workaround; it is the
  correct topology. A client reaches the gateway *through* a relay and never
  directly, so the gateway has no reason to accept a connection from anywhere
  else. It is the strongest control available today and it costs nothing.

---

## 1. One gateway, on a small server

### Choose the machine

Anything with 1 GB of memory. A GCP `e2-micro` in `us-west1`, `us-central1` or
`us-east1` is permanently free and enough. So is a $4/month VPS from any of the
usual places.

**Do not put it where your relay is**, later. For now it is the same person
either way; the point is to avoid building a habit you will have to unpick.

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
curl -fsSL https://go.dev/dl/go1.24.7.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
export PATH=$PATH:/usr/local/go/bin

sudo useradd --system --home /var/lib/osanwe --create-home osanwe
git clone https://github.com/EzraStone/Osanw- /tmp/osanwe && cd /tmp/osanwe
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

`Restart=always` has a consequence worth knowing before it surprises you: the
spent-token set lives in memory, so every restart makes every token spent so
far valid again. Until that state is shared and durable, a crash loop is a
free-money loop.

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

Open port 8444 to the world only once rate limiting exists.

---

## 2. One mint

It can share this machine at first. Bind it to loopback, so nothing outside
reaches it directly.

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

Then a unit like the gateway's, with `-addr 127.0.0.1:8445 -open`.

`-open` means it gives tokens to anyone who asks. On a loopback-bound mint that
is only you; the moment it faces the public it is a machine that prints money.
That is what a real `mint.Authorizer` is for, and it is not built.

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
git clone https://github.com/EzraStone/Osanw- && cd Osanw-
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

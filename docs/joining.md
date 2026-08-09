# Running the client on a new machine

From nothing to a working window. Ubuntu or WSL; macOS notes at the end.

## What you need from whoever runs the gateway

Three things, and none of them are secret:

| | Looks like | Why |
|---|---|---|
| **The mint's key id** | `mint-PYjUvRcEKzRxWkxaIWOCZw` | The client checks the mint against it |
| **The gateway's certificate** | a `-----BEGIN CERTIFICATE-----` block | So the client can verify what it is talking to |
| **How to reach the gateway** | a URL, or SSH access while it is private | |

The key id has to arrive by some route other than the mint itself. That is the
entire point of holding it separately: a mint handing every buyer a key of its
own would put each of them in an anonymity set of one while appearing to work
perfectly, and comparing against a value you got elsewhere is what makes the
anonymity set real. The client refuses to start if the two disagree.

The certificate is public — a certificate always is. The private half stays on
the server.

## 1. Prerequisites

```bash
sudo apt update && sudo apt install -y git curl python3 openssh-client

curl -fsSL https://go.dev/dl/go1.26.5.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
export PATH=$PATH:/usr/local/go/bin

go version
```

Expect `go1.26.5` or newer. Ubuntu's own `golang-go` package may be older and
not build this, which is why the tarball is used instead.

The `>> ~/.bashrc` line matters. Without it the `export` lasts only for the
current terminal, and the next one says `go: command not found`.

## 2. Get the code

```bash
git clone https://github.com/EzraStone/Osanw-
cd Osanw-
```

## 3. Save the gateway's certificate

```bash
cat > gateway.crt <<'EOF'
PASTE THE CERTIFICATE HERE, INCLUDING THE BEGIN AND END LINES
EOF
```

It is gitignored, so it will not follow you into a commit.

## 4. Reach the gateway

**If the operator gave you a URL**, skip to step 5 — there is nothing to set up.

**While the gateway is private**, which is how it starts, it listens on the
server's loopback with the firewall shut, so the only way in is an SSH tunnel.
That means you need SSH access to the server, which makes this an arrangement
for the operator's own machines rather than for other people.

```bash
ssh-keygen -t ed25519 -C YOUR_LOGIN -f ~/.ssh/osanwe -N ""
cat ~/.ssh/osanwe.pub
```

Give that public key to the operator, who adds it to the instance: GCP Console
→ Compute Engine → VM instances → the instance → Edit → SSH Keys → Add item →
Save. The comment at the end of the key becomes your username on the server.

Then, in a terminal you leave running:

```bash
ssh -i ~/.ssh/osanwe -N \
  -L 8444:localhost:8444 \
  -L 8445:localhost:8445 \
  YOUR_LOGIN@THE_SERVER_ADDRESS
```

`-N` means "forward, run nothing", so it sits silent. That is correct.

## 5. Run it

In a second terminal:

```bash
cd Osanw-
./demo/client.sh THE_MINT_KEY_ID
```

It checks that the gateway and mint are reachable, checks the mint's key
against the id you were given, builds, starts a relay and the client, and
prints a URL. Open that. Ctrl-C stops what it started.

## When it does not work

**`go: command not found`** — the PATH line in step 1 did not stick. Run the
`export` again, and check `grep go/bin ~/.bashrc` has it.

**`Cannot reach the gateway on 127.0.0.1:8444`** — the tunnel is not running,
or it dropped. The script prints the command to open one.

**`The mint serves key id … but you asked for …`** — either the mint rotated
and your id is stale, or something is serving a key of its own. Ask the
operator; do not edit the id to match, which would defeat the check entirely.

**A model name in a 404** — the gateway does not carry it. The refusal lists
what it does carry, and the window's picker fills itself from that list.

## What this arrangement gives you, and what it does not

The gateway runs on the server, so **the provider sees the server's address
rather than yours**. That is the property a single machine cannot provide, and
it is why any of this is worth the trouble.

The relay is still on your own machine, beside the client, so **the gateway
sees your address**. Only a relay run by somebody else fixes that. Until one
exists, one party can see both ends, and the design's central assumption does
not hold. See [`who-runs-what.md`](who-runs-what.md).

Check both with:

```bash
./demo/verify.sh
```

## macOS

Same, with `brew install go git` in place of step 1, and `xdg-open` becoming
`open` (the script handles that already).

## If you just want to see it work

None of the above is needed:

```bash
./demo/ui.sh
```

Everything on one machine with a stand-in provider — no key, no server, no
tunnel. Add `docs/routes.groq.conf` and a `GROQ_API_KEY` for real answers from
a free provider. It gives up the address property and keeps every other one.

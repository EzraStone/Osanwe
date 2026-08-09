# The directory

Phase 2 makes you get a relay's address and pin from its operator by hand. That
is the strongest trust story the system has, because nobody stands between you
and the operator you chose. But it does not scale past one relay, and it gives
you nothing to fall back on when that relay goes down.

The directory fixes discovery. **It does not remove trust, it moves it.**

## What the trade actually is

| | Manual pin | Directory |
|---|---|---|
| You trust | One operator you chose | Whoever signs the consensus |
| Discovery | None. You know one relay | Every listed relay |
| If your relay dies | You are offline | Another is selected |
| Compromise of the trusted party | That one relay reads nothing (it still cannot decrypt), but can refuse service | An authority can steer you to relays it prefers |

**A manual pin remains the stronger option and is never silently overridden.**
`bearer` refuses `-relay/-pin` and `-directory` together rather than picking one,
so you always know which is in force.

## What an authority can and cannot do

Descriptors travel inside a consensus as their own signed bytes and are verified
against each relay's own key. So:

- **It cannot forge a relay.** Inventing an entry, or moving an existing relay
  to an address it controls, requires forging that relay's signature.
- **It cannot read your traffic.** The directory is not on the data path at all.
- **It can omit.** A hostile authority can leave honest relays out until only
  the ones it likes remain. That is the same attack as forgery with more steps,
  and it is the reason for the threshold.

Requiring signatures from several independent authorities is what makes omission
expensive: every one of them has to agree to the same edited list.

## Running an authority

```bash
council -identity ./council.key -descriptors ./descriptors \
        -accept ./accept.txt -addr :9000

council -identity ./council.key -key      # print the public key for clients
```

`-accept` enables `POST /publish`. Without it the endpoint refuses everything.
The file is one identity per line, with an optional label, re-read on every
submission so admitting a relay does not need a restart:

```
# operators this authority carries
ed25519:AAAA...  north relay, ops@example.com
ed25519:BBBB...  south relay
```

`-lifetime` sets how long each consensus is valid, `-rebuild` how often it is
regenerated. Rebuild must be shorter than lifetime, or the consensus expires
before its replacement exists, `council` refuses to start otherwise.

A broken descriptor is skipped with a warning rather than aborting the rebuild;
one operator publishing a malformed file must not take the directory down. A
failed rebuild keeps serving the previous consensus, which is signed and still
fresh, returning nothing would take every client offline over a transient disk
error.

### Health checking

Before listing a relay, `council` opens a TLS connection to it and checks that
the key it presents matches the pin in its descriptor. That catches two things a
parser cannot: a relay that is simply down, and a relay whose certificate was
rotated without republishing, which to a client looks exactly like
impersonation.

The probe is a handshake and nothing more. It needs no credential, because a
relay presents its certificate before asking who is calling, and it stops short
of authenticating on purpose: an authority has no business holding relay
secrets, and a probe that could open a tunnel could carry traffic.

A key mismatch is a failure, never a new observation. The authority will not
quietly republish a key it just discovered; either the operator rotated it and
should republish a signed descriptor, or something is impersonating the relay.
Both are for the operator to resolve.

`-unhealthy-after` (default 3) is how many consecutive failures it takes to drop
a relay. Networks are unreliable, and a directory that removed a relay the first
time a probe timed out would flap constantly and take clients with it. A relay
that is failing but not yet dropped is logged, so trouble is visible before the
relay disappears. `-probe=false` turns the whole thing off and warns that the
consensus may then advertise relays that are down.

**One council is not a network.** Whoever holds that key decides which relays
clients can see. A real deployment runs several, operated by different people in
different jurisdictions, and clients require agreement from more than one.

## Producing an M-of-N consensus

Several `council` processes do not magically make a consensus. The authorities
must sign the **same exact body**, and somebody must combine those signatures
before clients can require a threshold greater than one. The shipped binary has
an intentionally boring file workflow for doing that. Files can travel by SCP,
email, removable media, or any other channel; signatures, rather than that
channel, protect them.

All authorities first exchange and pin the complete authority key set:

```bash
council -identity ./authority-a.key -key
council -identity ./authority-b.key -key
council -identity ./authority-c.key -key
```

They also agree on `-epoch` and `-lifetime` and keep the same locally approved
descriptor files. An epoch is a UTC time bucket. Within one 30-minute epoch,
two authorities with the same descriptors and a three-hour lifetime construct
the same body even when they run the command several minutes apart. Authority
hosts need synchronized clocks: `cosign` refuses a proposal from the preceding
epoch even while that old document remains fresh enough to serve. A descriptor
is included only when it is valid for the entire consensus window; evaluating
that rule at fixed epoch boundaries prevents two wall-clock sampling times from
disagreeing as a descriptor expires.

One authority creates a partial:

```bash
council build -identity ./authority-a.key \
  -descriptors ./descriptors -epoch 30m -lifetime 3h \
  -out ./round-a.consensus
```

A second authority does not blindly add a signature. `cosign` verifies every
existing signature, requires every signer and its own key to be in the supplied
authority set, checks that this is the current epoch, rebuilds the body from its
**own** descriptor directory, and signs only if the bytes match:

```bash
council cosign -identity ./authority-b.key \
  -descriptors ./descriptors -in ./round-a.consensus \
  -out ./round-ab.consensus -epoch 30m -lifetime 3h \
  -authority ed25519:AAAA... \
  -authority ed25519:BBBB... \
  -authority ed25519:CCCC...
```

Alternatively, every authority can run `build` independently. The body is
canonical: time boundaries are epoch-aligned and complete signed descriptors
have a total byte ordering. An aggregator then joins the independent partials:

```bash
council aggregate \
  -part ./round-a.consensus -part ./round-b.consensus \
  -out ./consensus -threshold 2 -epoch 30m -lifetime 3h \
  -authority ed25519:AAAA... \
  -authority ed25519:BBBB... \
  -authority ed25519:CCCC...
```

Aggregation is not voting by relay or line. Every partial must sign the same
exact body. A different epoch, lifetime, descriptor set, or descriptor version
is reported as a conflict; the tool will not invent a union or intersection
that no authority actually approved. Repeating one partial never increases the
signature count, unconfigured signers are refused, and no final file is written
until the configured threshold is met. Each command prints a SHA-256 body ID so
operators can compare the exact proposal over a separate channel. If independent
builders disagree, reconcile their descriptor directories and wait for the next
epoch; the anti-equivocation state correctly prevents either one from changing
its vote midway through the current epoch.

Every command that uses an authority private key also maintains persistent
anti-equivocation state at `<identity>.consensus-state` (override it with
`-signing-state`). It records the latest epoch and body hash before releasing
the signed output. Retrying the identical body is allowed; signing another body
for that epoch or going back to an older epoch is refused, including after a
restart. Keep this small file durable and use one shared state path for every
process that can access the same authority key. A companion `.lock` directory
serializes concurrent signers. If a process crashes and leaves that lock, inspect
the state and any produced partial before removing the lock; deleting it
casually throws away the protection against split votes.

The final file can be copied to any number of untrusted mirrors, or served by
`council` itself:

```bash
council serve -consensus ./consensus -addr :9000 \
  -threshold 2 -epoch 30m -lifetime 3h \
  -authority ed25519:AAAA... \
  -authority ed25519:BBBB... \
  -authority ed25519:CCCC...
```

`serve` verifies the configured authority set, threshold, epoch and freshness
before starting. It reloads an atomically replaced file, refuses a lower epoch
and refuses a different body for an epoch already served. Once its held
consensus expires it returns `503` rather than distributing stale data. The
highest installed epoch/body is persisted at `<consensus>.serve-state`
(`-serve-state` overrides it), so rollback and same-epoch conflict protection
survive a restart. Keep one durable state file per serving instance. Do not
share a state path between concurrently running mirror processes; give each
instance its own `-serve-state` path and consensus working copy.

The original daemon form (`council -descriptors ...`) remains useful for a
single-authority deployment and as a source of partials. Its rebuilds now use
the `-rebuild` duration as a shared UTC epoch, which removes clock sampling as a
source of body differences. Each daemon still serves only its own signature. A
client does **not** merge separate endpoint responses, so an operator must run
`aggregate` and publish the finalized file before setting a client threshold
above one. Once the daemon signs an epoch it freezes that body: a descriptor or
health change waits for the next epoch rather than making one authority key sign
two conflicting views for the same round. The persistent signing-state file
preserves that rule across restarts.

Online authorities with probing enabled are **not guaranteed to produce the
same body**: independent networks and failure counters can honestly produce
different health decisions. The recommended quorum path is the explicit
`build`/`cosign` workflow over reviewed descriptor sets. Aggregating daemon
partials requires a common membership decision—typically probing disabled for
that signing path, with the availability risk of `-probe=false` accepted—or the
aggregator will correctly report a conflict.

The offline `build` and `cosign` commands verify descriptor signatures and
freshness and fail if a local descriptor is unreadable, malformed, or repeats
an identity. They do not perform network health probes. That is deliberate: a
transiently different network view would create different bodies. Operators
should review their local descriptor set and health policy before signing. The
long-running authority mode still probes relays and will surface any resulting
body disagreement during aggregation instead of hiding it.

## Publishing a relay

```bash
ranger -dir . -allow api.anthropic.com \
       -nickname northrelay \
       -advertise relay.example.com:8443 \
       -contact ops@example.com \
       -publish https://council-a.example/publish,https://council-b.example/publish
```

`-advertise` is required when the relay binds a wildcard address, because a
relay listening on `:8443` does not know what address clients should dial and a
descriptor carrying a placeholder is unusable.

Use `-descriptor ./north.desc` instead of `-publish` to write the file out and
deliver it some other way. Publishing needs no credential and no transport
security: the descriptor is signed, so an authority verifies it on arrival, and
anything that altered it in flight would only produce a document the authority
rejects. Each endpoint is reported separately, since one authority being down
must not stop the others hearing about your relay.

### Getting admitted

Submission is **default-deny**. An authority publishes only identities on its
accept list, so the first step is sending your fingerprint to its operator:

```bash
ranger -dir . -identity          # prints ed25519:...
```

The operator adds a line to their accept file and you publish again. This is a
human decision on purpose. An open endpoint would let anyone register relays,
and a directory listing a thousand attacker-run relays beside three honest ones
has handed the attacker almost every client without breaking a signature.

A submission is refused if it is older than or the same age as the one already
stored (`409`). Otherwise anyone holding a copy of an old descriptor could
replay it and move your relay back to a previous address or key. That old
document's signature is still valid, which is precisely why freshness is checked
separately.

The relay's directory identity (`ranger.identity`) is separate from its TLS key.
The certificate can be rotated routinely; the identity is what clients remember,
and changing it makes the relay a different relay to everyone who knew it.

## Using the directory

```bash
bearer -directory https://council-a.example/consensus \
       -directory https://council-b.example/consensus \
       -authority ed25519:AAAA... \
       -authority ed25519:BBBB... \
       -threshold 2
```

Each URL must serve a finalized consensus that already carries two signatures;
they may be independent mirrors of the same file. The client deliberately does
not combine one signature from one HTTP response with another signature from a
different response.

Endpoints are tried in random order, and the first relay is chosen at random
from those serving your provider. Both are deliberate: always preferring the
first entry would concentrate clients on one relay, and a relay carrying
everyone's traffic sees everyone's timing.

`-threshold` defaults to 2. Setting it to 1 with several authorities configured
logs a warning, because it quietly reduces an M-of-N design to trusting whichever
authority answers first.

### When a relay goes away

The client re-fetches the consensus every 15 minutes and moves to another relay
when the one it is using stops answering. Neither needs a restart, and neither
is anything the user is asked to notice.

**A failed refresh keeps the relays already held.** Going relay-less because an
authority was briefly unreachable would turn a directory outage into a client
outage, which is backwards: a consensus is signed, so it stays trustworthy
while you hold it.

**But not forever.** Coasting is bounded by `MaxStale`, a day past the
consensus's own expiry, after which the client refuses to dial rather than
keep using the list. The two obvious policies are both wrong. Refusing at the
instant of expiry makes every brief authority outage an outage for every
client. Holding indefinitely is worse in a quieter way: a client that
permanently loses the directory keeps reaching for relays that may have been
withdrawn, rotated their keys, or been removed for misbehaving, and nothing
ever tells it. A client silently using a year-old relay list has the security
properties of that year-old list. The ceiling puts a limit on how far behind
the network a client can drift without noticing.

**Selection is sticky, not round-robin.** After the initial random pick the
client stays on that relay — Tor calls it a guard — and only moves when it
fails. Rotating for its own sake looks like it should help and does the
opposite: a client that keeps choosing fresh relays eventually chooses a
hostile one, and the chance of having used at least one grows with every
rotation. Spreading traffic across five relays does not divide what they learn
about you. It hands the same knowledge to five parties instead of one.

A relay that fails is set aside with an exponential backoff, from 15 seconds up
to a 10-minute cap, and the count survives a refresh so a relay that has been
failing for an hour does not get traffic thrown back at it every time a new
document arrives. If every relay is in backoff the client tries anyway, because
a backoff is a preference about ordering and letting it harden into "this
client will not make requests" is a worse failure than a slow one.

Two failures are treated specially. A relay presenting a key the directory did
not publish is logged as an error rather than a warning, since that is either a
rotation the operator never announced or something impersonating a relay. And
when *every* relay rejects the credential, the client says the secret is wrong
instead of listing four identical 407s — with one shared secret across a
directory of independently operated relays, that is the likely cause. See the
open question in the last section.

What this does not cover: a relay that vanishes without closing its
connections — a pulled cable rather than a stopped process — leaves an already
open tunnel that looks fine until it is used. That request fails before
failover can happen. Every HTTP client has this property; the recovery is
automatic, but it costs one request.

## Document format

Line-oriented text. **Signatures are always verified over the exact bytes
received**, never over a re-serialized surrogate. Substituting re-serialized
bytes during signature verification is behind a family of signature-bypass
bugs: any parser/serializer disagreement can change meaning while retaining a
valid signature. Separately, the consensus parser re-encodes the parsed
structure only to enforce that the received body was canonical; that comparison
never replaces verification of the original bytes.

Five rules follow from taking that seriously:

- Unknown fields are refused, not skipped, so a future field carrying real
  meaning cannot be ignored by an old client that still reports the document valid.
- Duplicate single-valued fields are refused, so meaning never depends on which
  line a reader happens to use.
- Anything after the signature is refused, since it is unsigned.
- One authority cannot sign twice, otherwise it could satisfy a threshold meant
  to require several by repeating itself.
- Consensus bodies have one canonical encoding and descriptor order, so two
  authorities cannot sign different bytes that merely parse to the same meaning.

Both documents expire. A signature never goes stale on its own, so without
expiry an old consensus could be replayed indefinitely after a relay was
withdrawn or its key rotated.

## Transport

The consensus is signed, so it does not need a secure channel. Fetching over
plain HTTP from an untrusted mirror is fine; fetching over HTTPS from a hostile
authority would not help. Responses are size-bounded so a broken or hostile
endpoint cannot exhaust a client's memory.

## What this still does not give you

The directory is discovery, not anonymity. In the BYOK path described here you
still authenticate to the provider as yourself. `eregion` and `mithlond` now
provide a separate blind-token/pooled-gateway path, but adding a directory does
not automatically move a BYOK client onto it.

### One secret, many relays

A client holds a single `OSANWE_SECRET`, but a consensus can list relays run by
people who have never met and who set their own secrets. Failover walks the
list, so in practice a client can only fail over to relays that happen to share
the credential it holds.

This remains unresolved for ranger directory failover, and is worth stating
rather than discovering. The implemented blind tokens are redeemed by
`mithlond`; they do not replace the `OSANWE_SECRET` used to authenticate a
bearer-to-ranger tunnel. The directory options are therefore still a single
network-wide secret (simple, and worth exactly as much as the least careful
operator holding it), a per-relay keyring on the client (honest, more to carry),
or open relays with rate limiting instead of a shared secret. Until one of those
is implemented, a directory is most useful across relays under one operator's
control.

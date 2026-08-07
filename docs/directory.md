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
while you hold it. It expires eventually, and that is the limit on how long a
client can coast.

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

Line-oriented text. **Signatures cover the exact bytes received**, and
verification never re-serialises a parsed struct to compare, that pattern is
behind a whole family of signature-bypass bugs, where any disagreement between
parser and serialiser becomes a way to change meaning while keeping a valid
signature.

Four rules follow from taking that seriously:

- Unknown fields are refused, not skipped, so a future field carrying real
  meaning cannot be ignored by an old client that still reports the document valid.
- Duplicate single-valued fields are refused, so meaning never depends on which
  line a reader happens to use.
- Anything after the signature is refused, since it is unsigned.
- One authority cannot sign twice, otherwise it could satisfy a threshold meant
  to require several by repeating itself.

Both documents expire. A signature never goes stale on its own, so without
expiry an old consensus could be replayed indefinitely after a relay was
withdrawn or its key rotated.

## Transport

The consensus is signed, so it does not need a secure channel. Fetching over
plain HTTP from an untrusted mirror is fine; fetching over HTTPS from a hostile
authority would not help. Responses are size-bounded so a broken or hostile
endpoint cannot exhaust a client's memory.

## What this still does not give you

The directory is discovery, not anonymity. You still authenticate to the
provider as yourself, because Phase 2 is bring-your-own-key. Unlinking the
account needs `eregion` and `mithlond`, which are Phase 3 and not built.

### One secret, many relays

A client holds a single `OSANWE_SECRET`, but a consensus can list relays run by
people who have never met and who set their own secrets. Failover walks the
list, so in practice a client can only fail over to relays that happen to share
the credential it holds.

This is unresolved, and worth stating rather than discovering. The options are
a single network-wide secret (simple, and worth exactly as much as the least
careful operator holding it), a per-relay keyring on the client (honest, more
to carry), open relays with rate limiting instead of a shared secret (which is
how a public network would have to work), or waiting for the blind-signed
tokens in Phase 3, which dissolve the problem by making the credential
unlinkable and per-request rather than per-relay.

Phase 3 is the real answer. Until then, a directory is most useful across
relays under one operator's control.

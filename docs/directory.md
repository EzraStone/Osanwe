# The directory

Phase 2 makes you get a relay's address and pin from its operator by hand. That
is the strongest trust story the system has — nobody stands between you and the
operator you chose — but it does not scale past one relay and gives you nothing
to fall back on when that relay goes down.

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
council -identity ./council.key -descriptors ./descriptors -addr :9000
council -identity ./council.key -key      # print the public key for clients
```

`-lifetime` sets how long each consensus is valid, `-rebuild` how often it is
regenerated. Rebuild must be shorter than lifetime, or the consensus expires
before its replacement exists — `council` refuses to start otherwise.

A broken descriptor is skipped with a warning rather than aborting the rebuild;
one operator publishing a malformed file must not take the directory down. A
failed rebuild keeps serving the previous consensus, which is signed and still
fresh — returning nothing would take every client offline over a transient disk
error.

**One council is not a network.** Whoever holds that key decides which relays
clients can see. A real deployment runs several, operated by different people in
different jurisdictions, and clients require agreement from more than one.

## Publishing a relay

```bash
ranger -dir . -allow api.anthropic.com \
       -nickname northrelay \
       -advertise relay.example.com:8443 \
       -contact ops@example.com \
       -descriptor ./northrelay.desc
```

Send the file to the authorities. `-advertise` matters: a relay bound to `:8443`
does not know what address clients should dial, and without it the descriptor
carries a placeholder and says so.

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

Endpoints are tried in random order, and the relay is chosen at random from
those serving your provider. Both are deliberate: always preferring the first
entry would concentrate clients on one relay, and a relay carrying everyone's
traffic sees everyone's timing.

`-threshold` defaults to 2. Setting it to 1 with several authorities configured
logs a warning, because it quietly reduces an M-of-N design to trusting whichever
authority answers first.

## Document format

Line-oriented text. **Signatures cover the exact bytes received**, and
verification never re-serialises a parsed struct to compare — that pattern is
behind a whole family of signature-bypass bugs, where any disagreement between
parser and serialiser becomes a way to change meaning while keeping a valid
signature.

Four rules follow from taking that seriously:

- Unknown fields are refused, not skipped, so a future field carrying real
  meaning cannot be ignored by an old client that still reports the document valid.
- Duplicate single-valued fields are refused, so meaning never depends on which
  line a reader happens to use.
- Anything after the signature is refused, since it is unsigned.
- One authority cannot sign twice — otherwise it could satisfy a threshold meant
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

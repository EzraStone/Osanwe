# Who runs what

A user runs one program on their own computer and nothing else. Everything in
this document that is not `bearer` is somebody else's job.

That distinction is easy to lose while developing, because one person testing
the system runs all six processes on one laptop. That arrangement proves the
software works and provides no anonymity at all: if the relay and the gateway
are both yours, nobody stands between you and the provider.

| Component | Who runs it | How many | Why |
|---|---|---|---|
| `bearer` | **the user** | one each, on their own machine | It holds the wallet and talks to a relay. It must be yours, or it is not your client |
| `ranger` | relay operators | as many as will volunteer | Sees an address, never the words. Must not be the user's, or it hides nothing |
| `council` | a few independent parties | 3–5 | Publishes which relays exist. Several, so no single one can hand you a relay it controls |
| `eregion` | whoever sells tokens | one, or a few | Sees the payment receipt and blinded issuance, never what the token later buys |
| `checkout` | whoever sells tokens | one per storefront | Creates fixed-price invoices without asking for buyer identity; uses a separate create-only payment key |
| `mithlond` | gateway operators | a few | Holds the provider accounts. Sees the words, never the address |

This is the shape Tor has. Most people run the client; a much smaller and more
technical group runs relays; nobody is asked to run infrastructure to be a
user.

## What a user actually does

1. Install one binary.
2. Buy tokens, or use whatever free allowance exists.
3. Open the window it prints.

No server, no API key, no configuration file, no cloud account. If a user ever
has to think about a VPS, the product has failed.

## Why the user must not run the relay

A relay hides the user's address from the gateway, and the gateway hides the
user's identity from the provider. Both only work because those two components
belong to different people.

A user who runs their own relay has built a longer path to the same place: the
relay sees their address because it *is* their machine, so the provider still
learns where the request came from. Running your own relay is useful for
testing and worth nothing for privacy.

The same is true of the gateway, more sharply. The gateway is what calls the
provider, so the provider sees the gateway's address. A gateway on the user's
laptop means the provider sees the user's home address, however many relays
were crossed on the way there.

## What the operator has to run, and pay for

Somebody has to stand up the parts a user does not. At the start that is one
person, which is normal — Tor began as a handful of nodes — and it is the real
work between here and something anyone else can use.

**A gateway.** A small server with a provider account behind it. The provider
bills this account for everyone, which is exactly why nothing identifies an
individual user to the provider, and exactly why the gateway needs rate
limiting before it faces the public. Without that, anyone who finds the address
spends the operator's credit.

**A mint.** Can share the gateway's machine at first, though they should be
separated later: the mint sees payment records and the gateway learns what was
asked, and one machine holding both is one subpoena away from holding the link
the whole design removes.

**A checkout.** The public fixed-price page can share the mint operator, but it
runs separately with a create-invoice-only key. The mint's view-invoices key is
never exposed to the buyer-facing process.

**At least one relay, ideally not yours.** A single operator running the relay
and the gateway is the collusion the security assumption forbids. Early on
this is unavoidable and should be stated plainly to users rather than implied
away. It stops being true when the first independent relay operator appears.

**Directory authorities.** Only once there is more than one relay worth
choosing between.

## The honest state of that today

None of it is deployed. The software runs, the demos pass, and there is no
network: no public gateway, no mint anyone can buy from, no relay run by a
stranger.

The gap between here and a product is not more features. It is one gateway,
one independently operated relay, provider cooperation, independent review of
the payment and redemption boundary, and the operational work that makes leaving
the services running survivable.

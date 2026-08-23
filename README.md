# Osanwë

**A privacy relay for AI inference, and an experimental account-unlinked token gateway.**

[Project website](https://ezrastone.github.io/Osanwe/) · [Technical beta charter](docs/beta.md) ·
[Current Phase 0 evidence](docs/phase0-results.md)

The runnable bring-your-own-key path hides a user's IP address and location from the model
provider, and keeps prompts unreadable to the relay. The provider still sees the user's own API
account. The account-unlinked path replaces that account credential with a blind-signed token, but
it is pre-launch software: there is no public network, the gateway operator can read prompts until
attested execution is implemented, and the production controls are still being completed.

Start with the [quickstart](docs/quickstart.md), read [who runs what](docs/who-runs-what.md), and do
not expose the token gateway before following the warnings in the [deployment guide](docs/deploying.md).

The first beta is being prepared for ten invited testers. It is free, text-only, and limited to
synthetic or deliberately non-sensitive prompts. The public website is an explanation and download
front door, not a hosted chat service: invited archives open the app on the tester's own loopback
interface and keep relay secrets out of the website.

## Accountless local client

The bearer binary now embeds a dependency-free local web client with Chat, Models, and Connect
views. Chat sends genuine multi-turn context, streams responses, and starts with ephemeral history.
People may explicitly save conversations in their browser's IndexedDB, restore them, export a
plaintext JSON copy, or delete them. No conversation-history endpoint exists on an Osanwë service.

The Models view reads the connected gateway's live catalog and shows enforced text capabilities,
request limits, and factual privacy labels. Provider retention and training remain **unknown** unless
the gateway has sourced metadata; the interface does not turn unknown policy into a privacy score.

The private-beta payment adapter is deliberately crypto-only. A non-empty receipt buys one token
and is presented at most once. Card, Monero, cash, and voucher adapters remain product work, and no
real-money launch should precede an external review of interrupted and concurrent issuance.

See the [v0.1 scope](docs/product/v0.1-scope.md), [privacy boundaries](docs/product/privacy-boundaries.md),
and [payment notes](docs/payments.md) for what is and is not promised.

> In Tolkien's *Ósanwe-kenta*, *ósanwë* is the direct transmission of thought between minds. Its
> central doctrine is that a mind, open by nature, may close itself against intrusion and that no
> power can rightfully force it open. A prompt is a thought in transit. This network is the barrier.

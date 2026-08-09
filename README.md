# Osanwë

**A privacy relay for AI inference, and an experimental account-unlinked token gateway.**

The runnable bring-your-own-key path hides a user's IP address and location from the model
provider, and keeps prompts unreadable to the relay. The provider still sees the user's own API
account. The account-unlinked path replaces that account credential with a blind-signed token, but
it is pre-launch software: there is no public network, the gateway operator can read prompts until
attested execution is implemented, and the production controls are still being completed.

Start with the [quickstart](docs/quickstart.md), read [who runs what](docs/who-runs-what.md), and do
not expose the token gateway before following the warnings in the [deployment guide](docs/deploying.md).

> In Tolkien's *Ósanwe-kenta*, *ósanwë* is the direct transmission of thought between minds. Its
> central doctrine is that a mind, open by nature, may close itself against intrusion and that no
> power can rightfully force it open. A prompt is a thought in transit. This network is the barrier.

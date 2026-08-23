# Privacy boundaries

Osanwë does not have one participant called “the service.” Privacy depends on what each participant
can observe and on those participants remaining separate.

## Local client

The local bearer process sees the request because the person's own application sends it there. It
keeps aggregate counters, current connection state, and an in-memory token wallet. It does not keep
per-request logs or conversation history.

The embedded browser client is allowed to keep conversation text only when the person explicitly
selects device-only history. That text never belongs in localStorage, logs, URLs, status responses,
or exported diagnostics.

## Relay

A relay sees the connecting network address and the encrypted destination flow. In the intended
path, a correctly configured and pinned relay receives ciphertext rather than prompt or answer text.
A pin or threshold-signed directory authenticates the configured relay key; it does not prove the
operator's identity, independence, location, deployment correctness, or non-collusion.

## Gateway

The gateway sees prompt and answer text and attaches its provider credential. A relay/gateway split
prevents one honest operator from seeing both identity-by-network and content. Until attested
execution is implemented, the gateway operator can read prompts and the interface must say so.

## Model provider

In bring-your-own-key mode, a connection routed through a correctly configured remote relay presents
the relay's egress address rather than directly carrying the person's originating network address;
the provider still sees the person's provider account. In token mode, it sees the gateway's account
instead. In both modes, prompt content, writing style, timing, or collusion can identify a person.

Provider retention and training policies are outside Osanwë's technical control. A model label may
state a policy only when it has a dated, attributable source; “unknown” is better than a guess.

## Mint and payment adapter

The mint learns that a payment entitlement was redeemed and blindly signs a credential. It must not
learn the unblinded credential later presented to the gateway. Payment processors may identify a
buyer according to the chosen rail, but the gateway must not receive that payment identity.

## Claims we can make today

- A correctly configured and pinned relay receives end-to-end ciphertext and has no protocol
  decryption key for the request.
- The gateway does not receive the user's source network address from the relay protocol.
- Blind-signed tokens cannot be directly cryptographically matched to their blinded issuance
  transcript; timing, metadata, small anonymity sets, and collusion remain.
- The local process exposes no server-side conversation-history endpoint.

## Claims we cannot make today

- The gateway operator is technically unable to read prompts.
- Writing style or prompt content cannot identify a person.
- Every provider deletes or declines to train on prompts.
- A single operator running both relay and gateway provides the intended separation.
- Anonymous access is immune to traffic analysis or abuse-driven shutdown.

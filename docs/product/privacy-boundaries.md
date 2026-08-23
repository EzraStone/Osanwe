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

A relay sees the connecting network address and the encrypted destination flow. It must not see
prompt or answer text. A pinned key or threshold-signed directory proves which relay was reached.

## Gateway

The gateway sees prompt and answer text and attaches its provider credential. A relay/gateway split
prevents one honest operator from seeing both identity-by-network and content. Until attested
execution is implemented, the gateway operator can read prompts and the interface must say so.

## Model provider

In bring-your-own-key mode, the provider sees the person's provider account but not their originating
network address. In token mode, it sees the gateway's account instead. In both modes, prompt content
and writing style can contain identifying information.

Provider retention and training policies are outside Osanwë's technical control. A model label may
state a policy only when it has a dated, attributable source; “unknown” is better than a guess.

## Mint and payment adapter

The mint learns that a payment entitlement was redeemed and blindly signs a credential. It must not
learn the unblinded credential later presented to the gateway. Payment processors may identify a
buyer according to the chosen rail, but the gateway must not receive that payment identity.

## Claims we can make today

- A correctly pinned relay cannot read the end-to-end encrypted request.
- The gateway does not receive the user's source network address from the relay protocol.
- Blind-signed tokens are not conventionally linkable to their issuance transcript.
- The local process exposes no server-side conversation-history endpoint.

## Claims we cannot make today

- The gateway operator is technically unable to read prompts.
- Writing style or prompt content cannot identify a person.
- Every provider deletes or declines to train on prompts.
- A single operator running both relay and gateway provides the intended separation.
- Anonymous access is immune to traffic analysis or abuse-driven shutdown.

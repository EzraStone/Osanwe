# ADR 0001 — Ship bring-your-own-key first; pursue provider cooperation in parallel

- **Status:** Accepted
- **Decides:** Design document §10 (Existential risk I — Terms of Service)
- **Supersedes:** nothing
- **Revisit when:** a provider offers a sanctioned privacy-preserving access mode, or Phase 2
  telemetry shows BYOK demand is insufficient to sustain a relay network

## Context

Osanwë's full anonymity claim — *the provider can serve your request but cannot tie it to a name,
card, or IP* — requires that the request not carry the user's own API key. That means pooled
account access at `mithlond`, where one provider account serves many users.

Pooling keys that way is very likely a violation of provider terms: account sharing,
misrepresentation of the account holder, and circumvention of rate-limiting and abuse controls. The
practical consequence is worse than the legal one. A pooled key can be revoked at any moment,
without notice and without appeal, which makes it a **single point of failure for the entire
network**. A privacy network whose availability depends on a counterparty that has every incentive
to remove it is not a network anyone should build a company on.

Three postures were considered:

| Posture | Anonymity delivered | Durability |
|---|---|---|
| Bring-your-own-key | Partial — defeats IP linkage only | Fully compliant |
| Cooperative (sanctioned mode) | Full, and legitimate | Slowest, most durable |
| Adversarial (pooled keys, expect bans) | Full, until the key dies | Fragile by construction |

## Decision

**Ship bring-your-own-key as v1 (Phase 2). Open the cooperative conversation with providers in
parallel. Treat the adversarial path as a research branch that never becomes the roadmap.**

Under BYOK the user supplies their own API key. The client holds an end-to-end TLS session to the
provider through a `CONNECT` tunnel, so no relay ever sees plaintext. The provider still knows which
account is asking, but no longer knows from where, and no intermediary learns anything at all.

## Rationale

- **It is honest and compliant.** No terms are violated, nothing is misrepresented, and no component
  of the system depends on a counterparty's forbearance.
- **The partial win is a real win.** Defeating IP and location linkage is meaningful on its own —
  it is most of what a VPN sells, with a materially better trust model, and without the
  exit-node problem that afflicts Tor.
- **It unblocks everything else.** Phase 2 builds the relay network, the operator community, the
  directory, the client, and the latency dataset. All of that is a prerequisite for Phase 3
  regardless of which posture eventually wins.
- **It preserves the cooperative option.** Arriving at a provider with a working, compliant,
  well-operated privacy relay is a far stronger position from which to ask for a sanctioned mode
  than arriving with a proposal — or with a history of ban evasion.
- **Providers have a genuine interest here.** Users being able to *prove* privacy rather than
  *trust* it is reputationally valuable to a model vendor, and at least one major platform vendor
  has already shipped a relay of substantially this shape.

## Consequences

**Accepted:**

- v1 does not deliver the headline anonymity claim. Marketing must describe what BYOK actually
  provides — IP and location unlinkability, and confidentiality from every intermediary — without
  implying the provider cannot identify the account. Overclaiming here would be the single most
  damaging thing the project could do to its credibility.
- The `eregion` mint and token machinery are deferred to Phase 3, so the cryptographic work is not
  validated early. Mitigated by prototyping issuance against a mock gateway during Phase 2.
- If no provider ever offers a sanctioned mode, the full claim may never ship compliantly. That is
  an acceptable outcome: a durable partial-privacy network is worth more than a full-privacy network
  that dies on its first key revocation.

**Enabled:**

- Phase 2 can ship without any legal blocker.
- Relay operators face materially less risk than Tor exit operators, since they provably cannot read
  traffic — a fact that should be central to operator recruitment.
- The §10 decision no longer gates Phase 0 or Phase 2 work.

## Follow-up

- [ ] Draft the provider outreach note describing a sanctioned privacy-preserving access mode.
- [ ] Confirm with counsel that BYOK relaying itself raises no terms issue for any target provider.
- [ ] Write the v1 marketing copy against this ADR and have someone adversarially check it for
      overclaiming.

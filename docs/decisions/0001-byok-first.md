# ADR 0001 — Implement bring-your-own-key first; pursue provider cooperation in parallel

- **Status:** Accepted as the candidate first deployable path, subject to provider-specific approval,
  counsel review, independent-relay evidence, and the beta gates. The repository also contains a
  pre-launch token-path prototype; that does not make either path public-ready.
- **Decides:** Design document §10 (Existential risk I — Terms of Service)
- **Supersedes:** nothing
- **Revisit when:** a provider offers a sanctioned privacy-preserving access mode, or Phase 2
  telemetry shows BYOK demand is insufficient to sustain a relay network

## Context

Osanwë's original provider-account/network-separation target — *the protocol does not directly give
the provider a user's account, payment identity, or source IP* — requires that the request not carry
the user's own API key. It does not address self-identifying content, writing style, timing, or
operator collusion. The account portion requires pooled access at `mithlond`, where one sanctioned
provider account serves many users.

Pooling keys that way is very likely a violation of provider terms: account sharing,
misrepresentation of the account holder, and circumvention of rate-limiting and abuse controls. The
practical consequence is worse than the legal one. A pooled key can be revoked at any moment,
without notice and without appeal, which makes it a **single point of failure for the entire
network**. A privacy network whose availability depends on a counterparty that has every incentive
to remove it is not a network anyone should build a company on.

Three postures were considered:

| Posture | Direct provider account/IP separation | Durability |
|---|---|---|
| Bring-your-own-key | IP path only; provider still knows the account | Provider-specific review required |
| Cooperative (sanctioned mode) | Both, if operational separation holds; content and timing remain | Slowest, most durable |
| Adversarial (pooled keys, expect bans) | Both until the key dies; content and timing remain | Fragile by construction |

## Decision

**Use bring-your-own-key as the candidate v1 posture after provider-specific approval, counsel
review, independent-relay evidence, and the beta gates. Open the cooperative conversation with
providers in parallel. Treat the adversarial path as a research branch that never becomes the roadmap.**

Under BYOK the user supplies their own API key to the loopback client. The client handles plaintext
locally and holds the TLS session to the provider through a `CONNECT` tunnel, so the relay receives
encrypted content. The provider still knows which account is asking. With a separately operated
relay and no collusion, the intended split keeps the provider from directly receiving the user's
source address; timing and request content can still identify them.

## Rationale

- **It is the least presumptive posture.** Each person uses their own provider account instead of a
  pooled credential. Whether a particular integration complies with provider terms or other legal
  requirements remains provider- and deployment-specific and must be reviewed before use.
- **The partial win is a real win.** Reducing direct IP and location linkage is meaningful on its
  own when the relay is separately operated, while still leaving timing, content, and collusion as
  explicit risks.
- **It unblocks everything else.** Phase 2 builds the relay network, the operator community, the
  directory, the client, and the latency dataset. All of that is a prerequisite for Phase 3
  regardless of which posture eventually wins.
- **It preserves the cooperative option.** Arriving at a provider with a working, carefully bounded,
  well-operated privacy relay is a far stronger position from which to ask for a sanctioned mode
  than arriving with a proposal — or with a history of ban evasion.
- **Providers may have a genuine interest here.** Letting users verify narrow technical properties
  instead of accepting an unqualified privacy label may be reputationally valuable to a model
  vendor, and at least one major platform vendor has deployed a relay of substantially this shape.

## Consequences

**Accepted:**

- v1 does not deliver the headline anonymity claim. Marketing must describe what BYOK actually
  targets — separation of the provider account from the source IP through an independently operated
  encrypted relay — without implying that timing, content, or collusion cannot identify someone.
  The provider still identifies the account. Overclaiming here would be the single most damaging
  thing the project could do to its credibility.
- The `eregion` mint and token machinery are deferred to Phase 3, so the cryptographic work is not
  validated early. Mitigated by prototyping issuance against a mock gateway during Phase 2.
- If no provider ever offers a sanctioned mode, the full claim may never have a sanctioned
  deployment. That is an acceptable outcome: a durable partial-privacy network is worth more than a
  full-privacy network that dies on its first key revocation.

**Enabled:**

- Phase 2 engineering can continue while provider-specific terms and legal review remain explicit
  deployment gates.
- A correctly pinned relay is designed to carry provider TLS ciphertext rather than prompt text;
  operator identity, deployment correctness, and independence still require operational evidence.
- The §10 decision no longer gates Phase 0 or Phase 2 work.

## Follow-up

- [ ] Draft the provider outreach note describing a sanctioned privacy-preserving access mode.
- [ ] Confirm with counsel that BYOK relaying itself raises no terms issue for any target provider.
- [ ] Write the v1 marketing copy against this ADR and have someone adversarially check it for
      overclaiming.

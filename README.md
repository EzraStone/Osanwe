# Osanwë

**An anonymity network for AI inference.** Use frontier models without the provider being able to
link the prompt to a person — and pool contributed hardware to serve open-weight models for those
who would rather trust no one at all.

> In Tolkien's *Ósanwe-kenta*, *ósanwë* is the direct transmission of thought between minds. Its
> central doctrine is that a mind, open by nature, may close itself against intrusion — and that no
> power can rightfully force it open. A prompt is thought in transit. This network is the barrier.

---

## Read the design document

**[→ Full design &amp; threat model](docs/index.html)** — `docs/index.html`

GitHub displays `.html` files as source rather than rendering them. To read it properly, either:

- enable **GitHub Pages** on this repository with source *Deploy from a branch* → `/docs`, or
- clone and open `docs/index.html` in a browser, or
- view it through <https://htmlpreview.github.io/>.

---

## The one-paragraph version

The original idea — contribute DGX Sparks to a Tor-like network and get anonymity with frontier
models — contains two different systems fused into one sentence. Frontier models are closed-weight,
so donated hardware can never run them; in that path a node only forwards bytes. Separating the
two is the design document's main contribution:

| | **System A — the relay** (`osanwe`) | **System B — the compute market** (`erebor`) |
|---|---|---|
| Models | Frontier, closed | Open-weight |
| A node | Forwards encrypted bytes | Executes model layers |
| Hardware | A cheap VPS | The DGX Spark, genuinely |
| Hard problem | Anonymous *authorization* | WAN latency, work verification |
| Field state | **Open** | **Crowded** |

Build **System A first**. And note that routing is the easy half: onion routing hides your IP, but
the request still carries an API key bound to a credit card bound to your name. The real problem is
anonymous authorization, solved with blind signatures rather than circuits.

## Architecture at a glance

Three parties, split so no single one holds both halves of the user's identity.

```
                      eregion (mint)
                   knows who paid, never
                     sees any prompt
                            │
                    blind-signed tokens
                            │
  bearer  ────────►  ranger  ────────►  mithlond  ────────►  provider
  client             relay              exit gateway         Anthropic
                     sees IP,           sees content,        sees content +
                     not content        not who              pooled key only

  └──── identity known here ────┘ │ └──── content readable here ────┘
                                  │
                    no party sits on both sides
```

Security rests on the mint, relay and gateway **not colluding** — the same assumption Apple's
Private Relay makes. It is stated plainly here rather than buried, because a privacy claim whose
assumptions are concealed is a lie with extra steps.

## Components

| Component | Role |
|---|---|
| `bearer` | Client SDK / CLI. Holds tokens, verifies gateway attestation |
| `eregion` | Token mint. Blind-signed issuance; learns billing identity and nothing else |
| `ranger` | Relay node. Sees client IP, never plaintext |
| `mithlond` | Exit gateway. Runs in an attested TEE; sees content, never identity |
| `council` | Directory authority. Signed node descriptors and consensus |
| `erebor` | Compute market for open-weight models (Phase 4) |

## What this does *not* protect against

Stated up front, deliberately:

- **Self-deanonymization through prompt content or writing style.** Unfixable at the network layer.
  Paste your codebase and no number of hops will save you.
- A **global passive adversary** performing end-to-end timing correlation. That needs a mixnet,
  which is incompatible with token streaming.
- **Collusion** between relay and gateway against a targeted user.
- Compromise of your own device.
- **Evading provider safety systems** — a non-goal by intent, not by limitation. Osanwë provides
  anonymity from identity linkage, not freedom from model safety policy.

## Status

Pre-implementation. Nothing is built yet. The immediate next step is **Phase 0**: a throwaway
single-hop proxy measuring time-to-first-token against a direct call. That one number decides
whether this is an interactive chat product or a batch product, and every later phase depends on
the answer.

See [§13 Build phases](docs/index.html) for the full roadmap.

# Abuse policy

**Status:** draft, pre-launch. Requires review by qualified counsel in every jurisdiction where
`eregion` or `mithlond` is operated before Phase 3 ships.

---

## 1. The line this project holds

Osanwë provides **anonymity from identity linkage**. It does **not** provide freedom from model
safety policy.

Those are different products. Only one of them is defensible, and conflating them would poison the
first. Every piece of documentation, marketing copy and support response must hold this line
without hedging.

Concretely:

- We work hard so that no party can determine **who** sent a prompt.
- We do **not** work to make prohibited content easier to generate, and we never strip, degrade,
  proxy around, or otherwise interfere with a provider's safety systems.
- A user who wants to generate prohibited material is not our customer, and the design should make
  Osanwë a worse tool for that purpose than the alternatives, not a better one.

## 2. Why this is existential, not cosmetic

An anonymous, unattributable frontier-model proxy is precisely the tool someone wants for
generating CSAM, malware, targeted harassment, or fraud at scale. If our answer to abuse is "we
cannot tell, we are anonymous," then the network becomes the abuse vector and it will be shut
down — deservedly, and probably quickly.

The failure mode is not gradual. There is no version of this project that survives becoming known
as the CSAM proxy. Abuse resistance therefore belongs in the first commit, not the first incident
response.

## 3. Prohibited uses

Using Osanwë for any of the following is prohibited and will result in refusal of service at the
mint and revocation of unspent tokens:

1. Generating, soliciting, or distributing child sexual abuse material, or any sexualized content
   involving minors.
2. Generating malware, ransomware, exploit code, or intrusion tooling for unauthorized use.
3. Targeted harassment, stalking, doxxing, or coordinated inauthentic behaviour.
4. Fraud, phishing, impersonation of real people or organizations, or generation of fraudulent
   documents and records.
5. Any use prohibited by the terms of the upstream provider being accessed.
6. Any use unlawful in the jurisdiction of the user or of the operating entities.

## 4. What we can and cannot see

Honesty here is load-bearing. Users, operators, regulators and law enforcement all need an accurate
picture, and an inflated claim in either direction damages us.

| Component | Sees content? | Sees identity? | Can detect abuse? |
|---|---|---|---|
| `eregion` (mint) | Never | Billing identity | No — but can refuse to sell |
| `ranger` (relay) | Never — ciphertext only | Client IP | No, by design |
| `mithlond` (gateway) | Yes, inside the TEE | Never | Yes, in-enclave only |
| Upstream provider | Yes | Pooled key only | Yes — their own systems |

The consequence worth internalizing: **for System A (frontier models), the provider's safety
systems remain fully operative.** Osanwë changes who the provider thinks is asking, not what the
provider is willing to answer. Our marginal abuse exposure on that path is real but bounded.

**System B (`erebor`, open-weight models on contributed hardware) is the harder case**, because
there is no provider safety layer behind it. Open weights on donated GPUs means no upstream filter
exists at all, and node operators are running the inference on their own machines. Abuse controls
matter most there, and Phase 4 must not ship without them.

## 5. Controls

| Control | Mechanism | Preserves anonymity? |
|---|---|---|
| **Anonymous rate limiting** | Anonymous credentials with per-epoch spending caps. Enforces limits without learning which user | **Yes** — this is exactly what the primitive is for |
| **Safety systems intact** | Provider-side filtering never stripped, degraded, or proxied around | **Yes** — orthogonal to identity |
| **Mint-level refusal** | `eregion` may decline to sell to a purchaser identity; unspent tokens revocable by key epoch | **Yes** — the mint never learns prompts |
| **Cost as friction** | Tokens are prepaid. Abuse at scale costs real money at real prices | **Yes** |
| **In-enclave classification** (System B) | Attested classifier runs inside `mithlond` / `erebor` nodes; operator cannot see content, client can verify exactly what code runs | **Yes**, with a caveat — see §6 |
| **Published policy + transparency reporting** | This document, plus periodic statistics | n/a |

### Deliberately excluded controls

- **Content logging.** We do not log prompts or completions. A log we hold is a log that can be
  subpoenaed, breached, or abused, and its existence would falsify the product's central claim.
- **Backdoors or key escrow.** We will not build a mechanism for privileged parties to deanonymize
  users. A capability that exists will eventually be used and eventually be compromised.
- **Client-side scanning.** Rejected for the same reasons the wider security community rejected it:
  it converts every user's device into a surveillance endpoint and the false-positive burden lands
  on the innocent.

## 6. In-enclave classification: the honest tradeoff

Running an abuse classifier inside the attested TEE is genuinely elegant — the operator cannot read
your prompt, but prohibited content can still be refused, and because the enclave measurement is
published and reproducible, **users can audit exactly what classification code runs against their
data.** That is strictly more accountable than the status quo, where classifiers are unauditable
server-side black boxes.

It is still a censorship mechanism, and it should be described as one rather than dressed up.

Mitigations we commit to:

- Reproducible builds, so the published measurement can be independently verified.
- The classifier's scope, thresholds and behaviour documented publicly.
- Refusals returned to the user as refusals — never silent degradation of output quality.
- No transmission of the offending content anywhere as a side effect of classification, except
  where §7 legally compels it.

## 7. Legal obligations

**This section is a checklist for counsel, not legal advice, and nothing here should be relied on
until a qualified lawyer has reviewed it.**

- **United States.** Under 18 U.S.C. § 2258A, providers of electronic communication or remote
  computing services must report apparent child sexual abuse material to NCMEC's CyberTipline upon
  obtaining actual knowledge, with penalties for failure to report. The statute imposes a reporting
  duty on knowledge obtained; it does not impose a general affirmative duty to monitor. Counsel must
  determine (a) whether the operating entities fall within the statutory definitions, and (b) what
  "actual knowledge" means for an architecture where the operator provably cannot read content.
- **European Union.** Obligations around detection and reporting have been in active legislative
  flux. Requirements must be confirmed as of the incorporation date, not assumed from prior drafts.
- **United Kingdom.** The Online Safety Act imposes duties on user-to-user and search services;
  whether Osanwë falls within scope requires a determination.
- **Everywhere.** Jurisdiction of incorporation for `eregion` and `mithlond` materially changes the
  obligations, and is one of the open questions in the design document (§15).

**Design implication:** the interaction between a statutory reporting duty and an architecture where
the operator cannot read content is genuinely unsettled, and it must be resolved *before* Phase 3
rather than discovered during an incident. If counsel concludes the duty cannot be satisfied under
the intended design, that is a Phase 3 blocker and the design changes — not the law.

## 8. Law enforcement response

- We respond to valid legal process.
- We will state plainly what we can and cannot produce. By design that is very little: no content
  logs, no mapping from payment to request, no record linking an IP to a prompt.
- We publish the volume and type of requests received in transparency reporting.
- We will not build new capability to satisfy a request. If we cannot produce something, the answer
  is that we cannot produce it.
- A warrant canary should be evaluated by counsel; note that their legal effectiveness is contested.

## 9. Incident response

1. **Triage.** Determine which path (System A or B), which control failed, and whether the conduct
   is ongoing.
2. **Contain.** Revoke by key epoch where necessary. Accept that epoch revocation affects every
   holder in that anonymity set — this is a real cost and must be weighed, not applied reflexively.
3. **Legal.** Engage counsel immediately on any matter touching §7. Reporting duties may be
   time-bound.
4. **Remediate.** Fix the control, not the symptom.
5. **Disclose.** Publish a post-incident report. A privacy project that handles its first incident
   opaquely does not get a second chance at credibility.

## 10. Review

This policy is reviewed at every phase gate and republished whenever a control changes. It must be
public before Phase 3 opens to users — not after.

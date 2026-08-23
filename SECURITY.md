# Security policy

Osanwë handles private prompts, provider credentials, payment entitlements, and anonymous bearer
tokens. Please report security problems privately before publishing details.

## Reporting

Use [GitHub private vulnerability reporting](https://github.com/EzraStone/Osanwe/security/advisories/new).
Include the affected component, an impact description, the smallest safe reproduction, and any
suggested mitigation.

Do not put real prompts, API keys, relay secrets, private keys, payment receipts, invoice IDs,
bearer tokens, unredacted packet captures, or identifying logs in a report. Use generated test
values. If a secret was exposed while investigating, revoke or rotate it immediately.

If private vulnerability reporting is unavailable, open a public issue containing only a request
for a private contact channel. Do not include exploit details in that issue.

## Response

The project will acknowledge a complete report, reproduce it when safe, and agree on a disclosure
window based on severity and whether operators must rotate credentials or rebuild persistent state.
No fixed response-time promise is made while the project has one maintainer.

## Supported versions

Osanwë is pre-release software. Only the current commit on the default branch receives security
fixes. No public gateway, mint, or relay should be assumed production-ready unless a release says
so explicitly.

## Scope priorities

High-priority reports include:

- recovering prompt or answer text from a relay;
- linking a blind issuance transcript to a redeemed token;
- redeeming one payment or token more than once;
- reaching provider administrative endpoints through the gateway;
- leaking provider, relay, mint, or checkout credentials;
- making a local web page spend tokens across origins; and
- bypassing aggregate provider budgets or durable spent-token state.

Privacy-policy disagreements and provider terms questions are important project issues, but are not
security vulnerabilities unless they demonstrate a concrete technical mismatch with a documented
claim.

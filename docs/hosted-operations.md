# Hosted API safeguards and zero-spend operations

Code safeguards are not a hosting bill cap. `/api/chat` and
`/api/providers/check` use bounded reads, deadlines, fixed provider destinations,
sanitized failures, and per-process request/concurrency limits. Requests beyond
the in-memory accounting capacity fail closed until entries expire. These maps
do not synchronize between Vercel instances, survive restarts, or prevent one
person using multiple IP addresses. Same-origin checks are not authentication.

## Shared edge control: pending account configuration

Before broader invitations, inspect the Vercel project's actual plan and billing
settings. Do not upgrade a plan, attach a payment method, accept usage pricing,
or provision a database without Ezra's approval. No such changes were made in
this development batch.

Vercel documents fixed-window WAF rate limiting across plans, including one rule
on Hobby. Its published pricing includes a free request allowance and metered
usage beyond it. A free allowance is not a permanent $0 guarantee.
Source: [Vercel rate limiting](https://vercel.com/docs/vercel-firewall/vercel-waf/rate-limiting).

For a five-person test, configure one rule matching **POST** to either exact path
`/api/chat` or `/api/providers/check`, counting by the platform's client IP, with
a conservative shared allowance of 10 requests per 60 seconds and a 60-second
block. Use the rule builder's supported controls; do not invent headers that
purport to prove edge enforcement. This limit intentionally covers both paths.
Shared Wi-Fi may hit the same limit; document it in tester instructions.

Validate on a protected preview with synthetic missing-key requests: the first
requests reach the app and return 401; exceeding the edge threshold is blocked
before a function/provider call; retry works after expiry. Record rule ID,
configuration date, host, statuses and deployment, never keys or IPs. Verify
deployment protection and production separately. Do not perform a public load test.

Hobby's documented quota behavior pauses features instead of requiring a paid
upgrade; confirm account eligibility and the exact feature's behavior first.
If the account is Pro, alerts alone are insufficient: inspect spend-management
pause behavior and included usage. Do not represent a configurable alert as a
hard $0 cap. If a zero-billing boundary cannot be established, keep broader
distribution blocked and use local verification.
Sources: [Hobby](https://vercel.com/docs/plans/hobby),
[plan limits](https://vercel.com/docs/plans).

## Incident and rollback checklist

Stop new invitations on abnormal usage, credential exposure, missing privacy
headers, or confirmed sandbox escape. Disable affected API paths at the edge
or pause the deployment after resolving the exact target. Ask the affected user
to revoke their key; never collect it for diagnosis. Revert only the responsible
code commit and deploy the reviewed replacement. Re-test connection, ordinary
answer, cancel, no-cache headers, and preview isolation before reopening.

No account-wide spend control, edge rule, or independent production validation
is claimed complete by this document. Billing remains an operator verification
gate, not something application code alone can guarantee.

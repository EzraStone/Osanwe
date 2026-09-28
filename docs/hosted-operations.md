# Hosted API safeguards and zero-spend operations

Code safeguards are not a hosting bill cap. `/api/chat` and
`/api/providers/check` use bounded reads, deadlines, fixed provider destinations,
sanitized failures, and per-process request/concurrency limits. Requests beyond
the in-memory accounting capacity fail closed until entries expire. These maps
do not synchronize between Vercel instances, survive restarts, or prevent one
person using multiple IP addresses. Same-origin checks are not authentication.

## Edge control: enabled, regional enforcement verified

On 2026-09-14 the project account displayed Hobby in its Billing settings. The
existing rule list was empty. One included fixed-window WAF rule was published:

- Name: `Osanwe beta AI request limit`.
- Rule ID: `rule_osanwe_beta_ai_request_limit_e6bktT`.
- Request path matches `^/api/(chat|providers/check)$` AND Method equals `POST`.
- Key: platform client IP; limit: 10 requests per 60-second window; action: 429.
- Scope: project rule, with no hostname or environment exclusions.

A bounded production check at 22:23 UTC alternated 12 credential-free POSTs
between both endpoints. The first ten returned 401, then each endpoint returned
429. GET `/client` remained 200. No provider key was supplied or provider inference
requested. This is one source/region observation, not a distributed load test.
At 22:29 UTC, after the window expired, both credential-free endpoint probes
again returned 401, confirming recovery for the observed source.
Vercel explicitly documents **per-region** counters; calling this a globally
shared request allowance would be incorrect. Protected-preview enforcement and
Hobby non-commercial eligibility remain to be checked before broader use.

No payment method, upgrade, paid add-on, or billable plan was activated by this
work. The observed plan and included allowance are not a permanent $0 guarantee.

## Account review and operating limits

Before broader invitations, inspect the Vercel project's actual plan and billing
settings. Ezra requires zero spending. Do not upgrade a plan, attach a payment
method, accept usage pricing, provision a billable service, or make paid provider
requests. Stop and ask before any operation whose cost is uncertain.

Vercel documents fixed-window WAF rate limiting across plans, including one rule
on Hobby. Its published pricing includes a free request allowance and metered
usage beyond it. A free allowance is not a permanent $0 guarantee.
Source: [Vercel rate limiting](https://vercel.com/docs/vercel-firewall/vercel-waf/rate-limiting).

For a five-person test, configure one rule matching **POST** to either exact path
`/api/chat` or `/api/providers/check`, counting by the platform's client IP, with
a conservative allowance of 10 requests per 60-second fixed window, per IP in
each edge region. Use the rule builder's supported controls; do not invent headers that
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

The enabled edge rule is not an account-wide spend control. Billing remains an
operator verification gate, not something application code alone can guarantee.

## Application emergency pause

The credential-free deployment check in `hosted-reliability.md` verifies static
client/runner policy and the public catalog. It is safe to run without a provider
key. It does not activate or validate the emergency pause described below.

Set the server-only environment variable `OSANWE_HOSTED_API_PAUSED=1` and deploy
that setting to make both AI endpoints return a no-store 503 response before
reading credentials or the request body and before contacting a provider. The
static client and source viewing remain available. There is no browser toggle,
public admin endpoint, or `NEXT_PUBLIC_` variant for this setting.

An absent variable, exactly `0`, or exactly `false` preserves normal operation.
All other configured values, including empty or misspelled values, pause calls.
Reopening requires the operator to correct/remove the setting and deploy again.

This is not an immediate edge kill switch: old deployments and in-flight calls
can remain active until separately stopped. For an urgent incident, use the
scoped edge deny/pause procedure first. The pause does not prevent hosting costs,
revoke provider keys, or cancel requests already sent to a provider. The setting
is implemented and tested locally, but has not been activated in production.

### Credential-free production-build drill

From `web`, run `npm run build` followed by `npm run test:pause`. The drill starts
temporary loopback-only production servers with `1` and a deliberately
misspelled pause value. Both `/api/chat` and `/api/providers/check` must return
no-store 503 responses and `Retry-After: 300` before parsing invalid JSON.
No credentials are provided, so even a broken pause cannot reach a provider.
The client page and public catalog must still load. Each child server is stopped
after its test. CI runs the same drill after its production build.

Both cases passed locally on September 28, 2026. This tests the built application,
not Vercel environment propagation, old deployments, in-flight cancellation,
edge enforcement, or billing. No production setting was changed by the drill.

# Separate release gates

Updated 2026-09-14. Credit expiry never overrides a security gate. Losing an
unused promotional credit is preferable to shipping an unsafe payment path.
Prepared tests, passing fixtures, and written procedures are not live evidence.

## Hosted bring-your-own-key preview

This path processes the visitor's API key, prompt, and answer at the hosting
service and provider. It is not the anonymous relay network.

Before expanding invitations:

1. Record direct and deployed readable-output verification for each advertised
   provider/model. TokenRouter's exact free route is the first candidate; the
   free-only six-request runner is prepared but a local key is still needed.
2. Verify Vercel plan limits, edge throttling, and the zero-billing operating
   boundary. Hobby and an enabled 10-request/60-second per-IP rule were checked
   on September 14. Production returned ten 401s then two edge 429s without
   credentials. The edge counters are regional, not a global spend cap.
   Protected-preview behavior and Hobby eligibility still need confirmation.
   See `hosted-operations.md` for evidence and remaining limits.
3. Keep interactive HTML disabled until the native WebRTC crash is resolved.
   The hosted client now automatically shows combined HTML/CSS/JavaScript as
   inert source, with no generated-document parsing or nested frames. This
   mitigates the hosted crash path; it does not repair the experimental sandbox
   or change the downloadable client. Standalone JavaScript remains available.
4. After items 1-3 are satisfied for the restricted test build, run the five
   observed sessions in `hosted-beta-sessions.md`, fix blockers, and record
   re-tests before expanding access. No completed sessions are recorded.

Do not require a mint/payment audit to test this non-payment hosted path.

## Downloadable relay beta

Require correct gateway/mint build and persistent state, an independently
operated relay, explicit provider permission for any pooled route, invitation
and quota enforcement, revocation/expiry drills, and clean-platform validation
of the exact checksummed downloads. None of these follows merely from hosting
the website. Keep `who-runs-what.md` and `release-candidate.md` authoritative
about actual operators and artifacts.

Complete the matched three-region Phase 0 campaign before making general
interactive-latency claims. Start measurement preparation alongside development;
do not wait for a payment audit. The August +448 ms result remains historical,
one-region evidence; a new provider requires matched new measurements.

## Paid token access

Remain closed until the complete entitlement/signing/checkout path is reviewed
for races, replay, restart recovery, linkage, and rollback. A reviewer must be a
real identified volunteer responding to a scoped public review request, or a
paid reviewer explicitly approved before purchase. No independent reviewer has
been secured; self-review alone does not satisfy this gate.

Paid beta scope is deliberately crypto-only through the implemented BTCPay
integration. Card, cash, and Monero support are not implied by the long-term
vision. Card processing and associated business/compliance decisions are
deferred. Provider permission, payment processing terms, and operational cost
controls must be resolved before accepting money.

## Development status

Implemented in this batch: readable-output checks, bounded upload/response
reads, safe public errors, upstream cancellation, lower per-request output
limits, retained rate accounting under client churn, free-only verification
tooling, preview Stop/recovery tests, native HTTP isolation tests, and safer
Phase 0 evidence/dry-run tooling.

Pending external inputs: locally entered TokenRouter key, remaining hosting checks,
five willing testers, two additional client regions and an authorized relay,
and an independent relay operator. The hosted source-only fallback and operator
pause control have been implemented. Pending code investigation: nested-frame
WebRTC crash before restoring interactive HTML. No live inference, new cloud provisioning, payments, volunteer
sessions, or new regional measurements are claimed by this status.

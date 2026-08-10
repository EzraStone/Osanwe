# Phase 0 results — latency measurement

**Status: IN PROGRESS (started 2026-08-10).** Offline adapter validation passes. Live baseline and
relayed measurements are pending a provider credential and relay access. This file remains the
Phase 0 exit criterion until it contains three-region numbers and a written verdict.

Run the harness per [`tools/README.md`](../tools/README.md) from at least three client regions and
paste the emitted Markdown tables below.

---

## The question

From design document §9:

> If the single-hop path lands within ~150 ms of baseline, Osanwë is a chat product. If it does
> not, it is an async and agentic-workload product — still valuable, but a different positioning,
> a different UI and a different go-to-market.

| p95 TTFT overhead | Verdict |
|---|---|
| < 150 ms | **PASS** — viable for interactive chat |
| 150–400 ms | **MARGINAL** — hardened opt-in tier only; re-measure with latency-weighted selection |
| > 400 ms | **FAIL for chat** — reposition toward async and agentic workloads |

## Conditions

Record these. Results are uninterpretable without them.

| | |
|---|---|
| Date | Started 2026-08-10 |
| Client regions | _pending_ |
| Relay region(s) | Planned: `us-west1-b`, using the existing closed gateway VM |
| Relay instance type | `e2-micro` (2 vCPUs, 1 GB RAM), 20 GB standard persistent disk |
| Provider / model | Planned: Gemini / `gemini-3.1-flash-lite` free tier; confirm at run time |
| Harness revision | `a05fabe` (provider preset and documentation; measurements still pending) |

## Results

<!-- Paste harness output below, one section per client region. -->

_pending_

## Verdict

_pending_ — state PASS / MARGINAL / FAIL, and the resulting product positioning decision, in prose.
This paragraph is what Phase 1 onward is built on, so write it as a decision and not as a summary
of the numbers.

## Relay blindness evidence

Per §14, capture traffic at the relay during the run and confirm no plaintext prompt fragment is
present. Record the capture command, the search performed, and the result.

_pending_

## Notes and anomalies

Record failed trials, retries, provider-side errors, and anything that would make a reader
distrust the numbers. A measurement whose caveats are hidden is worse than no measurement.

- 2026-08-10: All recorded SSE adapter fixtures passed locally for Messages, OpenAI-compatible
  chat, and Gemini formats; all provider presets produced structurally valid requests.
- 2026-08-10: The previous Gemini default, `gemini-2.0-flash`, had been shut down by the provider
  on 2026-06-01. The preset was updated before collecting measurements so no result depends on a
  dead model ID.
- No supported provider credential was present in the local process environment at the start of
  the campaign. No live inference request has been made yet.
- The local machine does not have the Google Cloud CLI installed, and the available browser
  session was initially not signed into Google. No cloud resource, firewall rule, or billing
  setting was created or changed during setup.
- 2026-08-10 read-only inventory: the existing VM is running in `us-west1-b` as an `e2-micro`
  with a 20 GB standard persistent disk. HTTP and HTTPS firewall toggles are off. The August 1-10
  billing report showed $0.43 gross usage fully offset by savings, for $0.00 net cost at inspection.
- A dedicated Gemini auth key created during setup was revoked before use after its value appeared
  in diagnostic browser output. No live request used it and no copy was stored locally. A manually
  created replacement is required before the baseline run.

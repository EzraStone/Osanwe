# Phase 0 results — latency measurement

**Status: NOT YET RUN.** This file is the Phase 0 exit criterion. Until it contains real numbers
and a written verdict, no later phase should begin.

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
| Date | _pending_ |
| Client regions | _pending_ |
| Relay region(s) | _pending_ |
| Relay instance type | _pending_ |
| Provider / model | _pending_ |
| Harness revision | _commit sha_ |

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

_pending_

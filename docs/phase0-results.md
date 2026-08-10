# Phase 0 results — latency measurement

**Status: IN PROGRESS (started 2026-08-10).** Offline adapter validation and the first live
client/relay route pass operationally. Two more independent client-region measurements remain.
The first route exceeds the interactive-chat latency budget, but this file remains the Phase 0
exit criterion until it contains three-region numbers and a final written verdict.

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
| Client regions | 1 of 3 complete: Chicago-area workstation; two independent regions pending |
| Relay region(s) | `us-west1-b`, using the existing gateway VM with a temporary authenticated CONNECT relay |
| Relay instance type | `e2-micro` (2 vCPUs, 1 GB RAM), 20 GB standard persistent disk |
| Provider / model | Groq free plan / `llama-3.1-8b-instant` |
| Harness revision | `5c84b2f` for the recorded run; `dbe43c2` records the verified 2.1-second free-tier default pacing |

## Results

### Chicago-area workstation baseline

`llama-3.1-8b-instant` via **Groq** · 30 direct runs · max_tokens=128 · reused connection ·
2026-08-10 16:38:43 CDT

| Metric | Direct |
|---|---:|
| **TTFT p50** | 183 ms |
| **TTFT p95** | 318 ms |
| TTFT p99 | 446 ms |
| TTFT mean | 200 ms |
| Total p50 | 203 ms |

All 30 recorded trials succeeded.

### Chicago-area workstation via `us-west1-b`

`llama-3.1-8b-instant` via **Groq** · 30 interleaved runs per arm · max_tokens=128 · reused
connection · 2026-08-10 17:08:24 CDT

| Metric | Direct | Proxied | Delta |
|---|---:|---:|---:|
| **TTFT p50** | 163 ms | 607 ms | +444 ms |
| **TTFT p95** | 273 ms | 722 ms | +448 ms |
| TTFT p99 | 295 ms | 730 ms | +435 ms |
| TTFT mean | 173 ms | 626 ms | +453 ms |
| Inter-token p50 | 0 ms | 0 ms | 0 ms |
| Inter-token p95 | 5 ms | 6 ms | +1 ms |
| Total p50 | 179 ms | 688 ms | +509 ms |

All 60 recorded trials (30 per arm) succeeded. The harness interleaved direct and proxied
requests and paused 2.1 seconds after every request to remain inside the provider's free-plan
rate limit.

## Verdict

**Provisional: FAIL for interactive chat on the measured route.** Its p95 TTFT overhead is
448 ms, above the 400 ms Phase 0 failure boundary. Unless the two remaining regions materially
contradict this result or relay placement removes the overhead, position the first product around
async and agentic workloads rather than interactive chat. The final decision remains pending the
required three-region dataset.

## Relay blindness evidence

Per §14, capture traffic at the relay during the run and confirm no plaintext prompt fragment is
present. Record the capture command, the search performed, and the result.

During the 30-pair interleaved run, the relay captured TCP traffic on port 8080 with:

```sh
sudo timeout 420 tcpdump -i any -w /tmp/osanwe-phase0/phase0.pcap 'port 8080'
```

The capture was 644,427 bytes with SHA-256
`a9aed346d3826fc45b39f65070c97d7a02b10a8fb23e2f00b1e0fa6b7eea8541`. A binary-safe fixed-string
search (`grep -aF`) found no occurrence of the fixed prompt fragment
`List the first twelve prime numbers`. The capture, relay process, relay files, and temporary
firewall rule were removed after verification.

## Notes and anomalies

Record failed trials, retries, provider-side errors, and anything that would make a reader
distrust the numbers. A measurement whose caveats are hidden is worse than no measurement.

- 2026-08-10: All recorded SSE adapter fixtures passed locally for Messages, OpenAI-compatible
  chat, and Gemini formats; all provider presets produced structurally valid requests.
- 2026-08-10: The previous Gemini default, `gemini-2.0-flash`, had been shut down by the provider
  on 2026-06-01. The preset was updated before collecting measurements so no result depends on a
  dead model ID.
- The campaign switched from the planned Gemini provider to Groq after creating a seven-day,
  dedicated Phase 0 key. The key is stored only in a gitignored local environment file and was
  never committed or printed by the harness.
- The first proxied smoke attempt timed out because the temporary firewall rule was initially
  restricted to the automation runtime's address rather than the workstation's address. The rule
  was corrected to the workstation's `/32` before any successful relayed measurement.
- The first remote relay start hit GCP's one-time SSH-key propagation delay. A readiness check was
  tightened to require an explicit marker and a live listening socket before measurements resumed.
- Five successful smoke pairs exposed a Windows console encoding error after measurement but
  before JSON output. Commit `5c84b2f` replaces the unsupported Unicode minus sign with ASCII and
  adds a cp1252 regression test; the recorded 30-pair dataset ran after that fix.
- 2026-08-10 read-only inventory: the existing VM is running in `us-west1-b` as an `e2-micro`
  with a 20 GB standard persistent disk. HTTP and HTTPS firewall toggles are off. The August 1-10
  billing report showed $0.43 gross usage fully offset by savings, for $0.00 net cost at inspection.
- A dedicated Gemini auth key created during setup was revoked before use after its value appeared
  in diagnostic browser output. No live request used it and no copy was stored locally. A manually
  created replacement was not required because the campaign moved to Groq.

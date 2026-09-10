# Phase 0: completing the evidence without buying infrastructure

Status: PREPARATION ONLY, 2026-09-09. No new regional measurements were made.
The recorded Chicago/Groq result remains historical and provisional. A switch
to TokenRouter requires a new matched campaign in all three regions; do not
combine its measurements with August's Groq numbers as one dataset.

Run this work alongside provider validation, not after payment review. First
validate the free model with a small synthetic check. The six-request web test
allowance does **not** authorize a full latency campaign. Obtain separate
free-only approval and confirm the current account quota before running one.

## Zero-network preparation

```text
python tools/phase0_latency.py --self-test
python tools/phase0_latency.py --provider tokenrouter --runs 30 --max-tokens 128 --proxy http://relay.example:8080 --client-region chicago --relay-region us-west --dry-run
```

The dry run sends nothing, reads no key, and reports **62 requests per region**:
30 measured pairs plus one warmup per arm. Three regions therefore require 186
requests if every warmup succeeds. Output ceilings are not a spending cap.

## Needed supplies

1. Three real client regions: the existing local workstation and two volunteers
   on existing machines, ideally Europe and another North American or Asian
   region. A location label is not evidence of where a machine actually ran.
2. An authorized existing relay host, with its actual owner, region, and firewall
   checked before reuse. An old VM description is not proof that it is still
   running, safe, or free. Do not create new instances to use expiring credits.
3. A dedicated short-lived free-model key entered privately on the test machines.
   Do not distribute a personal general-purpose key or paste it in a command line.
4. A verified allowance large enough for the campaign and pacing agreed with the
   provider's current quota. The 3-second preset is a starting delay, not proof of
   entitlement. Stop for 401/402/403/429 or changed pricing; no paid fallback.

A volunteer client is not necessarily an independent relay operator. Recruit
the independent operator separately; no willing operators currently exist.

## Matched protocol

Use the same harness commit, provider endpoint, exact model, fixed synthetic
prompt, output ceiling, relay placement, warm/cold choice, delay, and run count.
Record coarse client/relay regions, OS, timestamp, successful/failed counts and
the redacted JSON evidence under ignored `results/`. Keep credentials out of
labels, paths, screenshots, and metadata. Review even redacted results before
publishing them; timing and endpoints are still metadata.

Report each region separately: direct p50/p95, relayed p50/p95, and the difference
of p95s. This statistic is not the p95 of per-pair differences. Report failures
and sample size rather than silently discarding a bad region. Investigate any
failed trials before deciding. Do not declare a final verdict from one region
or pool unmatched campaigns to obtain a favorable number.

Apply the existing thresholds consistently: <150 ms PASS, 150 to <400 ms MARGINAL,
and >=400 ms FAIL for chat on that route. Review negative results explicitly;
they may favor asynchronous workflows but do not themselves prove that a full
relay/gateway product is usable. The CONNECT harness measures one hop, not blind
token authorization or a multi-hop network.

After measurement, stop the throwaway relay and revoke test credentials. Packet
captures are optional private artifacts: capture only synthetic test traffic on
an authorized host, minimize metadata, and never publish a raw capture without
review. Confirming absence of prompt plaintext is not proof against timing
correlation, collusion, or provider access to the prompt.

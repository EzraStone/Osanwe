# Phase 0 tooling

Two throwaway programs whose only job is to answer the question that gates the whole project
(design document §9):

> Is Osanwë an interactive chat product, or a batch product?

Neither is a prototype of anything. `throwaway_relay.py` is not an early `ranger`, and none of this
code should survive into Phase 2.

| File | Role |
|---|---|
| `throwaway_relay.py` | Minimal HTTP `CONNECT` relay. Runs on a VPS in another region |
| `phase0_latency.py` | Interleaved A/B latency harness. Runs on your machine |

## Running the measurement

**1. On a VPS in a different region from you.** Pick somewhere with a plausible network path to the
provider — not somewhere exotic, since you are measuring a realistic deployment.

```bash
SECRET=$(openssl rand -hex 24); echo "$SECRET"

python3 throwaway_relay.py \
    --port 8080 \
    --secret "$SECRET" \
    --allow api.anthropic.com
```

Destinations are default-deny and authentication is mandatory — the relay refuses to start as an
open proxy. That is deliberate: an unauthenticated `CONNECT` proxy on a public IP is found by
scanners within hours and becomes someone else's abuse relay. **Shut it down when the measurement
is finished.**

**2. On your machine.**

```bash
export ANTHROPIC_API_KEY=sk-ant-...
pip install requests

# Control arm first, to see this machine's baseline:
python3 phase0_latency.py --runs 20

# The real measurement:
python3 phase0_latency.py --runs 30 \
    --proxy "http://relay:$SECRET@vps.example.com:8080" \
    --label "eu-west-1" \
    --json ../results/eu-west-1.json
```

**3. Repeat from at least three client regions**, per §9, and paste the emitted Markdown tables into
`docs/phase0-results.md`.

## Reading the output

The harness prints a Markdown table and a verdict against the §9 budget:

| p95 TTFT overhead | Verdict |
|---|---|
| < 150 ms | **PASS** — viable for interactive chat |
| 150–400 ms | **MARGINAL** — hardened opt-in tier only; re-measure with latency-weighted selection |
| > 400 ms | **FAIL for chat** — reposition toward async and agentic workloads |

## Why the harness is built the way it is

- **Interleaved arms.** Trials alternate direct/proxied rather than running in two blocks. Provider
  load drifts over minutes; interleaving makes that drift hit both arms equally instead of biasing
  whichever ran second. This is the single most important methodological choice here.
- **Percentiles, not means.** A privacy tool with a good median and a bad tail is a bad product,
  because the tail is what users remember. p95 is the number that decides.
- **Warm by default, `--cold` available.** Connection setup amortizes across a session; per-request
  overhead does not. Only the latter belongs in the steady-state budget.
- **A discarded warmup run.** The first request pays TLS handshake and provider cold-start, which
  would otherwise land entirely on whichever arm went first.
- **Fixed prompt, model and `max_tokens`, `temperature=0`.** Provider-side variance will swamp the
  signal otherwise.

## A second, free result

While the relay is running, take a packet capture on it:

```bash
sudo tcpdump -i any -w phase0.pcap 'port 8080'
```

Then confirm the capture contains no plaintext prompt fragment. That is the **relay blindness**
property from §14, it is the claim users will care about most, and it is the easiest one to
demonstrate convincingly. Getting the evidence now costs nothing.

## Limitations

- Single provider (Anthropic Messages API). Add others by changing `--base-url` and adjusting the
  SSE parsing in `run_trial`.
- Measures one relay hop. Multi-hop needs relay chaining, which is Phase 2 work.
- The relay is single-process and thread-per-connection. Fine for tens of sequential trials,
  useless as a load test — and load is not what Phase 0 is asking about.

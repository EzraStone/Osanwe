#!/usr/bin/env python3
"""
Phase 0 latency harness for Osanwë.

Measures the cost of relaying a streaming LLM request through a proxy hop, so the
project can answer its gating question before any protocol code is written:

    Is Osanwë an interactive chat product, or a batch product?

Design document §9 sets the bar: if a single relay hop lands within ~150 ms of the
direct baseline at p95, Osanwë is a chat product. If it does not, the positioning,
the UI and the go-to-market all change.

Methodology notes that matter:

  * Trials are INTERLEAVED (direct, proxied, direct, proxied, ...) rather than run
    in two blocks. Provider-side load drifts over minutes; interleaving makes that
    drift affect both arms equally instead of biasing whichever arm ran later.
  * Percentiles are reported, not just means. A privacy tool with a good median and
    a bad tail is a bad product, because the tail is what users remember.
  * Connection setup is separated from steady-state cost. Setup amortizes across a
    session; per-request overhead does not. Only the latter belongs in the budget.
  * Prompt, model and max_tokens are held fixed. Provider-side variance will
    otherwise swamp the signal being measured.

Usage:

    export ANTHROPIC_API_KEY=sk-ant-...

    # Baseline only, to establish the control from this machine:
    python3 tools/phase0_latency.py --runs 20

    # The real measurement, against a relay in another region:
    python3 tools/phase0_latency.py --runs 30 \\
        --proxy http://198.51.100.7:8080 \\
        --label "eu-west-1 ranger" \\
        --json results/eu-west-1.json

Run it from at least three client regions against the provider, per §9, and commit
the resulting tables to docs/phase0-results.md.

Dependency: requests  (pip install requests)
"""

from __future__ import annotations

import argparse
import json
import os
import statistics
import sys
import time
from dataclasses import dataclass, field, asdict

try:
    import requests
except ImportError:
    sys.exit("This harness needs `requests`. Install it with:  pip install requests")


DEFAULT_BASE_URL = "https://api.anthropic.com"
DEFAULT_MODEL = "claude-sonnet-5"
API_VERSION = "2023-06-01"

# Held fixed across every trial. Long enough to produce a usable inter-token
# distribution, short enough that a 30-run interleaved sweep stays cheap.
FIXED_PROMPT = (
    "List the first twelve prime numbers, one per line, with no commentary."
)


# --------------------------------------------------------------------------- #
# measurement
# --------------------------------------------------------------------------- #


@dataclass
class Trial:
    """One streamed completion, timed."""

    arm: str                      # "direct" or "proxied"
    ttft_ms: float                # request sent -> first text token
    total_ms: float               # request sent -> stream closed
    inter_token_ms: list[float] = field(default_factory=list)
    tokens: int = 0
    error: str | None = None

    @property
    def ok(self) -> bool:
        return self.error is None


def run_trial(
    arm: str,
    session: requests.Session,
    *,
    base_url: str,
    api_key: str,
    model: str,
    max_tokens: int,
    proxies: dict | None,
    timeout: float,
) -> Trial:
    """Issue one streaming request and time it."""
    body = {
        "model": model,
        "max_tokens": max_tokens,
        "temperature": 0,
        "stream": True,
        "messages": [{"role": "user", "content": FIXED_PROMPT}],
    }
    headers = {
        "x-api-key": api_key,
        "anthropic-version": API_VERSION,
        "content-type": "application/json",
        "accept": "text/event-stream",
    }

    started = time.perf_counter()
    first_token_at: float | None = None
    last_token_at: float | None = None
    gaps: list[float] = []
    tokens = 0

    try:
        resp = session.post(
            f"{base_url.rstrip('/')}/v1/messages",
            headers=headers,
            json=body,
            stream=True,
            proxies=proxies,
            timeout=timeout,
        )
        if resp.status_code != 200:
            detail = resp.text[:200].replace("\n", " ")
            return Trial(arm, 0.0, 0.0, error=f"HTTP {resp.status_code}: {detail}")

        for raw in resp.iter_lines(decode_unicode=True):
            if not raw or not raw.startswith("data: "):
                continue
            payload = raw[6:]
            if payload == "[DONE]":
                break
            try:
                event = json.loads(payload)
            except json.JSONDecodeError:
                continue

            if event.get("type") != "content_block_delta":
                continue
            if event.get("delta", {}).get("type") != "text_delta":
                continue

            now = time.perf_counter()
            tokens += 1
            if first_token_at is None:
                first_token_at = now
            else:
                gaps.append((now - last_token_at) * 1000.0)
            last_token_at = now

        finished = time.perf_counter()

    except requests.RequestException as exc:
        return Trial(arm, 0.0, 0.0, error=f"{type(exc).__name__}: {exc}")

    if first_token_at is None:
        return Trial(arm, 0.0, 0.0, error="stream produced no text tokens")

    return Trial(
        arm=arm,
        ttft_ms=(first_token_at - started) * 1000.0,
        total_ms=(finished - started) * 1000.0,
        inter_token_ms=gaps,
        tokens=tokens,
    )


# --------------------------------------------------------------------------- #
# statistics
# --------------------------------------------------------------------------- #


def pct(values: list[float], p: float) -> float:
    """Nearest-rank percentile. Stable for the small n this harness produces."""
    if not values:
        return float("nan")
    ordered = sorted(values)
    k = max(0, min(len(ordered) - 1, int(round(p / 100.0 * len(ordered) + 0.5)) - 1))
    return ordered[k]


def summarize(trials: list[Trial], arm: str) -> dict:
    ok = [t for t in trials if t.arm == arm and t.ok]
    failed = [t for t in trials if t.arm == arm and not t.ok]
    if not ok:
        return {"arm": arm, "n": 0, "failed": len(failed)}

    ttfts = [t.ttft_ms for t in ok]
    totals = [t.total_ms for t in ok]
    gaps = [g for t in ok for g in t.inter_token_ms]

    return {
        "arm": arm,
        "n": len(ok),
        "failed": len(failed),
        "ttft_p50": pct(ttfts, 50),
        "ttft_p95": pct(ttfts, 95),
        "ttft_p99": pct(ttfts, 99),
        "ttft_mean": statistics.fmean(ttfts),
        "total_p50": pct(totals, 50),
        "total_p95": pct(totals, 95),
        "intertoken_p50": pct(gaps, 50) if gaps else float("nan"),
        "intertoken_p95": pct(gaps, 95) if gaps else float("nan"),
        "tokens_mean": statistics.fmean([t.tokens for t in ok]),
    }


def ms(x: float) -> str:
    return "—" if x != x else f"{x:,.0f}"  # NaN-safe


def render_markdown(direct: dict, proxied: dict | None, meta: dict) -> str:
    lines: list[str] = []
    lines.append(f"### {meta['label']}")
    lines.append("")
    arms_note = "interleaved runs per arm" if proxied else "runs"
    lines.append(
        f"`{meta['model']}` · {meta['runs']} {arms_note} · "
        f"max_tokens={meta['max_tokens']} · "
        f"{'reused connection' if meta['warm'] else 'fresh connection per request'} · "
        f"{meta['timestamp']}"
    )
    lines.append("")
    lines.append("| Metric | Direct | Proxied | Delta |")
    lines.append("|---|---:|---:|---:|")

    def row(name: str, key: str) -> str:
        d = direct.get(key, float("nan"))
        if not proxied:
            return f"| {name} | {ms(d)} ms | — | — |"
        p = proxied.get(key, float("nan"))
        delta = p - d
        sign = "+" if delta >= 0 else "−"
        return f"| {name} | {ms(d)} ms | {ms(p)} ms | {sign}{ms(abs(delta))} ms |"

    lines.append(row("**TTFT p50**", "ttft_p50"))
    lines.append(row("**TTFT p95**", "ttft_p95"))
    lines.append(row("TTFT p99", "ttft_p99"))
    lines.append(row("TTFT mean", "ttft_mean"))
    lines.append(row("Inter-token p50", "intertoken_p50"))
    lines.append(row("Inter-token p95", "intertoken_p95"))
    lines.append(row("Total p50", "total_p50"))
    lines.append("")

    if proxied:
        overhead = proxied.get("ttft_p95", float("nan")) - direct.get("ttft_p95", float("nan"))
        if overhead != overhead:
            verdict = "**INCONCLUSIVE** — insufficient successful trials."
        elif overhead < 150:
            verdict = (
                f"**PASS** — p95 TTFT overhead of {overhead:,.0f} ms is within the 150 ms budget. "
                "Osanwë is viable as an interactive chat product."
            )
        elif overhead < 400:
            verdict = (
                f"**MARGINAL** — p95 TTFT overhead of {overhead:,.0f} ms exceeds the 150 ms chat "
                "budget but fits the hardened opt-in tier. Chat viability is borderline; re-measure "
                "with latency-weighted relay selection before deciding."
            )
        else:
            verdict = (
                f"**FAIL for chat** — p95 TTFT overhead of {overhead:,.0f} ms. Reposition toward "
                "async and agentic workloads, where TTFT matters far less (§9)."
            )
        lines.append(f"> {verdict}")
        lines.append("")

    failures = direct.get("failed", 0) + (proxied.get("failed", 0) if proxied else 0)
    if failures:
        lines.append(f"> ⚠️ {failures} trial(s) failed and were excluded. Investigate before trusting these numbers.")
        lines.append("")

    return "\n".join(lines)


# --------------------------------------------------------------------------- #
# entry point
# --------------------------------------------------------------------------- #


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Phase 0 latency harness — measures relay overhead on streaming LLM requests.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--runs", type=int, default=20, help="trials per arm (default: 20)")
    ap.add_argument("--proxy", help="proxy URL, e.g. http://host:8080. Omitted = baseline only")
    ap.add_argument("--label", default=None, help="label for the results table")
    ap.add_argument("--model", default=DEFAULT_MODEL, help=f"model id (default: {DEFAULT_MODEL})")
    ap.add_argument("--base-url", default=DEFAULT_BASE_URL, help="provider base URL")
    ap.add_argument("--max-tokens", type=int, default=128, help="max_tokens (default: 128)")
    ap.add_argument("--timeout", type=float, default=60.0, help="per-request timeout in seconds")
    ap.add_argument("--cold", action="store_true",
                    help="fresh connection per request, measuring setup cost too "
                         "(default: reuse the connection, measuring steady state)")
    ap.add_argument("--json", dest="json_out", help="also write raw results to this JSON path")
    args = ap.parse_args()

    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if not api_key:
        return _fail("ANTHROPIC_API_KEY is not set.")
    if args.runs < 5:
        return _fail("Use at least 5 runs per arm; percentiles are meaningless below that.")

    proxies = {"http": args.proxy, "https": args.proxy} if args.proxy else None
    arms = ["direct"] + (["proxied"] if proxies else [])
    warm = not args.cold

    label = args.label or (f"via {args.proxy}" if args.proxy else "baseline, no proxy")

    print(f"Osanwë Phase 0 — {label}", file=sys.stderr)
    print(f"  model={args.model}  runs={args.runs}/arm  "
          f"{'warm' if warm else 'cold'} connections", file=sys.stderr)
    if not proxies:
        print("  no --proxy given: establishing the control arm only", file=sys.stderr)
    print(file=sys.stderr)

    sessions = {arm: requests.Session() for arm in arms}

    def do(arm: str) -> Trial:
        sess = sessions[arm] if warm else requests.Session()
        return run_trial(
            arm, sess,
            base_url=args.base_url, api_key=api_key, model=args.model,
            max_tokens=args.max_tokens,
            proxies=proxies if arm == "proxied" else None,
            timeout=args.timeout,
        )

    # Discarded warmup: first request pays TLS handshake and provider cold-start.
    print("  warmup...", file=sys.stderr)
    for arm in arms:
        w = do(arm)
        if not w.ok:
            return _fail(f"warmup failed on the {arm} arm: {w.error}")

    trials: list[Trial] = []
    for i in range(args.runs):
        for arm in arms:                       # interleaved, deliberately
            t = do(arm)
            trials.append(t)
            mark = f"{t.ttft_ms:>7,.0f} ms" if t.ok else "  FAILED"
            print(f"  [{i + 1:>3}/{args.runs}] {arm:<8} {mark}", file=sys.stderr)

    print(file=sys.stderr)

    direct = summarize(trials, "direct")
    proxied = summarize(trials, "proxied") if proxies else None

    meta = {
        "label": label,
        "model": args.model,
        "runs": args.runs,
        "max_tokens": args.max_tokens,
        "warm": warm,
        "proxy": args.proxy,
        "timestamp": time.strftime("%Y-%m-%d %H:%M:%S %Z"),
    }

    print(render_markdown(direct, proxied, meta))

    if args.json_out:
        os.makedirs(os.path.dirname(os.path.abspath(args.json_out)), exist_ok=True)
        with open(args.json_out, "w", encoding="utf-8") as fh:
            json.dump(
                {"meta": meta, "direct": direct, "proxied": proxied,
                 "trials": [asdict(t) for t in trials]},
                fh, indent=2,
            )
        print(f"\nRaw results written to {args.json_out}", file=sys.stderr)

    return 0


def _fail(msg: str) -> int:
    print(f"error: {msg}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())

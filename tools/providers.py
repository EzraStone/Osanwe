"""Provider adapters for the Phase 0 latency harness.

Nearly every hosted model API is one of three wire formats, so there are three
adapters here rather than one per vendor:

  messages   Anthropic's /v1/messages, deltas in content_block_delta events
  chat       OpenAI's /v1/chat/completions, deltas in choices[].delta.content.
             DeepSeek, GLM, Groq, OpenRouter, Together, Fireworks, Cerebras,
             xAI and Ollama all speak this, differing only in base URL,
             credential and model name
  gemini     Google's :streamGenerateContent?alt=sse, deltas in
             candidates[].content.parts[].text

Phase 0 measures the DIFFERENCE between a direct request and a relayed one
against the same endpoint. The relay only ever sees ciphertext, so relay
overhead does not depend on which provider is behind it. That means a free
provider is a perfectly valid instrument for the measurement, even if the
absolute baseline it reports would not transfer to a different vendor.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Callable


@dataclass(frozen=True)
class Provider:
    """How to talk to one provider, and how to read its stream."""

    name: str
    fmt: str          # messages | chat | gemini
    base_url: str
    model: str
    key_env: str | None       # None means no credential is needed, e.g. Ollama
    notes: str = ""

    # Set for providers whose free tier is aggressively rate limited, so the
    # harness can pace itself instead of collecting a run full of 429s.
    suggested_delay: float = 0.0


# The registry. Adding a vendor that speaks an existing format is one line.
PROVIDERS: dict[str, Provider] = {
    "anthropic": Provider(
        "anthropic", "messages", "https://api.anthropic.com",
        "claude-sonnet-5", "ANTHROPIC_API_KEY",
        "prepaid credit, no free tier"),

    "openai": Provider(
        "openai", "chat", "https://api.openai.com",
        "gpt-4o-mini", "OPENAI_API_KEY",
        "prepaid credit"),

    "gemini": Provider(
        "gemini", "gemini", "https://generativelanguage.googleapis.com",
        "gemini-2.0-flash", "GEMINI_API_KEY",
        "free tier, no card required", suggested_delay=1.0),

    "deepseek": Provider(
        "deepseek", "chat", "https://api.deepseek.com",
        "deepseek-chat", "DEEPSEEK_API_KEY",
        "prepaid credit, inexpensive"),

    "glm": Provider(
        "glm", "chat", "https://open.bigmodel.cn/api/paas/v4",
        "glm-4-flash", "ZHIPUAI_API_KEY",
        "glm-4-flash is free", suggested_delay=0.5),

    "groq": Provider(
        "groq", "chat", "https://api.groq.com/openai",
        "llama-3.1-8b-instant", "GROQ_API_KEY",
        "free tier, rate limited", suggested_delay=1.0),

    "openrouter": Provider(
        "openrouter", "chat", "https://openrouter.ai/api",
        "meta-llama/llama-3.1-8b-instruct", "OPENROUTER_API_KEY",
        "some models free"),

    "together": Provider(
        "together", "chat", "https://api.together.xyz",
        "meta-llama/Llama-3.1-8B-Instruct-Turbo", "TOGETHER_API_KEY"),

    "fireworks": Provider(
        "fireworks", "chat", "https://api.fireworks.ai/inference",
        "accounts/fireworks/models/llama-v3p1-8b-instruct", "FIREWORKS_API_KEY"),

    "cerebras": Provider(
        "cerebras", "chat", "https://api.cerebras.ai",
        "llama3.1-8b", "CEREBRAS_API_KEY",
        "free tier, rate limited", suggested_delay=1.0),

    "xai": Provider(
        "xai", "chat", "https://api.x.ai",
        "grok-2-latest", "XAI_API_KEY"),

    "mistral": Provider(
        "mistral", "chat", "https://api.mistral.ai",
        "mistral-small-latest", "MISTRAL_API_KEY"),

    "ollama": Provider(
        "ollama", "chat", "http://localhost:11434",
        "llama3.1", None,
        "local, no credential; note bearer requires an https upstream"),
}


def resolve(name: str | None, base_url: str | None) -> Provider:
    """Pick a provider by name, or infer one from a base URL."""
    if name:
        key = name.strip().lower()
        if key not in PROVIDERS:
            raise SystemExit(
                f"unknown provider {name!r}. Known: {', '.join(sorted(PROVIDERS))}\n"
                f"For anything else OpenAI-compatible, use --provider openai --base-url <url>."
            )
        return PROVIDERS[key]

    if base_url:
        host = base_url.lower()
        for p in PROVIDERS.values():
            if p.base_url.lower().split("//", 1)[-1].split("/", 1)[0] in host:
                return p
        # An unrecognised endpoint is far more likely to be OpenAI-compatible
        # than anything else, so that is the least surprising default.
        return PROVIDERS["openai"]

    return PROVIDERS["anthropic"]


# --------------------------------------------------------------------------- #
# request construction
# --------------------------------------------------------------------------- #


def build_request(p: Provider, base_url: str, model: str, prompt: str,
                  max_tokens: int, api_key: str | None) -> tuple[str, dict, dict]:
    """Return (url, headers, json_body) for a streaming completion."""
    base = base_url.rstrip("/")
    headers = {"content-type": "application/json", "accept": "text/event-stream"}

    if p.fmt == "messages":
        if api_key:
            headers["x-api-key"] = api_key
        headers["anthropic-version"] = "2023-06-01"
        return (
            f"{base}/v1/messages",
            headers,
            {
                "model": model,
                "max_tokens": max_tokens,
                "temperature": 0,
                "stream": True,
                "messages": [{"role": "user", "content": prompt}],
            },
        )

    if p.fmt == "chat":
        if api_key:
            headers["authorization"] = f"Bearer {api_key}"
        return (
            f"{base}/v1/chat/completions",
            headers,
            {
                "model": model,
                "max_tokens": max_tokens,
                "temperature": 0,
                "stream": True,
                "messages": [{"role": "user", "content": prompt}],
            },
        )

    if p.fmt == "gemini":
        if api_key:
            headers["x-goog-api-key"] = api_key
        return (
            f"{base}/v1beta/models/{model}:streamGenerateContent?alt=sse",
            headers,
            {
                "contents": [{"role": "user", "parts": [{"text": prompt}]}],
                "generationConfig": {"temperature": 0, "maxOutputTokens": max_tokens},
            },
        )

    raise SystemExit(f"unsupported format {p.fmt!r}")


# --------------------------------------------------------------------------- #
# stream parsing
# --------------------------------------------------------------------------- #

# Every format terminates differently, and getting this wrong shows up as a
# phantom extra token or a hang rather than an obvious error.
DONE_SENTINEL = "[DONE]"


def extract_delta(p: Provider, payload: str) -> str | None:
    """Pull the text increment out of one SSE data payload.

    Returns None for events that carry no text, which is most of them: role
    announcements, usage records, finish reasons and keepalives. Counting those
    as tokens would report a time-to-first-token earlier than any text actually
    arrived, which is exactly the number this harness exists to measure.
    """
    payload = payload.strip()
    if not payload or payload == DONE_SENTINEL:
        return None

    try:
        event = json.loads(payload)
    except json.JSONDecodeError:
        return None
    if not isinstance(event, dict):
        return None

    if p.fmt == "messages":
        if event.get("type") != "content_block_delta":
            return None
        delta = event.get("delta") or {}
        if delta.get("type") != "text_delta":
            return None
        return delta.get("text") or None

    if p.fmt == "chat":
        choices = event.get("choices") or []
        if not choices:
            return None
        delta = choices[0].get("delta") or {}
        text = delta.get("content")
        # Reasoning models stream thinking separately; it is still the model
        # producing output, so it counts toward time to first token.
        if not text:
            text = delta.get("reasoning_content")
        return text or None

    if p.fmt == "gemini":
        candidates = event.get("candidates") or []
        if not candidates:
            return None
        parts = ((candidates[0].get("content") or {}).get("parts")) or []
        for part in parts:
            text = part.get("text")
            if text:
                return text
        return None

    return None


def is_done(payload: str) -> bool:
    return payload.strip() == DONE_SENTINEL


# --------------------------------------------------------------------------- #
# offline fixtures, used by --self-test
# --------------------------------------------------------------------------- #

# One real-shaped stream per format. These let every adapter be verified with
# no key, no network and no account, which matters because the alternative is
# discovering a parsing bug only after a paid run has produced nonsense.
FIXTURES: dict[str, list[tuple[str, str | None]]] = {
    "messages": [
        ('{"type":"message_start","message":{"id":"msg_1"}}', None),
        ('{"type":"content_block_start","index":0}', None),
        ('{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}', "Hel"),
        ('{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}', "lo"),
        ('{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}', None),
        ('{"type":"content_block_stop","index":0}', None),
        ('{"type":"message_delta","usage":{"output_tokens":2}}', None),
        ('{"type":"message_stop"}', None),
    ],
    "chat": [
        ('{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant"}}]}', None),
        ('{"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"}}]}', "Hel"),
        ('{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"}}]}', "lo"),
        ('{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}', None),
        ('{"id":"c1","choices":[],"usage":{"total_tokens":9}}', None),
        ("[DONE]", None),
    ],
    "gemini": [
        ('{"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"}}]}', "Hel"),
        ('{"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"}}]}', "lo"),
        ('{"candidates":[{"finishReason":"STOP","content":{"parts":[],"role":"model"}}]}', None),
        ('{"usageMetadata":{"totalTokenCount":9}}', None),
    ],
}


def self_test() -> int:
    """Verify every adapter against its fixture. Returns a process exit code."""
    failures = 0
    for fmt, cases in FIXTURES.items():
        probe = Provider("probe", fmt, "http://x", "m", None)
        got = []
        for payload, want in cases:
            actual = extract_delta(probe, payload)
            if actual != want:
                print(f"  FAIL {fmt}: {payload[:60]}... -> {actual!r}, want {want!r}")
                failures += 1
            if actual:
                got.append(actual)
        joined = "".join(got)
        if joined != "Hello":
            print(f"  FAIL {fmt}: reassembled {joined!r}, want 'Hello'")
            failures += 1
        else:
            print(f"  ok   {fmt}: parsed 'Hello' and ignored {len(cases)-2} non-text events")

    # A request must be constructible for every registered provider, so a typo
    # in the registry is caught here rather than at the start of a paid run.
    for name, p in sorted(PROVIDERS.items()):
        try:
            url, headers, body = build_request(p, p.base_url, p.model, "hi", 16, "k")
        except SystemExit as exc:
            print(f"  FAIL {name}: {exc}")
            failures += 1
            continue
        if not url.startswith(p.base_url):
            print(f"  FAIL {name}: url {url} does not start with base {p.base_url}")
            failures += 1
        if p.key_env and not any(k in headers for k in ("x-api-key", "authorization", "x-goog-api-key")):
            print(f"  FAIL {name}: no credential header built")
            failures += 1
        if p.fmt == "gemini" and "alt=sse" not in url:
            print(f"  FAIL {name}: gemini url must request SSE")
            failures += 1
    print(f"  ok   {len(PROVIDERS)} providers build a valid request")

    if failures:
        print(f"\n{failures} failure(s)")
        return 1
    print("\nall adapters pass")
    return 0

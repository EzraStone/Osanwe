# Hosted streaming boundary

The hosted BYOK client requests provider streaming and forwards only normalized text deltas. It does
not forward provider request IDs, usage records, account fields, internal error bodies, tool calls,
citations, images, audio, or reasoning traces.

## Normalized protocol

The browser receives only two successful event shapes:

```json
{"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}
{"type":"message_stop"}
```

A provider-side failure after HTTP success becomes a sanitized `error` event. A connection ending
without `message_stop` is incomplete and is not added to future model context as a completed answer.

## Supported upstream families

- OpenAI-compatible Chat Completions, including Groq and TokenRouter
- OpenAI Responses
- Anthropic Messages
- Google Gemini `streamGenerateContent`

Each parser accepts arbitrary transport chunk boundaries and both LF and CRLF event framing. The
server stops after one terminal event and caps each provider stream at 2 MiB. The browser's Stop
control cancels its response body; the server then cancels the provider request and releases local
capacity.

## Local measurement

The composer reports time to first text in milliseconds after a completed response. This is a local,
session-only observation measured from the visitor pressing Send until the first normalized text
delta reaches the browser. Osanwë does not upload or aggregate it. It is not directly comparable to
Phase 0 unless the route, client region, provider, model, connection reuse, and trial count are also
controlled.

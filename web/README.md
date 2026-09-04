# Osanwë hosted bring-your-own-key beta

This directory contains the shareable web client. It is intentionally separate from the
static public site in `docs/` and from the downloadable local client.

## Privacy boundary

The hosted app preserves the same Osanwë interface and local code runner as the downloadable
client. A visitor can choose Groq, OpenAI, Anthropic, Google Gemini, OpenRouter, TokenRouter, xAI,
Mistral AI, DeepSeek, Together AI, or Fireworks AI, enter a model ID, and load that provider's
API key for one browser tab. The key is sent to the same-origin `/api/chat` route for each
request and is not written by the app to browser storage, cookies, D1, R2, or application logs.
The route forwards it only to the selected provider's fixed HTTPS endpoint.
TokenRouter lists `z-ai/glm-5.3-free` first so testers can deliberately choose its free GLM route.

Arbitrary provider URLs are deliberately unsupported. Accepting a visitor-supplied upstream URL
would turn the hosted function into an SSRF and open-proxy surface. New providers belong in the
reviewed server registry; custom model IDs are allowed within each registered provider.

The hosting platform necessarily handles the API key and conversation in plaintext at runtime.
This is a compatibility path, not the anonymous Osanwë relay path. The UI states this before a
key can be connected.

Provider responses are streamed through a bounded normalizer described in
[`docs/hosted-streaming.md`](../docs/hosted-streaming.md). Only text deltas, a terminal marker, or a
sanitized error reach the browser.

## Local verification

```text
npm install
npm test
npm run lint
npm run build
npx playwright install chromium
npm run test:browser
npm run dev
```

Open `http://localhost:3000`. It redirects to the exact hosted client at `/client`. A live
provider call requires the visitor's own key and may incur charges on that provider account.
The browser suite uses a synthetic provider response and never spends provider credit. It proves
that generated JavaScript runs automatically and that interactive HTML either remains inside the
enforced no-network boundary or fails closed on browsers that do not yet support that boundary.

## Hosting

The app uses standard Next.js so the static client and same-origin provider proxy can be
deployed together on Vercel. Keep the project root set to `web`; no shared provider key or
server-side secret is required because each visitor supplies a session-only key in Settings.

The default test suite never spends provider credit. An intentionally awkward opt-in smoke test
exists for release verification and runs only when all four variables are set:

```text
OSANWE_LIVE_PROVIDER=groq
OSANWE_LIVE_MODEL=openai/gpt-oss-20b
OSANWE_LIVE_API_KEY=...
OSANWE_LIVE_CONFIRM=YES
```

Set `OSANWE_LIVE_CHECK_CONFIRM=YES` as well to authorize one additional bounded connection-check
request. This is separate so a normal live chat verification never silently doubles provider use.

The Settings connection check and release procedure are documented in
[`docs/hosted-provider-validation.md`](../docs/hosted-provider-validation.md).

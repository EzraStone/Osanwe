# Osanwë hosted compatibility beta

This directory contains the shareable web client. It is intentionally separate from the
static public site in `docs/` and from the downloadable local client.

## Privacy boundary

The hosted app lets a visitor supply a Groq or OpenAI API key for one browser tab. The key is
held in React memory, sent to the same-origin `/api/chat` route for each request, and is not
written to browser storage, cookies, D1, R2, or application logs. The route forwards it only to
the selected provider's fixed HTTPS endpoint.

The hosting platform necessarily handles the API key and conversation in plaintext at runtime.
This is a compatibility path, not the anonymous Osanwë relay path. The UI states this before a
key can be connected.

## Local verification

```text
npm install
npm test
npm run lint
npm run build
npm run dev
```

Open `http://localhost:3000`. A live provider call requires the visitor's own key and may incur
charges on that provider account.
